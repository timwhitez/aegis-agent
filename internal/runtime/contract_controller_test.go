package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-cli-agent/internal/session"
)

func TestSessionContractTracksRequiredArtifactAndCompletionGate(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	message := session.NewMessage("user", "Write reports/final.md with the final implementation summary.")
	if err := store.AppendMessage(meta.ID, message); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if err := refreshContractForSession(store, nil, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}

	contract, err := store.LoadContract(meta.ID)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if contract.SchemaVersion != 1 {
		t.Fatalf("expected schema version 1, got %#v", contract)
	}
	if len(contract.RequiredArtifacts) != 1 {
		t.Fatalf("expected one required artifact, got %#v", contract.RequiredArtifacts)
	}
	wantPath := filepath.Join(meta.Workdir, "reports", "final.md")
	if contract.RequiredArtifacts[0].Path != wantPath {
		t.Fatalf("expected artifact path %q, got %q", wantPath, contract.RequiredArtifacts[0].Path)
	}
	if !containsString(contract.CompletionGates, "required_artifact") {
		t.Fatalf("expected required_artifact gate, got %#v", contract.CompletionGates)
	}

	tracker, err := store.LoadArtifactTracker(meta.ID)
	if err != nil {
		t.Fatalf("load tracker: %v", err)
	}
	if len(tracker) != 1 || tracker[0].Path != wantPath {
		t.Fatalf("expected tracker to mirror contract artifact, got %#v", tracker)
	}

	summary, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "session.md"))
	if err != nil {
		t.Fatalf("read session summary: %v", err)
	}
	if !strings.Contains(string(summary), "reports/final.md") || !strings.Contains(string(summary), "Contract") {
		t.Fatalf("expected session summary to include contract artifact, got:\n%s", string(summary))
	}
}

func TestContractRefreshEmitsArtifactRequiredEvent(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if err := store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final.md with the final implementation summary.")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	var events []string
	if err := refreshContractForSession(store, func(eventType string, data map[string]any) {
		events = append(events, eventType)
		if eventType == "artifact.required" && data["count"] != 1 {
			t.Fatalf("expected one required artifact event, got %#v", data)
		}
	}, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}
	if !containsString(events, "contract.created") || !containsString(events, "artifact.required") {
		t.Fatalf("expected contract.created and artifact.required events, got %#v", events)
	}
}

func TestCompletionControllerRequiresSessionTouchedArtifact(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	artifactPath := filepath.Join(meta.Workdir, "reports", "final.md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("old content"), 0o600); err != nil {
		t.Fatalf("write baseline artifact: %v", err)
	}
	if err := store.AppendMessage(meta.ID, session.NewMessage("user", "Write reports/final.md with the final implementation summary.")); err != nil {
		t.Fatalf("append message: %v", err)
	}
	if err := refreshContractForSession(store, nil, meta); err != nil {
		t.Fatalf("refresh contract: %v", err)
	}

	var events []string
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, func(eventType string, _ map[string]any) {
		events = append(events, eventType)
	})
	kind, text := controller.requiredArtifactGate("finish")
	if kind != "required_artifact" || !strings.Contains(text, "not touched or changed") {
		t.Fatalf("expected stale artifact block, kind=%q text=%q", kind, text)
	}

	if err := os.WriteFile(artifactPath, []byte("new content"), 0o600); err != nil {
		t.Fatalf("update artifact: %v", err)
	}
	controller.TrackToolResult("write_file", session.ToolResult{
		Name:          "write_file",
		LLMOutput:     "wrote reports/final.md",
		DisplayOutput: "wrote reports/final.md",
		Metadata:      map[string]any{"path": artifactPath},
	}, 2)
	kind, text = controller.requiredArtifactGate("finish")
	if kind != "" || text != "" {
		t.Fatalf("expected artifact gate to pass after tracked write, kind=%q text=%q", kind, text)
	}
	if !containsString(events, "artifact.tracked") || !containsString(events, "artifact.gate.passed") {
		t.Fatalf("expected artifact tracking and pass events, got %#v", events)
	}
}

func TestProviderAttemptsLedgerAndLongRunCheckpointAreDurable(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	meta.ParentSessionID = "parent-session"
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := store.SaveState(meta.ID, session.State{
		Status:    session.StatusRunning,
		Phase:     "provider",
		LastError: "temporary timeout",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if err := store.AppendProviderAttempt(meta.ID, session.ProviderAttempt{
		Outcome:   "retry",
		Provider:  "openai",
		Model:     "gpt-5.4",
		Attempt:   2,
		Error:     "temporary timeout",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("append provider attempt: %v", err)
	}
	if err := writeSessionSummary(store, meta.ID); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := writeLongRunCheckpoint(store, meta.ID); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	attempts, err := store.LoadProviderAttempts(meta.ID)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Outcome != "retry" || attempts[0].Error != "temporary timeout" {
		t.Fatalf("unexpected provider attempts: %#v", attempts)
	}
	checkpoint, err := store.LoadLongRunCheckpoint(meta.ID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SessionID != meta.ID || len(checkpoint.ResumeHints) == 0 {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint)
	}
}

func TestCheckpointResumeHintWarnsOnIsolationAndTrustDrift(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	meta.Isolation = &session.IsolationInfo{
		Mode:          "copy",
		RequestedMode: "copy",
		Workdir:       filepath.Join(meta.Workdir, "isolated-old"),
		RootDir:       filepath.Join(meta.Workdir, "root-old"),
	}
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	if err := store.SaveContract(meta.ID, session.SessionContract{
		SchemaVersion: 1,
		ContractID:    "contract_" + meta.ID,
		Source:        "user_instruction",
		TrustSource:   "explicit_user",
		Profile:       "large_project",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("save contract: %v", err)
	}
	if err := writeLongRunCheckpoint(store, meta.ID); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	meta.Isolation = &session.IsolationInfo{
		Mode:          "copy",
		RequestedMode: "copy",
		Workdir:       filepath.Join(meta.Workdir, "isolated-new"),
		RootDir:       filepath.Join(meta.Workdir, "root-old"),
	}
	if err := store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save drifted metadata: %v", err)
	}
	contract, err := store.LoadContract(meta.ID)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	contract.TrustSource = "trusted_workspace"
	if err := store.SaveContract(meta.ID, contract); err != nil {
		t.Fatalf("save drifted contract: %v", err)
	}
	injected, warnings, err := appendCheckpointResumeHint(store, meta, meta.Provider, meta.Model)
	if err != nil {
		t.Fatalf("append checkpoint hint: %v", err)
	}
	if !injected {
		t.Fatal("expected checkpoint resume hint")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "isolation workdir changed") || !strings.Contains(joined, "trust source changed") {
		t.Fatalf("expected isolation and trust drift warnings, got %#v", warnings)
	}
	messages, err := store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Text, "drift warnings") {
		t.Fatalf("expected drift warning in resume note, got %#v", messages)
	}
}

func TestParentCoordinationGateBlocksWaitAllAndAllowsWaitAnyAfterOneCompletion(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	controller := NewCompletionController(store, meta.ID, meta.Workdir, false, nil)

	if err := addParentChildSession(store, meta.ID, "child-1", "wait-all"); err != nil {
		t.Fatalf("add child: %v", err)
	}
	decision := controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{}`))
	if decision.Status != GateBlock || decision.GateID != "parent_coordination" {
		t.Fatalf("expected wait-all parent coordination block, got %#v", decision)
	}

	if err := addParentQueueJob(store, meta.ID, "job-1", "wait-any"); err != nil {
		t.Fatalf("add queue job: %v", err)
	}
	if err := resolveParentQueueJob(store, meta.ID, "job-1", session.QueueStatusCompleted); err != nil {
		t.Fatalf("resolve queue job: %v", err)
	}
	decision = controller.EvaluateToolCall(nil, "finish", json.RawMessage(`{}`))
	if decision.Status != GateAllow {
		t.Fatalf("expected wait-any to allow finish after one completion, got %#v", decision)
	}
}

func TestParentCoordinationWritesParkedAndResumedEvents(t *testing.T) {
	store, meta := newRuntimeTestSession(t)
	if err := addParentChildSession(store, meta.ID, "child-1", "wait-all"); err != nil {
		t.Fatalf("add child: %v", err)
	}
	coordination, err := store.LoadParentCoordination(meta.ID)
	if err != nil {
		t.Fatalf("load coordination: %v", err)
	}
	if !coordination.Parked {
		t.Fatalf("expected parent to be parked, got %#v", coordination)
	}
	if err := resolveParentChildSession(store, meta.ID, "child-1", session.StatusCompleted); err != nil {
		t.Fatalf("resolve child: %v", err)
	}
	coordination, err = store.LoadParentCoordination(meta.ID)
	if err != nil {
		t.Fatalf("reload coordination: %v", err)
	}
	if coordination.Parked {
		t.Fatalf("expected parent to be resumed, got %#v", coordination)
	}
	events, err := store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	var foundParked, foundResumed bool
	for _, event := range events {
		if event.Type == "parent.coordination.parked" {
			foundParked = true
		}
		if event.Type == "parent.coordination.resumed" {
			foundResumed = true
		}
	}
	if !foundParked || !foundResumed {
		t.Fatalf("expected parked/resumed events, got %#v", events)
	}
}

func newRuntimeTestSession(t *testing.T) (*session.Store, session.SessionMetadata) {
	t.Helper()
	store := session.NewStore(t.TempDir())
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               session.NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             session.ModeRun,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store, meta
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
