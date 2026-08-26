package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
)

const commandArtifactUnavailableReason = "artifact_unavailable"

type commandOutputResultOptions struct {
	Summary       string
	StatusMessage string
	IsError       bool
	Metadata      map[string]any
}

type commandOutputCollector struct {
	mu       sync.Mutex
	execCtx  ExecContext
	toolName string
	policy   config.ToolOutputConfig
	preview  *commandHeadTailBuffer
	pending  []byte

	rawBytes        int
	stream          *session.ToolOutputArtifactStream
	artifactInitial session.ToolOutputArtifactResult
	artifactErr     error
	spillAttempted  bool
	closed          bool
	result          session.ToolResult
	peakBuffered    int
}

func newCommandOutputCollector(execCtx ExecContext, toolName string) *commandOutputCollector {
	policy := effectiveCommandToolOutputPolicy(execCtx.Config)
	previewLimit := policy.DisplayOutputMaxBytes
	if policy.LLMOutputMaxBytes > previewLimit {
		previewLimit = policy.LLMOutputMaxBytes
	}
	collector := &commandOutputCollector{
		execCtx:  execCtx,
		toolName: strings.TrimSpace(toolName),
		policy:   policy,
		preview:  newCommandHeadTailBuffer(previewLimit),
		pending:  make([]byte, 0, policy.LLMOutputMaxBytes),
	}
	collector.observeBuffered()
	return collector
}

// Write is safe for stdout and stderr copy goroutines to call concurrently.
// Each Write call is serialized as one indivisible segment, matching the
// ordering guarantee of os/exec when both streams share the same writer.
func (c *commandOutputCollector) Write(payload []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, fmt.Errorf("command output collector is closed")
	}
	if len(payload) == 0 {
		return 0, nil
	}
	c.rawBytes += len(payload)
	c.preview.Write(payload)

	if c.stream != nil {
		_, _ = c.stream.Write(payload)
		c.observeBuffered()
		return len(payload), nil
	}
	if c.spillAttempted {
		c.observeBuffered()
		return len(payload), nil
	}
	if len(c.pending)+len(payload) <= c.policy.LLMOutputMaxBytes {
		c.pending = append(c.pending, payload...)
		c.observeBuffered()
		return len(payload), nil
	}

	c.startArtifactStream()
	if c.stream != nil {
		if len(c.pending) > 0 {
			_, _ = c.stream.Write(c.pending)
		}
		_, _ = c.stream.Write(payload)
	}
	c.pending = nil
	c.observeBuffered()
	return len(payload), nil
}

func (c *commandOutputCollector) finalize(options commandOutputResultOptions) session.ToolResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return cloneCommandToolResult(c.result)
	}
	c.closed = true

	artifact := c.artifactInitial
	if c.stream != nil {
		streamResult, err := c.stream.Close()
		artifact = streamResult
		if err != nil && c.artifactErr == nil {
			c.artifactErr = err
		}
	}

	metadata := cloneCommandMetadata(options.Metadata)
	fullInline := c.stream == nil && !c.spillAttempted && len(c.pending) == c.rawBytes && utf8.Valid(c.pending)
	renderPreview := func(limit int) (string, int) {
		if limit <= 0 {
			return "", 0
		}
		if c.rawBytes == 0 {
			return boundedCommandText("(no output)", limit), 0
		}
		return c.preview.Render(limit)
	}
	previewLLM, llmSourceBytes := renderPreview(c.policy.LLMOutputMaxBytes)
	previewDisplay, displaySourceBytes := renderPreview(c.policy.DisplayOutputMaxBytes)
	llmStatus := compactCommandLLMStatus(options.StatusMessage)

	if c.stream == nil && !c.spillAttempted && len(c.pending) == c.rawBytes && c.rawBytes > 0 {
		candidate := commandLLMOutput(string(c.pending), options.Summary)
		if llmStatus != "" {
			candidate = commandLLMOutput(llmStatus, options.Summary)
			candidate = candidate + "\n" + string(c.pending)
		}
		if !fullInline || len(candidate) > c.policy.LLMOutputMaxBytes {
			artifact = c.writeLateArtifact(c.pending)
			fullInline = false
		}
	}
	summary := commandSummaryWithTruncated(options.Summary, !fullInline)

	artifactPath := commandArtifactDisplayPath(c.execCtx, artifact.AbsolutePath)
	artifactNotice := commandArtifactNotice(c.policy.LLMOutputMaxBytes, artifactPath, c.rawBytes, artifact.PersistedBytes, c.rawBytes-artifact.PersistedBytes, artifact.Complete, artifact.Truncated, artifact.Reason)
	if fullInline {
		artifactNotice = ""
	}

	llmOutput, llmPreviewBudget := buildCommandLLMChannelWithPreviewBudget(summary, llmStatus, artifactNotice, previewLLM, c.policy.LLMOutputMaxBytes)
	if llmPreviewBudget < len(previewLLM) {
		previewLLM, llmSourceBytes = renderPreview(llmPreviewBudget)
		llmOutput, _ = buildCommandLLMChannelWithPreviewBudget(summary, llmStatus, artifactNotice, previewLLM, c.policy.LLMOutputMaxBytes)
	}
	displayOutput, displayPreviewBudget := buildCommandDisplayChannelWithPreviewBudget(options.StatusMessage, artifactNotice, previewDisplay, c.rawBytes, fullInline, c.policy.DisplayOutputMaxBytes)
	if displayPreviewBudget < len(previewDisplay) {
		previewDisplay, displaySourceBytes = renderPreview(displayPreviewBudget)
		displayOutput, _ = buildCommandDisplayChannelWithPreviewBudget(options.StatusMessage, artifactNotice, previewDisplay, c.rawBytes, fullInline, c.policy.DisplayOutputMaxBytes)
	}

	persistedBytes := artifact.PersistedBytes
	omittedBytes := c.rawBytes - persistedBytes
	artifactComplete := artifact.Complete && artifactPath != ""
	artifactTruncated := artifact.Truncated && artifactPath != ""
	recoverable := artifactComplete
	budgetReason := artifact.Reason
	if fullInline {
		persistedBytes = 0
		omittedBytes = 0
		artifactPath = ""
		artifactComplete = false
		artifactTruncated = false
		recoverable = true
		budgetReason = "inline"
	} else if budgetReason == "" {
		if artifactComplete {
			budgetReason = "llm_output_max_bytes"
		} else {
			budgetReason = commandArtifactUnavailableReason
		}
	}
	if omittedBytes < 0 {
		omittedBytes = c.rawBytes
		persistedBytes = 0
		artifactPath = ""
		artifactComplete = false
		artifactTruncated = false
		recoverable = false
		budgetReason = commandArtifactUnavailableReason
	}

	metadata["raw_length"] = c.rawBytes
	metadata["truncated"] = !fullInline
	metadata["tool_output_budget_version"] = session.ToolOutputBudgetVersion
	metadata["raw_bytes"] = c.rawBytes
	metadata["persisted_bytes"] = persistedBytes
	metadata["inline_bytes"] = len(llmOutput)
	metadata["omitted_bytes"] = omittedBytes
	metadata["artifact_path"] = artifactPath
	metadata["artifact_complete"] = artifactComplete
	metadata["artifact_truncated"] = artifactTruncated
	metadata["budget_reason"] = budgetReason
	metadata["recoverable"] = recoverable
	metadata["display_raw_bytes"] = c.rawBytes
	metadata["display_inline_bytes"] = len(displayOutput)
	displayOmitted := c.rawBytes - displaySourceBytes
	if displayOmitted < 0 {
		displayOmitted = 0
	}
	metadata["display_omitted_bytes"] = displayOmitted
	metadata["preview_source_bytes"] = llmSourceBytes
	metadata["collector_buffer_limit"] = c.bufferLimitLocked()
	metadata["collector_peak_buffered_bytes"] = c.peakBuffered
	if c.artifactErr != nil {
		metadata["artifact_error"] = c.artifactErr.Error()
	}

	c.result = session.ToolResult{
		Name:          c.toolName,
		LLMOutput:     llmOutput,
		DisplayOutput: displayOutput,
		IsError:       options.IsError,
		Metadata:      metadata,
	}
	return cloneCommandToolResult(c.result)
}

func compactCommandLLMStatus(status string) string {
	switch status {
	case TimedOutToolExecutionMessage:
		return "[Command timed out and was terminated. Verify state before re-running side-effecting commands.]"
	case InterruptedToolExecutionMessage:
		return "[Tool execution was interrupted and may have partially executed. Verify state before re-running side-effecting commands.]"
	default:
		return status
	}
}

func commandSummaryWithTruncated(summary string, truncated bool) string {
	for _, suffix := range []string{"truncated=false]", "truncated=true]"} {
		if strings.HasSuffix(summary, suffix) {
			return strings.TrimSuffix(summary, suffix) + fmt.Sprintf("truncated=%t]", truncated)
		}
	}
	return summary
}

func (c *commandOutputCollector) startArtifactStream() {
	c.spillAttempted = true
	if c.execCtx.Store == nil || strings.TrimSpace(c.execCtx.SessionID) == "" || strings.TrimSpace(c.execCtx.EphemeralArtifactRoot) == "" {
		c.artifactInitial = session.ToolOutputArtifactResult{Reason: commandArtifactUnavailableReason}
		return
	}
	stream, initial, err := c.execCtx.Store.BeginToolOutputArtifactStream(
		c.execCtx.SessionID,
		c.execCtx.EphemeralArtifactRoot,
		c.toolName+"-"+c.execCtx.ToolCallID,
		session.ToolOutputArtifactQuota{
			FileMaxBytes:    c.policy.ArtifactFileMaxBytes,
			SessionMaxBytes: c.policy.ArtifactSessionMaxBytes,
			MaxFiles:        c.policy.ArtifactMaxFiles,
		},
	)
	c.stream = stream
	c.artifactInitial = initial
	if err != nil {
		c.artifactErr = err
	}
}

func (c *commandOutputCollector) writeLateArtifact(payload []byte) session.ToolOutputArtifactResult {
	c.spillAttempted = true
	if c.execCtx.Store == nil || strings.TrimSpace(c.execCtx.SessionID) == "" || strings.TrimSpace(c.execCtx.EphemeralArtifactRoot) == "" {
		return session.ToolOutputArtifactResult{RawBytes: len(payload), OmittedBytes: len(payload), Reason: commandArtifactUnavailableReason}
	}
	result, err := c.execCtx.Store.WriteToolOutputArtifact(
		c.execCtx.SessionID,
		c.execCtx.EphemeralArtifactRoot,
		c.toolName+"-"+c.execCtx.ToolCallID,
		payload,
		session.ToolOutputArtifactQuota{
			FileMaxBytes:    c.policy.ArtifactFileMaxBytes,
			SessionMaxBytes: c.policy.ArtifactSessionMaxBytes,
			MaxFiles:        c.policy.ArtifactMaxFiles,
		},
	)
	if err != nil {
		c.artifactErr = err
		return session.ToolOutputArtifactResult{RawBytes: len(payload), OmittedBytes: len(payload), Reason: session.ToolOutputArtifactReasonWriteFailed}
	}
	return result
}

func (c *commandOutputCollector) bufferedBytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bufferedBytesLocked()
}

func (c *commandOutputCollector) peakBufferedBytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peakBuffered
}

func (c *commandOutputCollector) rawByteCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rawBytes
}

func (c *commandOutputCollector) bufferLimit() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bufferLimitLocked()
}

func (c *commandOutputCollector) bufferedBytesLocked() int {
	return len(c.pending) + c.preview.BufferedBytes()
}

func (c *commandOutputCollector) bufferLimitLocked() int {
	return c.policy.LLMOutputMaxBytes + c.preview.Limit()
}

func (c *commandOutputCollector) observeBuffered() {
	if current := c.bufferedBytesLocked(); current > c.peakBuffered {
		c.peakBuffered = current
	}
}

type commandHeadTailBuffer struct {
	limit     int
	headLimit int
	tailLimit int
	head      []byte
	tail      []byte
	rawBytes  int
}

func newCommandHeadTailBuffer(limit int) *commandHeadTailBuffer {
	if limit < 1 {
		limit = 1
	}
	headLimit := limit / 2
	tailLimit := limit - headLimit
	return &commandHeadTailBuffer{
		limit:     limit,
		headLimit: headLimit,
		tailLimit: tailLimit,
		head:      make([]byte, 0, headLimit),
		tail:      make([]byte, 0, tailLimit),
	}
}

func (b *commandHeadTailBuffer) Write(payload []byte) {
	b.rawBytes += len(payload)
	if len(b.head) < b.headLimit {
		take := b.headLimit - len(b.head)
		if take > len(payload) {
			take = len(payload)
		}
		b.head = append(b.head, payload[:take]...)
		payload = payload[take:]
	}
	if len(payload) == 0 || b.tailLimit == 0 {
		return
	}
	if len(payload) >= b.tailLimit {
		b.tail = append(b.tail[:0], payload[len(payload)-b.tailLimit:]...)
		return
	}
	if len(b.tail)+len(payload) > b.tailLimit {
		drop := len(b.tail) + len(payload) - b.tailLimit
		copy(b.tail, b.tail[drop:])
		b.tail = b.tail[:len(b.tail)-drop]
	}
	b.tail = append(b.tail, payload...)
}

func (b *commandHeadTailBuffer) Render(maxBytes int) (string, int) {
	if maxBytes <= 0 || b.rawBytes == 0 {
		return "", 0
	}
	var headSource string
	var tailSource string
	if b.rawBytes <= len(b.head)+len(b.tail) {
		data := make([]byte, 0, len(b.head)+len(b.tail))
		data = append(data, b.head...)
		data = append(data, b.tail...)
		text := commandUTF8Safe(string(data))
		if len(text) <= maxBytes {
			return text, b.rawBytes
		}
		headSource = text
		tailSource = text
	} else {
		headSource = commandUTF8Safe(string(b.head))
		tailSource = commandUTF8Safe(string(b.tail))
	}

	marker := fmt.Sprintf("\n...[command output: %d source bytes omitted from preview]...\n", b.rawBytes)
	if len(marker) >= maxBytes {
		return prefixAtRuneBoundary(marker, maxBytes), 0
	}
	available := maxBytes - len(marker)
	head := prefixAtRuneBoundary(headSource, available/2)
	tail := suffixAtRuneBoundary(tailSource, available-len(head))
	omitted := b.rawBytes - len(head) - len(tail)
	if omitted < 0 {
		omitted = 0
	}
	marker = fmt.Sprintf("\n...[command output: %d source bytes omitted from preview]...\n", omitted)
	for len(head)+len(marker)+len(tail) > maxBytes && (len(head) > 0 || len(tail) > 0) {
		if len(tail) > len(head) {
			tail = suffixAtRuneBoundary(tail, len(tail)-1)
		} else {
			head = prefixAtRuneBoundary(head, len(head)-1)
		}
	}
	return head + marker + tail, len(head) + len(tail)
}

func (b *commandHeadTailBuffer) BufferedBytes() int { return len(b.head) + len(b.tail) }
func (b *commandHeadTailBuffer) Limit() int         { return b.limit }

func effectiveCommandToolOutputPolicy(cfg *config.Config) config.ToolOutputConfig {
	policy := config.ToolOutputConfig{}
	if cfg != nil {
		policy = cfg.Runtime.ToolOutput
	}
	if policy.LLMOutputMaxBytes <= 0 {
		policy.LLMOutputMaxBytes = config.DefaultToolOutputLLMMaxBytes
	}
	if policy.DisplayOutputMaxBytes <= 0 {
		policy.DisplayOutputMaxBytes = config.DefaultToolOutputDisplayMaxBytes
	}
	if policy.ArtifactFileMaxBytes <= 0 {
		policy.ArtifactFileMaxBytes = config.DefaultToolOutputArtifactFileMaxBytes
	}
	if policy.ArtifactSessionMaxBytes <= 0 {
		policy.ArtifactSessionMaxBytes = config.DefaultToolOutputArtifactSessionMaxBytes
	}
	if policy.ArtifactMaxFiles <= 0 {
		policy.ArtifactMaxFiles = config.DefaultToolOutputArtifactMaxFiles
	}
	return policy
}

func commandArtifactDisplayPath(execCtx ExecContext, absolute string) string {
	absolute = strings.TrimSpace(absolute)
	if absolute == "" {
		return ""
	}
	if execCtx.Store != nil && strings.TrimSpace(execCtx.SessionID) != "" {
		if rel, err := filepath.Rel(execCtx.Store.SessionDir(execCtx.SessionID), absolute); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Clean(absolute)
}

func commandArtifactNotice(inlineLimit int, path string, rawBytes, persistedBytes, omittedBytes int, complete, truncated bool, reason string) string {
	switch {
	case complete && path != "":
		return fmt.Sprintf("[Complete artifact: %s; raw_bytes=%d. Page with read_file byte_offset/byte_limit (inline_limit=%d).]", path, rawBytes, inlineLimit)
	case truncated && path != "":
		return fmt.Sprintf("[Partial artifact: %s; saved=%d/%d bytes omitted=%d reason=%s; unrecoverable.]", path, persistedBytes, rawBytes, omittedBytes, reason)
	case rawBytes > 0:
		if strings.TrimSpace(reason) == "" {
			reason = commandArtifactUnavailableReason
		}
		return fmt.Sprintf("[Artifact unavailable: raw_bytes=%d reason=%s; omitted bytes are not recoverable.]", rawBytes, reason)
	default:
		return ""
	}
}

func buildCommandLLMChannel(summary, status, notice, preview string, maxBytes int) string {
	output, _ := buildCommandLLMChannelWithPreviewBudget(summary, status, notice, preview, maxBytes)
	return output
}

func buildCommandLLMChannelWithPreviewBudget(summary, status, notice, preview string, maxBytes int) (string, int) {
	if maxBytes <= 0 {
		return "", 0
	}
	summary = commandUTF8Safe(summary)
	status = commandUTF8Safe(status)
	notice = commandUTF8Safe(notice)
	preview = commandUTF8Safe(preview)
	components := make([]string, 0, 4)
	for _, component := range []string{summary, status, notice} {
		if strings.TrimSpace(component) != "" {
			components = append(components, component)
		}
	}
	header := strings.Join(components, "\n")
	if preview == "" && len(header) <= maxBytes {
		return header, 0
	}
	if header == "" && len(preview) <= maxBytes {
		return preview, len(preview)
	}
	if header != "" && preview != "" && len(header)+1+len(preview) <= maxBytes {
		return header + "\n" + preview, len(preview)
	}

	// On overflow, command status and the exact artifact pointer outrank the
	// potentially huge workdir-bearing summary and source preview. The normal
	// non-overflow layout above remains byte-for-byte compatible.
	priorityParts := make([]string, 0, 2)
	for _, component := range []string{status, notice} {
		if strings.TrimSpace(component) != "" {
			priorityParts = append(priorityParts, component)
		}
	}
	priority := strings.Join(priorityParts, "\n")
	if len(priority) > maxBytes {
		if notice != "" && len(notice) <= maxBytes {
			return notice, 0
		}
		if status != "" && len(status) <= maxBytes {
			return status, 0
		}
		if notice != "" {
			return boundedCommandText(notice, maxBytes), 0
		}
		return boundedCommandText(status, maxBytes), 0
	}

	lowParts := 0
	if summary != "" {
		lowParts++
	}
	if preview != "" {
		lowParts++
	}
	separatorBytes := 0
	if priority != "" && lowParts > 0 {
		separatorBytes++
	}
	if lowParts == 2 {
		separatorBytes++
	}
	lowBudget := maxBytes - len(priority) - separatorBytes
	if lowBudget < 0 {
		lowBudget = 0
	}
	summaryBudget, previewBudget := 0, 0
	switch {
	case summary != "" && preview != "":
		summaryBudget = lowBudget * 2 / 3
		previewBudget = lowBudget - summaryBudget
	case summary != "":
		summaryBudget = lowBudget
	case preview != "":
		previewBudget = lowBudget
	}
	summary = boundedCommandText(summary, summaryBudget)
	preview = boundedCommandText(preview, previewBudget)
	components = components[:0]
	for _, component := range []string{summary, status, notice, preview} {
		if component != "" {
			components = append(components, component)
		}
	}
	return strings.Join(components, "\n"), previewBudget
}

func buildCommandDisplayChannelWithPreviewBudget(status, notice, preview string, rawBytes int, fullInline bool, maxBytes int) (string, int) {
	if fullInline && strings.TrimSpace(status) == "" && strings.TrimSpace(notice) == "" {
		return boundedCommandText(preview, maxBytes), maxBytes
	}
	if rawBytes == 0 && strings.TrimSpace(status) != "" && strings.TrimSpace(notice) == "" {
		return boundedCommandText(status, maxBytes), 0
	}
	return buildCommandLLMChannelWithPreviewBudget("", status, notice, preview, maxBytes)
}

func boundedCommandText(value string, maxBytes int) string {
	value = commandUTF8Safe(value)
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	text, _, _ := truncateOutput(value, maxBytes)
	return text
}

func commandUTF8Safe(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	source := []byte(value)
	output := make([]byte, 0, len(source))
	for len(source) > 0 {
		_, size := utf8.DecodeRune(source)
		if size == 1 && source[0] >= utf8.RuneSelf {
			output = append(output, '?')
			source = source[1:]
			continue
		}
		output = append(output, source[:size]...)
		source = source[size:]
	}
	return string(output)
}

func cloneCommandMetadata(input map[string]any) map[string]any {
	output := make(map[string]any, len(input)+24)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneCommandToolResult(input session.ToolResult) session.ToolResult {
	result := input
	result.Metadata = cloneCommandMetadata(input.Metadata)
	return result
}
