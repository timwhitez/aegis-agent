package runtime

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"go-cli-agent/internal/fileutil"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
	"go-cli-agent/internal/tools"
)

var artifactPathPattern = regexp.MustCompile(`(?i)(?:[a-z]:)?(?:/|\.?/)?[a-z0-9._/-]+\.md`)
var explicitInspectionPathPattern = regexp.MustCompile(`(?i)(?:\./)?[a-z0-9._/-]+\.(?:md|go|ts|py|rs|txt|jsonl?|ya?ml|toml|sh)`)
var literalAndSeparatorPattern = regexp.MustCompile(`(?i)\s*,?\s+and\s+`)
var explicitTargetPhrasePattern = regexp.MustCompile(`(?i)(?:原始目标是|目标是|最新目标是|target(?:\s+url)?\s*(?:is|=)|scope\s*(?:is|=))\s*([^\s，。；;]+)`)
var pathLikeTargetPattern = regexp.MustCompile(`/[A-Za-z0-9._~!$&'()*+,;=:@%/-]+`)
var nonZeroExitPattern = regexp.MustCompile(`(?i)(exit(?:ed)?(?: code| status)?|exit_code|non[- ]?zero).{0,20}[1-9][0-9]*`)

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
		builder.WriteString("The harness provides tools, skills, session state, and safety boundaries; you decide the plan from the user's goal and current repository facts.\n")
		builder.WriteString("Ground claims in files, session facts, and command results. Inspect the owning code path before designing, editing, or declaring behavior.\n")
		builder.WriteString("Do exactly the requested work, keep changes scoped, and prefer concrete action over narration.\n")
		if mode == session.ModeExec {
			builder.WriteString("In exec mode, you must use the finish tool when the task is complete.\n")
		} else {
			builder.WriteString("In run mode, do not stop just because you produced text without a tool call; keep working until you use finish or the harness enters an explicit wait state.\n")
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
	builder.WriteString("- Prefer dedicated tools for their purpose: `grep_files` or `grep` for discovery, `read_file` for known files, `write_file` or `edit_file` for file changes, and `shell` for build, test, package, git, or runtime commands.\n")
	builder.WriteString("- Use the `shell` tool's `workdir` argument instead of embedding `cd` in commands whenever possible.\n")
	builder.WriteString("- For unfamiliar code, use scoped discovery and read the owning files, contracts, and tests needed for the task; multi-file analysis often requires multiple targeted reads.\n")
	builder.WriteString("- When several tool calls are independent in the same turn, issue them together; keep dependent operations sequential.\n")
	builder.WriteString("- Do not guess required tool arguments, paths, or skill names. Inspect first, or ask if the value cannot be discovered safely.\n")
	builder.WriteString("- Create new files only for requested deliverables, tests, configs, or artifacts that are necessary to complete the task.\n")
	builder.WriteString("- Use `load_skill` only with exact names listed under Available skills; never invent aliases or legacy skill names.\n")
	builder.WriteString("- Before reporting validation success, inspect actual command results or validation artifacts; if validation failed, was partial, or was not run, say that plainly.\n")
	builder.WriteString("- Before running project validation, identify the relevant project or build root instead of assuming the initial workdir is always the build root.\n")
	builder.WriteString("- Treat todo/task tools as a progress ledger only: preserve existing entries, append newly discovered work, and mark items complete only after the real file/code/command work is done. Updating todo/task state is never a substitute for doing or validating the task.\n")
	builder.WriteString("- If you use child or background agents, check their final status and reconcile their durable results before final parent conclusions.\n")
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
		builder.WriteString("- Before specialized work, check whether a listed skill clearly matches the user request; if it does, load that skill before proceeding.\n")
		builder.WriteString("- If the user explicitly names a skill, load that exact skill for the turn unless a higher-priority instruction forbids it.\n")
		builder.WriteString("- Use `load_skill` to load full skill content only when needed, passing the exact listed skill name.\n")
		builder.WriteString("- After loading a skill, follow its instructions within the current project, user, and system constraints.\n")
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
	builder.WriteString("Inspect whether the workspace is empty or already structured, then set up foundations quickly, keep scope crisp, and leave the workspace ready for follow-on implementation.\n")
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
		if mode == session.ModeExec {
			return fmt.Sprintf("A requested artifact was already written to %s. If required side effects are done, call finish.", path)
		}
		return fmt.Sprintf("A requested artifact was already written to %s. Continue only for required side effects or corrections.", path)
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
		if data, _, err := fileutil.ReadRegularFileNoSymlink(pos.Path); err == nil {
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
	return harnessReminder{
		Kind: "report_consistency",
		Text: "Harness reminder: " + reportConsistencyRecoveryInstruction(workdir, messages),
	}
}

func reportConsistencyGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	switch toolName {
	case "write_file", "edit_file":
		target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
		if !ok || !validationFactConsistencyTarget(messages, target.Path, target.Content) {
			return "", ""
		}
		if evidence, ok := validationSuccessContradiction(workdir, messages, target.Content); ok {
			return "validation_fact_consistency", "Validation fact-consistency guard: this artifact claims validation succeeded, but durable session evidence records failure: " + evidence + ". Rewrite the artifact with the actual validation status before writing it."
		}
		return "", ""
	case "finish":
		if finalArtifactStaleAfterSupportingDocs(messages) {
			return "report_consistency", "Report-consistency guard: " + reportConsistencyRecoveryInstruction(workdir, messages)
		}
		final := latestFinalArtifactWritePosition(messages)
		if !final.Valid || strings.TrimSpace(final.Path) == "" {
			return "", ""
		}
		if data, _, err := fileutil.ReadRegularFileNoSymlink(final.Path); err == nil {
			content := string(data)
			if validationFactConsistencyTarget(messages, final.Path, content) {
				if evidence, ok := validationSuccessContradiction(workdir, messages, content); ok {
					return "validation_fact_consistency", "Validation fact-consistency guard: the final artifact claims validation succeeded, but durable session evidence records failure: " + evidence + ". Fix the artifact before finishing."
				}
			}
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

func reportConsistencyRecoveryInstruction(workdir string, messages []session.Message) string {
	final := latestFinalArtifactWritePosition(messages)
	finalPath := displayPromptPath(workdir, final.Path)
	staleDocs := displayWritePositionPaths(workdir, supportingDocWritesAfterFinal(messages))
	if len(staleDocs) == 0 {
		staleDocs = []string{"reports/progress.md or reports/validation.md"}
	}
	if strings.TrimSpace(finalPath) == "" {
		finalPath = "the final deliverable"
	}
	return fmt.Sprintf("supporting docs changed after the final deliverable %s: %s. Reading files again will not clear this guard. Edit or rewrite %s after those supporting docs so the final conclusion is newest, then call finish. Do not restart broad exploration.", finalPath, strings.Join(staleDocs, ", "), finalPath)
}

func supportingDocWritesAfterFinal(messages []session.Message) []writePosition {
	final := latestFinalArtifactWritePosition(messages)
	if !final.Valid {
		return nil
	}
	var out []writePosition
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for j, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if strings.TrimSpace(path) == "" || !isSupportingReportDocPath(path) {
				continue
			}
			pos := writePosition{
				Valid:        true,
				MessageIndex: i,
				ResultIndex:  j,
				Path:         path,
				Result:       result,
			}
			if positionAfter(pos, final) {
				out = append(out, pos)
			}
		}
	}
	return out
}

func displayWritePositionPaths(workdir string, positions []writePosition) []string {
	seen := map[string]bool{}
	var out []string
	for _, pos := range positions {
		display := displayPromptPath(workdir, pos.Path)
		if strings.TrimSpace(display) == "" || seen[display] {
			continue
		}
		seen[display] = true
		out = append(out, display)
	}
	return out
}

func latestFinalArtifactWritePosition(messages []session.Message) writePosition {
	return latestWritePosition(messages, func(path string, _ session.ToolResult) bool {
		return looksFinalArtifactPath(path)
	})
}

func latestSupportingDocWritePosition(messages []session.Message) writePosition {
	return latestWritePosition(messages, func(path string, _ session.ToolResult) bool {
		return isSupportingReportDocPath(path)
	})
}

func isSupportingReportDocPath(path string) bool {
	lowered := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	for _, suffix := range []string{"reports/progress.md", "reports/validation.md"} {
		if lowered == suffix || strings.HasSuffix(lowered, "/"+suffix) {
			return true
		}
	}
	return false
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

func validationFactConsistencyTarget(messages []session.Message, path, content string) bool {
	if !hasStrongValidationSuccessClaim(content) {
		return false
	}
	if idx := latestExternalInstructionIndex(messages); idx >= 0 {
		lowered := strings.ToLower(messages[idx].Text)
		if explicitNonReviewTaskOptOut(lowered) {
			return false
		}
		if looksAuditOrReviewTask(messages[idx].Text) && looksDeliverablePath(path) {
			return true
		}
	}
	if isValidationProjectMemoryPath(path) {
		return true
	}
	return looksReviewArtifactCandidate(path, content)
}

func validationSuccessContradiction(workdir string, messages []session.Message, artifactContent string) (string, bool) {
	if !hasStrongValidationSuccessClaim(artifactContent) || hasExplicitValidationFailureClaim(artifactContent) {
		return "", false
	}
	if evidence, ok := validationFailureEvidenceFromMessages(messages); ok {
		return evidence, true
	}
	if evidence, ok := validationFailureEvidenceFromSupportingDocs(workdir); ok {
		return evidence, true
	}
	return "", false
}

func validationFailureEvidenceFromMessages(messages []session.Message) (string, bool) {
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
			evidence, ok := validationFailureEvidenceFromToolResult(result)
			if ok {
				return evidence, true
			}
		}
	}
	return "", false
}

func validationFailureEvidenceFromToolResult(result session.ToolResult) (string, bool) {
	command, _ := result.Metadata["command"].(string)
	output := result.DisplayOutput
	if strings.TrimSpace(output) == "" {
		output = result.LLMOutput
	}
	combined := command + "\n" + output
	if result.Name == "shell" {
		if result.IsError && hasValidationEvidenceContext(combined) {
			return validationShellEvidence(command, result, "failed"), true
		}
		if exitCode, ok := metadataExitCode(result.Metadata); ok && exitCode != 0 && hasValidationEvidenceContext(combined) {
			return validationShellEvidence(command, result, fmt.Sprintf("exited with code %d", exitCode)), true
		}
	}
	if hasValidationEvidenceContext(combined) && hasClearValidationFailureEvidence(combined) {
		return validationShellEvidence(command, result, "reported validation failure"), true
	}
	return "", false
}

func validationFailureEvidenceFromSupportingDocs(workdir string) (string, bool) {
	for _, rel := range []string{filepath.Join("reports", "progress.md"), filepath.Join("reports", "validation.md")} {
		path, err := tools.ResolveWorkspacePath(workdir, rel)
		if err != nil {
			continue
		}
		data, _, err := fileutil.ReadRegularFileNoSymlink(path)
		if err != nil {
			continue
		}
		if hasValidationEvidenceContext(string(data)) && hasClearValidationFailureEvidence(string(data)) {
			return rel + " records validation failure", true
		}
	}
	return "", false
}

func validationShellEvidence(command string, result session.ToolResult, reason string) string {
	label := strings.TrimSpace(command)
	if label == "" {
		label = result.Name
	}
	return fmt.Sprintf("%s %s", quoteForPrompt(label, 160), reason)
}

func metadataExitCode(metadata map[string]any) (int, bool) {
	if metadata == nil {
		return 0, false
	}
	switch value := metadata["exit_code"].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func isValidationProjectMemoryPath(path string) bool {
	lowered := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	for _, suffix := range []string{"reports/progress.md", "reports/validation.md"} {
		if lowered == suffix || strings.HasSuffix(lowered, "/"+suffix) {
			return true
		}
	}
	return false
}

func hasStrongValidationSuccessClaim(content string) bool {
	lowered := strings.ToLower(content)
	for _, phrase := range []string{
		"validation passed",
		"validation succeeded",
		"validation successful",
		"validation completed successfully",
		"verification passed",
		"verification succeeded",
		"verification completed successfully",
		"tests passed",
		"all tests passed",
		"test suite passed",
		"checks passed",
		"all checks passed",
		"build passed",
		"lint passed",
		"typecheck passed",
		"green test run",
		"验证通过",
		"校验通过",
		"验证已通过",
		"测试通过",
		"全部测试通过",
		"检查通过",
		"构建通过",
		"全部通过",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

func hasExplicitValidationFailureClaim(content string) bool {
	lowered := strings.ToLower(content)
	for _, phrase := range []string{
		"validation failed",
		"validation did not pass",
		"validation was not run",
		"validation not run",
		"verification failed",
		"tests failed",
		"test failed",
		"checks failed",
		"build failed",
		"lint failed",
		"typecheck failed",
		"failed to run",
		"unable to run",
		"could not run",
		"not executed",
		"non-zero",
		"exit code",
		"exit status",
		"验证失败",
		"验证未通过",
		"测试失败",
		"测试未通过",
		"检查失败",
		"未执行",
		"无法运行",
		"非零",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

func hasValidationEvidenceContext(content string) bool {
	lowered := strings.ToLower(content)
	for _, token := range []string{
		"validation",
		"validate",
		"verification",
		"verify",
		"test",
		"check",
		"build",
		"lint",
		"typecheck",
		"qa",
		"验证",
		"校验",
		"测试",
		"检查",
		"构建",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func hasClearValidationFailureEvidence(content string) bool {
	lowered := strings.ToLower(content)
	for _, phrase := range []string{
		"validation failed",
		"validation did not pass",
		"verification failed",
		"tests failed",
		"test failed",
		"checks failed",
		"build failed",
		"lint failed",
		"typecheck failed",
		"failed to run",
		"unable to run",
		"could not run",
		"non-zero",
		"验证失败",
		"验证未通过",
		"测试失败",
		"测试未通过",
		"检查失败",
		"无法运行",
		"非零",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return nonZeroExitPattern.MatchString(content)
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
	case "write_file", "edit_file", "finish", "todo_write", "task_create", "task_update", "agent_spawn", "agent_wait", "agent_stop", "agent_prompt":
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
		cleaned = prefixAtRuneBoundary(cleaned, limit-3) + "..."
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
	if mode == session.ModeExec {
		note = fmt.Sprintf("A requested artifact was already written to %s. If required side effects are done, call finish.", path)
	} else {
		note = fmt.Sprintf("A requested artifact was already written to %s. Continue only for required side effects or corrections.", path)
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
	if stats.RetrievalCount < 8 && stats.UniqueReadPaths < 4 && stats.DiscoveryCount < 4 {
		return harnessReminder{}
	}
	return harnessReminder{
		Kind: "large_project_coordination",
		Text: "Harness reminder: this looks like a large project task. Consider externalizing durable project memory at reports/spec.md, reports/plan.md, reports/progress.md, and reports/validation.md, and keeping a task board aligned with the written plan when that helps recovery or collaboration.",
	}
}

func projectMemoryRefreshReminder(workdir string, messages []session.Message) harnessReminder {
	need := projectMemoryRefreshNeed(workdir, messages)
	if !need.Active {
		return harnessReminder{}
	}
	return harnessReminder{
		Kind: "project_memory_refresh",
		Text: "Harness reminder: recent implementation or validation work outpaced the durable project-memory stack. For durable handoff, " + projectMemoryRefreshInstruction(need) + " so the next session inherits current progress and QA state. If you delegate before refreshing it, include the current progress and validation context directly in the child prompt.",
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
	return "", ""
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
		for _, segment := range artifactInstructionSegments(line) {
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
		for _, segment := range artifactInstructionSegments(line) {
			for _, match := range artifactPathPattern.FindAllString(segment, -1) {
				candidate := strings.Trim(match, " `\"'.,;:()[]{}")
				if candidate == "" {
					continue
				}
				paths = append(paths, candidate)
			}
		}
	}
	return uniqueCleanPaths(paths)
}

func artifactInstructionSegments(line string) []string {
	trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
	if trimmed == "" {
		return nil
	}
	var out []string
	for _, fragment := range splitArtifactInstructionLine(trimmed) {
		segment := artifactInstructionSegment(fragment)
		if segment != "" {
			out = append(out, segment)
		}
	}
	return out
}

func splitArtifactInstructionLine(line string) []string {
	var out []string
	start := 0
	for i, r := range line {
		if !isArtifactSentenceBoundary(r) {
			continue
		}
		next := nextNonSpaceRune(line[i+len(string(r)):])
		if next != 0 && r == '.' && !isLikelySentenceStart(next) {
			continue
		}
		fragment := strings.TrimSpace(line[start:i])
		if fragment != "" {
			out = append(out, fragment)
		}
		start = i + len(string(r))
	}
	if tail := strings.TrimSpace(line[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func isArtifactSentenceBoundary(r rune) bool {
	switch r {
	case '.', ';', '!', '?', '。', '；', '！', '？':
		return true
	default:
		return false
	}
}

func nextNonSpaceRune(text string) rune {
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		return r
	}
	return 0
}

func isLikelySentenceStart(r rune) bool {
	if r >= 'A' && r <= 'Z' {
		return true
	}
	if r >= 0x4e00 && r <= 0x9fff {
		return true
	}
	return false
}

func artifactInstructionSegment(line string) string {
	trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
	if trimmed == "" {
		return ""
	}
	lowered := strings.ToLower(trimmed)
	if artifactCreationVerbIndex(lowered) < 0 {
		return ""
	}
	if negation := artifactNegationIndex(lowered); negation == 0 {
		return ""
	} else if negation > 0 {
		trimmed = strings.TrimSpace(trimmed[:negation])
		lowered = strings.ToLower(trimmed)
		if artifactCreationVerbIndex(lowered) < 0 {
			return ""
		}
	}
	if verbIndex := artifactCreationVerbIndex(lowered); verbIndex > 0 {
		trimmed = strings.TrimSpace(trimmed[verbIndex:])
	}
	return trimmed
}

func containsArtifactCreationVerb(lowered string) bool {
	return artifactCreationVerbIndex(lowered) >= 0
}

func artifactCreationVerbIndex(lowered string) int {
	best := -1
	for _, token := range []string{"write ", "draft ", "create "} {
		idx := strings.Index(lowered, token)
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best
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

type agentsDoc struct {
	Path    string
	Content string
}

func loadAgentsChain(workdir string) []agentsDoc {
	current, err := filepath.Abs(workdir)
	if err != nil {
		return nil
	}
	path, err := tools.ResolveWorkspacePath(current, "AGENTS.md")
	if err != nil {
		return nil
	}
	if data, _, err := fileutil.ReadRegularFileNoSymlink(path); err == nil {
		return []agentsDoc{{Path: path, Content: strings.TrimSpace(string(data))}}
	}
	return nil
}
