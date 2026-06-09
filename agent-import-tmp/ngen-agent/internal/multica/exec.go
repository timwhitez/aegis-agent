package multica

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

var (
	multicaIssueIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	multicaMarkerPattern  = regexp.MustCompile(`(?i)\bngen-[a-z0-9_-]*(?:ok|e2e|done|complete|marker)[a-z0-9_-]*\b`)
)

type ExecOptions struct {
	Workdir        string
	ConfigPath     string
	ConfigScope    string
	ResumeTaskID   string
	RunRole        string
	TimeoutSeconds int
}

func RunExec(ctx context.Context, opts ExecOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	timeoutSeconds := opts.TimeoutSeconds
	if timeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
		defer cancel()
	}
	resolution, err := ResolveConfig(opts.Workdir, opts.ConfigPath, opts.ConfigScope)
	if err != nil {
		fmt.Fprintf(stderr, "exec: resolve config: %v\n", err)
		return 13
	}
	svc := ngenrt.New(resolution.Workdir, resolution.Config)
	envelope, err := readInputEnvelope(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "exec: read input: %v\n", err)
		return 13
	}
	prompt := envelopeText(envelope)
	if prompt == "" {
		fmt.Fprintln(stderr, "exec: input envelope content is empty")
		return 13
	}

	taskID := strings.TrimSpace(opts.ResumeTaskID)
	runMode := "auto"
	var metadata task.MulticaRunMetadata
	if taskID == "" {
		taskFile := taskFromEnvelope(envelope, prompt, resolution, opts.RunRole)
		if timeoutSeconds <= 0 && multicaLeaderTaskFile(taskFile) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 600*time.Second)
			defer cancel()
		}
		runMode = runModeForObjective(taskFile.Objective, false)
		spec, createErr := svc.Create(ctx, taskFile)
		if createErr != nil {
			fmt.Fprintf(stderr, "exec: create task: %v\n", createErr)
			return 13
		}
		taskID = spec.TaskID
		metadata = NewRunMetadata(spec, resolution)
		if err := svc.Store.SaveMulticaRunMetadata(metadata); err != nil {
			fmt.Fprintf(stderr, "exec: write run metadata: %v\n", err)
			return 13
		}
		guidance := CollectWorkspaceGuidance(taskID, resolution.Workdir)
		if err := svc.Store.SaveWorkspaceGuidance(guidance); err != nil {
			fmt.Fprintf(stderr, "exec: write workspace guidance: %v\n", err)
			return 13
		}
	} else {
		loaded, loadErr := svc.Store.LoadMulticaRunMetadata(taskID)
		if loadErr != nil {
			status := resultStatusBlocked(taskID, opts.RunRole, resolution.EffectiveModel, "multica_run_metadata_missing", map[string]any{"error": loadErr.Error()})
			_ = json.NewEncoder(stdout).Encode(status)
			return 10
		}
		metadata = loaded
		if drift := configDrift(metadata, resolution); len(drift) > 0 {
			status := resultStatusBlocked(taskID, opts.RunRole, effectiveModelFromMetadata(metadata), "multica_model_config_drift", drift)
			_ = json.NewEncoder(stdout).Encode(status)
			return 10
		}
		if spec, specErr := svc.Store.LoadTask(taskID); specErr == nil {
			runMode = runModeForObjective(spec.Objective, true)
		}
	}

	encoder := json.NewEncoder(stdout)
	emit := func(msg StreamOutputMessage) error {
		return encoder.Encode(msg)
	}
	if err := emit(systemMessage(taskID, opts.RunRole, metadata, resolution)); err != nil {
		fmt.Fprintf(stderr, "exec: stdout write failed: %v\n", err)
		return 13
	}
	if snapshot, err := svc.Status(ctx, taskID); err == nil {
		if err := emit(statusMessage(snapshot, opts.RunRole, metadata)); err != nil {
			fmt.Fprintf(stderr, "exec: stdout write failed: %v\n", err)
			return 13
		}
	}

	resultCh := make(chan runResult, 1)
	go func() {
		var (
			snapshot task.StatusSnapshot
			events   []task.Event
			runErr   error
		)
		switch runMode {
		case "run":
			snapshot, events, runErr = svc.Run(ctx, taskID)
		case "resume":
			snapshot, events, runErr = svc.Resume(ctx, taskID)
		default:
			snapshot, events, runErr = svc.Auto(ctx, taskID)
		}
		resultCh <- runResult{Snapshot: snapshot, Events: events, Err: runErr}
	}()

	emitted := map[string]struct{}{}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := flushEvents(svc, taskID, opts.RunRole, metadata, emitted, emit); err != nil {
				fmt.Fprintf(stderr, "exec: stream flush: %v\n", err)
				return 13
			}
		case result := <-resultCh:
			if err := flushEvents(svc, taskID, opts.RunRole, metadata, emitted, emit); err != nil {
				fmt.Fprintf(stderr, "exec: final flush: %v\n", err)
				return 13
			}
			status := terminalStatus(result.Snapshot, result.Err, ctx.Err())
			if result.Snapshot.TaskID == "" {
				if snapshot, err := svc.Status(context.Background(), taskID); err == nil {
					result.Snapshot = snapshot
				}
			}
			final := resultMessage(result.Snapshot, opts.RunRole, metadata, resolution, status, result.Err, svc)
			if err := emit(final); err != nil {
				fmt.Fprintf(stderr, "exec: stdout write failed: %v\n", err)
				return 13
			}
			return exitCodeFromResult(status)
		}
	}
}

func multicaLeaderTaskFile(tf task.TaskFile) bool {
	text := strings.ToLower(strings.Join([]string{tf.Objective, tf.Title}, "\n"))
	return strings.Contains(text, "multica issue execution mode") &&
		(strings.Contains(text, "multica run role: leader") || strings.Contains(text, "multica run role: master"))
}

type runResult struct {
	Snapshot task.StatusSnapshot
	Events   []task.Event
	Err      error
}

func readInputEnvelope(stdin io.Reader) (StreamInputMessage, error) {
	decoder := json.NewDecoder(stdin)
	var envelope StreamInputMessage
	if err := decoder.Decode(&envelope); err != nil {
		return StreamInputMessage{}, err
	}
	var extra any
	err := decoder.Decode(&extra)
	if err != nil && !errors.Is(err, io.EOF) {
		return StreamInputMessage{}, err
	}
	if err == nil {
		return StreamInputMessage{}, errors.New("expected exactly one JSON input envelope")
	}
	if envelope.Protocol != ProtocolName || envelope.ProtocolVersion != ProtocolVersion {
		return StreamInputMessage{}, fmt.Errorf("unsupported protocol %q version %d", envelope.Protocol, envelope.ProtocolVersion)
	}
	if envelope.Type != "user" || envelope.Role != "user" {
		return StreamInputMessage{}, fmt.Errorf("expected user input envelope, got type=%q role=%q", envelope.Type, envelope.Role)
	}
	return envelope, nil
}

func envelopeText(envelope StreamInputMessage) string {
	var parts []string
	for _, block := range envelope.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func taskFromEnvelope(envelope StreamInputMessage, prompt string, resolution ConfigResolution, runRole string) task.TaskFile {
	kind, preset := inferTaskKind(resolution.Workdir, prompt)
	criteria := criteriaFromPrompt(prompt)
	var constraints []string
	objective := prompt
	if strings.TrimSpace(envelope.SystemPrompt) != "" {
		objective += "\n\nSystem prompt:\n" + strings.TrimSpace(envelope.SystemPrompt)
	}
	if len(envelope.Metadata) > 0 {
		objective += "\n\nMultica metadata:\n"
		keys := make([]string, 0, len(envelope.Metadata))
		for key := range envelope.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			objective += fmt.Sprintf("- %s=%s\n", key, envelope.Metadata[key])
		}
	}
	if issue, ok := multicaIssueAssignmentFromEnvelope(envelope, prompt, resolution.Workdir); ok {
		kind = task.KindCoding
		preset = ""
		issue.RunRole = multicaRunRoleFromInputs(runRole, envelope, prompt, resolution.Workdir)
		objective = multicaIssueObjective(objective, issue)
		criteria = multicaIssueCriteria(issue)
		constraints = multicaIssueConstraints(issue)
	}
	return task.TaskFile{
		Kind:             kind,
		PresetID:         preset,
		Title:            titleFromPrompt(prompt),
		Objective:        objective,
		SuccessCriteria:  criteria,
		Constraints:      constraints,
		WorkspaceRoot:    resolution.Workdir,
		PermissionModeID: task.EffectivePermissionModeID(resolution.Config.Permission.DefaultMode),
	}
}

type multicaIssueAssignment struct {
	IssueID      string
	IssueContext string
	Markers      []string
	RunRole      string
}

func multicaIssueAssignmentFromEnvelope(envelope StreamInputMessage, prompt, workdir string) (multicaIssueAssignment, bool) {
	var textParts []string
	textParts = append(textParts, prompt, envelope.SystemPrompt)
	metadataIssueID := ""
	keys := make([]string, 0, len(envelope.Metadata))
	for key := range envelope.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.EqualFold(key, "issue_id") {
			metadataIssueID = multicaIssueIDPattern.FindString(envelope.Metadata[key])
		}
		textParts = append(textParts, key+"="+envelope.Metadata[key])
	}
	issueContext := readMulticaIssueContext(workdir)
	if issueContext != "" {
		textParts = append(textParts, issueContext)
	}
	combined := strings.Join(textParts, "\n")
	lower := strings.ToLower(combined)
	hasIssueSignal := metadataIssueID != "" || issueContext != "" || strings.Contains(lower, "issue")
	hasMulticaSignal := metadataIssueID != "" || issueContext != "" || strings.Contains(lower, "multica")
	if !hasIssueSignal || !hasMulticaSignal {
		return multicaIssueAssignment{}, false
	}
	issueID := metadataIssueID
	if issueID == "" {
		issueID = multicaIssueIDPattern.FindString(combined)
	}
	if issueID == "" {
		return multicaIssueAssignment{}, false
	}
	return multicaIssueAssignment{
		IssueID:      issueID,
		IssueContext: issueContext,
		Markers:      multicaMarkersFromText(combined),
	}, true
}

func multicaMarkersFromText(text string) []string {
	matches := multicaMarkerPattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	var out []string
	for _, match := range matches {
		marker := strings.TrimSpace(match)
		if marker == "" {
			continue
		}
		key := strings.ToLower(marker)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, marker)
	}
	return out
}

func readMulticaIssueContext(workdir string) string {
	if strings.TrimSpace(workdir) == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(workdir, ".agent_context", "issue_context.md"))
	if err != nil {
		return ""
	}
	const maxIssueContextBytes = 20000
	if len(data) > maxIssueContextBytes {
		data = data[:maxIssueContextBytes]
	}
	return strings.TrimSpace(string(data))
}

func multicaRunRoleFromInputs(runRole string, envelope StreamInputMessage, prompt, workdir string) string {
	if role := normalizeMulticaRunRole(runRole); role != "" {
		return role
	}
	var values []string
	values = append(values, prompt, envelope.SystemPrompt)
	keys := make([]string, 0, len(envelope.Metadata))
	for key := range envelope.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := envelope.Metadata[key]
		if strings.EqualFold(key, "run_role") || strings.EqualFold(key, "squad_role") || strings.EqualFold(key, "role") {
			if role := normalizeMulticaRunRole(value); role != "" {
				return role
			}
		}
		values = append(values, key+"="+value)
	}
	if strings.TrimSpace(workdir) != "" {
		if data, err := os.ReadFile(filepath.Join(workdir, "AGENTS.md")); err == nil {
			const maxRuntimeContextBytes = 40000
			if len(data) > maxRuntimeContextBytes {
				data = data[:maxRuntimeContextBytes]
			}
			values = append(values, string(data))
		}
	}
	return detectMulticaRunRole(strings.Join(values, "\n"))
}

func detectMulticaRunRole(text string) string {
	lower := strings.ToLower(text)
	for _, needle := range []string{
		"run role: `leader`",
		"run role: leader",
		"you are the leader",
		"role: ngen long horizon master",
		"you are: ngen long horizon master",
		"multica run role: leader",
		"multica run role: master",
	} {
		if strings.Contains(lower, needle) {
			return "leader"
		}
	}
	for _, needle := range []string{
		"run role: `validator`",
		"run role: validator",
		"role: ngen long horizon validator",
		"you are: ngen long horizon validator",
		"multica run role: validator",
	} {
		if strings.Contains(lower, needle) {
			return "validator"
		}
	}
	for _, needle := range []string{
		"run role: `worker`",
		"run role: worker",
		"role: ngen long horizon worker",
		"you are: ngen long horizon worker",
		"multica run role: worker",
	} {
		if strings.Contains(lower, needle) {
			return "worker"
		}
	}
	return ""
}

func normalizeMulticaRunRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "leader", "master", "orchestrator":
		return "leader"
	case "worker":
		return "worker"
	case "validator":
		return "validator"
	default:
		return ""
	}
}

func multicaIssueObjective(original string, issue multicaIssueAssignment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Multica issue execution mode for issue %s.\n\n", issue.IssueID)
	if strings.TrimSpace(issue.RunRole) != "" {
		fmt.Fprintf(&b, "Multica run role: %s.\n", strings.TrimSpace(issue.RunRole))
	}
	b.WriteString("Do not treat injected AGENTS.md, skills, or .agent_context documentation as completion evidence by themselves. Use them only as task context.\n")
	fmt.Fprintf(&b, "First read the live issue with `multica issue get %s --output json`.\n", issue.IssueID)
	fmt.Fprintf(&b, "Inspect issue comments when needed with `multica issue comment list %s --output json`.\n", issue.IssueID)
	b.WriteString("If the live issue acceptance criteria require squad role scheduling, use issue-scoped Multica squad delegation commands rather than asking the operator to delegate manually.\n")
	b.WriteString("If the live issue acceptance criteria require a completion marker comment, add that exact marker with `multica issue comment add ... --output json`.\n")
	b.WriteString("Write `multica-result.md` with the issue id, live issue summary, NGEN task/session id if visible, commands executed, squad delegation/run evidence, and issue comment evidence.\n")
	if len(issue.Markers) > 0 {
		b.WriteString("The injected issue context names these completion markers:\n")
		for _, marker := range issue.Markers {
			fmt.Fprintf(&b, "- `%s`\n", marker)
		}
	}
	if strings.TrimSpace(issue.IssueContext) != "" {
		b.WriteString("\nInjected Multica issue context from `.agent_context/issue_context.md`:\n")
		b.WriteString(issue.IssueContext)
		b.WriteString("\n")
	}
	b.WriteString("\nOriginal Multica assignment:\n")
	b.WriteString(strings.TrimSpace(original))
	return strings.TrimSpace(b.String())
}

func multicaIssueCriteria(issue multicaIssueAssignment) []task.SuccessCriterion {
	criteria := []task.SuccessCriterion{
		{
			ID:        "SC-001",
			Statement: fmt.Sprintf("The read-only live issue command `multica issue get %s --output json` passes.", issue.IssueID),
		},
		{
			ID:        "SC-002",
			Statement: fmt.Sprintf("A completed repair command record shows `multica issue comment add %s` issue comment evidence when this Multica issue task has marker or public-response requirements.", issue.IssueID),
		},
	}
	nextID := 3
	if !multicaLeaderRunRole(issue.RunRole) {
		roleMarkers := multicaMarkersForRunRole(issue.Markers, issue.RunRole)
		for _, marker := range roleMarkers {
			criteria = append(criteria, task.SuccessCriterion{
				ID:        fmt.Sprintf("SC-%03d", nextID),
				Statement: fmt.Sprintf(`The exact Multica issue marker "%s" appears in command-backed issue comment evidence.`, marker),
			})
			nextID++
		}
	}
	if multicaLeaderRunRole(issue.RunRole) {
		criteria = append(criteria, task.SuccessCriterion{
			ID:        fmt.Sprintf("SC-%03d", nextID),
			Statement: fmt.Sprintf("If the live issue requests worker-role or validator-role squad scheduling, Multica issue run/delegation evidence for %s shows those roles were scheduled or completed; final completion still requires completed role evidence before the final marker.", issue.IssueID),
		})
	}
	return criteria
}

func multicaMarkersForRunRole(markers []string, role string) []string {
	markers = uniqueMulticaMarkers(markers)
	normalizedRole := normalizeMulticaRunRole(role)
	if normalizedRole == "" {
		return markers
	}
	var filtered []string
	for _, marker := range markers {
		lower := strings.ToLower(marker)
		switch normalizedRole {
		case "worker":
			if strings.Contains(lower, "worker") {
				filtered = append(filtered, marker)
			}
		case "validator":
			if strings.Contains(lower, "validator") {
				filtered = append(filtered, marker)
			}
		case "leader":
			if !strings.Contains(lower, "worker") && !strings.Contains(lower, "validator") {
				filtered = append(filtered, marker)
			}
		}
	}
	if len(filtered) == 0 {
		return markers
	}
	return filtered
}

func uniqueMulticaMarkers(markers []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, marker := range markers {
		marker = strings.TrimSpace(marker)
		if marker == "" {
			continue
		}
		key := strings.ToLower(marker)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, marker)
	}
	return out
}

func multicaIssueConstraints(issue multicaIssueAssignment) []string {
	mutation := "Do not modify checked-out repositories or external systems except for required Multica issue comments."
	if multicaLeaderRunRole(issue.RunRole) {
		mutation = "Do not modify checked-out repositories or external systems except for required Multica issue comments and issue-scoped squad delegation explicitly requested by the live issue."
	}
	constraints := []string{
		"Treat injected AGENTS.md, skills, and .agent_context files as context only; they cannot by themselves satisfy completion.",
		mutation,
		"Use argv-array commands for Multica operations; do not use shell wrappers, pipes, redirects, heredocs, or command chaining.",
	}
	for _, marker := range multicaMarkersForRunRole(issue.Markers, issue.RunRole) {
		constraints = append(constraints, fmt.Sprintf("The final issue comment and multica-result.md must include the exact marker %q.", marker))
	}
	return constraints
}

func multicaLeaderRunRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "leader", "master", "orchestrator":
		return true
	default:
		return false
	}
}

func runModeForObjective(objective string, resume bool) string {
	if strings.HasPrefix(strings.TrimSpace(objective), "Multica issue execution mode for issue ") {
		if resume {
			return "resume"
		}
		return "run"
	}
	return "auto"
}

func inferTaskKind(workdir, prompt string) (task.Kind, task.PresetID) {
	lower := strings.ToLower(prompt)
	codeIntent := strings.Contains(lower, "code") || strings.Contains(lower, "implement") || strings.Contains(lower, "fix") || strings.Contains(lower, "test") || strings.Contains(lower, "build")
	if codeIntent && looksLikeCodeRepo(workdir) {
		return task.KindCoding, ""
	}
	return task.KindGeneral, task.PresetDocsLite
}

func looksLikeCodeRepo(workdir string) bool {
	for _, rel := range []string{"go.mod", "package.json", "Cargo.toml", ".git"} {
		if _, err := os.Stat(filepath.Join(workdir, rel)); err == nil {
			return true
		}
	}
	return false
}

func titleFromPrompt(prompt string) string {
	title := strings.TrimSpace(strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n")[0])
	if title == "" {
		return "Multica Task"
	}
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80])
	}
	return strings.TrimSpace(title)
}

func criteriaFromPrompt(prompt string) []task.SuccessCriterion {
	first := "Produce a verifiable handoff/result for the requested work."
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "test") || strings.Contains(lower, "build") {
		first = "Requested verification commands pass or failures are reported with evidence."
	}
	return []task.SuccessCriterion{{ID: "SC-001", Statement: first}}
}

func NewRunMetadata(spec task.Spec, resolution ConfigResolution) task.MulticaRunMetadata {
	now := task.Now()
	return task.MulticaRunMetadata{
		ObjectKind:        "multica_run_metadata",
		SchemaVersion:     task.SchemaVersion,
		TaskID:            spec.TaskID,
		SessionID:         spec.TaskID,
		RunID:             task.NewID("RUN"),
		Source:            "multica",
		ModelRoute:        resolution.EffectiveModel.Route,
		ProviderMode:      resolution.EffectiveModel.ProviderMode,
		ProviderModel:     resolution.EffectiveModel.ProviderModel,
		ConfigSource:      resolution.ConfigSource,
		ConfigFingerprint: resolution.ConfigFingerprint,
		PermissionModeID:  spec.PermissionModeID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func configDrift(metadata task.MulticaRunMetadata, resolution ConfigResolution) map[string]any {
	drift := map[string]any{}
	if metadata.ModelRoute != resolution.EffectiveModel.Route {
		drift["previous_model_route"] = metadata.ModelRoute
		drift["current_model_route"] = resolution.EffectiveModel.Route
	}
	if metadata.ProviderMode != resolution.EffectiveModel.ProviderMode {
		drift["previous_provider_mode"] = metadata.ProviderMode
		drift["current_provider_mode"] = resolution.EffectiveModel.ProviderMode
	}
	if metadata.ProviderModel != resolution.EffectiveModel.ProviderModel {
		drift["previous_provider_model"] = metadata.ProviderModel
		drift["current_provider_model"] = resolution.EffectiveModel.ProviderModel
	}
	if metadata.ConfigFingerprint != resolution.ConfigFingerprint {
		drift["previous_config_fingerprint"] = metadata.ConfigFingerprint
		drift["current_config_fingerprint"] = resolution.ConfigFingerprint
	}
	return drift
}

func effectiveModelFromMetadata(metadata task.MulticaRunMetadata) EffectiveModel {
	return EffectiveModel{
		Route:         metadata.ModelRoute,
		ProviderMode:  metadata.ProviderMode,
		ProviderModel: metadata.ProviderModel,
	}
}

func systemMessage(taskID, runRole string, metadata task.MulticaRunMetadata, resolution ConfigResolution) StreamOutputMessage {
	return StreamOutputMessage{
		Type:            "system",
		Protocol:        ProtocolName,
		ProtocolVersion: ProtocolVersion,
		TaskID:          taskID,
		SessionID:       taskID,
		RunRole:         strings.TrimSpace(runRole),
		ModelRoute:      metadata.ModelRoute,
		ProviderMode:    metadata.ProviderMode,
		ProviderModel:   metadata.ProviderModel,
		Metadata: map[string]any{
			"config_source":      resolution.ConfigSource,
			"config_fingerprint": resolution.ConfigFingerprint,
			"permission_mode_id": metadata.PermissionModeID,
		},
	}
}

func statusMessage(snapshot task.StatusSnapshot, runRole string, metadata task.MulticaRunMetadata) StreamOutputMessage {
	status := statusFromState(snapshot.State)
	msg := StreamOutputMessage{
		Type:          "status",
		TaskID:        snapshot.TaskID,
		SessionID:     snapshot.TaskID,
		RunRole:       strings.TrimSpace(runRole),
		ModelRoute:    metadata.ModelRoute,
		ProviderMode:  metadata.ProviderMode,
		ProviderModel: metadata.ProviderModel,
		Status:        status,
		Metadata: map[string]any{
			"phase":              snapshot.Phase,
			"state":              snapshot.State,
			"status_reason_code": snapshot.StatusReasonCode,
			"status_detail_ref":  snapshot.StatusDetailRef,
			"last_event_ref":     snapshot.LastEventRef,
		},
	}
	if status == "blocked" {
		msg.Metadata["needs_input"] = true
	}
	return msg
}

func resultStatusBlocked(taskID, runRole string, effective EffectiveModel, reason string, metadata map[string]any) StreamOutputMessage {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["status_reason_code"] = reason
	return StreamOutputMessage{
		Type:          "result",
		TaskID:        taskID,
		SessionID:     taskID,
		RunRole:       strings.TrimSpace(runRole),
		ModelRoute:    effective.Route,
		ProviderMode:  effective.ProviderMode,
		ProviderModel: effective.ProviderModel,
		Status:        "blocked",
		IsError:       true,
		Handoff: &StructuredHandoff{
			Summary:          reason,
			TaskID:           taskID,
			State:            "Blocked",
			StatusReasonCode: reason,
			ModelRoute:       effective.Route,
			ProviderMode:     effective.ProviderMode,
			ProviderModel:    effective.ProviderModel,
		},
		Metadata: metadata,
	}
}

func flushEvents(svc *ngenrt.Service, taskID, runRole string, metadata task.MulticaRunMetadata, emitted map[string]struct{}, emit func(StreamOutputMessage) error) error {
	events, err := svc.Store.ReadEvents(taskID)
	if err != nil {
		return nil
	}
	for _, event := range events {
		if _, ok := emitted[event.EventID]; ok {
			continue
		}
		emitted[event.EventID] = struct{}{}
		for _, msg := range eventMessages(svc, event, runRole, metadata) {
			if err := emit(msg); err != nil {
				return err
			}
		}
	}
	return nil
}

func eventMessages(svc *ngenrt.Service, event task.Event, runRole string, metadata task.MulticaRunMetadata) []StreamOutputMessage {
	base := StreamOutputMessage{
		TaskID:        event.TaskID,
		SessionID:     event.TaskID,
		RunRole:       strings.TrimSpace(runRole),
		ModelRoute:    metadata.ModelRoute,
		ProviderMode:  metadata.ProviderMode,
		ProviderModel: metadata.ProviderModel,
		ID:            event.EventID,
		Metadata: map[string]any{
			"event_type": event.Type,
			"event_id":   event.EventID,
			"refs":       event.Refs,
			"phase":      event.Phase,
			"state":      event.State,
		},
	}
	if record, ok := commandRecordForEvent(svc, event); ok {
		toolName := record.Kind
		if toolName == "" {
			toolName = "command"
		}
		callID := firstNonEmpty(record.CommandRecordID, record.CommandID)
		use := base
		use.Type = "tool_use"
		use.Tool = &ToolProjection{
			Name:   toolName,
			CallID: callID,
			Input: map[string]any{
				"argv":   record.Argv,
				"reason": record.Summary,
			},
		}
		result := base
		result.Type = "tool_result"
		result.IsError = record.Status == "failed"
		result.Tool = &ToolProjection{
			Name:   toolName,
			CallID: callID,
			Output: commandOutputSummary(record),
			Status: record.Status,
		}
		return []StreamOutputMessage{use, result}
	}
	if record, ok := workspaceEditRecordForEvent(svc, event); ok {
		callID := firstNonEmpty(record.EditRecordID, record.EditID)
		use := base
		use.Type = "tool_use"
		use.Tool = &ToolProjection{
			Name:   "workspace_edit",
			CallID: callID,
			Input: map[string]any{
				"provider_mode": record.ProviderMode,
				"summary":       record.Summary,
				"file_changes":  record.FileChanges,
			},
		}
		result := base
		result.Type = "tool_result"
		result.IsError = record.Status == "failed"
		result.Tool = &ToolProjection{
			Name:   "workspace_edit",
			CallID: callID,
			Output: workspaceEditOutputSummary(record),
			Status: record.Status,
		}
		return []StreamOutputMessage{use, result}
	}
	msg := base
	if event.Type == "done" || event.Type == "failed" || strings.Contains(event.Type, "blocked") || strings.HasSuffix(event.Type, "_requested") {
		msg.Type = "status"
		msg.Status = statusFromState(event.State)
		if msg.Status == "blocked" {
			msg.Metadata["needs_input"] = true
		}
	} else {
		msg.Type = "log"
		msg.Log = &LogEntry{Level: "info", Message: event.Summary}
	}
	return []StreamOutputMessage{msg}
}

func workspaceEditRecordForEvent(svc *ngenrt.Service, event task.Event) (task.WorkspaceEditRecord, bool) {
	var wanted string
	for _, ref := range event.Refs {
		if strings.HasPrefix(ref, "workspace_edits.jsonl#edit_record_id=") {
			wanted = strings.TrimPrefix(ref, "workspace_edits.jsonl#edit_record_id=")
			break
		}
	}
	if wanted == "" {
		return task.WorkspaceEditRecord{}, false
	}
	records, err := svc.Store.ReadWorkspaceEdits(event.TaskID)
	if err != nil {
		return task.WorkspaceEditRecord{}, false
	}
	for _, record := range records {
		if record.EditRecordID == wanted {
			return record, true
		}
	}
	return task.WorkspaceEditRecord{}, false
}

func commandRecordForEvent(svc *ngenrt.Service, event task.Event) (task.CommandRunRecord, bool) {
	var wanted string
	for _, ref := range event.Refs {
		if strings.HasPrefix(ref, "command_runs.jsonl#command_record_id=") {
			wanted = strings.TrimPrefix(ref, "command_runs.jsonl#command_record_id=")
			break
		}
	}
	if wanted == "" {
		return task.CommandRunRecord{}, false
	}
	records, err := svc.Store.ReadCommandRuns(event.TaskID)
	if err != nil {
		return task.CommandRunRecord{}, false
	}
	for _, record := range records {
		if record.CommandRecordID == wanted {
			return record, true
		}
	}
	return task.CommandRunRecord{}, false
}

func workspaceEditOutputSummary(record task.WorkspaceEditRecord) string {
	var changes []string
	for _, change := range record.FileChanges {
		changes = append(changes, strings.TrimSpace(change.Action)+" "+strings.TrimSpace(change.Path))
	}
	if len(changes) == 0 {
		return strings.TrimSpace(record.Summary)
	}
	return strings.TrimSpace(record.Summary + "\n" + strings.Join(changes, "\n"))
}

func commandOutputSummary(record task.CommandRunRecord) string {
	var parts []string
	if strings.TrimSpace(record.StdoutExcerpt) != "" {
		parts = append(parts, "stdout: "+strings.TrimSpace(record.StdoutExcerpt))
	}
	if strings.TrimSpace(record.StderrExcerpt) != "" {
		parts = append(parts, "stderr: "+strings.TrimSpace(record.StderrExcerpt))
	}
	if len(parts) == 0 {
		parts = append(parts, strings.TrimSpace(record.Summary))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func resultMessage(snapshot task.StatusSnapshot, runRole string, metadata task.MulticaRunMetadata, resolution ConfigResolution, status string, runErr error, svc *ngenrt.Service) StreamOutputMessage {
	handoff := buildStructuredHandoff(snapshot, metadata, svc)
	msg := StreamOutputMessage{
		Type:          "result",
		TaskID:        snapshot.TaskID,
		SessionID:     snapshot.TaskID,
		RunRole:       strings.TrimSpace(runRole),
		ModelRoute:    metadata.ModelRoute,
		ProviderMode:  metadata.ProviderMode,
		ProviderModel: metadata.ProviderModel,
		Status:        status,
		Usage:         usageForTask(snapshot.TaskID, resolution.EffectiveModel.Route, svc),
		Handoff:       handoff,
		Metadata: map[string]any{
			"status_reason_code": snapshot.StatusReasonCode,
			"config_source":      metadata.ConfigSource,
			"config_fingerprint": metadata.ConfigFingerprint,
		},
	}
	if runErr != nil {
		msg.IsError = true
		msg.Metadata["error"] = runErr.Error()
	}
	return msg
}

func buildStructuredHandoff(snapshot task.StatusSnapshot, metadata task.MulticaRunMetadata, svc *ngenrt.Service) *StructuredHandoff {
	summary := "NGEN task finished with state " + string(snapshot.State) + "."
	if data, err := os.ReadFile(filepath.Join(svc.Store.TaskRoot(snapshot.TaskID), "handoff.md")); err == nil {
		if extracted := handoffSummary(string(data)); extracted != "" {
			summary = extracted
		}
	}
	handoff := &StructuredHandoff{
		Summary:          summary,
		TaskID:           snapshot.TaskID,
		State:            string(snapshot.State),
		Phase:            string(snapshot.Phase),
		StatusReasonCode: snapshot.StatusReasonCode,
		ModelRoute:       metadata.ModelRoute,
		ProviderMode:     metadata.ProviderMode,
		ProviderModel:    metadata.ProviderModel,
		HandoffRef:       snapshot.HandoffRef,
		CompletionRef:    snapshot.CompletionRef,
		VerificationRef:  snapshot.LastVerificationRef,
		ReviewRef:        snapshot.LastReviewRef,
		CriteriaRef:      "criteria/latest.json",
	}
	for _, clue := range snapshot.RestoreClues {
		handoff.RestoreRefs = append(handoff.RestoreRefs, ArtifactRef{Ref: clue.Ref, Kind: "restore", Summary: clue.Summary})
	}
	if criteria, err := svc.Store.LoadCriteria(snapshot.TaskID); err == nil {
		for _, item := range criteria.Criteria {
			digest := CriterionDigest{
				ID:           item.CriterionID,
				Statement:    item.Statement,
				Status:       item.Status,
				EvidenceRefs: item.EvidenceRefs,
			}
			if item.Status == "met" {
				handoff.MetCriteria = append(handoff.MetCriteria, digest)
			} else {
				handoff.OpenCriteria = append(handoff.OpenCriteria, digest)
			}
		}
	}
	if workers, err := svc.Store.ListWorkerContracts(snapshot.TaskID); err == nil {
		for _, worker := range workers {
			digest := WorkerDigest{
				WorkerID:                   worker.WorkerID,
				ChildTaskID:                worker.ChildTaskID,
				Role:                       worker.Role,
				Status:                     worker.Status,
				BlockedReasonCode:          worker.BlockedReasonCode,
				BlockedDetailRef:           worker.BlockedDetailRef,
				RequiresParentAction:       worker.RequiresParentAction,
				ParentActionType:           worker.ParentActionType,
				ParentActionOptions:        append([]string(nil), worker.ParentActionOptions...),
				ParentActionSummary:        worker.ParentActionSummary,
				ParentActionUnresolved:     worker.ParentActionUnresolved,
				EvidenceScore:              worker.EvidenceScore,
				EvidenceGrade:              worker.EvidenceGrade,
				MissingEvidence:            append([]string(nil), worker.MissingEvidence...),
				TrustedForParentCompletion: worker.TrustedForParentCompletion,
				ConflictCount:              worker.ConflictCount,
			}
			if result, err := svc.Store.LoadWorkerResult(snapshot.TaskID, worker.WorkerID); err == nil {
				digest.ChildState = string(result.ChildState)
				digest.CompletionStatus = result.CompletionStatus
				digest.ReviewStatus = result.ReviewStatus
				digest.VerificationStatus = result.VerificationStatus
				digest.Summary = result.Summary
				digest.EvidenceRefs = append([]string(nil), result.EvidenceRefs...)
			} else {
				digest.Summary = worker.ResultSummary
			}
			handoff.WorkerResults = append(handoff.WorkerResults, digest)
		}
	}
	if snapshot.MissionID != "" {
		handoff.Mission = &MissionDigest{
			MissionID:           snapshot.MissionID,
			Status:              snapshot.MissionStatus,
			CurrentMilestoneID:  snapshot.MissionCurrentMilestoneID,
			LatestValidationRef: snapshot.MissionLatestValidationRef,
		}
	}
	return handoff
}

func handoffSummary(markdown string) string {
	var lines []string
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") {
			continue
		}
		lines = append(lines, trimmed)
		if len(strings.Join(lines, " ")) > 320 {
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

func usageForTask(taskID, modelRoute string, svc *ngenrt.Service) map[string]Usage {
	records, err := svc.Store.ReadProviderUsage(taskID)
	if err != nil || len(records) == 0 {
		return nil
	}
	for i := len(records) - 1; i >= 0; i-- {
		if usage, ok := ParseUsageSummary(records[i].TokenUsage, records[i].PromptCacheUsage); ok {
			return map[string]Usage{modelRoute: usage}
		}
	}
	return nil
}

func terminalStatus(snapshot task.StatusSnapshot, runErr error, ctxErr error) string {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(ctxErr, context.Canceled):
		return "cancelled"
	case runErr != nil:
		return "failed"
	default:
		return statusFromState(snapshot.State)
	}
}

func statusFromState(state task.StateName) string {
	switch state {
	case task.StateDone:
		return "completed"
	case task.StateBlocked, task.StateWaiting:
		return "blocked"
	case task.StateFailed:
		return "failed"
	case task.StateAborted:
		return "cancelled"
	default:
		return strings.ToLower(string(state))
	}
}

func exitCodeFromResult(status string) int {
	switch status {
	case "completed":
		return 0
	case "blocked":
		return 10
	case "cancelled":
		return 12
	default:
		return 11
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
