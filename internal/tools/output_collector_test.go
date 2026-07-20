package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

func TestOutputCollectorBoundsMemoryAndAccountsArtifact(t *testing.T) {
	execCtx := newOutputCollectorExecContext(t, smallOutputCollectorConfig())
	collector := newCommandOutputCollector(execCtx, "shell")
	var raw []byte
	for index := 0; index < 200; index++ {
		chunk := []byte(strings.Repeat(string(rune('a'+index%26)), 97))
		raw = append(raw, chunk...)
		if n, err := collector.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("collector write n=%d err=%v", n, err)
		}
		if got, limit := collector.bufferedBytes(), collector.bufferLimit(); got > limit {
			t.Fatalf("collector buffer grew beyond fixed limit: got=%d limit=%d", got, limit)
		}
	}
	result := collector.finalize(commandOutputResultOptions{
		Summary:  "[command_result tool=shell exit_code=0]",
		Metadata: map[string]any{"exit_code": 0},
	})
	assertFinalizedCommandBudget(t, result)
	if got := commandMetadataInt(t, result.Metadata, "raw_bytes"); got != len(raw) {
		t.Fatalf("raw_bytes=%d, want %d", got, len(raw))
	}
	persisted := commandMetadataInt(t, result.Metadata, "persisted_bytes")
	omitted := commandMetadataInt(t, result.Metadata, "omitted_bytes")
	if persisted+omitted != len(raw) || persisted != execCtx.Config.Runtime.ToolOutput.ArtifactFileMaxBytes {
		t.Fatalf("artifact accounting persisted/omitted=%d/%d raw=%d metadata=%#v", persisted, omitted, len(raw), result.Metadata)
	}
	if result.Metadata["artifact_complete"] != false || result.Metadata["artifact_truncated"] != true || result.Metadata["recoverable"] != false {
		t.Fatalf("hard-capped collector artifact mislabeled: %#v", result.Metadata)
	}
	if strings.Contains(result.LLMOutput, "Complete artifact") || strings.Contains(result.LLMOutput, "Full output") || !strings.Contains(result.LLMOutput, "Partial artifact") {
		t.Fatalf("partial artifact notice is inaccurate: %q", result.LLMOutput)
	}
	artifact := commandArtifactAbsolutePath(t, execCtx, result)
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("read partial command artifact: %v", err)
	}
	if !bytes.Equal(data, raw[:persisted]) {
		t.Fatalf("partial artifact is not an exact source prefix")
	}
	if collector.peakBufferedBytes() > collector.bufferLimit() {
		t.Fatalf("peak collector buffer=%d, limit=%d", collector.peakBufferedBytes(), collector.bufferLimit())
	}
}

func TestOutputCollectorCompleteArtifactMatchesRawMergedBytes(t *testing.T) {
	cfg := smallOutputCollectorConfig()
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	execCtx := newOutputCollectorExecContext(t, cfg)
	collector := newCommandOutputCollector(execCtx, "shell")
	raw := []byte(strings.Repeat("stdout-line\n", 100) + strings.Repeat("stderr-line\n", 100))
	for start := 0; start < len(raw); start += 113 {
		end := start + 113
		if end > len(raw) {
			end = len(raw)
		}
		_, _ = collector.Write(raw[start:end])
	}
	result := collector.finalize(commandOutputResultOptions{Summary: "[command_result tool=shell exit_code=0]", Metadata: map[string]any{"exit_code": 0}})
	assertFinalizedCommandBudget(t, result)
	if result.Metadata["artifact_complete"] != true || result.Metadata["recoverable"] != true || commandMetadataInt(t, result.Metadata, "omitted_bytes") != 0 {
		t.Fatalf("complete collector artifact mislabeled: %#v", result.Metadata)
	}
	if result.Metadata["truncated"] != true {
		t.Fatalf("compatibility truncated field must describe shortened inline output: %#v", result.Metadata)
	}
	if !strings.Contains(result.LLMOutput, "Complete artifact") || strings.Contains(result.LLMOutput, "Full output") {
		t.Fatalf("complete collector notice missing or legacy label used: %q", result.LLMOutput)
	}
	data, err := os.ReadFile(commandArtifactAbsolutePath(t, execCtx, result))
	if err != nil {
		t.Fatalf("read complete command artifact: %v", err)
	}
	if !bytes.Equal(data, raw) {
		t.Fatalf("complete collector artifact changed source bytes")
	}
}

func TestBuildCommandLLMChannelPreservesArtifactPointerBeforeLongSummary(t *testing.T) {
	const maxBytes = 512
	artifactPath := "artifacts/tool-outputs/" + strings.Repeat("deep-segment-", 15) + "output.txt"
	notice := commandArtifactNotice(maxBytes, artifactPath, 4096, 4096, 0, true, false, "")
	status := InterruptedToolExecutionMessage
	summary := "[command_result tool=shell workdir=" + strings.Repeat("very-long-workdir/", 300) + "]"
	result := buildCommandLLMChannel(summary, status, notice, strings.Repeat("preview", 200), maxBytes)
	if len(result) > maxBytes || !utf8.ValidString(result) {
		t.Fatalf("bounded channel size/UTF-8 invalid: bytes=%d valid=%t", len(result), utf8.ValidString(result))
	}
	if !strings.Contains(result, notice) || !strings.Contains(result, artifactPath) {
		t.Fatalf("artifact pointer was truncated behind low-priority summary text: %q", result)
	}
	if !strings.Contains(result, status) {
		t.Fatalf("command status was dropped behind low-priority summary text: %q", result)
	}
}

func TestOutputCollectorSerializesConcurrentStdoutStderrWrites(t *testing.T) {
	cfg := smallOutputCollectorConfig()
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 64 * 1024
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 128 * 1024
	execCtx := newOutputCollectorExecContext(t, cfg)
	collector := newCommandOutputCollector(execCtx, "shell")
	const writers = 40
	var wg sync.WaitGroup
	for index := 0; index < writers; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			letter := byte('A' + index%26)
			chunk := append(bytes.Repeat([]byte{letter}, 599), '\n')
			if n, err := collector.Write(chunk); err != nil || n != len(chunk) {
				t.Errorf("concurrent collector write n=%d err=%v", n, err)
			}
		}()
	}
	wg.Wait()
	result := collector.finalize(commandOutputResultOptions{Summary: "[command_result tool=shell exit_code=0]"})
	data, err := os.ReadFile(commandArtifactAbsolutePath(t, execCtx, result))
	if err != nil {
		t.Fatalf("read concurrent artifact: %v", err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) != writers {
		t.Fatalf("merged writer line count=%d, want %d", len(lines), writers)
	}
	for _, line := range lines {
		if len(line) != 599 || bytes.Count(line, line[:1]) != len(line) {
			t.Fatalf("stdout/stderr write was interleaved within one Write call")
		}
	}
}

func TestOutputCollectorPreviewIsUTF8SafeAndArtifactIsByteExact(t *testing.T) {
	cfg := smallOutputCollectorConfig()
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 8192
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
	execCtx := newOutputCollectorExecContext(t, cfg)
	collector := newCommandOutputCollector(execCtx, "shell")
	raw := append([]byte(strings.Repeat("前缀", 300)), 0xff, 0xfe)
	raw = append(raw, []byte(strings.Repeat("后缀", 300))...)
	for start := 0; start < len(raw); start += 17 {
		end := start + 17
		if end > len(raw) {
			end = len(raw)
		}
		_, _ = collector.Write(raw[start:end])
	}
	result := collector.finalize(commandOutputResultOptions{Summary: "[command_result tool=shell exit_code=0]"})
	if !utf8.ValidString(result.LLMOutput) || !utf8.ValidString(result.DisplayOutput) {
		t.Fatalf("collector preview is not valid UTF-8: %#v", result)
	}
	data, err := os.ReadFile(commandArtifactAbsolutePath(t, execCtx, result))
	if err != nil {
		t.Fatalf("read UTF-8 test artifact: %v", err)
	}
	if !bytes.Equal(data, raw) {
		t.Fatalf("artifact did not preserve invalid/raw source bytes")
	}
}

func TestOutputCollectorPersistsSmallInvalidUTF8Source(t *testing.T) {
	cfg := smallOutputCollectorConfig()
	execCtx := newOutputCollectorExecContext(t, cfg)
	collector := newCommandOutputCollector(execCtx, "shell")
	raw := bytes.Repeat([]byte{0xff, 'a'}, 200)
	_, _ = collector.Write(raw)
	result := collector.finalize(commandOutputResultOptions{Summary: "[command_result tool=shell exit_code=0 truncated=false]"})
	if !utf8.ValidString(result.LLMOutput) || !utf8.ValidString(result.DisplayOutput) {
		t.Fatalf("small invalid source produced invalid preview: %#v", result)
	}
	if result.Metadata["artifact_complete"] != true || result.Metadata["recoverable"] != true || result.Metadata["truncated"] != true {
		t.Fatalf("small invalid source was mislabeled as fully recoverable inline text: %#v", result.Metadata)
	}
	if !strings.Contains(result.LLMOutput, "truncated=true") || strings.Contains(result.LLMOutput, "truncated=false") {
		t.Fatalf("command summary disagrees with finalized compatibility metadata: %q", result.LLMOutput)
	}
	if sourceBytes := commandMetadataInt(t, result.Metadata, "preview_source_bytes"); sourceBytes < 0 || sourceBytes > len(raw) {
		t.Fatalf("preview source accounting escaped raw bounds: source=%d raw=%d", sourceBytes, len(raw))
	}
	data, err := os.ReadFile(commandArtifactAbsolutePath(t, execCtx, result))
	if err != nil || !bytes.Equal(data, raw) {
		t.Fatalf("small invalid source artifact changed bytes: data=%v err=%v", data, err)
	}
}

func TestOutputCollectorWithoutSessionStoreFailsClosedAndStaysBounded(t *testing.T) {
	cfg := smallOutputCollectorConfig()
	execCtx := ExecContext{Config: cfg, Workdir: t.TempDir(), ToolCallID: "call_no_store"}
	collector := newCommandOutputCollector(execCtx, "shell")
	rawBytes := 0
	for index := 0; index < 100; index++ {
		chunk := []byte(strings.Repeat("x", 211))
		rawBytes += len(chunk)
		_, _ = collector.Write(chunk)
	}
	result := collector.finalize(commandOutputResultOptions{Summary: "[command_result tool=shell exit_code=0]"})
	assertFinalizedCommandBudget(t, result)
	if commandMetadataInt(t, result.Metadata, "raw_bytes") != rawBytes || commandMetadataInt(t, result.Metadata, "persisted_bytes") != 0 || commandMetadataInt(t, result.Metadata, "omitted_bytes") != rawBytes {
		t.Fatalf("store-less collector accounting mismatch: %#v", result.Metadata)
	}
	if result.Metadata["artifact_path"] != "" || result.Metadata["recoverable"] != false || !strings.Contains(result.LLMOutput, "Artifact unavailable") {
		t.Fatalf("store-less collector did not fail closed: %#v", result)
	}
	if collector.peakBufferedBytes() > collector.bufferLimit() {
		t.Fatalf("store-less collector grew beyond its fixed buffer")
	}
}

func TestShellStreamingCollectorFinalizesLargeTimeoutAndNonZeroExit(t *testing.T) {
	for _, tc := range []struct {
		name             string
		command          string
		timeout          int
		artifactFileCap  int
		wantFailureClass string
		wantExitCode     int
		wantComplete     bool
	}{
		{
			name:             "timeout_after_continuous_output",
			command:          "chunk=$(printf '%01024d' 0); chunk=${chunk//0/Z}; while :; do printf '%s' \"$chunk\"; sleep 0.01; done",
			timeout:          1,
			artifactFileCap:  1024,
			wantFailureClass: FailureClassTimeout,
			wantExitCode:     -1,
			wantComplete:     false,
		},
		{
			name:            "non_zero_exit_after_large_output",
			command:         "head -c 3000 /dev/zero | tr '\\0' x; exit 7",
			artifactFileCap: 8192,
			wantExitCode:    7,
			wantComplete:    true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := smallOutputCollectorConfig()
			cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = tc.artifactFileCap
			cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 16384
			root := t.TempDir()
			store, sessionID, artifactRoot := newOutputCollectorSession(t, root)
			registry, err := NewRegistry(cfg, nil, store, nil)
			if err != nil {
				t.Fatalf("new registry: %v", err)
			}
			args := mustJSON(t, map[string]any{"command": tc.command})
			if tc.timeout > 0 {
				args = mustJSON(t, map[string]any{"command": tc.command, "timeout": tc.timeout})
			}
			result, execErr := registry.Execute(context.Background(), "shell", ExecContext{
				SessionID:             sessionID,
				ToolCallID:            "call_" + tc.name,
				Workdir:               root,
				EphemeralArtifactRoot: artifactRoot,
				Store:                 store,
				Config:                cfg,
			}, args)
			if execErr != nil || !result.IsError {
				t.Fatalf("execute large failing shell: result=%#v err=%v", result, execErr)
			}
			assertFinalizedCommandBudget(t, result)
			if tc.wantFailureClass != "" && result.Metadata[MetadataFailureClass] != tc.wantFailureClass {
				t.Fatalf("failure class=%#v, want %s", result.Metadata[MetadataFailureClass], tc.wantFailureClass)
			}
			if exitCode := commandMetadataInt(t, result.Metadata, "exit_code"); exitCode != tc.wantExitCode {
				t.Fatalf("exit_code=%d, want %d", exitCode, tc.wantExitCode)
			}
			rawBytes := commandMetadataInt(t, result.Metadata, "raw_bytes")
			persistedBytes := commandMetadataInt(t, result.Metadata, "persisted_bytes")
			omittedBytes := commandMetadataInt(t, result.Metadata, "omitted_bytes")
			if rawBytes <= cfg.Runtime.ToolOutput.LLMOutputMaxBytes || rawBytes != persistedBytes+omittedBytes {
				t.Fatalf("large failing output accounting mismatch: %#v", result.Metadata)
			}
			peak := commandMetadataInt(t, result.Metadata, "collector_peak_buffered_bytes")
			limit := commandMetadataInt(t, result.Metadata, "collector_buffer_limit")
			if peak > limit {
				t.Fatalf("collector grew with timed output: peak=%d limit=%d", peak, limit)
			}
			if result.Metadata["artifact_complete"] != tc.wantComplete || result.Metadata["recoverable"] != tc.wantComplete {
				t.Fatalf("artifact completeness mismatch: %#v", result.Metadata)
			}
			if tc.wantFailureClass == FailureClassTimeout {
				llmSourceBytes := commandMetadataInt(t, result.Metadata, "preview_source_bytes")
				displaySourceBytes := commandMetadataInt(t, result.Metadata, "display_raw_bytes") - commandMetadataInt(t, result.Metadata, "display_omitted_bytes")
				if llmSourceBytes != strings.Count(result.LLMOutput, "Z") || displaySourceBytes != strings.Count(result.DisplayOutput, "Z") {
					t.Fatalf("visible preview source accounting mismatch: llm=%d/%d display=%d/%d metadata=%#v", llmSourceBytes, strings.Count(result.LLMOutput, "Z"), displaySourceBytes, strings.Count(result.DisplayOutput, "Z"), result.Metadata)
				}
			}
			if tc.wantComplete {
				if omittedBytes != 0 || persistedBytes != rawBytes || !strings.Contains(result.LLMOutput, "Complete artifact") {
					t.Fatalf("non-zero complete artifact mismatch: %#v", result)
				}
			} else if persistedBytes != tc.artifactFileCap || omittedBytes <= 0 || result.Metadata["artifact_truncated"] != true || !strings.Contains(result.LLMOutput, "Partial artifact") || !strings.Contains(result.LLMOutput, "timed out") || !strings.Contains(result.LLMOutput, "[command_result tool=shell") || !strings.Contains(result.LLMOutput, "exit_code=-1") {
				t.Fatalf("timeout partial artifact mismatch: %#v", result)
			}
			if _, statErr := os.Stat(commandArtifactAbsolutePath(t, ExecContext{Store: store, SessionID: sessionID}, result)); statErr != nil {
				t.Fatalf("stat failing command artifact: %v", statErr)
			}
		})
	}
}

func TestShellAndSkillCommandUseCurrentResultArtifacts(t *testing.T) {
	cfg := smallOutputCollectorConfig()
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 4096
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 32768
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "streamer")
	if err := os.MkdirAll(filepath.Join(skillDir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: streamer\ndescription: stream output\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	command := "head -c 3000 /dev/zero | tr '\\\\0' x"
	toolYAML := "name: stream_skill\ndescription: Stream output\ncommand: [\"bash\", \"-lc\", \"" + command + "\"]\ninput_schema:\n  type: object\n  properties: {}\n"
	if err := os.WriteFile(filepath.Join(skillDir, "tools", "stream.yaml"), []byte(toolYAML), 0o644); err != nil {
		t.Fatalf("write skill tool: %v", err)
	}
	catalog, err := skills.Scan([]string{filepath.Join(root, "skills")})
	if err != nil {
		t.Fatalf("scan skills: %v", err)
	}
	registry, err := NewRegistry(cfg, catalog, nil, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	store, sessionID, artifactRoot := newOutputCollectorSession(t, root)

	results := map[string]session.ToolResult{}
	for name, args := range map[string]json.RawMessage{
		"shell":        json.RawMessage(`{"command":"head -c 3000 /dev/zero | tr '\\0' x"}`),
		"stream_skill": json.RawMessage(`{}`),
	} {
		result, execErr := registry.Execute(context.Background(), name, ExecContext{
			SessionID:             sessionID,
			ToolCallID:            "call_" + name,
			Workdir:               root,
			EphemeralArtifactRoot: artifactRoot,
			Store:                 store,
			Config:                cfg,
			Catalog:               catalog,
		}, args)
		if execErr != nil || result.IsError {
			t.Fatalf("execute %s: result=%#v err=%v", name, result, execErr)
		}
		assertFinalizedCommandBudget(t, result)
		if result.Metadata["artifact_complete"] != true || commandMetadataInt(t, result.Metadata, "raw_bytes") != 3000 {
			t.Fatalf("%s did not expose a complete current-result artifact: %#v", name, result)
		}
		results[name] = result
	}
	for _, key := range []string{"raw_bytes", "persisted_bytes", "omitted_bytes", "artifact_complete", "artifact_truncated", "recoverable"} {
		if results["shell"].Metadata[key] != results["stream_skill"].Metadata[key] {
			t.Fatalf("shell/skill metadata mismatch for %s: shell=%#v skill=%#v", key, results["shell"].Metadata[key], results["stream_skill"].Metadata[key])
		}
	}
	if def := registry.Get("stream_skill"); def == nil || !def.Ephemeral {
		t.Fatalf("trusted skill command is not marked ephemeral: %#v", def)
	}

	readResult, readErr := registry.Execute(context.Background(), "read_file", ExecContext{
		SessionID:             sessionID,
		ToolCallID:            "call_read_artifact",
		Workdir:               root,
		EphemeralArtifactRoot: artifactRoot,
		Store:                 store,
		Config:                cfg,
		Catalog:               catalog,
	}, mustJSON(t, map[string]any{
		"path":        results["shell"].Metadata["artifact_path"],
		"byte_offset": 0,
		"byte_limit":  64,
	}))
	if readErr != nil || readResult.IsError || !strings.Contains(readResult.LLMOutput, strings.Repeat("x", 32)) {
		t.Fatalf("read current command artifact: result=%#v err=%v", readResult, readErr)
	}
}

func TestCommandToolsProductionCodeDoesNotUseCombinedOutput(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "registry.go"))
	if err != nil {
		t.Fatalf("read registry.go: %v", err)
	}
	if bytes.Contains(data, []byte("CombinedOutput(")) {
		t.Fatal("command tools still use execution-after full buffering via CombinedOutput")
	}
}

func smallOutputCollectorConfig() *config.Config {
	cfg := config.Default()
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 512
	cfg.Runtime.ToolOutput.DisplayOutputMaxBytes = 768
	cfg.Runtime.ToolOutput.ArtifactFileMaxBytes = 1024
	cfg.Runtime.ToolOutput.ArtifactSessionMaxBytes = 4096
	cfg.Runtime.ToolOutput.ArtifactMaxFiles = 8
	return cfg
}

func newOutputCollectorSession(t *testing.T, workdir string) (*session.Store, string, string) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	sessionID := session.NewSessionID()
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          workdir,
		Mode:             session.ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	if err := store.Create(meta, session.State{Status: session.StatusRunning, Phase: "tool_execute", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create collector session: %v", err)
	}
	artifactRoot := filepath.Join(store.SessionDir(sessionID), "artifacts", "tool-outputs")
	return store, sessionID, artifactRoot
}

func newOutputCollectorExecContext(t *testing.T, cfg *config.Config) ExecContext {
	t.Helper()
	workdir := t.TempDir()
	store, sessionID, artifactRoot := newOutputCollectorSession(t, workdir)
	return ExecContext{
		SessionID:             sessionID,
		ToolCallID:            "call_collector",
		Workdir:               workdir,
		EphemeralArtifactRoot: artifactRoot,
		Store:                 store,
		Config:                cfg,
	}
}

func assertFinalizedCommandBudget(t *testing.T, result session.ToolResult) {
	t.Helper()
	if len(result.LLMOutput) > 512 || len(result.DisplayOutput) > 768 {
		t.Fatalf("command result exceeded configured inline caps: llm=%d display=%d", len(result.LLMOutput), len(result.DisplayOutput))
	}
	if result.Metadata["tool_output_budget_version"] != session.ToolOutputBudgetVersion {
		t.Fatalf("command result is not finalized with shared budget version: %#v", result.Metadata)
	}
	if commandMetadataInt(t, result.Metadata, "inline_bytes") != len(result.LLMOutput) || commandMetadataInt(t, result.Metadata, "display_inline_bytes") != len(result.DisplayOutput) {
		t.Fatalf("inline metadata mismatch: %#v", result.Metadata)
	}
}

func commandArtifactAbsolutePath(t *testing.T, execCtx ExecContext, result session.ToolResult) string {
	t.Helper()
	path, _ := result.Metadata["artifact_path"].(string)
	if path == "" {
		t.Fatalf("artifact_path missing: %#v", result.Metadata)
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(execCtx.Store.SessionDir(execCtx.SessionID), filepath.FromSlash(path))
}

func commandMetadataInt(t *testing.T, metadata map[string]any, key string) int {
	t.Helper()
	value, ok := metadata[key].(int)
	if !ok {
		t.Fatalf("metadata %s is not int: %#v", key, metadata[key])
	}
	return value
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return data
}
