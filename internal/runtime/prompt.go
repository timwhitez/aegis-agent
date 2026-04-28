package runtime

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
)

var artifactPathPattern = regexp.MustCompile(`(?i)(?:[a-z]:)?(?:/|\.?/)?[a-z0-9._/-]+\.md`)
var explicitInspectionPathPattern = regexp.MustCompile(`(?i)(?:\./)?[a-z0-9._/-]+\.(?:md|go|ts|py|rs|txt|jsonl?|ya?ml|toml|sh)`)
var literalAndSeparatorPattern = regexp.MustCompile(`(?i)\s*,?\s+and\s+`)
var explicitTargetPhrasePattern = regexp.MustCompile(`(?i)(?:原始目标是|目标是|最新目标是|target(?:\s+url)?\s*(?:is|=)|scope\s*(?:is|=))\s*([^\s，。；;]+)`)
var pathLikeTargetPattern = regexp.MustCompile(`/[A-Za-z0-9._~!$&'()*+,;=:@%/-]+`)
var noAuth401Pattern = regexp.MustCompile(`(?is)(去掉\s*authorization|without\s+authorization|no\s+authorization|no\s+auth).{0,120}(401|unauthorized|未授权)`)
var noClearUnauthorizedPattern = regexp.MustCompile(`(?is)(暂未发现.{0,40}未授权|至少受\s*bearer\s*token\s*保护|protected by bearer|bearer token protected)`)

func buildSystemPrompt(workdir, mode, systemOverride string, skillSummaries []skills.Summary, skillTools []skills.CommandTool, state session.State, messages []session.Message, agentContext ...string) string {
	var builder strings.Builder
	agentName := ""
	agentRole := ""
	if len(agentContext) > 0 {
		agentName = strings.TrimSpace(agentContext[0])
	}
	if len(agentContext) > 1 {
		agentRole = strings.TrimSpace(agentContext[1])
	}
	if override := strings.TrimSpace(systemOverride); override != "" {
		builder.WriteString("## User System Instructions\n")
		builder.WriteString(override)
		builder.WriteString("\n\n")
	}
	if mode == session.ModeInit {
		builder.WriteString(buildInitializerPrompt(workdir))
	} else {
		builder.WriteString(fmt.Sprintf("You are a general-purpose CLI agent working in %s.\n", workdir))
		builder.WriteString("Use tools to gather context, edit files, run commands, and complete tasks.\n")
		builder.WriteString("Be concise. Prefer acting over narrating.\n")
		if mode == session.ModeExec {
			builder.WriteString("In exec mode, you must use the finish tool when the task is complete.\n")
		} else {
			builder.WriteString("In run mode, natural pause is allowed, but use finish when the task is clearly complete.\n")
		}
	}
	if role, guidance := roleGuidance(agentRole, agentName); role != "" {
		builder.WriteString("\n## Session Role\n")
		builder.WriteString(fmt.Sprintf("This session is acting as the %s role.\n", role))
		for _, item := range guidance {
			builder.WriteString("- " + item + "\n")
		}
	}
	builder.WriteString("\n## Tool Use\n")
	builder.WriteString("- Tool names are capabilities, not workspace files or shell binaries.\n")
	builder.WriteString("- Workspace boundary is the current workdir. Do not read `../` or absolute paths outside it unless the user explicitly expands scope.\n")
	builder.WriteString("- Use `load_skill` only with exact names listed under Available skills; never invent aliases or legacy skill names.\n")
	if notes := runtimeBehaviorNotes(workdir, mode, messages); len(notes) > 0 {
		builder.WriteString("\n## Runtime Notes\n")
		for _, note := range notes {
			builder.WriteString("- " + note + "\n")
		}
	}
	if len(skillSummaries) > 0 {
		builder.WriteString("\n## Skills\n")
		builder.WriteString("A skill is a local instructions file that may guide how to approach a task.\n")
		builder.WriteString("### Available skills\n")
		for _, summary := range skillSummaries {
			builder.WriteString(fmt.Sprintf("- %s: %s\n", summary.Name, summary.Description))
		}
		builder.WriteString("### How to use skills\n")
		builder.WriteString("- If the user explicitly names a skill, prefer using it for that turn.\n")
		builder.WriteString("- Use `load_skill` tool to load the full skill content when needed, passing the exact listed skill name.\n")
	}
	if len(skillTools) > 0 {
		builder.WriteString("\n## Skill Command Tools\n")
		builder.WriteString("These tools are already loaded into the tool list for this session.\n")
		builder.WriteString("Call them directly by tool name when relevant; they are not files, shell commands, or skills.\n")
		for _, tool := range skillTools {
			builder.WriteString(fmt.Sprintf("- %s: %s\n", tool.Name, tool.Description))
		}
	}
	if state.LastError != "" {
		builder.WriteString("\nPrevious run note:\n")
		builder.WriteString(fmt.Sprintf("- Last error: %s\n", state.LastError))
	}
	if state.IncompleteReason == "incomplete_no_finish" {
		builder.WriteString("- The previous exec-style run ended without an explicit finish tool call. If the task is complete in this run, you must call finish.\n")
	}
	agentsChain := loadAgentsChain(workdir)
	if len(agentsChain) > 0 {
		builder.WriteString("\n## Project Instructions\n")
		for _, item := range agentsChain {
			builder.WriteString(fmt.Sprintf("%s\n", item.Content))
		}
	}
	return strings.TrimSpace(builder.String())
}

func buildInitializerPrompt(workdir string) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("You are a project initializer agent working in %s.\n", workdir))
	builder.WriteString("Set up foundations quickly, keep scope crisp, and leave the workspace ready for follow-on implementation.\n")
	builder.WriteString("Use `feature_list_create` early to capture the roadmap before writing scaffolding.\n")
	builder.WriteString("Do not implement product features yet; focus on project shape, config, scripts, and handoff clarity.\n")
	builder.WriteString("Use `finish` once the repository is initialized and the next implementation step is obvious.\n")
	return builder.String()
}

func runtimeBehaviorNotes(workdir, mode string, messages []session.Message) []string {
	var notes []string
	if note := deliveryNote(workdir, mode, messages); note != "" {
		notes = append(notes, note)
	}
	if note := durableProjectMemoryNote(workdir, messages); note != "" {
		notes = append(notes, note)
	}
	if note := recentSteerNote(messages); note != "" {
		notes = append(notes, note)
	}
	if note := targetConsistencyNote(messages); note != "" {
		notes = append(notes, note)
	}
	if note := exactRequestedArtifactPathNote(workdir, messages); note != "" {
		notes = append(notes, note)
	}
	if note := exactArtifactTemplateNote(workdir, messages); note != "" {
		notes = append(notes, note)
	}
	if note := exactArtifactLiteralNote(workdir, messages); note != "" {
		notes = append(notes, note)
	}
	if note := retrievalBudgetNote(messages); note != "" {
		notes = append(notes, note)
	}
	return notes
}

func deliveryNote(workdir, mode string, messages []session.Message) string {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return ""
	}
	text := messages[idx].Text
	isAudit := looksAuditOrReviewTask(text) && requiresReviewArtifact(text)

	stats := collectRecentToolStats(messages[idx+1:])
	hasArtifact := strings.TrimSpace(stats.DeliverableWritePath) != ""

	if hasArtifact {
		path := displayPromptPath(workdir, stats.DeliverableWritePath)
		if stats.CompletedTodoWrite {
			if mode == session.ModeExec {
				return fmt.Sprintf("A requested artifact was already written to %s and recent todo state is fully completed. Do not do more retrieval. Call finish now unless one required side effect is still missing.", path)
			}
			return fmt.Sprintf("A requested artifact was already written to %s and recent todo state is fully completed. Do not do more retrieval unless one required side effect is still missing.", path)
		}
		if mode == session.ModeExec {
			return fmt.Sprintf("A requested artifact was already written to %s. Do not reopen evidence just to restate it. If bookkeeping is done, call finish now.", path)
		}
		return fmt.Sprintf("A requested artifact was already written to %s. Do not reopen evidence just to restate it; only do the minimum remaining side effects.", path)
	}

	if isAudit {
		if req := exactArtifactTemplateRequirement("", messages); req.Active {
			return "This audit/review task includes an exact opening template or section-order requirement. Preserve the required title/setup block verbatim before findings, then keep the findings section evidence-scoped with Severity, Confidence, Evidence, Snippet, and Why it matters fields."
		}
		if !looksArtifactRequest(text) {
			return "For audit or review tasks, write a durable Markdown artifact before finishing. If the user did not specify a path, prefer reports/final-audit.md. Keep findings first, and separate unresolved questions or inference-limited points from validated findings."
		}
		return "For audit or review deliverables, write findings first. Each finding should record severity, confidence, exact evidence path/line, and a short quoted snippet or identifier from the cited lines either inline in Evidence or in a separate Snippet field, plus why it matters. If you quote or name a snippet, make sure the cited line range literally contains that text; correct the line numbers or widen the range instead of citing a nearby heading or context line. When summarizing delegated, background, or subrun evidence in a parent artifact, inline at least one decisive assertion, event, or snippet from the child/subrun instead of only pointing to the downstream artifact path. Keep unresolved questions or inference-limited points in separate sections instead of mixing them into validated findings."
	}

	return ""
}

func hasProjectMemoryWriteActivity(messages []session.Message) bool {
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if isProjectMemoryPath(path) {
				return true
			}
		}
	}
	return false
}

func hasTaskGraphWriteActivity(messages []session.Message) bool {
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			switch result.Name {
			case "todo_write", "task_create", "task_update":
				return true
			}
		}
	}
	return false
}

func joinPromptItems(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", and ") + ", and " + items[len(items)-1]
}

func recentSteerNote(messages []session.Message) string {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return ""
	}
	msg := messages[idx]
	source, _ := msg.Meta["source"].(string)
	if source != "steer" {
		return ""
	}
	text := quoteForPrompt(msg.Text, 220)
	if interrupt, _ := msg.Meta["interrupt"].(bool); interrupt {
		return fmt.Sprintf("A recent interrupt steer is now the active priority: %s. Stop following the pre-steer exploration plan. If that steer says to use current evidence, write an artifact, or finish, do that before any extra retrieval.", text)
	}
	return fmt.Sprintf("A recent steer updated task priority: %s. Follow that steer first and do only the minimum extra retrieval required for it.", text)
}

type targetConsistencyRequirement struct {
	Active           bool
	Index            int
	Display          string
	TargetLiterals   []string
	ConflictLiterals []string
}

func targetConsistencyNote(messages []session.Message) string {
	req := latestTargetConsistencyRequirement(messages)
	if !req.Active {
		return ""
	}
	return fmt.Sprintf("The latest explicit target anchor is %s. Final reports and durable spec/plan updates must reflect that target before finish.", req.Display)
}

func latestTargetConsistencyRequirement(messages []session.Message) targetConsistencyRequirement {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		source, _ := msg.Meta["source"].(string)
		if source == "harness_reminder" || source == "compaction_summary" {
			continue
		}
		req := targetConsistencyRequirementFromText(msg.Text)
		if req.Active {
			req.Index = i
			return req
		}
	}
	return targetConsistencyRequirement{}
}

func targetConsistencyRequirementFromText(text string) targetConsistencyRequirement {
	var seeds []string
	for _, match := range explicitTargetPhrasePattern.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			seeds = append(seeds, trimTargetToken(match[1]))
		}
	}
	if len(seeds) == 0 {
		return targetConsistencyRequirement{}
	}
	var literals []string
	var display string
	for _, seed := range seeds {
		if strings.TrimSpace(seed) == "" {
			continue
		}
		extracted := targetLiteralsFromSeed(seed)
		if len(extracted) == 0 {
			continue
		}
		if display == "" {
			display = extracted[0]
		}
		literals = append(literals, extracted...)
	}
	literals = uniqueTargetLiterals(literals)
	if len(literals) == 0 {
		return targetConsistencyRequirement{}
	}
	if display == "" {
		display = literals[0]
	}
	return targetConsistencyRequirement{
		Active:           true,
		Display:          display,
		TargetLiterals:   literals,
		ConflictLiterals: conflictTargetLiterals(literals),
	}
}

func targetLiteralsFromSeed(seed string) []string {
	seed = trimTargetToken(seed)
	if seed == "" {
		return nil
	}
	var out []string
	decoded := decodeTargetToken(seed)
	if decoded != "" {
		out = append(out, decoded)
	}
	if strings.Contains(seed, "://") {
		if parsed, err := url.Parse(seed); err == nil {
			for _, values := range parsed.Query() {
				for _, value := range values {
					value = decodeTargetToken(value)
					if strings.HasPrefix(value, "/") {
						out = append(out, value)
					}
				}
			}
			if parsed.Path != "" {
				out = append(out, decodeTargetToken(parsed.Path))
			}
		}
	}
	for _, match := range pathLikeTargetPattern.FindAllString(seed, -1) {
		out = append(out, decodeTargetToken(match))
	}
	if decoded != seed {
		for _, match := range pathLikeTargetPattern.FindAllString(decoded, -1) {
			out = append(out, decodeTargetToken(match))
		}
	}
	return uniqueTargetLiterals(preferSpecificTargetLiterals(out))
}

func trimTargetToken(value string) string {
	return strings.Trim(strings.TrimSpace(value), " \t\r\n`\"'<>()[]{}，。；;,")
}

func decodeTargetToken(value string) string {
	value = trimTargetToken(value)
	if value == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(value); err == nil {
		value = decoded
	}
	return trimTargetToken(strings.ReplaceAll(value, "\\u002f", "/"))
}

func preferSpecificTargetLiterals(items []string) []string {
	var paths []string
	for _, item := range items {
		cleaned := trimTargetToken(item)
		if strings.HasPrefix(cleaned, "/") && !strings.HasPrefix(cleaned, "//") {
			paths = append(paths, cleaned)
		}
	}
	if len(paths) == 0 {
		return items
	}
	longest := 0
	for _, path := range paths {
		if len(path) > longest {
			longest = len(path)
		}
	}
	var out []string
	for _, path := range paths {
		if len(path) == longest {
			out = append(out, path)
		}
	}
	return out
}

func uniqueTargetLiterals(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range items {
		cleaned := trimTargetToken(item)
		if cleaned == "" {
			continue
		}
		key := strings.ToLower(cleaned)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func conflictTargetLiterals(items []string) []string {
	var out []string
	for _, item := range items {
		lowered := strings.ToLower(item)
		for _, pair := range [][2]string{{"/sim", "/list"}, {"/list", "/sim"}} {
			if strings.Contains(lowered, pair[0]) {
				out = append(out, strings.Replace(item, pair[0], pair[1], 1))
			}
		}
	}
	return uniqueTargetLiterals(out)
}

func targetContentSatisfiesRequirement(content string, req targetConsistencyRequirement) bool {
	if !req.Active {
		return true
	}
	haystack := normalizedTargetHaystack(content)
	for _, literal := range req.TargetLiterals {
		if strings.Contains(haystack, strings.ToLower(decodeTargetToken(literal))) {
			return true
		}
	}
	return false
}

func contentHasTargetScopeConflict(content string, req targetConsistencyRequirement) bool {
	if !req.Active || len(req.ConflictLiterals) == 0 {
		return false
	}
	prefix := content
	if len(prefix) > 4000 {
		prefix = prefix[:4000]
	}
	haystack := normalizedTargetHaystack(prefix)
	if targetContentSatisfiesRequirement(content, req) {
		for _, correction := range []string{"旧", "修正", "纠偏", "stale", "wrong", "previous", "corrected"} {
			if strings.Contains(haystack, correction) {
				return false
			}
		}
	}
	for _, conflict := range req.ConflictLiterals {
		conflict = strings.ToLower(decodeTargetToken(conflict))
		if conflict == "" || !strings.Contains(haystack, conflict) {
			continue
		}
		if strings.Contains(haystack, "目标") || strings.Contains(haystack, "target") || strings.Contains(haystack, "scope") || strings.Contains(haystack, "endpoint") {
			return true
		}
	}
	return false
}

func normalizedTargetHaystack(content string) string {
	lowered := strings.ToLower(strings.ReplaceAll(content, "\\u002f", "/"))
	if decoded, err := url.QueryUnescape(lowered); err == nil && decoded != lowered {
		return lowered + "\n" + decoded
	}
	return lowered
}

func targetConsistencyReminder(workdir string, messages []session.Message) harnessReminder {
	req := latestTargetConsistencyRequirement(messages)
	if !req.Active || targetConsistencySatisfiedByMessages(workdir, messages, req) {
		return harnessReminder{}
	}
	return harnessReminder{
		Kind: "target_consistency",
		Text: fmt.Sprintf("Harness reminder: the latest explicit target anchor is %s. Refresh any stale reports/spec.md or reports/plan.md target notes and make the final deliverable use that target before finish.", req.Display),
	}
}

func targetConsistencyGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	req := latestTargetConsistencyRequirement(messages)
	if !req.Active {
		return "", ""
	}
	switch toolName {
	case "write_file", "edit_file":
		target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
		if !ok || (!looksFinalArtifactPath(target.Path) && !isTargetAnchorProjectMemoryPath(target.Path)) {
			return "", ""
		}
		if targetContentSatisfiesRequirement(target.Content, req) && !contentHasTargetScopeConflict(target.Content, req) {
			return "", ""
		}
		return "target_consistency", fmt.Sprintf("Target-consistency guard: the latest explicit target anchor is %s. This artifact still appears to use a stale or missing target. Update the artifact content to reflect %s before writing it.", req.Display, req.Display)
	case "finish":
		if targetConsistencySatisfiedByMessages(workdir, messages, req) {
			return "", ""
		}
		return "target_consistency", fmt.Sprintf("Target-consistency guard: before finishing, write or update the final deliverable so it reflects the latest explicit target anchor %s.", req.Display)
	default:
		return "", ""
	}
}

func targetConsistencySatisfiedByMessages(workdir string, messages []session.Message, req targetConsistencyRequirement) bool {
	pos := latestFinalArtifactWritePosition(messages)
	if !pos.Valid {
		return false
	}
	if valid, ok := pos.Result.Metadata["target_consistency_valid"].(bool); ok && valid {
		return true
	}
	if strings.TrimSpace(pos.Path) != "" {
		if data, err := os.ReadFile(pos.Path); err == nil {
			return targetContentSatisfiesRequirement(string(data), req) && !contentHasTargetScopeConflict(string(data), req)
		}
	}
	_ = workdir
	return false
}

func annotateTargetConsistencyResult(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage, result *session.ToolResult) {
	if result == nil || result.IsError {
		return
	}
	req := latestTargetConsistencyRequirement(messages)
	if !req.Active {
		return
	}
	target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
	if !ok || (!looksFinalArtifactPath(target.Path) && !isTargetAnchorProjectMemoryPath(target.Path)) {
		return
	}
	if !targetContentSatisfiesRequirement(target.Content, req) || contentHasTargetScopeConflict(target.Content, req) {
		return
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["target_consistency_valid"] = true
	result.Metadata["target_consistency_target"] = req.Display
}

func isTargetAnchorProjectMemoryPath(path string) bool {
	lowered := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	for _, suffix := range []string{"reports/spec.md", "reports/plan.md"} {
		if lowered == suffix || strings.HasSuffix(lowered, "/"+suffix) {
			return true
		}
	}
	return false
}

type writePosition struct {
	Valid        bool
	MessageIndex int
	ResultIndex  int
	Path         string
	Result       session.ToolResult
}

func reportConsistencyReminder(workdir string, messages []session.Message) harnessReminder {
	if !finalArtifactStaleAfterSupportingDocs(messages) {
		return harnessReminder{}
	}
	path := latestFinalArtifactWritePosition(messages).Path
	return harnessReminder{
		Kind: "report_consistency",
		Text: fmt.Sprintf("Harness reminder: supporting docs under reports/progress.md or reports/validation.md changed after the final report %s. Reconcile and rewrite the final report before finish.", displayPromptPath(workdir, path)),
	}
}

func reportConsistencyGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	switch toolName {
	case "write_file", "edit_file":
		target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
		if !ok || !looksFinalArtifactPath(target.Path) {
			return "", ""
		}
		if reportContradictsSupportingDocs(workdir, target.Content) {
			return "report_consistency", "Report-consistency guard: the final report claims unauthenticated or anonymous success, but current reports/progress.md or reports/validation.md record no-Authorization 401 / bearer-token protection. Reconcile the conclusion before writing the report."
		}
		return "", ""
	case "finish":
		if finalArtifactStaleAfterSupportingDocs(messages) {
			return "report_consistency", "Report-consistency guard: reports/progress.md or reports/validation.md changed after the final deliverable. Rewrite or edit the final report so the conclusion matches the latest supporting docs before finishing."
		}
		final := latestFinalArtifactWritePosition(messages)
		if !final.Valid || strings.TrimSpace(final.Path) == "" {
			return "", ""
		}
		if data, err := os.ReadFile(final.Path); err == nil && reportContradictsSupportingDocs(workdir, string(data)) {
			return "report_consistency", "Report-consistency guard: the final report contradicts the current validation/progress conclusion about unauthenticated access. Fix the final report before finishing."
		}
		return "", ""
	default:
		return "", ""
	}
}

func finalArtifactStaleAfterSupportingDocs(messages []session.Message) bool {
	final := latestFinalArtifactWritePosition(messages)
	support := latestSupportingDocWritePosition(messages)
	return final.Valid && support.Valid && positionAfter(support, final)
}

func latestFinalArtifactWritePosition(messages []session.Message) writePosition {
	return latestWritePosition(messages, func(path string, _ session.ToolResult) bool {
		return looksFinalArtifactPath(path)
	})
}

func latestSupportingDocWritePosition(messages []session.Message) writePosition {
	return latestWritePosition(messages, func(path string, _ session.ToolResult) bool {
		return isProjectMemoryPath(path) && !strings.HasSuffix(strings.ToLower(filepath.ToSlash(path)), "/reports/spec.md") && !strings.HasSuffix(strings.ToLower(filepath.ToSlash(path)), "/reports/plan.md")
	})
}

func latestWritePosition(messages []session.Message, predicate func(string, session.ToolResult) bool) writePosition {
	var out writePosition
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for j, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if strings.TrimSpace(path) == "" || !predicate(path, result) {
				continue
			}
			out = writePosition{
				Valid:        true,
				MessageIndex: i,
				ResultIndex:  j,
				Path:         path,
				Result:       result,
			}
		}
	}
	return out
}

func positionAfter(left, right writePosition) bool {
	if !left.Valid || !right.Valid {
		return false
	}
	if left.MessageIndex != right.MessageIndex {
		return left.MessageIndex > right.MessageIndex
	}
	return left.ResultIndex > right.ResultIndex
}

func reportContradictsSupportingDocs(workdir, reportContent string) bool {
	if !containsUnauthorizedSuccessClaim(reportContent) {
		return false
	}
	support := readSupportingDocs(workdir)
	if strings.TrimSpace(support) == "" {
		return false
	}
	return supportingDocsDenyUnauthorized(support)
}

func readSupportingDocs(workdir string) string {
	var builder strings.Builder
	for _, rel := range []string{filepath.Join("reports", "progress.md"), filepath.Join("reports", "validation.md")} {
		data, err := os.ReadFile(filepath.Join(workdir, rel))
		if err == nil {
			builder.WriteString("\n")
			builder.Write(data)
		}
	}
	return builder.String()
}

func containsUnauthorizedSuccessClaim(content string) bool {
	lowered := strings.ToLower(content)
	authClaim := strings.Contains(lowered, "无认证") ||
		strings.Contains(lowered, "匿名") ||
		strings.Contains(lowered, "未授权") ||
		strings.Contains(lowered, "no authorization") ||
		strings.Contains(lowered, "no auth") ||
		strings.Contains(lowered, "anonymous")
	success := strings.Contains(lowered, "code=0") ||
		strings.Contains(lowered, `"code":0`) ||
		strings.Contains(lowered, `"code": 0`) ||
		strings.Contains(lowered, "200") ||
		strings.Contains(lowered, "成功") ||
		strings.Contains(lowered, "可访问")
	return authClaim && success
}

func supportingDocsDenyUnauthorized(content string) bool {
	lowered := strings.ToLower(content)
	return noAuth401Pattern.MatchString(lowered) || noClearUnauthorizedPattern.MatchString(lowered)
}

func countToolResults(messages []session.Message) int {
	total := 0
	for _, msg := range messages {
		if msg.Role == "tool" {
			total += len(msg.ToolResults)
		}
	}
	return total
}

func countCompactionSummaries(messages []session.Message) int {
	total := 0
	for _, msg := range messages {
		source, _ := msg.Meta["source"].(string)
		if msg.Role == "user" && source == "compaction_summary" {
			total++
		}
	}
	return total
}

func retrievalBudgetNote(messages []session.Message) string {
	start := latestExternalInstructionIndex(messages)
	if start >= 0 {
		start++
	} else {
		start = 0
	}
	stats := collectRecentToolStats(messages[start:])
	if stats.RetrievalCount < 6 {
		return ""
	}
	if stats.ActionCount > 0 && stats.RepeatedReadCount == 0 && stats.DiscoveryCount < 4 {
		return ""
	}
	if stats.UniqueReadPaths > 0 && stats.RepeatedReadCount > 0 {
		return fmt.Sprintf("Recent work already used %d read-only tool calls across %d tracked file paths, including %d reread(s). Do not reread files just to reconfirm earlier evidence. Keep runtime-reserved final proof rereads for the smallest exact line window that still matters. Use current evidence unless one missing exact line would materially change the answer.", stats.RetrievalCount, stats.UniqueReadPaths, stats.RepeatedReadCount)
	}
	if stats.UniqueReadPaths > 0 {
		return fmt.Sprintf("Recent work already used %d read-only tool calls across %d tracked file paths. Use current evidence unless one missing exact line would materially change the answer, and prefer acting now over more exploration.", stats.RetrievalCount, stats.UniqueReadPaths)
	}
	return fmt.Sprintf("Recent work already used %d read-only tool calls. Use current evidence and prefer acting now over more exploration.", stats.RetrievalCount)
}

func exactRequestedArtifactPathNote(workdir string, messages []session.Message) string {
	paths := requestedArtifactPaths(workdir, messages)
	if len(paths) == 0 {
		return ""
	}
	display := displayRequestedArtifactPaths(workdir, paths)
	if len(display) == 1 {
		return fmt.Sprintf("The latest external instruction requested the deliverable at %s. Use that exact path and do not substitute a same-named file elsewhere.", display[0])
	}
	return fmt.Sprintf("The latest external instruction requested deliverables at %s. Use those exact paths and do not substitute same-named files elsewhere.", joinPromptItems(display))
}

func exactArtifactTemplateNote(workdir string, messages []session.Message) string {
	req := exactArtifactTemplateRequirement(workdir, messages)
	if !req.Active {
		return ""
	}
	display := displayRequestedArtifactPaths(workdir, req.Paths)
	return fmt.Sprintf("This task has an exact artifact opening template for %s. Keep the required opening block verbatim and before any findings or alternate section order.", joinPromptItems(display))
}

func exactArtifactLiteralNote(workdir string, messages []session.Message) string {
	req := exactArtifactLiteralRequirement(workdir, messages)
	if !req.Active {
		return ""
	}
	display := displayRequestedArtifactPaths(workdir, req.Paths)
	required := make([]string, 0, len(req.RequiredLiterals))
	for _, item := range req.RequiredLiterals {
		required = append(required, fmt.Sprintf("`%s`", item))
	}
	return fmt.Sprintf("This task requires exact literal anchors in %s. Preserve these exact strings verbatim in the deliverable before finishing: %s.", joinPromptItems(display), joinPromptItems(required))
}

func latestExternalInstructionIndex(messages []session.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		source, _ := messages[i].Meta["source"].(string)
		if source == "harness_reminder" || source == "compaction_summary" {
			continue
		}
		return i
	}
	return -1
}

type toolStats struct {
	RetrievalCount       int
	DiscoveryCount       int
	ActionCount          int
	UniqueReadPaths      int
	RepeatedReadCount    int
	DeliverableWritePath string
	CompletedTodoWrite   bool
}

func collectRecentToolStats(messages []session.Message) toolStats {
	readPaths := map[string]int{}
	stats := toolStats{}
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			switch {
			case isRetrievalResult(result):
				stats.RetrievalCount++
				if isDiscoveryTool(result.Name) {
					stats.DiscoveryCount++
				}
				if result.Name == "read_file" {
					if path, _ := result.Metadata["path"].(string); strings.TrimSpace(path) != "" {
						readPaths[path]++
					}
				}
			case isActionTool(result.Name):
				stats.ActionCount++
				if result.Name == "write_file" {
					if path, _ := result.Metadata["path"].(string); looksFinalArtifactPath(path) {
						stats.DeliverableWritePath = path
					}
				}
				if result.Name == "todo_write" && todoListAllCompleted(result.DisplayOutput) {
					stats.CompletedTodoWrite = true
				}
			}
		}
	}
	stats.UniqueReadPaths = len(readPaths)
	for _, count := range readPaths {
		if count > 1 {
			stats.RepeatedReadCount += count - 1
		}
	}
	return stats
}

func isDiscoveryTool(name string) bool {
	switch name {
	case "glob", "grep", "grep_files":
		return true
	default:
		return false
	}
}

func isRetrievalTool(name string) bool {
	switch name {
	case "read_file", "glob", "grep", "grep_files", "load_skill", "todo_read", "task_list", "task_get":
		return true
	default:
		return false
	}
}

func isRetrievalResult(result session.ToolResult) bool {
	if isRetrievalTool(result.Name) {
		return true
	}
	return shellResultIsReadOnlyRetrieval(result)
}

func isActionTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "finish", "todo_write", "task_create", "task_update", "agent_spawn":
		return true
	default:
		return false
	}
}

func quoteForPrompt(text string, limit int) string {
	cleaned := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if cleaned == "" {
		return `""`
	}
	if limit > 0 && len(cleaned) > limit {
		cleaned = cleaned[:limit-3] + "..."
	}
	return fmt.Sprintf("%q", cleaned)
}

type harnessReminder struct {
	Kind string
	Text string
}

func nextHarnessReminder(workdir, mode string, messages []session.Message) harnessReminder {
	if reminder := steerCompletionReminder(workdir, mode, messages); reminder.Text != "" && !shouldSuppressHarnessReminder(messages, reminder) {
		return reminder
	}
	if reminder := targetConsistencyReminder(workdir, messages); reminder.Text != "" && !shouldSuppressHarnessReminder(messages, reminder) {
		return reminder
	}
	if reminder := reportConsistencyReminder(workdir, messages); reminder.Text != "" && !shouldSuppressHarnessReminder(messages, reminder) {
		return reminder
	}
	if reminder := artifactCompletionReminder(workdir, mode, messages); reminder.Text != "" && !shouldSuppressHarnessReminder(messages, reminder) {
		return reminder
	}
	if reminder := largeProjectCoordinationReminder(workdir, messages); reminder.Text != "" && !shouldSuppressHarnessReminder(messages, reminder) {
		return reminder
	}
	if reminder := retrievalTailReminder(messages); reminder.Text != "" && !shouldSuppressHarnessReminder(messages, reminder) {
		return reminder
	}
	if reminder := projectMemoryRefreshReminder(workdir, messages); reminder.Text != "" && !shouldSuppressHarnessReminder(messages, reminder) {
		return reminder
	}
	return harnessReminder{}
}

func shouldSuppressHarnessReminder(messages []session.Message, reminder harnessReminder) bool {
	if reminder.Kind == "steer_completion_escalated" {
		return false
	}
	return hasHarnessReminderKindSinceLatestExternal(messages, reminder.Kind)
}

func artifactCompletionReminder(workdir, mode string, messages []session.Message) harnessReminder {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return harnessReminder{}
	}
	if !looksArtifactRequest(messages[idx].Text) {
		return harnessReminder{}
	}
	stats := collectRecentToolStats(messages[idx+1:])
	if strings.TrimSpace(stats.DeliverableWritePath) == "" {
		return harnessReminder{}
	}
	path := displayPromptPath(workdir, stats.DeliverableWritePath)
	var note string
	if stats.CompletedTodoWrite {
		if mode == session.ModeExec {
			note = fmt.Sprintf("A requested artifact was already written to %s and recent todo state is fully completed. Do not do more retrieval. Call finish now unless one required side effect is still missing.", path)
		} else {
			note = fmt.Sprintf("A requested artifact was already written to %s and recent todo state is fully completed. Do not do more retrieval unless one required side effect is still missing.", path)
		}
	} else {
		if mode == session.ModeExec {
			note = fmt.Sprintf("A requested artifact was already written to %s. Do not reopen evidence just to restate it. If bookkeeping is done, call finish now.", path)
		} else {
			note = fmt.Sprintf("A requested artifact was already written to %s. Do not reopen evidence just to restate it; only do the minimum remaining side effects.", path)
		}
	}
	return harnessReminder{
		Kind: "artifact_written",
		Text: "Harness reminder: " + note,
	}
}

func largeProjectCoordinationReminder(workdir string, messages []session.Message) harnessReminder {
	if !looksLargeProjectTask(messages) {
		return harnessReminder{}
	}
	if hasProjectMemoryWriteActivity(messages) || hasTaskGraphWriteActivity(messages) {
		return harnessReminder{}
	}
	if present := loadProjectMemoryStack(workdir).PresentPaths(); len(present) > 0 {
		return harnessReminder{}
	}
	stats := collectRecentToolStats(messages)
	if stats.RetrievalCount < 4 && stats.UniqueReadPaths < 2 && stats.DiscoveryCount < 2 {
		return harnessReminder{}
	}
	return harnessReminder{
		Kind: "large_project_coordination",
		Text: "Harness reminder: this looks like a large project task. Before more repo-scale retrieval or implementation, externalize a durable project-memory stack at reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md, then keep a durable task board aligned with the written plan via todo_write plus task_create/task_update.",
	}
}

func projectMemoryRefreshReminder(workdir string, messages []session.Message) harnessReminder {
	need := projectMemoryRefreshNeed(workdir, messages)
	if !need.Active {
		return harnessReminder{}
	}
	return harnessReminder{
		Kind: "project_memory_refresh",
		Text: "Harness reminder: recent implementation or validation work outpaced the durable project-memory stack. For durable handoff or finalization, " + projectMemoryRefreshInstruction(need) + " so the next session inherits current progress and QA state. If you delegate before refreshing it, include the current progress and validation context directly in the child prompt.",
	}
}

func steerCompletionReminder(workdir, mode string, messages []session.Message) harnessReminder {
	directive := latestInterruptSteerDirective(messages)
	if !directive.Active || (!directive.UseCurrentEvidence && !directive.WriteArtifact && !directive.Finish) {
		return harnessReminder{}
	}
	state := collectSteerDirectiveState(messages[directive.Index+1:])
	if state.HasArtifactWrite || state.HasFinish {
		return harnessReminder{}
	}
	goal := directive.promptGoal()
	if state.BookkeepingCount > 0 || state.BlockedDetourCount > 0 || state.AssistantTurnsWithoutDelivery > 0 {
		return harnessReminder{
			Kind: "steer_completion_escalated",
			Text: "Harness reminder: the latest interrupt steer explicitly redirected this run to " + goal + ". A non-delivery reply, bookkeeping step, or blocked detour already happened after that steer. Do not spend another turn on retrieval, todo/task bookkeeping, skill loading, or narration. Deliver now with current evidence.",
		}
	}
	return harnessReminder{
		Kind: "steer_completion",
		Text: "Harness reminder: the latest interrupt steer explicitly redirected this run to " + goal + ". Do not do more retrieval, todo/task bookkeeping, or skill loading before delivery. Use current evidence and act now.",
	}
}

func retrievalTailReminder(messages []session.Message) harnessReminder {
	note := retrievalBudgetNote(messages)
	if note == "" {
		return harnessReminder{}
	}
	return harnessReminder{
		Kind: "retrieval_tail",
		Text: "Harness reminder: " + note + " If one missing exact line still matters, use at most two narrowly targeted read_file calls. Broad discovery is no longer allowed. If the task is complete after acting, call finish.",
	}
}

func toolGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage, yoloOpt ...bool) (string, string) {
	yolo := len(yoloOpt) > 0 && yoloOpt[0]
	if kind, text := explicitInspectionScopeGuard(workdir, messages, toolName, rawArgs); text != "" {
		return kind, text
	}
	if kind, text := exactArtifactPathGuard(workdir, messages, toolName, rawArgs); text != "" {
		return kind, text
	}
	if kind, text := exactArtifactTemplateGuard(workdir, messages, toolName, rawArgs); text != "" {
		return kind, text
	}
	if kind, text := exactArtifactLiteralGuard(workdir, messages, toolName, rawArgs); text != "" {
		return kind, text
	}
	if kind, text := targetConsistencyGuard(workdir, messages, toolName, rawArgs); text != "" {
		return kind, text
	}
	if kind, text := reportConsistencyGuard(workdir, messages, toolName, rawArgs); text != "" {
		return kind, text
	}
	if !yolo {
		if kind, text := reviewArtifactGuard(workdir, messages, toolName, rawArgs); text != "" {
			return kind, text
		}
	}
	if kind, text := deferredInterruptFinishGuard(messages, toolName); text != "" {
		return kind, text
	}
	if kind, text := steerCompletionGuard(messages, toolName, rawArgs); text != "" {
		return kind, text
	}
	if yolo {
		return "", ""
	}
	reminderIndex, reminderKind := latestHarnessReminderSinceLatestExternal(messages)
	if reminderIndex < 0 {
		return "", ""
	}
	switch reminderKind {
	case "artifact_written":
		if !isReadOnlyRetrievalCall(toolName, rawArgs) {
			return "", ""
		}
		return reminderKind, "Retrieval guard: a requested artifact was already written. Do not do more read-only exploration. Use current evidence, update bookkeeping if needed, and finish."
	case "project_memory_refresh":
		return projectMemoryRefreshGuard(workdir, messages, toolName, rawArgs)
	case "retrieval_tail":
		return retrievalTailGuard(workdir, messages, reminderIndex, toolName, rawArgs)
	default:
		return "", ""
	}
}

type explicitInspectionScope struct {
	Active bool
	Paths  []string
}

func explicitInspectionScopeGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	scope := latestExplicitInspectionScope(messages)
	if !scope.Active {
		return "", ""
	}
	display := make([]string, 0, len(scope.Paths))
	for _, path := range scope.Paths {
		display = append(display, displayPromptPath(workdir, path))
	}
	switch toolName {
	case "glob", "grep_files":
		return "explicit_scope", fmt.Sprintf("Inspection-scope guard: the latest external instruction already enumerated exact files (%s). Do not use %s for directory discovery. Use read_file or grep directly on those paths instead.", joinPromptItems(display), toolName)
	case "shell":
		if requestedShellCommandIsReadOnly(rawArgs) {
			return "explicit_scope", fmt.Sprintf("Inspection-scope guard: the latest external instruction already enumerated exact files (%s). Do not use read-only shell search here; use read_file or grep directly on those paths instead.", joinPromptItems(display))
		}
	}
	return "", ""
}

func latestExplicitInspectionScope(messages []session.Message) explicitInspectionScope {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return explicitInspectionScope{}
	}
	text := messages[idx].Text
	lowered := strings.ToLower(text)
	if !strings.Contains(lowered, "inspect only") &&
		!strings.Contains(lowered, "only inspect") {
		return explicitInspectionScope{}
	}
	seen := map[string]struct{}{}
	var paths []string
	for _, match := range explicitInspectionPathPattern.FindAllString(text, -1) {
		candidate := strings.TrimSpace(strings.Trim(match, ".,;:()[]{}<>`'\""))
		if candidate == "" {
			continue
		}
		candidate = filepath.ToSlash(candidate)
		if strings.HasPrefix(candidate, "./") {
			candidate = strings.TrimPrefix(candidate, "./")
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		paths = append(paths, candidate)
	}
	if len(paths) == 0 {
		return explicitInspectionScope{}
	}
	return explicitInspectionScope{Active: true, Paths: paths}
}

func deferredInterruptFinishGuard(messages []session.Message, toolName string) (string, string) {
	if toolName != "finish" {
		return "", ""
	}
	directive := latestInterruptSteerDirective(messages)
	if !directive.Active || !directive.DeferFinish {
		return "", ""
	}
	return "steer_deferred_finish", "Completion guard: the latest interrupt steer explicitly said to stop without finishing so a later continue can close the task. Do not call finish in this run. Refresh any requested artifacts, then stop and wait for continue."
}

func projectMemoryRefreshGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	need := projectMemoryRefreshNeed(workdir, messages)
	if !need.Active {
		return "", ""
	}
	if isProjectMemoryWriteRequested(workdir, toolName, rawArgs) {
		return "", ""
	}
	switch {
	case isReadOnlyRetrievalCall(toolName, rawArgs):
		return "project_memory_refresh", "Project-memory guard: recent implementation or validation work outpaced the durable handoff files. " + projectMemoryRefreshInstruction(need) + " before more read-only retrieval."
	case toolName == "finish":
		return "project_memory_refresh", "Project-memory guard: before finishing this large task, " + projectMemoryRefreshInstruction(need) + " so progress and validation handoff state matches the latest work."
	default:
		return "", ""
	}
}

func exactArtifactPathGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	requested := requestedArtifactPaths(workdir, messages)
	if len(requested) == 0 {
		return "", ""
	}
	switch toolName {
	case "write_file", "edit_file":
		_, hasPath, pathErr := requestedArtifactPath(workdir, toolName, rawArgs)
		if hasPath && pathErr != nil {
			return "artifact_path", fmt.Sprintf("Artifact path guard: requested writes must stay within the workspace. Use the exact requested in-workspace path %s instead.", joinPromptItems(displayRequestedArtifactPaths(workdir, requested)))
		}
		target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
		if !ok || isProjectMemoryPath(target.Path) || !looksFinalArtifactPath(target.Path) || pathInList(target.Path, requested) {
			return "", ""
		}
		return "artifact_path", fmt.Sprintf("Artifact path guard: the latest external instruction requested the deliverable at %s. Do not write the final artifact to %s. Use the exact requested path instead.", joinPromptItems(displayRequestedArtifactPaths(workdir, requested)), displayPromptPath(workdir, target.Path))
	case "finish":
		if anyRequestedArtifactWritten(messages, requested) {
			return "", ""
		}
		return "artifact_path", fmt.Sprintf("Artifact path guard: before finishing, write the requested deliverable to %s.", joinPromptItems(displayRequestedArtifactPaths(workdir, requested)))
	default:
		return "", ""
	}
}

func exactArtifactTemplateGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	req := exactArtifactTemplateRequirement(workdir, messages)
	if !req.Active {
		return "", ""
	}
	switch toolName {
	case "write_file", "edit_file":
		target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
		if !ok || !pathInList(target.Path, req.Paths) {
			return "", ""
		}
		if validateExactArtifactTemplate(target.Content, req) {
			return "", ""
		}
		return "artifact_template", "Artifact template guard: this deliverable has an exact required opening block. Preserve the requested title and setup lines verbatim before any findings or alternate section headings."
	case "finish":
		if exactArtifactTemplateSatisfied(messages, req) {
			return "", ""
		}
		return "artifact_template", "Artifact template guard: before finishing, write the requested deliverable with the exact required opening block and section order."
	default:
		return "", ""
	}
}

func exactArtifactTemplateSatisfied(messages []session.Message, req exactArtifactTemplate) bool {
	start := latestExternalInstructionIndex(messages)
	if start >= 0 {
		start++
	} else {
		start = 0
	}
	for _, msg := range messages[start:] {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if !pathInList(path, req.Paths) {
				continue
			}
			valid, _ := result.Metadata["exact_template_valid"].(bool)
			if valid {
				return true
			}
		}
	}
	return false
}

func exactArtifactLiteralGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	req := exactArtifactLiteralRequirement(workdir, messages)
	if !req.Active {
		return "", ""
	}
	switch toolName {
	case "write_file", "edit_file":
		target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
		if !ok || !pathInList(target.Path, req.Paths) {
			return "", ""
		}
		missing := missingExactArtifactLiterals(target.Content, req)
		if len(missing) == 0 {
			return "", ""
		}
		return "artifact_literal", fmt.Sprintf("Artifact literal guard: this deliverable must preserve the exact literal anchors %s. Add the missing literal strings before writing the final artifact.", joinPromptItems(quotePromptItems(missing)))
	case "finish":
		if exactArtifactLiteralSatisfied(messages, req) {
			return "", ""
		}
		return "artifact_literal", fmt.Sprintf("Artifact literal guard: before finishing, write the requested deliverable with the exact literal anchors %s.", joinPromptItems(quotePromptItems(req.RequiredLiterals)))
	default:
		return "", ""
	}
}

func exactArtifactLiteralSatisfied(messages []session.Message, req exactArtifactLiteral) bool {
	start := latestExternalInstructionIndex(messages)
	if start >= 0 {
		start++
	} else {
		start = 0
	}
	for _, msg := range messages[start:] {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if !pathInList(path, req.Paths) {
				continue
			}
			valid, _ := result.Metadata["exact_literals_valid"].(bool)
			if valid {
				return true
			}
		}
	}
	return false
}

func steerCompletionGuard(messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	_, reminderKind := latestHarnessReminderSinceLatestExternal(messages)
	if reminderKind != "steer_completion" && reminderKind != "steer_completion_escalated" {
		return "", ""
	}
	if isReadOnlyRetrievalCall(toolName, rawArgs) {
		return reminderKind, "Completion guard: the latest interrupt steer already told you to use current evidence and deliver. Do not do more retrieval. Write the requested artifact, make the minimal required edit, or call finish."
	}
	if isCompletionDetourTool(toolName) {
		return reminderKind, "Completion guard: the latest interrupt steer already redirected this run to immediate delivery. Do not spend another turn on todo/task bookkeeping or skill loading before writing the artifact or finishing."
	}
	return "", ""
}

func retrievalTailGuard(workdir string, messages []session.Message, reminderIndex int, toolName string, rawArgs json.RawMessage) (string, string) {
	if !isReadOnlyRetrievalCall(toolName, rawArgs) {
		return "", ""
	}
	switch toolName {
	case "glob", "grep", "grep_files":
		if allowReviewArtifactRepairSearch(workdir, messages, reminderIndex, toolName, rawArgs) {
			return "", ""
		}
		return "retrieval_tail", "Retrieval guard: broad discovery is blocked after the recent harness reminder. Use current evidence or at most two narrowly targeted read_file calls on new or non-overlapping slices before acting."
	case "shell":
		return "retrieval_tail", "Retrieval guard: read-only shell inspection is blocked after the recent harness reminder. Use current evidence or at most two narrowly targeted native read_file calls on new or non-overlapping slices before acting."
	case "read_file":
		requestedWindow, ok := requestedReadWindow(workdir, rawArgs)
		if !ok {
			return "retrieval_tail", "Retrieval guard: post-reminder read_file must target one concrete path. If you cannot name the exact file, act with current evidence."
		}
		priorReads := collectReadWindows(messages[:reminderIndex])
		postReminderReads := collectReadWindows(messages[reminderIndex+1:])
		priorReadSeq := collectReadWindowSequence(messages[:reminderIndex])
		postReminderReadSeq := collectReadWindowSequence(messages[reminderIndex+1:])
		if allowReviewArtifactRepairRead(messages, reminderIndex, requestedWindow, priorReadSeq, postReminderReadSeq) {
			return "", ""
		}
		if allowReservedFinalProofRead(requestedWindow, priorReadSeq, postReminderReadSeq) {
			return "", ""
		}
		if overlapsAnyWindow(postReminderReads[requestedWindow.Path], requestedWindow) {
			return "retrieval_tail", "Retrieval guard: after the recent harness reminder, rereading the same file slice is blocked. Use current evidence and act now."
		}
		if overlapsAnyWindow(priorReads[requestedWindow.Path], requestedWindow) {
			return "retrieval_tail", "Retrieval guard: after the recent harness reminder, rereading an already inspected file slice is blocked. Use current evidence and act now."
		}
		postReminderCount := countExploratoryReadWindows(postReminderReadSeq, priorReadSeq)
		if postReminderCount >= 3 {
			return "retrieval_tail", "Retrieval guard: after the recent harness reminder, you already used the maximum targeted read_file budget. Stop exploring and act with current evidence."
		}
		if postReminderCount >= 2 && !(isDurableGuidancePath(requestedWindow.Path) && countGuidanceReadWindows(postReminderReadSeq, priorReadSeq) == 0) {
			return "retrieval_tail", "Retrieval guard: after the recent harness reminder, you already used the two allowed targeted read_file calls. Stop exploring and act with current evidence."
		}
		return "", ""
	default:
		return "", ""
	}
}

func allowReviewArtifactRepairRead(messages []session.Message, reminderIndex int, requestedWindow readWindow, priorReadSeq, postReminderReadSeq []readWindow) bool {
	if !isReservedFinalProofWindow(requestedWindow) {
		return false
	}
	if !hasPreviouslyInspectedWindow(requestedWindow, priorReadSeq, postReminderReadSeq) {
		return false
	}
	reviewArtifactIndex := latestReviewArtifactRepairIndex(messages)
	if reviewArtifactIndex <= reminderIndex {
		return false
	}
	if len(postReminderReadSeq) >= reservedFinalProofReadBudget() {
		return false
	}
	return true
}

func allowReviewArtifactRepairSearch(workdir string, messages []session.Message, reminderIndex int, toolName string, rawArgs json.RawMessage) bool {
	if toolName != "grep" {
		return false
	}
	reviewArtifactIndex := latestReviewArtifactRepairIndex(messages)
	if reviewArtifactIndex <= reminderIndex {
		return false
	}
	path, ok := requestedGrepPath(workdir, rawArgs)
	if !ok {
		return false
	}
	priorReadSeq := collectReadWindowSequence(messages[:reminderIndex])
	postReminderReadSeq := collectReadWindowSequence(messages[reminderIndex+1:])
	return hasPreviouslyInspectedPath(path, priorReadSeq, postReminderReadSeq)
}

func isProjectMemoryWriteRequested(workdir, toolName string, rawArgs json.RawMessage) bool {
	path, hasPath, err := requestedArtifactPath(workdir, toolName, rawArgs)
	if !hasPath || err != nil {
		return false
	}
	return isProjectMemoryPath(path)
}

func latestReviewArtifactRepairIndex(messages []session.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" {
			continue
		}
		for _, result := range messages[i].ToolResults {
			if !result.IsError {
				continue
			}
			guard, _ := result.Metadata["guard"].(string)
			if guard != "review_artifact" {
				continue
			}
			if result.Name == "write_file" || result.Name == "edit_file" {
				return i
			}
		}
	}
	return -1
}

func allowReservedFinalProofRead(requestedWindow readWindow, priorReadSeq, postReminderReadSeq []readWindow) bool {
	if !isReservedFinalProofWindow(requestedWindow) {
		return false
	}
	if !hasPreviouslyInspectedWindow(requestedWindow, priorReadSeq, postReminderReadSeq) {
		return false
	}
	if overlapsReservedFinalProofRead(postReminderReadSeq, priorReadSeq, requestedWindow) {
		return false
	}
	return countReservedFinalProofReads(postReminderReadSeq, priorReadSeq) < reservedFinalProofReadBudget()
}

func isReservedFinalProofWindow(window readWindow) bool {
	span := window.End - window.Offset
	return span > 0 && span <= 40
}

func reservedFinalProofReadBudget() int {
	return 2
}

func hasHarnessReminderKindSinceLatestExternal(messages []session.Message, kind string) bool {
	_, reminderKind := latestHarnessReminderSinceLatestExternal(messages)
	return reminderKind == kind
}

func latestHarnessReminderSinceLatestExternal(messages []session.Message) (int, string) {
	start := latestExternalInstructionIndex(messages)
	if start >= 0 {
		start++
	} else {
		start = 0
	}
	lastIndex := -1
	lastKind := ""
	for i := start; i < len(messages); i++ {
		if messages[i].Role != "user" {
			continue
		}
		source, _ := messages[i].Meta["source"].(string)
		if source != "harness_reminder" {
			continue
		}
		lastIndex = i
		lastKind, _ = messages[i].Meta["kind"].(string)
	}
	return lastIndex, lastKind
}

func looksAuditOrReviewTask(text string) bool {
	lowered := strings.ToLower(text)
	if explicitNonReviewTaskOptOut(lowered) {
		return false
	}
	for _, token := range []string{
		"audit",
		"findings",
		"finding",
		"drift",
		"traceability",
		"gap",
		"risk",
		"alignment",
		"aligned",
		"审计",
		"评审",
		"复核",
		"风险",
		"差异",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	for _, phrase := range []string{
		"review the",
		"review this",
		"review only",
		"code review",
		"repo review",
		"api review",
		"task review",
		"security review",
		"design review",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

func explicitNonReviewTaskOptOut(lowered string) bool {
	for _, phrase := range []string{
		"not a review or audit task",
		"not a review task",
		"not an audit task",
		"this is not a review or audit task",
		"this is not a review task",
		"this is not an audit task",
		"do not treat this as a review",
		"do not treat this as an audit",
		"不是审计任务",
		"不是评审任务",
		"不是 review",
		"不是 audit",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

func looksArtifactRequest(text string) bool {
	lowered := strings.ToLower(text)
	for _, token := range []string{
		".md",
		"/reports/",
		"/artifacts/",
		"artifact",
		"report",
		"summary",
		"brief",
		"write ",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func requestedArtifactPaths(workdir string, messages []session.Message) []string {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return nil
	}
	return requestedArtifactPathsFromText(workdir, messages[idx].Text)
}

func requestedArtifactPathsFromText(workdir, text string) []string {
	if !looksArtifactRequest(text) {
		return nil
	}
	lines := strings.Split(text, "\n")
	paths := make([]string, 0, 2)
	for _, line := range lines {
		segment := artifactInstructionSegment(line)
		if segment == "" {
			continue
		}
		for _, match := range artifactPathPattern.FindAllString(segment, -1) {
			candidate := strings.Trim(match, " `\"'.,;:()[]{}")
			if candidate == "" {
				continue
			}
			resolved, ok := cleanRequestedPath(workdir, candidate)
			if !ok || !looksFinalArtifactPath(resolved) {
				continue
			}
			paths = append(paths, resolved)
		}
	}
	return uniqueCleanPaths(paths)
}

type exactArtifactTemplate struct {
	Active        bool
	Paths         []string
	RequiredLines []string
}

type exactArtifactLiteral struct {
	Active           bool
	Paths            []string
	RequiredLiterals []string
}

func exactArtifactTemplateRequirement(workdir string, messages []session.Message) exactArtifactTemplate {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return exactArtifactTemplate{}
	}
	return exactArtifactTemplateFromText(workdir, messages[idx].Text)
}

func exactArtifactTemplateFromText(workdir, text string) exactArtifactTemplate {
	if !looksArtifactRequest(text) {
		return exactArtifactTemplate{}
	}
	requiredLines := exactArtifactTemplateLines(text)
	if len(requiredLines) == 0 {
		return exactArtifactTemplate{}
	}
	paths := requestedArtifactPathsFromText(workdir, text)
	if strings.TrimSpace(workdir) == "" {
		paths = extractLiteralArtifactPaths(text)
	}
	if len(paths) == 0 {
		return exactArtifactTemplate{}
	}
	return exactArtifactTemplate{
		Active:        true,
		Paths:         uniqueCleanPaths(paths),
		RequiredLines: requiredLines,
	}
}

func exactArtifactLiteralRequirement(workdir string, messages []session.Message) exactArtifactLiteral {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return exactArtifactLiteral{}
	}
	return exactArtifactLiteralFromText(workdir, messages[idx].Text)
}

func exactArtifactLiteralFromText(workdir, text string) exactArtifactLiteral {
	if !looksArtifactRequest(text) {
		return exactArtifactLiteral{}
	}
	required := exactArtifactRequiredLiterals(text)
	if len(required) == 0 {
		return exactArtifactLiteral{}
	}
	paths := requestedArtifactPathsFromText(workdir, text)
	if strings.TrimSpace(workdir) == "" {
		paths = extractLiteralArtifactPaths(text)
	}
	if len(paths) == 0 {
		return exactArtifactLiteral{}
	}
	return exactArtifactLiteral{
		Active:           true,
		Paths:            uniqueCleanPaths(paths),
		RequiredLiterals: required,
	}
}

func exactArtifactTemplateLines(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	trigger := -1
	for i, line := range lines {
		lowered := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(lowered, "must begin exactly with these lines") || strings.Contains(lowered, "exactly this template") {
			trigger = i
			break
		}
	}
	if trigger < 0 {
		return nil
	}
	collected := make([]string, 0, 8)
	started := false
	for _, line := range lines[trigger+1:] {
		trimmed := strings.TrimSpace(line)
		if !started && trimmed == "" {
			continue
		}
		if started && trimmed != "" && isExactTemplateStopLine(trimmed) {
			break
		}
		started = true
		collected = append(collected, strings.TrimRight(line, "\r"))
	}
	for len(collected) > 0 && strings.TrimSpace(collected[len(collected)-1]) == "" {
		collected = collected[:len(collected)-1]
	}
	if len(collected) == 0 {
		return nil
	}
	return collected
}

func exactArtifactRequiredLiterals(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var required []string
	for _, line := range lines {
		required = append(required, exactLiteralStringsFromLine(line)...)
	}
	required = append(required, exactLiteralBulletPrefixes(lines)...)
	return uniqueLiteralItems(required)
}

func exactLiteralStringsFromLine(line string) []string {
	const trigger = "must literally include the strings "
	trimmed := strings.TrimRight(line, "\r")
	lowered := strings.ToLower(trimmed)
	idx := strings.Index(lowered, trigger)
	if idx < 0 {
		return nil
	}
	segment := strings.TrimSpace(trimmed[idx+len(trigger):])
	for _, marker := range []string{" in its ", " in the ", " so ", " before ", " even if ", "."} {
		if cut := strings.Index(strings.ToLower(segment), marker); cut >= 0 {
			segment = strings.TrimSpace(segment[:cut])
			break
		}
	}
	if segment == "" {
		return nil
	}
	segment = literalAndSeparatorPattern.ReplaceAllString(segment, ", ")
	parts := strings.Split(segment, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		candidate := strings.TrimSpace(strings.Trim(part, "`\"'"))
		if candidate == "" {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func exactLiteralBulletPrefixes(lines []string) []string {
	trigger := -1
	for i, line := range lines {
		lowered := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(lowered, "exact standalone bullet prefixes") {
			trigger = i
			break
		}
	}
	if trigger < 0 {
		return nil
	}
	collected := make([]string, 0, 8)
	started := false
	for _, line := range lines[trigger+1:] {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if !started && trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			started = true
			collected = append(collected, trimmed)
			continue
		}
		if started {
			break
		}
		if trimmed == "" {
			continue
		}
		if isExactTemplateStopLine(trimmed) {
			break
		}
	}
	return collected
}

func isExactTemplateStopLine(line string) bool {
	lowered := strings.ToLower(strings.TrimSpace(line))
	for _, prefix := range []string{
		"do not ",
		"if you ",
		"write ",
		"then call finish",
		"for each ",
	} {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

func extractLiteralArtifactPaths(text string) []string {
	lines := strings.Split(text, "\n")
	paths := make([]string, 0, 2)
	for _, line := range lines {
		segment := artifactInstructionSegment(line)
		if segment == "" {
			continue
		}
		for _, match := range artifactPathPattern.FindAllString(segment, -1) {
			candidate := strings.Trim(match, " `\"'.,;:()[]{}")
			if candidate == "" {
				continue
			}
			paths = append(paths, candidate)
		}
	}
	return uniqueCleanPaths(paths)
}

func artifactInstructionSegment(line string) string {
	trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
	if trimmed == "" {
		return ""
	}
	lowered := strings.ToLower(trimmed)
	if !containsArtifactCreationVerb(lowered) {
		return ""
	}
	if negation := artifactNegationIndex(lowered); negation == 0 {
		return ""
	} else if negation > 0 {
		trimmed = strings.TrimSpace(trimmed[:negation])
		lowered = strings.ToLower(trimmed)
		if !containsArtifactCreationVerb(lowered) {
			return ""
		}
	}
	return trimmed
}

func containsArtifactCreationVerb(lowered string) bool {
	for _, token := range []string{"write ", "draft ", "create "} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func artifactNegationIndex(lowered string) int {
	minIndex := -1
	for _, token := range []string{
		" do not write",
		" do not draft",
		" do not create",
		" don't write",
		" don't draft",
		" don't create",
		" must not write",
		" must not draft",
		" must not create",
		" never write",
		" never draft",
		" never create",
		"不要写",
		"不要创建",
	} {
		if idx := strings.Index(lowered, token); idx >= 0 && (minIndex < 0 || idx < minIndex) {
			minIndex = idx
		}
	}
	if minIndex >= 0 {
		return minIndex
	}
	for _, token := range []string{
		"do not write",
		"do not draft",
		"do not create",
		"don't write",
		"don't draft",
		"don't create",
		"must not write",
		"must not draft",
		"must not create",
		"never write",
		"never draft",
		"never create",
		"不要写",
		"不要创建",
	} {
		if strings.HasPrefix(lowered, token) {
			return 0
		}
	}
	return -1
}

func validateExactArtifactTemplate(content string, req exactArtifactTemplate) bool {
	if !req.Active || len(req.RequiredLines) == 0 {
		return true
	}
	required := strings.Join(req.RequiredLines, "\n")
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, required) {
		return false
	}
	if len(normalized) == len(required) {
		return true
	}
	return normalized[len(required)] == '\n'
}

func missingExactArtifactLiterals(content string, req exactArtifactLiteral) []string {
	if !req.Active || len(req.RequiredLiterals) == 0 {
		return nil
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	missing := make([]string, 0, len(req.RequiredLiterals))
	for _, literal := range req.RequiredLiterals {
		if !strings.Contains(normalized, literal) {
			missing = append(missing, literal)
		}
	}
	return missing
}

func uniqueLiteralItems(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		cleaned := strings.TrimSpace(item)
		if cleaned == "" {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func quotePromptItems(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprintf("`%s`", item))
	}
	return out
}

func uniqueCleanPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func displayRequestedArtifactPaths(workdir string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, displayPromptPath(workdir, path))
	}
	return out
}

func looksDeliverablePath(path string) bool {
	lowered := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if lowered == "" {
		return false
	}
	base := filepath.Base(lowered)
	ext := strings.ToLower(filepath.Ext(base))
	if ext == ".md" {
		return true
	}
	if strings.Contains(lowered, "/reports/") || strings.Contains(lowered, "/artifacts/") {
		return isDocumentArtifactExtension(ext)
	}
	if !isDocumentArtifactExtension(ext) {
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	for _, token := range []string{"report", "summary", "audit", "brief", "review", "artifact"} {
		if strings.Contains(stem, token) {
			return true
		}
	}
	return false
}

func isDocumentArtifactExtension(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case "", ".md", ".txt", ".json", ".yaml", ".yml", ".csv":
		return true
	default:
		return false
	}
}

func looksFinalArtifactPath(path string) bool {
	if !looksDeliverablePath(path) {
		return false
	}
	return !isProjectMemoryPath(path)
}

func isProjectMemoryPath(path string) bool {
	lowered := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if lowered == "" {
		return false
	}
	for _, suffix := range []string{
		"reports/spec.md",
		"reports/plan.md",
		"reports/progress.md",
		"reports/validation.md",
	} {
		if lowered == suffix || strings.HasSuffix(lowered, "/"+suffix) {
			return true
		}
	}
	return false
}

func isCodePath(path string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".go", ".rs", ".ts", ".tsx", ".js", ".jsx", ".py", ".java", ".c", ".cc", ".cpp", ".h":
		return true
	default:
		return false
	}
}

func displayPromptPath(workdir, path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	if rel, err := filepath.Rel(workdir, path); err == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return rel
	}
	return path
}

func pathInList(path string, paths []string) bool {
	cleaned := filepath.Clean(path)
	for _, candidate := range paths {
		if cleaned == filepath.Clean(candidate) {
			return true
		}
	}
	return false
}

func anyRequestedArtifactWritten(messages []session.Message, requested []string) bool {
	start := latestExternalInstructionIndex(messages)
	if start >= 0 {
		start++
	} else {
		start = 0
	}
	for _, msg := range messages[start:] {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if pathInList(path, requested) {
				return true
			}
		}
	}
	return false
}

func annotateExactArtifactTemplateResult(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage, result *session.ToolResult) {
	if result == nil || result.IsError {
		return
	}
	req := exactArtifactTemplateRequirement(workdir, messages)
	if !req.Active {
		return
	}
	target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
	if !ok || !pathInList(target.Path, req.Paths) {
		return
	}
	if !validateExactArtifactTemplate(target.Content, req) {
		return
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["exact_template_valid"] = true
	result.Metadata["exact_template_path"] = displayPromptPath(workdir, target.Path)
}

func annotateExactArtifactLiteralResult(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage, result *session.ToolResult) {
	if result == nil || result.IsError {
		return
	}
	req := exactArtifactLiteralRequirement(workdir, messages)
	if !req.Active {
		return
	}
	target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
	if !ok || !pathInList(target.Path, req.Paths) {
		return
	}
	if len(missingExactArtifactLiterals(target.Content, req)) != 0 {
		return
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["exact_literals_valid"] = true
	result.Metadata["exact_literals_path"] = displayPromptPath(workdir, target.Path)
}

func isReadOnlyRetrievalCall(toolName string, rawArgs json.RawMessage) bool {
	if isRetrievalTool(toolName) {
		return true
	}
	return toolName == "shell" && requestedShellCommandIsReadOnly(rawArgs)
}

func requestedShellCommandIsReadOnly(rawArgs json.RawMessage) bool {
	command, ok := requestedShellCommand(rawArgs)
	return ok && looksReadOnlyShellCommand(command)
}

func requestedShellCommand(rawArgs json.RawMessage) (string, bool) {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(rawArgs, &input); err != nil {
		return "", false
	}
	command := strings.TrimSpace(input.Command)
	return command, command != ""
}

func shellResultIsReadOnlyRetrieval(result session.ToolResult) bool {
	if result.Name != "shell" {
		return false
	}
	command, _ := result.Metadata["command"].(string)
	return looksReadOnlyShellCommand(command)
}

func looksReadOnlyShellCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	for _, token := range []string{"&&", "||", ";", "\n", "\r", ">", "<", "$(", "`"} {
		if strings.Contains(command, token) {
			return false
		}
	}
	segments := strings.Split(command, "|")
	for _, segment := range segments {
		fields := strings.Fields(strings.TrimSpace(segment))
		if len(fields) == 0 {
			return false
		}
		if !isReadOnlyShellProgram(fields[0]) {
			return false
		}
	}
	return true
}

func isReadOnlyShellProgram(program string) bool {
	switch program {
	case "cat", "sed", "head", "tail", "grep", "rg", "find", "ls", "wc", "nl", "stat":
		return true
	default:
		return false
	}
}

func todoListAllCompleted(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var todos []session.TodoItem
	if err := json.Unmarshal([]byte(raw), &todos); err != nil || len(todos) == 0 {
		return false
	}
	for _, item := range todos {
		if item.Status != "completed" && item.Status != "cancelled" {
			return false
		}
	}
	return true
}

type interruptSteerDirective struct {
	Active             bool
	Index              int
	UseCurrentEvidence bool
	WriteArtifact      bool
	Finish             bool
	DeferFinish        bool
}

func latestInterruptSteerDirective(messages []session.Message) interruptSteerDirective {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return interruptSteerDirective{}
	}
	msg := messages[idx]
	source, _ := msg.Meta["source"].(string)
	interrupt, _ := msg.Meta["interrupt"].(bool)
	if source != "steer" || !interrupt {
		return interruptSteerDirective{}
	}
	lowered := strings.ToLower(msg.Text)
	deferFinish := mentionsDeferredFinish(lowered)
	directive := interruptSteerDirective{
		Active:             true,
		Index:              idx,
		UseCurrentEvidence: mentionsCurrentEvidence(lowered),
		WriteArtifact:      mentionsArtifactWrite(msg.Text),
		Finish:             !deferFinish && mentionsImmediateFinish(lowered),
		DeferFinish:        deferFinish,
	}
	if !directive.UseCurrentEvidence && mentionsConditionalDelivery(lowered) && !directive.DeferFinish {
		return interruptSteerDirective{}
	}
	if !directive.UseCurrentEvidence && !directive.WriteArtifact && !directive.Finish && !directive.DeferFinish {
		return interruptSteerDirective{}
	}
	return directive
}

func (d interruptSteerDirective) promptGoal() string {
	var goals []string
	if d.UseCurrentEvidence {
		goals = append(goals, "use current evidence")
	}
	if d.WriteArtifact {
		goals = append(goals, "write the requested artifact")
	}
	if d.Finish {
		goals = append(goals, "finish immediately")
	}
	if d.DeferFinish {
		goals = append(goals, "stop without finishing so a later continue can close the task")
	}
	if len(goals) == 0 {
		return "deliver immediately"
	}
	if len(goals) == 1 {
		return goals[0]
	}
	return strings.Join(goals[:len(goals)-1], ", ") + ", and " + goals[len(goals)-1]
}

type steerDirectiveState struct {
	HasArtifactWrite              bool
	HasFinish                     bool
	AssistantTurnsWithoutDelivery int
	BookkeepingCount              int
	BlockedDetourCount            int
}

func collectSteerDirectiveState(messages []session.Message) steerDirectiveState {
	state := steerDirectiveState{}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			state.AssistantTurnsWithoutDelivery++
			continue
		}
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name == "finish" || result.Final {
				state.HasFinish = true
			}
			if result.Name == "write_file" {
				if path, _ := result.Metadata["path"].(string); looksFinalArtifactPath(path) {
					state.HasArtifactWrite = true
				}
			}
			if isCompletionDetourTool(result.Name) {
				state.BookkeepingCount++
			}
			if guard, _ := result.Metadata["guard"].(string); guard == "steer_completion" || guard == "steer_completion_escalated" {
				state.BlockedDetourCount++
			}
		}
	}
	return state
}

func isCompletionDetourTool(name string) bool {
	switch name {
	case "todo_write", "todo_read", "task_create", "task_update", "task_list", "task_get", "load_skill":
		return true
	default:
		return false
	}
}

func mentionsCurrentEvidence(lowered string) bool {
	for _, token := range []string{
		"current evidence",
		"existing evidence",
		"use what you have",
		"from what you have",
		"stop reading",
		"do not do more reads",
		"do not do more retrieval",
		"don't read",
		"no more reads",
		"当前证据",
		"不要再读",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func mentionsArtifactWrite(text string) bool {
	lowered := strings.ToLower(text)
	if !looksArtifactRequest(text) {
		return false
	}
	for _, token := range []string{"write", "draft", "create", "save", "生成", "写"} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func mentionsImmediateFinish(lowered string) bool {
	for _, token := range []string{"finish", "call finish", "complete now", "end now", "wrap up", "完成", "结束"} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func mentionsDeferredFinish(lowered string) bool {
	for _, token := range []string{
		"do not finish",
		"don't finish",
		"dont finish",
		"without finishing",
		"stop without finishing",
		"do not call finish",
		"don't call finish",
		"dont call finish",
		"do not complete",
		"don't complete",
		"dont complete",
		"do not wrap up",
		"don't wrap up",
		"dont wrap up",
		"resume later",
		"later resume",
		"continue later",
		"later continue",
		"wait for continue",
		"await continue",
		"so a later continue can close",
		"不要完成",
		"不要结束",
		"先不要完成",
		"稍后继续",
		"之后继续",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func mentionsConditionalDelivery(lowered string) bool {
	for _, token := range []string{
		"once ",
		"after ",
		"when ",
		"until ",
		"if ",
		"完成后",
		"之后",
		"等到",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func requestedReadPath(workdir string, rawArgs json.RawMessage) string {
	window, ok := requestedReadWindow(workdir, rawArgs)
	if !ok {
		return ""
	}
	return window.Path
}

func requestedGrepPath(workdir string, rawArgs json.RawMessage) (string, bool) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rawArgs, &input); err != nil {
		return "", false
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	return filepath.Clean(path), true
}

type readWindow struct {
	Path   string
	Offset int
	End    int
}

func requestedReadWindow(workdir string, rawArgs json.RawMessage) (readWindow, bool) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(rawArgs, &input); err != nil {
		return readWindow{}, false
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		return readWindow{}, false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 120
	}
	if limit > 120 {
		limit = 120
	}
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}
	return readWindow{
		Path:   filepath.Clean(path),
		Offset: offset,
		End:    offset + limit,
	}, true
}

func collectReadPaths(messages []session.Message) map[string]int {
	paths := map[string]int{}
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "read_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			paths[filepath.Clean(path)]++
		}
	}
	return paths
}

func collectReadWindows(messages []session.Message) map[string][]readWindow {
	windows := map[string][]readWindow{}
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "read_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			offset := metadataInt(result.Metadata, "offset")
			end := metadataInt(result.Metadata, "end")
			window := readWindow{
				Path:   filepath.Clean(path),
				Offset: offset,
				End:    end,
			}
			windows[window.Path] = append(windows[window.Path], window)
		}
	}
	return windows
}

func collectReadWindowSequence(messages []session.Message) []readWindow {
	var windows []readWindow
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "read_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			windows = append(windows, readWindow{
				Path:   filepath.Clean(path),
				Offset: metadataInt(result.Metadata, "offset"),
				End:    metadataInt(result.Metadata, "end"),
			})
		}
	}
	return windows
}

func metadataInt(meta map[string]any, key string) int {
	if meta == nil {
		return 0
	}
	switch value := meta[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if v, err := value.Int64(); err == nil {
			return int(v)
		}
	}
	return 0
}

func overlapsAnyWindow(existing []readWindow, candidate readWindow) bool {
	for _, item := range existing {
		if item.Path != candidate.Path {
			continue
		}
		if item.End == 0 || candidate.End == 0 {
			return true
		}
		if candidate.Offset < item.End && item.Offset < candidate.End {
			return true
		}
	}
	return false
}

func countReadWindows(windows map[string][]readWindow) int {
	total := 0
	for _, items := range windows {
		total += len(items)
	}
	return total
}

func countExploratoryReadWindows(windows, priorReads []readWindow) int {
	total := 0
	seen := seenReadWindows(priorReads)
	for _, item := range windows {
		if isReservedFinalProofReadCandidate(item, seen[item.Path]) {
			seen[item.Path] = append(seen[item.Path], item)
			continue
		}
		total++
		seen[item.Path] = append(seen[item.Path], item)
	}
	return total
}

func countReservedFinalProofReads(windows, priorReads []readWindow) int {
	total := 0
	seen := seenReadWindows(priorReads)
	for _, item := range windows {
		if isReservedFinalProofReadCandidate(item, seen[item.Path]) {
			total++
		}
		seen[item.Path] = append(seen[item.Path], item)
	}
	return total
}

func isReservedFinalProofReadCandidate(window readWindow, inspected []readWindow) bool {
	if !isReservedFinalProofWindow(window) {
		return false
	}
	return overlapsAnyWindow(inspected, window)
}

func overlapsReservedFinalProofRead(existing, prior []readWindow, candidate readWindow) bool {
	seen := seenReadWindows(prior)
	for _, item := range existing {
		if !isReservedFinalProofReadCandidate(item, seen[item.Path]) {
			seen[item.Path] = append(seen[item.Path], item)
			continue
		}
		if candidate.Offset < item.End && item.Offset < candidate.End {
			return true
		}
		seen[item.Path] = append(seen[item.Path], item)
	}
	return false
}

func countGuidanceReadWindows(windows, priorReads []readWindow) int {
	total := 0
	seen := seenReadWindows(priorReads)
	for _, item := range windows {
		if !isDurableGuidancePath(item.Path) {
			seen[item.Path] = append(seen[item.Path], item)
			continue
		}
		if !isReservedFinalProofReadCandidate(item, seen[item.Path]) {
			total++
		}
		seen[item.Path] = append(seen[item.Path], item)
	}
	return total
}

func hasPreviouslyInspectedWindow(window readWindow, priorReads, recentReads []readWindow) bool {
	for _, item := range priorReads {
		if item.Path == window.Path && window.Offset < item.End && item.Offset < window.End {
			return true
		}
	}
	for _, item := range recentReads {
		if item.Path == window.Path && window.Offset < item.End && item.Offset < window.End {
			return true
		}
	}
	return false
}

func hasPreviouslyInspectedPath(path string, priorReads, recentReads []readWindow) bool {
	for _, item := range priorReads {
		if item.Path == path {
			return true
		}
	}
	for _, item := range recentReads {
		if item.Path == path {
			return true
		}
	}
	return false
}

func seenReadWindows(windows []readWindow) map[string][]readWindow {
	seen := map[string][]readWindow{}
	for _, item := range windows {
		seen[item.Path] = append(seen[item.Path], item)
	}
	return seen
}

func isDurableGuidancePath(path string) bool {
	clean := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if clean == "" {
		return false
	}
	if isProjectMemoryPath(clean) {
		return true
	}
	return strings.HasSuffix(clean, ".md") && strings.Contains(clean, "/spec/")
}

type agentsDoc struct {
	Path    string
	Content string
}

func loadAgentsChain(workdir string) []agentsDoc {
	current, err := filepath.Abs(workdir)
	if err != nil {
		return nil
	}
	var chain []agentsDoc
	for {
		path := filepath.Join(current, "AGENTS.md")
		if data, err := os.ReadFile(path); err == nil {
			chain = append(chain, agentsDoc{Path: path, Content: strings.TrimSpace(string(data))})
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}
