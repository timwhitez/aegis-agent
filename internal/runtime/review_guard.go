package runtime

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/review"
	"aegis-agent/internal/session"
	"aegis-agent/internal/tools"
)

type reviewArtifactRequirement struct {
	Active bool
}

func activeReviewArtifactRequirement(messages []session.Message) reviewArtifactRequirement {
	idx := latestExternalInstructionIndex(messages)
	if idx < 0 {
		return reviewArtifactRequirement{}
	}
	text := messages[idx].Text
	if !looksAuditOrReviewTask(text) {
		return reviewArtifactRequirement{}
	}
	if !requiresReviewArtifact(text) {
		return reviewArtifactRequirement{}
	}
	return reviewArtifactRequirement{Active: true}
}

func requiresReviewArtifact(text string) bool {
	lowered := strings.ToLower(text)
	for _, phrase := range []string{
		"write a report", "write the report", "create a report", "create the report",
		"write a review report", "write an audit report", "create a review report", "create an audit report",
		"draft a report", "draft the report", "review artifact", "audit artifact",
		"markdown report", "durable report", "durable artifact",
		"写一份报告", "写报告", "创建报告", "生成报告", "审计报告", "评审报告",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	paths := extractLiteralArtifactPaths(text)
	if len(paths) > 0 {
		for _, path := range paths {
			if isChangeSummaryPath(path) || isProjectMemoryPath(path) {
				continue
			}
			if looksRequestedReviewArtifactPath(path) {
				return true
			}
		}
		return false
	}
	return false
}

func looksRequestedReviewArtifactPath(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	if base == "" {
		return false
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	for _, token := range []string{
		"audit",
		"review",
		"finding",
		"summary",
		"report",
		"incident",
		"recovery",
	} {
		if strings.Contains(stem, token) {
			return true
		}
	}
	return false
}

func reviewArtifactGuard(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage) (string, string) {
	requirement := activeReviewArtifactRequirement(messages)
	if !requirement.Active {
		return "", ""
	}
	requestedPaths := requestedArtifactPaths(workdir, messages)

	switch toolName {
	case "write_file", "edit_file":
		_, hasPath, pathErr := requestedArtifactPath(workdir, toolName, rawArgs)
		if hasPath && pathErr != nil {
			return "review_artifact", "Review artifact guard: requested artifact/report writes must stay within the workspace before execution. Fix the path and try again."
		}
		target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
		if !ok || (!pathInList(target.Path, requestedPaths) && !looksReviewArtifactCandidate(target.Path, target.Content)) {
			return "", ""
		}
		validation := review.ValidateMarkdownArtifactWithWorkspace(workdir, target.Content)
		if validation.Valid {
			return "", ""
		}
		return "review_artifact", "Review artifact guard: this audit/review report is missing the canonical structure. Include a findings section, per-finding Severity/Confidence/Evidence/Why it matters fields with concrete path:line evidence plus a quoted snippet or identifier from the cited lines (either inline in Evidence or in a separate Snippet field), and a separate unresolved or remaining-risks section with real content before writing the artifact. If a validator issue says a snippet does not match the cited lines, fix the cited line numbers or widen the cited range instead of just rephrasing the finding." + reviewIssueDetail(validation)
	case "finish":
		if reviewArtifactSatisfied(workdir, messages) {
			return "", ""
		}
		if len(requestedPaths) > 0 {
			return "review_artifact", fmt.Sprintf("Review artifact guard: before finishing this audit/review task, write the report artifact to %s with findings plus Severity/Confidence/Evidence/Why it matters fields with concrete path:line support, snippet-level evidence support, and a separate unresolved or remaining-risks section.", joinPromptItems(displayRequestedArtifactPaths(workdir, requestedPaths)))
		}
		return "review_artifact", "Review artifact guard: before finishing this audit/review task, write the explicitly requested report artifact with findings plus Severity/Confidence/Evidence/Why it matters fields, concrete path:line support, snippet-level evidence support, and a separate unresolved or remaining-risks section."
	default:
		return "", ""
	}
}

func reviewIssueDetail(validation review.ValidationResult) string {
	if len(validation.Issues) == 0 {
		return ""
	}
	limit := len(validation.Issues)
	if limit > 3 {
		limit = 3
	}
	detail := " Current validator issues: " + strings.Join(validation.Issues[:limit], "; ") + "."
	if hasValidationIssue(validation.Issues, "snippets must match the cited lines") {
		detail += " Fix snippet-backed evidence by citing the line(s) that literally contain the quoted text or identifier, or widen the cited range until they do; a nearby heading or adjacent line still fails validation."
	}
	if hasValidationIssue(validation.Issues, "readable in-workspace files and line ranges") {
		detail += " Use explicit workspace-relative repo paths like `internal/app/app.go:42-44` and avoid omitted-path or ellipsis shorthand."
	}
	return detail
}

func hasValidationIssue(issues []string, needle string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, needle) {
			return true
		}
	}
	return false
}

func annotateReviewArtifactResult(workdir string, messages []session.Message, toolName string, rawArgs json.RawMessage, result *session.ToolResult) {
	if result == nil || result.IsError {
		return
	}
	requirement := activeReviewArtifactRequirement(messages)
	if !requirement.Active {
		return
	}
	target, ok := requestedArtifactWrite(workdir, toolName, rawArgs)
	requestedPaths := requestedArtifactPaths(workdir, messages)
	if !ok || (!pathInList(target.Path, requestedPaths) && !looksReviewArtifactCandidate(target.Path, target.Content)) {
		return
	}
	validation := review.ValidateMarkdownArtifactWithWorkspace(workdir, target.Content)
	if !validation.Valid {
		return
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["review_artifact_valid"] = true
	result.Metadata["review_artifact_candidate"] = true
	result.Metadata["review_artifact_findings"] = validation.FindingCount
	result.Metadata["review_artifact_no_findings"] = validation.NoFindings
	result.Metadata["review_artifact_verified_evidence"] = validation.VerifiedEvidenceCount
}

func reviewArtifactSatisfied(workdir string, messages []session.Message) bool {
	requestedPaths := requestedArtifactPaths(workdir, messages)
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
			valid, _ := result.Metadata["review_artifact_valid"].(bool)
			if !valid {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if len(requestedPaths) > 0 {
				if pathInList(path, requestedPaths) {
					return true
				}
				continue
			}
			candidate, _ := result.Metadata["review_artifact_candidate"].(bool)
			if candidate {
				return true
			}
			if looksFinalArtifactPath(path) {
				return true
			}
		}
	}
	return false
}

type artifactWriteTarget struct {
	Path    string
	Content string
}

func requestedArtifactWrite(workdir, toolName string, rawArgs json.RawMessage) (artifactWriteTarget, bool) {
	switch toolName {
	case "write_file":
		var input struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(rawArgs, &input); err != nil {
			return artifactWriteTarget{}, false
		}
		path, ok := cleanRequestedPath(workdir, input.Path)
		if !ok {
			return artifactWriteTarget{}, false
		}
		return artifactWriteTarget{Path: path, Content: input.Content}, true
	case "edit_file":
		var input struct {
			Path    string `json:"path"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		}
		if err := json.Unmarshal(rawArgs, &input); err != nil {
			return artifactWriteTarget{}, false
		}
		path, ok := cleanRequestedPath(workdir, input.Path)
		if !ok {
			return artifactWriteTarget{}, false
		}
		current, _, err := fileutil.ReadRegularFileNoSymlink(path)
		if err != nil {
			return artifactWriteTarget{}, false
		}
		updated := strings.Replace(string(current), input.OldText, input.NewText, 1)
		if updated == string(current) && input.OldText != input.NewText {
			return artifactWriteTarget{}, false
		}
		return artifactWriteTarget{Path: path, Content: updated}, true
	default:
		return artifactWriteTarget{}, false
	}
}

func requestedArtifactPath(workdir, toolName string, rawArgs json.RawMessage) (string, bool, error) {
	switch toolName {
	case "write_file", "edit_file":
		var input struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(rawArgs, &input); err != nil {
			return "", false, nil
		}
		path := strings.TrimSpace(input.Path)
		if path == "" {
			return "", false, nil
		}
		resolved, err := resolveRequestedPath(workdir, path)
		if err != nil {
			return "", true, err
		}
		return resolved, true, nil
	default:
		return "", false, nil
	}
}

func resolveRequestedPath(workdir, value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	path, err := tools.ResolveWorkspacePath(workdir, path)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	return path, nil
}

func cleanRequestedPath(workdir, value string) (string, bool) {
	path, err := resolveRequestedPath(workdir, value)
	if err != nil {
		return "", false
	}
	return path, true
}

func looksReviewArtifactCandidate(path, content string) bool {
	if isProjectMemoryPath(path) {
		return false
	}
	if isChangeSummaryPath(path) {
		return false
	}
	if looksFinalArtifactPath(path) {
		return true
	}
	return looksReviewArtifactContent(content)
}

func isChangeSummaryPath(path string) bool {
	lowered := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if lowered == "" {
		return false
	}
	const suffix = "reports/change-summary.md"
	return lowered == suffix || strings.HasSuffix(lowered, "/"+suffix)
}

func looksReviewArtifactContent(content string) bool {
	lowered := strings.ToLower(content)
	score := 0
	for _, token := range []string{"## findings", "# findings", "### finding", "## finding"} {
		if strings.Contains(lowered, token) {
			score++
			break
		}
	}
	for _, token := range []string{"severity:", "confidence:", "evidence:", "why it matters:"} {
		if strings.Contains(lowered, token) {
			score++
			break
		}
	}
	for _, token := range []string{"remaining risks", "remaining risk", "unresolved questions", "unresolved question"} {
		if strings.Contains(lowered, token) {
			score++
			break
		}
	}
	return score >= 2
}

func durableProjectMemoryNote(workdir string, messages []session.Message) string {
	if !looksLargeProjectTask(messages) {
		return ""
	}
	stack := loadProjectMemoryStack(workdir)
	if refresh := projectMemoryRefreshNeed(workdir, messages); refresh.Active {
		return fmt.Sprintf("A durable project-memory stack is available: %s. It is stale relative to %s. For durable handoff or finalization, %s. If you delegate before refreshing it, include the current progress and validation context directly in the child prompt.", strings.Join(stack.PresentPaths(), ", "), joinPromptItems(refresh.Reasons), projectMemoryRefreshInstruction(refresh))
	}
	if present := stack.PresentPaths(); len(present) > 0 {
		return fmt.Sprintf("A durable project-memory stack is available: %s. Refresh from these files before more implementation or repo-scale rereads, and keep spec, plan, progress, and validation current.", strings.Join(present, ", "))
	}
	return "For larger multi-step engineering or review tasks, maintain a durable project-memory stack under reports/: spec.md, plan.md, progress.md, and validation.md. Refresh from those files before more implementation or repo-scale rereads."
}

type projectMemoryRefreshState struct {
	Active         bool
	Reasons        []string
	Actions        []string
	SteerDirective string
}

func projectMemoryRefreshNeed(workdir string, messages []session.Message) projectMemoryRefreshState {
	if !looksLargeProjectTask(messages) {
		return projectMemoryRefreshState{}
	}
	stack := loadProjectMemoryStack(workdir)
	if len(stack.PresentPaths()) == 0 && !hasProjectMemoryWriteActivity(messages) {
		return projectMemoryRefreshState{}
	}
	lastProjectMemory := latestProjectMemoryWriteIndex(messages)
	lastSpecPlanWrite := latestProjectMemoryPathWriteIndex(messages,
		filepath.Join("reports", "spec.md"),
		filepath.Join("reports", "plan.md"),
	)
	lastSourceEdit := latestSourceMutationIndex(messages)
	lastValidation := latestValidationCommandIndex(messages)
	lastRequirementChange := latestRequirementChangeIndex(messages)
	if lastSourceEdit <= lastProjectMemory && lastValidation <= lastProjectMemory && lastRequirementChange <= lastSpecPlanWrite {
		return projectMemoryRefreshState{}
	}
	actions := []string{}
	if lastRequirementChange > lastSpecPlanWrite {
		for _, rel := range []string{
			filepath.Join("reports", "spec.md"),
			filepath.Join("reports", "plan.md"),
		} {
			if projectMemoryStackHasPath(stack, rel) {
				actions = append(actions, "refresh "+rel)
				continue
			}
			actions = append(actions, "write "+rel)
		}
	}
	for _, rel := range []string{
		filepath.Join("reports", "progress.md"),
		filepath.Join("reports", "validation.md"),
	} {
		if projectMemoryStackHasPath(stack, rel) {
			actions = append(actions, "refresh "+rel)
			continue
		}
		actions = append(actions, "write "+rel)
	}
	reasons := []string{}
	steerDirective := ""
	if lastSourceEdit > lastProjectMemory {
		reasons = append(reasons, "recent source edits")
	}
	if lastValidation > lastProjectMemory {
		reasons = append(reasons, "recent validation commands")
	}
	if lastRequirementChange > lastSpecPlanWrite {
		reasons = append(reasons, "recent steer or scope change")
		steerDirective = latestRequirementChangeSummary(messages)
	}
	return projectMemoryRefreshState{
		Active:         len(actions) > 0 && len(reasons) > 0,
		Reasons:        uniqueStrings(reasons),
		Actions:        uniqueStrings(actions),
		SteerDirective: steerDirective,
	}
}

func projectMemoryRefreshInstruction(state projectMemoryRefreshState) string {
	if len(state.Actions) == 0 {
		return "refresh reports/progress.md and reports/validation.md"
	}
	instruction := joinPromptItems(state.Actions)
	if strings.TrimSpace(state.SteerDirective) == "" {
		return instruction
	}
	return instruction + " so they reflect the latest steer priority: " + state.SteerDirective
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func latestProjectMemoryWriteIndex(messages []session.Message) int {
	last := -1
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if isProjectMemoryPath(path) {
				last = i
			}
		}
	}
	return last
}

func latestProjectMemoryPathWriteIndex(messages []session.Message, relPaths ...string) int {
	if len(relPaths) == 0 {
		return -1
	}
	normalized := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		normalized = append(normalized, filepath.ToSlash(filepath.Clean(rel)))
	}
	last := -1
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			lowered := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
			for _, rel := range normalized {
				if lowered == rel || strings.HasSuffix(lowered, "/"+rel) {
					last = i
					break
				}
			}
		}
	}
	return last
}

func latestRequirementChangeIndex(messages []session.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		source, _ := msg.Meta["source"].(string)
		if source == "harness_reminder" {
			continue
		}
		if source == "steer" {
			return i
		}
	}
	return -1
}

func latestRequirementChangeSummary(messages []session.Message) string {
	idx := latestRequirementChangeIndex(messages)
	if idx < 0 {
		return ""
	}
	return quoteForPrompt(messages[idx].Text, 220)
}

func latestSourceMutationIndex(messages []session.Message) int {
	last := -1
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "write_file" && result.Name != "edit_file" {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if isProjectMemoryPath(path) || !isCodePath(path) {
				continue
			}
			last = i
		}
	}
	return last
}

func latestValidationCommandIndex(messages []session.Message) int {
	last := -1
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.Name != "shell" {
				continue
			}
			command, _ := result.Metadata["command"].(string)
			if looksValidationShellCommand(command) {
				last = i
			}
		}
	}
	return last
}

func looksValidationShellCommand(command string) bool {
	lowered := strings.ToLower(strings.TrimSpace(command))
	if lowered == "" {
		return false
	}
	for _, token := range []string{
		"go test",
		"pytest",
		"cargo test",
		"npm test",
		"pnpm test",
		"yarn test",
		"mvn test",
		"gradle test",
		"ctest",
		"make test",
		"go build",
		"npm run build",
		"pnpm build",
		"yarn build",
	} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func projectMemoryStackHasPath(stack projectMemoryStack, rel string) bool {
	for _, file := range stack.Files {
		if !file.Present {
			continue
		}
		if filepath.Clean(file.Path) == filepath.Clean(rel) {
			return true
		}
	}
	return false
}

func looksLargeProjectTask(messages []session.Message) bool {
	idx := latestExternalInstructionIndex(messages)
	if idx >= 0 {
		lowered := strings.ToLower(messages[idx].Text)
		if explicitSmallTaskOptOut(lowered) {
			return false
		}
		for _, phrase := range []string{
			"large project",
			"large repository",
			"complex repository",
			"validation matrix",
			"complex matrix",
			"full repository",
			"entire repository",
			"enterprise repo",
			"large refactor",
			"large architecture",
			"long-horizon project",
			"大型项目",
			"复杂仓库",
			"复杂矩阵",
			"全量仓库",
			"大型重构",
		} {
			if strings.Contains(lowered, phrase) {
				return true
			}
		}
		score := 0
		for _, token := range []string{
			"repo",
			"repository",
			"architecture",
			"traceability",
			"matrix",
			"codex",
			"opencode",
			"surpass",
			"enterprise",
			"refactor",
			"long-horizon",
			"全量",
			"架构",
			"工程",
			"矩阵",
			"重构",
		} {
			if strings.Contains(lowered, token) {
				score += 2
			}
		}
		for _, token := range []string{
			"large",
			"complex",
			"project",
			"validation",
			"full",
			"大型",
			"复杂",
			"验证",
		} {
			if strings.Contains(lowered, token) {
				score++
			}
		}
		if score >= 3 {
			return true
		}
	}
	stats := collectRecentToolStats(messages)
	return stats.UniqueReadPaths >= 6 || stats.RetrievalCount >= 8
}

func explicitSmallTaskOptOut(lowered string) bool {
	for _, phrase := range []string{
		"smallest-correct-fix",
		"smallest correct fix",
		"not a large-project task",
		"not a large project task",
		"not a large multi-step task",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	if strings.Contains(lowered, "do not create a todo list") && (strings.Contains(lowered, "durable reports stack") || strings.Contains(lowered, "reports/spec.md")) {
		return true
	}
	return false
}
