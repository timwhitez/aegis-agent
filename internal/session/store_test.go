package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go-cli-agent/internal/fileutil"
)

func TestStoreEnsureRootReappliesOwnerOnlyMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(root, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatalf("chmod drift: %v", err)
	}
	store := NewStoreWithDirMode(root, 0o700)
	if err := store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected root mode 0700, got %s", perm.String())
	}
}

func TestStoreAppendMessageReappliesParentAndFileModes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	sessionDir := store.SessionDir(meta.ID)
	messagePath := filepath.Join(sessionDir, "messages.jsonl")
	if err := os.Chmod(sessionDir, 0o777); err != nil {
		t.Fatalf("chmod session dir: %v", err)
	}
	if err := os.Chmod(messagePath, 0o666); err != nil {
		t.Fatalf("chmod message file: %v", err)
	}

	if err := store.AppendMessage(meta.ID, NewMessage("user", "hello")); err != nil {
		t.Fatalf("append message: %v", err)
	}

	sessionInfo, err := os.Stat(sessionDir)
	if err != nil {
		t.Fatalf("stat session dir: %v", err)
	}
	if perm := sessionInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected session dir mode 0700, got %s", perm.String())
	}
	messageInfo, err := os.Stat(messagePath)
	if err != nil {
		t.Fatalf("stat messages: %v", err)
	}
	if perm := messageInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected messages mode 0600, got %s", perm.String())
	}
}

func TestStoreAppendMessageRejectsSymlinkJSONL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := os.Remove(messagePath); err != nil {
		t.Fatalf("remove messages: %v", err)
	}
	if err := os.Symlink(outside, messagePath); err != nil {
		t.Fatalf("symlink messages: %v", err)
	}

	if err := store.AppendMessage(meta.ID, NewMessage("user", "hello")); err == nil {
		t.Fatal("expected symlinked messages.jsonl append to fail")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside symlink target was modified: %q", string(data))
	}
}

func TestStoreFeatureListRejectsSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside-feature-list.json")
	original := []byte(`{"features":[{"id":"feature_0001","status":"pending"}]}` + "\n")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	featureListPath := filepath.Join(store.SessionDir(meta.ID), "feature_list.json")
	if err := os.Symlink(outside, featureListPath); err != nil {
		t.Fatalf("symlink feature list: %v", err)
	}

	if _, err := store.LoadFeatureList(meta.ID); err == nil {
		t.Fatal("expected symlinked feature_list.json load to fail")
	}
	err := store.SaveFeatureList(meta.ID, FeatureList{Features: []Feature{{ID: "feature_0001", Status: "completed"}}})
	if err == nil {
		t.Fatal("expected symlinked feature_list.json save to fail")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != string(original) {
		t.Fatalf("outside symlink target was modified: %q", string(data))
	}
}

func TestStoreWriteTranscriptIgnoresPredictableTempSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	transcriptPath := filepath.Join(store.SessionDir(meta.ID), "artifacts", "transcripts", "audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o700); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.tmp")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, transcriptPath+".tmp"); err != nil {
		t.Fatalf("symlink predictable tmp: %v", err)
	}

	written, err := store.WriteTranscript(meta.ID, "audit.jsonl", []Message{NewMessage("user", "hello")})
	if err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if written != transcriptPath {
		t.Fatalf("unexpected transcript path: %s", written)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside predictable tmp symlink target was modified: %q", string(data))
	}
	if info, err := os.Lstat(transcriptPath); err != nil {
		t.Fatalf("stat transcript: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("transcript path should not be a symlink")
	}
}

func TestStoreSaveStateIgnoresPredictableTempSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	outside := filepath.Join(t.TempDir(), "outside-state.tmp")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, statePath+".tmp"); err != nil {
		t.Fatalf("symlink predictable state tmp: %v", err)
	}

	if err := store.SaveState(meta.ID, State{Status: StatusAwaitingInput, Phase: "turn_decide"}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("outside predictable state tmp symlink target was modified: %q", string(data))
	}
	if info, err := os.Lstat(statePath); err != nil {
		t.Fatalf("stat state: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("state path should not be a symlink")
	}
}

func TestStoreLoadStateRejectsSymlinkJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside-state.json")
	if err := os.WriteFile(outside, []byte(`{"status":"completed","phase":"outside"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	if err := os.Symlink(outside, statePath); err != nil {
		t.Fatalf("symlink state: %v", err)
	}

	if _, err := store.LoadState(meta.ID); err == nil {
		t.Fatal("expected symlinked state.json read to fail")
	}
}

func TestStoreLoadStateRejectsSymlinkSessionDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "state.json"), []byte(`{"status":"completed","phase":"outside"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write outside state: %v", err)
	}
	sessionDir := store.SessionDir(meta.ID)
	if err := os.RemoveAll(sessionDir); err != nil {
		t.Fatalf("remove session dir: %v", err)
	}
	if err := os.Symlink(outside, sessionDir); err != nil {
		t.Fatalf("symlink session dir: %v", err)
	}

	if _, err := store.LoadState(meta.ID); err == nil {
		t.Fatal("expected symlinked session dir read to fail")
	}
}

func TestStoreLoadStateRejectsOversizedJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	if err := os.Truncate(statePath, fileutil.MaxRegularFileReadBytes+1); err != nil {
		t.Fatalf("truncate state: %v", err)
	}
	if _, err := store.LoadState(meta.ID); err == nil || !strings.Contains(err.Error(), "exceeds maximum readable size") {
		t.Fatalf("expected oversized state.json read to fail, got %v", err)
	}
}

func TestStoreLoadMessagesRejectsSymlinkJSONL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	outsideMessage, err := json.Marshal(NewMessage("user", "outside"))
	if err != nil {
		t.Fatalf("marshal outside message: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside-messages.jsonl")
	if err := os.WriteFile(outside, append(outsideMessage, '\n'), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := os.Remove(messagePath); err != nil {
		t.Fatalf("remove messages: %v", err)
	}
	if err := os.Symlink(outside, messagePath); err != nil {
		t.Fatalf("symlink messages: %v", err)
	}

	if _, err := store.LoadMessages(meta.ID); err == nil {
		t.Fatal("expected symlinked messages.jsonl read to fail")
	}
}

func TestStoreLoadMessagesRejectsOversizedJSONLRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	messagePath := filepath.Join(store.SessionDir(meta.ID), "messages.jsonl")
	if err := os.Truncate(messagePath, fileutil.MaxRegularFileReadBytes+1); err != nil {
		t.Fatalf("truncate messages: %v", err)
	}
	if _, err := store.LoadMessages(meta.ID); err == nil || !strings.Contains(err.Error(), "session JSONL record exceeds maximum readable size") {
		t.Fatalf("expected oversized JSONL record read to fail, got %v", err)
	}
}

func TestStoreProviderRawSidecarRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	sidecar := ProviderRawSidecar{
		Provider:           "openai",
		Model:              "gpt-test",
		Turn:               2,
		ProviderResponseID: "resp_2",
		StopReason:         "done_candidate",
		SelectedRawItems: map[string]any{
			"status": "completed",
		},
	}
	if err := store.SaveProviderRawSidecar(meta.ID, sidecar); err != nil {
		t.Fatalf("save sidecar: %v", err)
	}
	loaded, err := store.LoadProviderRawSidecar(meta.ID, 2)
	if err != nil {
		t.Fatalf("load sidecar: %v", err)
	}
	if loaded.SchemaVersion != 1 || strings.TrimSpace(loaded.Timestamp) == "" {
		t.Fatalf("expected default schema version and timestamp, got %#v", loaded)
	}
	if loaded.Provider != "openai" || loaded.Model != "gpt-test" || loaded.ProviderResponseID != "resp_2" || loaded.StopReason != "done_candidate" {
		t.Fatalf("unexpected sidecar: %#v", loaded)
	}
	if loaded.SelectedRawItems["status"] != "completed" {
		t.Fatalf("unexpected raw items: %#v", loaded.SelectedRawItems)
	}
	if filepath.Base(store.ProviderRawSidecarPath(meta.ID, 2)) != "2.json" {
		t.Fatalf("unexpected sidecar path: %s", store.ProviderRawSidecarPath(meta.ID, 2))
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func hasPlanModeHistoryType(items []PlanModeHistoryEntry, target string) bool {
	for _, item := range items {
		if item.Type == target {
			return true
		}
	}
	return false
}

func hasGoalHistoryType(items []GoalHistoryEntry, target string) bool {
	for _, item := range items {
		if item.Type == target {
			return true
		}
	}
	return false
}

func TestStoreGoalLifecycleAccountingAndSummary(t *testing.T) {
	store := NewStore(t.TempDir())
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}
	tokenBudget := int64(5)
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Converge the feature safely",
		TokenBudget:         &tokenBudget,
		SuccessCriteria:     []string{"tests pass"},
		ValidationPlan:      []string{"go test ./internal/session"},
		Features:            []string{"durable goal state"},
		Milestones:          []string{"first checkpoint"},
		CreateTasksFromPlan: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if goal.Mode != GoalModeMission || goal.Mission == nil || len(goal.Mission.Features) != 1 || len(goal.Mission.Features[0].TaskIDs) != 1 {
		t.Fatalf("expected mission goal, got %#v", goal)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Subject != "durable goal state" || !containsString(tasks[0].Labels, "mission") {
		t.Fatalf("expected mission feature task, got %#v", tasks)
	}
	loaded, err := store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	if loaded.GoalID != goal.GoalID || loaded.SuccessCriteria[0].Text != "tests pass" || loaded.Mission.Features[0].TaskIDs[0] != tasks[0].ID {
		t.Fatalf("unexpected loaded goal: %#v", loaded)
	}
	items, err := store.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].GoalStatus != GoalStatusActive || items[0].GoalMode != GoalModeMission {
		t.Fatalf("expected goal summary fields, got %#v", items)
	}
	updated, limited, err := store.UpdateGoalAccounting(meta.ID, GoalUsageDelta{TokensUsedDelta: 6, TimeUsedSecondsDelta: 2, SourceTurn: 1})
	if err != nil {
		t.Fatalf("update accounting: %v", err)
	}
	if !limited || updated.Status != GoalStatusBudgetLimited || updated.TokensUsed != 6 {
		t.Fatalf("expected budget limited accounting, got limited=%v goal=%#v", limited, updated)
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	if len(history) < 3 {
		t.Fatalf("expected create/accounting/budget history, got %#v", history)
	}
	if cleared, err := store.ClearGoal(meta.ID); err != nil || !cleared {
		t.Fatalf("clear goal cleared=%v err=%v", cleared, err)
	}
	if _, err := store.LoadGoal(meta.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing goal after clear, got %v", err)
	}
}

func TestStoreGoalApprovalCreatesLinkedPlanMode(t *testing.T) {
	store := NewStore(t.TempDir())
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Plan before implementing",
		RequirePlanApproval: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if !created || planMode.LinkedGoalID != goal.GoalID || planMode.Status != PlanModeStatusPlanning {
		t.Fatalf("expected linked planning mode, created=%v plan=%#v goal=%#v", created, planMode, goal)
	}
	again, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode again: %v", err)
	}
	if created || again.PlanModeID != planMode.PlanModeID {
		t.Fatalf("expected existing linked plan mode, created=%v again=%#v", created, again)
	}
}

func TestStoreGoalApprovalRelinksExistingPendingPlanMode(t *testing.T) {
	store := NewStore(t.TempDir())
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	initial, err := store.CreatePlanMode(meta.ID, PlanModeDraft{
		Enabled:   true,
		Objective: "Plan before implementing",
		Source:    PlanModeSourceCLI,
	})
	if err != nil {
		t.Fatalf("create plan mode: %v", err)
	}
	if initial.LinkedGoalID != "" {
		t.Fatalf("expected initially unlinked plan mode, got %#v", initial)
	}
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Plan before implementing",
		RequirePlanApproval: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	planMode, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if created || planMode.PlanModeID != initial.PlanModeID || planMode.LinkedGoalID != goal.GoalID || planMode.Status != PlanModeStatusPlanning {
		t.Fatalf("expected existing pending plan mode relinked, created=%v plan=%#v initial=%#v goal=%#v", created, planMode, initial, goal)
	}
	history, err := store.LoadPlanModeHistory(meta.ID)
	if err != nil {
		t.Fatalf("load plan mode history: %v", err)
	}
	if !hasPlanModeHistoryType(history, "planmode.linked_goal") {
		t.Fatalf("expected relink history entry, got %#v", history)
	}
}

func TestStoreGoalApprovalCreatesFreshPendingGateAfterNeedsApprovalReset(t *testing.T) {
	store := NewStore(t.TempDir())
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:             true,
		Mode:                GoalModeMission,
		Objective:           "Plan before implementing",
		RequirePlanApproval: true,
		Source:              GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	first, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure plan mode: %v", err)
	}
	if !created || first.LinkedGoalID != goal.GoalID || first.Status != PlanModeStatusPlanning {
		t.Fatalf("expected initial linked planning mode, created=%v plan=%#v goal=%#v", created, first, goal)
	}
	if _, err := store.SubmitPlanMode(meta.ID, PlanModeSubmitInput{
		Title:        "Plan",
		Summary:      "Implement after approval.",
		PlanMarkdown: "# Plan\n\nImplement after approval.\n\n# Verification\n\nRun tests.",
		Verification: []string{"go test ./internal/session"},
		Source:       PlanModeSourceTool,
	}); err != nil {
		t.Fatalf("submit plan mode: %v", err)
	}
	if _, err := store.ApprovePlanMode(meta.ID, PlanModeSourceCLI); err != nil {
		t.Fatalf("approve plan mode: %v", err)
	}
	executing, err := store.MarkPlanModeExecuting(meta.ID, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("mark plan mode executing: %v", err)
	}
	if executing.PlanModeID != first.PlanModeID || executing.Status != PlanModeStatusExecuting {
		t.Fatalf("expected first plan mode executing, got %#v", executing)
	}
	goal, err = store.LoadGoal(meta.ID)
	if err != nil {
		t.Fatalf("load goal: %v", err)
	}
	goal.Mission.PlanStatus = "needs_approval"
	if err := store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save reset goal: %v", err)
	}
	second, created, err := store.EnsurePlanModeForGoal(meta.ID, goal, PlanModeSourceCLI)
	if err != nil {
		t.Fatalf("ensure reset plan mode: %v", err)
	}
	if !created || second.PlanModeID == first.PlanModeID || second.LinkedGoalID != goal.GoalID || second.Status != PlanModeStatusPlanning {
		t.Fatalf("expected fresh pending plan mode after needs_approval reset, created=%v first=%#v second=%#v goal=%#v", created, first, second, goal)
	}
}

func TestStoreCompleteGoalPersistsAuditAndItemEvidence(t *testing.T) {
	store := NewStore(t.TempDir())
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:         true,
		Objective:       "Finish with evidence",
		SuccessCriteria: []string{"tests pass"},
		ValidationPlan:  []string{"go test ./internal/session"},
		Source:          GoalSourceCLI,
	}); err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal, err := store.CompleteGoal(meta.ID, GoalCompletionInput{
		Source:      GoalSourceTool,
		CompletedBy: "tool",
		Summary:     "All requested checks passed.",
		Evidence:    []string{"go test ./internal/session"},
		CriteriaStatuses: []GoalItemStatusUpdate{{
			ID:       "criterion_0001",
			Status:   "verified",
			Evidence: []string{"criterion evidence"},
		}},
		ValidationStatuses: []GoalItemStatusUpdate{{
			ID:       "validation_0001",
			Status:   "verified",
			Evidence: []string{"validation evidence"},
		}},
	})
	if err != nil {
		t.Fatalf("complete goal: %v", err)
	}
	if goal.Status != GoalStatusComplete || goal.CompletedAt == "" || goal.CompletionAudit == nil {
		t.Fatalf("expected completed goal audit, got %#v", goal)
	}
	if goal.CompletionAudit.Summary == "" || !containsString(goal.CompletionAudit.Evidence, "go test ./internal/session") {
		t.Fatalf("expected completion evidence in snapshot, got %#v", goal.CompletionAudit)
	}
	if goal.SuccessCriteria[0].Status != "verified" || !containsString(goal.SuccessCriteria[0].Evidence, "criterion evidence") {
		t.Fatalf("expected criterion evidence in snapshot, got %#v", goal.SuccessCriteria[0])
	}
	if goal.ValidationPlan[0].Status != "verified" || goal.ValidationPlan[0].LastRunAt == "" || !containsString(goal.ValidationPlan[0].Evidence, "validation evidence") {
		t.Fatalf("expected validation evidence in snapshot, got %#v", goal.ValidationPlan[0])
	}
}

func TestMissionPlanCoverageReportsUncoveredAndInvalidAssignments(t *testing.T) {
	goal := SessionGoal{
		SchemaVersion: 1,
		SessionID:     "session_cov",
		GoalID:        "goal_cov",
		Mode:          GoalModeMission,
		Objective:     "Check coverage",
		Status:        GoalStatusActive,
		Mission: &MissionPlan{
			ValidationContract: []GoalValidation{
				{ID: "validation_api", Status: "pending"},
				{ID: "validation_cli", Status: "pending"},
				{ID: "validation_docs", Status: "pending"},
			},
			Features: []MissionFeature{
				{ID: "feature_api", Title: "API", Status: "pending", ClaimedAssertions: []string{"validation_api", "validation_unknown"}},
				{ID: "feature_empty", Title: "Empty", Status: "pending"},
			},
			Milestones: []MissionMilestone{
				{ID: "milestone_cli", Title: "CLI", Status: "pending", ValidationIDs: []string{"validation_cli"}},
				{ID: "milestone_empty", Title: "Empty", Status: "pending"},
			},
		},
	}
	coverage := CheckMissionPlanCoverage(goal)
	if coverage.ValidationTotal != 3 || coverage.CoveredAssertions != 2 || !coverage.ApprovalBlocked {
		t.Fatalf("unexpected coverage summary: %#v", coverage)
	}
	if !containsString(coverage.UncoveredAssertions, "validation_docs") || !containsString(coverage.FeaturesWithoutAssertions, "feature_empty") || !containsString(coverage.MilestonesWithoutValidation, "milestone_empty") {
		t.Fatalf("expected uncovered and unassigned facts, got %#v", coverage)
	}
	if !containsString(coverage.UnknownClaimedAssertions, "validation_unknown") {
		t.Fatalf("expected unknown claimed assertion, got %#v", coverage)
	}
}

func TestStoreRecordGoalProgressUpdatesMissionValidationAndBudgetWrapUp(t *testing.T) {
	store := NewStore(t.TempDir())
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	goal, err := store.CreateGoal(meta.ID, GoalDraft{
		Enabled:        true,
		Mode:           GoalModeMission,
		Objective:      "Record durable progress",
		ValidationPlan: []string{"go test ./internal/session"},
		Features:       []string{"feature work"},
		Milestones:     []string{"milestone work"},
		StopOnBudget:   true,
		Source:         GoalSourceCLI,
	})
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	goal.Status = GoalStatusBudgetLimited
	goal.BudgetWrapUpRequestedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveGoal(meta.ID, goal); err != nil {
		t.Fatalf("save budget goal: %v", err)
	}
	exitCode := 0
	updated, record, err := store.RecordGoalProgress(meta.ID, GoalProgressInput{
		Source:          GoalSourceTool,
		Kind:            "budget_wrapup",
		Summary:         "Implemented feature work; remaining docs.",
		Evidence:        []string{"go test ./internal/session"},
		LinkedArtifacts: []string{"reports/progress.md"},
		Commands:        []GoalProgressCommand{{Command: "go test ./internal/session", ExitCode: &exitCode, Summary: "passed"}},
		Blockers:        []string{"manual browser validation pending"},
		FeatureUpdates: []MissionFeatureProgressUpdate{{
			ID:                "feature_0001",
			Status:            "completed",
			Evidence:          []string{"feature evidence"},
			ClaimedAssertions: []string{"validation_0001"},
			ChildSessionIDs:   []string{"child_eval"},
		}},
		MilestoneUpdates: []MissionMilestoneProgressUpdate{{
			ID:            "milestone_0001",
			Status:        "completed",
			ValidationIDs: []string{"validation_0001"},
		}},
		ValidationUpdates: []GoalValidationProgressUpdate{{
			ID:              "validation_0001",
			Status:          "verified",
			Evidence:        []string{"validator evidence"},
			ChildSessionIDs: []string{"child_eval"},
			EvaluatorEvidence: []GoalEvaluatorEvidence{{
				ChildSessionID: "child_eval",
				Summary:        "independent evaluator passed",
				Status:         "verified",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("record progress: %v", err)
	}
	if record.Kind != "budget_wrapup" || updated.BudgetWrapUpRecordedAt == "" || !HasBudgetWrapUpRecord(updated) {
		t.Fatalf("expected budget wrap-up record, record=%#v goal=%#v", record, updated)
	}
	if updated.Mission.Features[0].Status != "completed" || !containsString(updated.Mission.Features[0].ClaimedAssertions, "validation_0001") {
		t.Fatalf("expected feature progress update, got %#v", updated.Mission.Features[0])
	}
	if updated.Mission.ValidationContract[0].Status != "verified" || len(updated.Mission.ValidationContract[0].EvaluatorEvidence) != 1 {
		t.Fatalf("expected validation evaluator evidence, got %#v", updated.Mission.ValidationContract[0])
	}
	history, err := store.LoadGoalHistory(meta.ID)
	if err != nil {
		t.Fatalf("load goal history: %v", err)
	}
	if !hasGoalHistoryType(history, "goal.progress.recorded") || !hasGoalHistoryType(history, "mission.validation.updated") {
		t.Fatalf("expected progress and validation history, got %#v", history)
	}
}

func TestStoreHonorsConfiguredDirModeForDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o750)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{Status: StatusRunning, Phase: "prepare", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Join(store.SessionDir(meta.ID), "artifacts"))
	if err != nil {
		t.Fatalf("stat artifacts dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o750 {
		t.Fatalf("expected artifacts dir mode 0750, got %s", perm.String())
	}

	fileInfo, err := os.Stat(filepath.Join(store.SessionDir(meta.ID), "state.json"))
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o640 {
		t.Fatalf("expected file mode 0640, got %s", perm.String())
	}
}

func TestStoreSaveStateRefreshesUpdatedAt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{
		Status:    StatusRunning,
		Phase:     "prepare",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	state.Status = StatusAwaitingInput
	if err := store.SaveState(meta.ID, state); err != nil {
		t.Fatalf("save state: %v", err)
	}

	loaded, err := store.LoadState(meta.ID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.UpdatedAt == "2026-01-01T00:00:00Z" || loaded.UpdatedAt == "" {
		t.Fatalf("expected UpdatedAt to refresh, got %q", loaded.UpdatedAt)
	}
}

func TestStoreRejectsPathLikeRecordIDs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create valid session: %v", err)
	}

	badSession := "../outside"
	if err := store.Create(SessionMetadata{ID: badSession}, State{}); err == nil {
		t.Fatal("expected Create to reject path-like session id")
	}
	if _, err := store.LoadMetadata(badSession); err == nil {
		t.Fatal("expected LoadMetadata to reject path-like session id")
	}
	if err := store.DeleteSessionTree(badSession); err == nil {
		t.Fatal("expected DeleteSessionTree to reject path-like session id")
	}
	if _, err := os.Stat(filepath.Join(root, "..", "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected outside path to remain absent, got %v", err)
	}

	if err := store.SaveTask(meta.ID, Task{ID: "../state"}); err == nil {
		t.Fatal("expected SaveTask to reject path-like task id")
	}
	if _, err := store.GetTask(meta.ID, "../state"); err == nil {
		t.Fatal("expected GetTask to reject path-like task id")
	}
	if err := store.SaveJob(QueueJob{ID: "../job", Status: QueueStatusQueued, Prompt: "x", Mode: ModeExec}); err == nil {
		t.Fatal("expected SaveJob to reject path-like queue job id")
	}
	if _, err := store.LoadJob("../job"); err == nil {
		t.Fatal("expected LoadJob to reject path-like queue job id")
	}
}

func TestStoreRejectsPathLikeArtifactPaths(t *testing.T) {
	temp := t.TempDir()
	root := filepath.Join(temp, "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create valid session: %v", err)
	}
	statePath := filepath.Join(store.SessionDir(meta.ID), "state.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state before artifact write: %v", err)
	}

	if _, err := store.WriteArtifact(meta.ID, "../state.json", map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject parent traversal")
	}
	if _, err := store.WriteArtifact(meta.ID, "../../../escaped.json", map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject root escape")
	}
	if _, err := store.WriteArtifact(meta.ID, "/tmp/escaped.json", map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject absolute path")
	}
	if _, err := store.WriteArtifact(meta.ID, `compactions\..\state.json`, map[string]any{"bad": true}); err == nil {
		t.Fatal("expected WriteArtifact to reject backslash parent traversal")
	}
	if _, err := store.WriteTranscript(meta.ID, "../events.jsonl", []Message{NewMessage("user", "bad")}); err == nil {
		t.Fatal("expected WriteTranscript to reject parent traversal")
	}

	stateAfter, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after rejected artifact writes: %v", err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Fatalf("state.json was modified by rejected artifact write:\nbefore: %s\nafter: %s", stateBefore, stateAfter)
	}
	if _, err := os.Stat(filepath.Join(temp, "escaped.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected escaped artifact to remain absent, got %v", err)
	}
}

func TestUpdateSteerRequestsMergesConcurrentAppend(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := NewSteerRequest("first", false)
	if err := storeA.AppendSteerRequest(meta.ID, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	snapshot, err := storeA.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	snapshot[0].Status = SteerStatusAccepted

	second := NewSteerRequest("second", false)
	if err := storeB.AppendSteerRequest(meta.ID, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if err := storeA.UpdateSteerRequests(meta.ID, snapshot); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}

	loaded, err := storeA.LoadSteerRequests(meta.ID)
	if err != nil {
		t.Fatalf("load merged: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected both steer requests to survive merge, got %#v", loaded)
	}
	statusByText := map[string]string{}
	for _, request := range loaded {
		statusByText[request.Text] = request.Status
	}
	if statusByText["first"] != SteerStatusAccepted || statusByText["second"] != SteerStatusPending {
		t.Fatalf("unexpected merged statuses: %#v", loaded)
	}
}

func TestUpdateBackgroundNotificationsMergesConcurrentAppend(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := storeA.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := NewBackgroundNotification(QueueJob{
		ID:            "job_background_first",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_first",
		SessionStatus: StatusCompleted,
	})
	if err := storeA.AppendBackgroundNotification(meta.ID, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	snapshot, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	snapshot[0].DeliveryStatus = BackgroundNotificationAccepted

	second := NewBackgroundNotification(QueueJob{
		ID:            "job_background_second",
		Status:        QueueStatusCompleted,
		SessionID:     "child_background_second",
		SessionStatus: StatusCompleted,
	})
	if err := storeB.AppendBackgroundNotification(meta.ID, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	if err := storeA.UpdateBackgroundNotifications(meta.ID, snapshot); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}

	loaded, err := storeA.LoadBackgroundNotifications(meta.ID)
	if err != nil {
		t.Fatalf("load merged: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected both background notifications to survive merge, got %#v", loaded)
	}
	statusByJob := map[string]string{}
	for _, notification := range loaded {
		statusByJob[notification.QueueJobID] = notification.DeliveryStatus
	}
	if statusByJob["job_background_first"] != BackgroundNotificationAccepted || statusByJob["job_background_second"] != BackgroundNotificationPending {
		t.Fatalf("unexpected merged statuses: %#v", loaded)
	}
}

func TestAppendSteerRequestRejectsSymlinkLockFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.lock")
	lockPath := filepath.Join(store.SessionDir(meta.ID), "control", "steer.lock")
	if err := os.Symlink(outside, lockPath); err != nil {
		t.Fatalf("symlink lock: %v", err)
	}

	if err := store.AppendSteerRequest(meta.ID, NewSteerRequest("hello", false)); err == nil {
		t.Fatal("expected symlinked steer lock to fail")
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside lock target should not be created, got %v", err)
	}
}

func TestAppendBackgroundNotificationRejectsSymlinkLockFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.lock")
	lockPath := filepath.Join(store.SessionDir(meta.ID), "control", "background.lock")
	if err := os.Symlink(outside, lockPath); err != nil {
		t.Fatalf("symlink lock: %v", err)
	}

	notification := NewBackgroundNotification(QueueJob{ID: "job_background_lock", Status: QueueStatusCompleted})
	if err := store.AppendBackgroundNotification(meta.ID, notification); err == nil {
		t.Fatal("expected symlinked background lock to fail")
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside lock target should not be created, got %v", err)
	}
}

func TestAppendSteerRequestRejectsSymlinkControlDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	if err := store.Create(meta, State{Status: StatusRunning, Phase: "prepare", UpdatedAt: now}); err != nil {
		t.Fatalf("create: %v", err)
	}
	controlDir := filepath.Join(store.SessionDir(meta.ID), "control")
	if err := os.RemoveAll(controlDir); err != nil {
		t.Fatalf("remove control dir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, controlDir); err != nil {
		t.Fatalf("symlink control dir: %v", err)
	}

	if err := store.AppendSteerRequest(meta.ID, NewSteerRequest("hello", false)); err == nil {
		t.Fatal("expected symlinked control dir to fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "steer.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside control target should not receive steer.jsonl, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "steer.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside control target should not receive steer.lock, got %v", err)
	}
}

func TestStoreListIncludesLastErrorInSummaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStoreWithDirMode(root, 0o700)
	meta := SessionMetadata{
		SchemaVersion:    1,
		ID:               NewSessionID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeRun,
		Provider:         "fake",
		Model:            "fake",
		CompletionPolicy: CompletionPolicyInteractive,
	}
	state := State{
		Status:    StatusFailed,
		Phase:     "provider_call",
		LastError: "auth_unavailable: no auth available",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create: %v", err)
	}

	items, err := store.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one summary, got %#v", items)
	}
	if items[0].LastError != state.LastError {
		t.Fatalf("expected last_error to be preserved, got %#v", items[0])
	}
}

func TestStoreClaimNextQueuedJobIsAtomicAcrossStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	storeA := NewStore(root)
	storeB := NewStore(root)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_atomic",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := storeA.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	type claimResult struct {
		job QueueJob
		ok  bool
		err error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{storeA, storeB} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			job, ok, err := store.ClaimNextQueuedJob()
			results <- claimResult{job: job, ok: ok, err: err}
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)

	var claimed []claimResult
	for result := range results {
		if result.err != nil {
			t.Fatalf("claim job: %v", result.err)
		}
		if result.ok {
			claimed = append(claimed, result)
		}
	}
	if len(claimed) != 1 {
		t.Fatalf("expected exactly one claimant, got %d", len(claimed))
	}
	if claimed[0].job.ID != job.ID || claimed[0].job.Status != QueueStatusRunning {
		t.Fatalf("unexpected claimed job: %#v", claimed[0].job)
	}
	loaded, err := storeA.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load claimed job: %v", err)
	}
	if loaded.Status != QueueStatusRunning {
		t.Fatalf("expected running persisted status, got %#v", loaded)
	}
}

func TestClaimNextQueuedJobWritesLease(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_lease",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
	}
	if err := store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if !ok {
		t.Fatal("expected queued job to be claimed")
	}
	if claimed.Status != QueueStatusRunning || claimed.ClaimedBy == "" || claimed.ClaimedAt == "" || claimed.HeartbeatAt == "" || claimed.WorkerPID == 0 || claimed.ProcessStartID == "" {
		t.Fatalf("expected running lease fields, got %#v", claimed)
	}
	if claimed.HeartbeatAt != claimed.ClaimedAt {
		t.Fatalf("expected initial heartbeat to match claimed_at, got %#v", claimed)
	}
}

func TestClaimNextQueuedJobSkipsMismatchedQueueFilename(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	if err := store.ensureQueueDirs(); err != nil {
		t.Fatalf("ensure queue dirs: %v", err)
	}
	mismatched := QueueJob{
		SchemaVersion: 1,
		ID:            "job_actual",
		Status:        QueueStatusQueued,
		Prompt:        "do work",
		Mode:          ModeExec,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.writeJSONFile(filepath.Join(store.queueStatusDir(QueueStatusQueued), "job_other.json"), mismatched); err != nil {
		t.Fatalf("write mismatched job: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedJob()
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if ok {
		t.Fatalf("expected mismatched queue file to be skipped, got %#v", claimed)
	}
	if _, err := os.Stat(filepath.Join(store.queueStatusDir(QueueStatusQueued), "job_other.json")); err != nil {
		t.Fatalf("expected mismatched job to remain queued for diagnostics, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.queueStatusDir(QueueStatusRunning), "job_other.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected mismatched job not to move to running, got %v", err)
	}
}

func TestReconcileStaleRunningJobWithoutSessionFailsJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	oldHeartbeat := time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_stale_orphan",
		Status:        QueueStatusRunning,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
		ClaimedBy:     "process:test",
		ClaimedAt:     oldHeartbeat,
		HeartbeatAt:   oldHeartbeat,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if reconciled.Status != QueueStatusFailed || !strings.Contains(reconciled.LastError, "stale") || !strings.Contains(reconciled.LastError, "no linked session") {
		t.Fatalf("expected stale orphan job to fail, got %#v", reconciled)
	}
}

func TestReconcileKeepsRecentHeartbeatRunningJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		SchemaVersion: 1,
		ID:            "job_recent_orphan",
		Status:        QueueStatusRunning,
		Prompt:        "do work",
		Mode:          ModeExec,
		Background:    true,
		ClaimedBy:     "process:test",
		ClaimedAt:     now,
		HeartbeatAt:   now,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save running job: %v", err)
	}

	reconciled, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if reconciled.Status != QueueStatusRunning || reconciled.LastError != "" {
		t.Fatalf("expected recent orphan job to remain running, got %#v", reconciled)
	}
}

func TestListChildrenAndParentJobsUseCreationOrder(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	base := time.Date(2026, 5, 9, 3, 22, 19, 0, time.UTC)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_order",
		CreatedAt:        base.Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_order",
	}
	parentState := State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: base.Format(time.RFC3339Nano)}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	children := []struct {
		id        string
		createdAt time.Time
		updatedAt time.Time
	}{
		{"child_first", base.Add(1 * time.Second), base.Add(1 * time.Second)},
		{"child_second", base.Add(2 * time.Second), base.Add(10 * time.Second)},
	}
	for _, child := range children {
		meta := SessionMetadata{
			SchemaVersion:    1,
			ID:               child.id,
			CreatedAt:        child.createdAt.Format(time.RFC3339Nano),
			Workdir:          t.TempDir(),
			Mode:             ModeExec,
			Provider:         "openai",
			Model:            "gpt-5.4",
			CompletionPolicy: CompletionPolicyAutonomous,
			ParentSessionID:  parentMeta.ID,
			RootSessionID:    parentMeta.ID,
			AgentRole:        "evaluator",
			Depth:            1,
		}
		state := State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: child.updatedAt.Format(time.RFC3339Nano)}
		if err := store.Create(meta, state); err != nil {
			t.Fatalf("create child %s: %v", child.id, err)
		}
	}
	listedChildren, err := store.ListChildren(parentMeta.ID, 10)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(listedChildren) != 2 || listedChildren[0].ID != "child_first" || listedChildren[1].ID != "child_second" {
		t.Fatalf("expected child creation order, got %#v", listedChildren)
	}

	firstJob := QueueJob{
		SchemaVersion:   1,
		ID:              "job_first",
		CreatedAt:       base.Add(1 * time.Second).Format(time.RFC3339Nano),
		Status:          QueueStatusQueued,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "first",
		Mode:            ModeExec,
		Background:      true,
	}
	secondJob := QueueJob{
		SchemaVersion:   1,
		ID:              "job_second",
		CreatedAt:       base.Add(2 * time.Second).Format(time.RFC3339Nano),
		Status:          QueueStatusQueued,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "second",
		Mode:            ModeExec,
		Background:      true,
	}
	if err := store.SaveJob(firstJob); err != nil {
		t.Fatalf("save first job: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := store.SaveJob(secondJob); err != nil {
		t.Fatalf("save second job: %v", err)
	}
	listedJobs, err := store.ListJobsByParent(parentMeta.ID, 10)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(listedJobs) != 2 || listedJobs[0].ID != "job_first" || listedJobs[1].ID != "job_second" {
		t.Fatalf("expected job creation order, got %#v", listedJobs)
	}
}

func TestReconcileFailedJobUpdatesLinkedRunningSession(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_failed_job",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_failed_job",
	}
	parentState := State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	coordination := ParentCoordination{
		SchemaVersion:       1,
		ParentSessionID:     parentMeta.ID,
		WaitMode:            "all",
		UnresolvedQueueJobs: []string{"job_linked_failed"},
		UpdatedAt:           now,
	}
	if err := store.SaveParentCoordination(parentMeta.ID, coordination); err != nil {
		t.Fatalf("save parent coordination: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_linked_failed",
		CreatedAt:       now,
		Status:          QueueStatusFailed,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		AgentName:       "scan-store-and-data-layer",
		AgentRole:       "evaluator",
		Prompt:          "scan",
		Mode:            ModeExec,
		Background:      true,
		LastError:       "json: error calling MarshalJSON for type json.RawMessage: unexpected end of JSON input",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save failed job: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_linked_failed",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "scan-store-and-data-layer",
		AgentRole:        "evaluator",
		QueueJobID:       job.ID,
		Depth:            1,
	}
	childState := State{Status: StatusRunning, Phase: "compact", UpdatedAt: now}
	if err := store.Create(childMeta, childState); err != nil {
		t.Fatalf("create child: %v", err)
	}

	repaired, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load repaired job: %v", err)
	}
	if repaired.Status != QueueStatusFailed || repaired.SessionID != childMeta.ID || repaired.SessionStatus != StatusFailed {
		t.Fatalf("expected failed job linked to failed child session, got %#v", repaired)
	}
	if !strings.Contains(repaired.LastError, "MarshalJSON") {
		t.Fatalf("expected job error to be preserved, got %#v", repaired)
	}
	loadedChildState, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state: %v", err)
	}
	if loadedChildState.Status != StatusFailed || !strings.Contains(loadedChildState.LastError, "MarshalJSON") {
		t.Fatalf("expected linked child state to be failed, got %#v", loadedChildState)
	}
	loadedCoordination, err := store.LoadParentCoordination(parentMeta.ID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if stringSliceContains(loadedCoordination.UnresolvedQueueJobs, job.ID) || !stringSliceContains(loadedCoordination.FailedQueueJobs, job.ID) {
		t.Fatalf("expected parent coordination to move job to failed, got %#v", loadedCoordination)
	}
}

func TestDeleteSessionTreeDoesNotDeadlockWithReconcilableJob(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_delete",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_delete",
	}
	if err := store.Create(parentMeta, State{Status: StatusRunning, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_delete",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		QueueJobID:       "job_delete",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.SaveJob(QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusFailed,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		Prompt:          "delete",
		Mode:            ModeExec,
		Background:      true,
		LastError:       "failed before delete",
	}); err != nil {
		t.Fatalf("save job: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- store.DeleteSessionTree(parentMeta.ID)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delete session tree: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("DeleteSessionTree appears deadlocked")
	}
	if _, err := os.Stat(store.SessionDir(parentMeta.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected parent session removed, got %v", err)
	}
	if _, err := os.Stat(store.SessionDir(childMeta.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected child session removed, got %v", err)
	}
	if _, err := store.LoadJob(childMeta.QueueJobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected linked job removed, got %v", err)
	}
}

func TestClearHistoryRejectsSymlinkedSessionDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	if err := store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "session-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := store.ClearHistory()
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "session-link")); statErr != nil {
		t.Fatalf("expected symlink to remain, got %v", statErr)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("expected outside dir to remain, got %v", statErr)
	}
}

func TestClearHistoryRemovesRegularRootFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	if err := store.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	stale := filepath.Join(root, "stale.json")
	if err := os.WriteFile(stale, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	if err := store.ClearHistory(); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	if _, statErr := os.Stat(stale); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected stale file removed, got %v", statErr)
	}
}

func TestListPageReconcilesLinkedQueueJobStatus(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_list_reconcile",
		CreatedAt:        now,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_list_reconcile",
		RootSessionID:    "parent_list_reconcile",
		AgentName:        "child-list",
		AgentRole:        "evaluator",
		QueueJobID:       "job_list_reconcile",
		Depth:            1,
	}
	childState := State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: now}
	if err := store.Create(childMeta, childState); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.SaveJob(QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       now,
		Status:          QueueStatusFailed,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "fail",
		Mode:            ModeExec,
		Background:      true,
		LastError:       "worker failed",
	}); err != nil {
		t.Fatalf("save failed job: %v", err)
	}

	items, _, err := store.ListPage(10, 0)
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	var childSummary *SessionSummary
	for i := range items {
		if items[i].ID == childMeta.ID {
			childSummary = &items[i]
			break
		}
	}
	if childSummary == nil || childSummary.Status != StatusFailed || childSummary.LastError != "worker failed" {
		t.Fatalf("expected list page to reconcile linked failed job, got %#v", childSummary)
	}
	loadedState, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state: %v", err)
	}
	if loadedState.Status != StatusFailed || loadedState.LastError != "worker failed" {
		t.Fatalf("expected child state to be reconciled, got %#v", loadedState)
	}
}

func TestReconcileStaleLinkedRunningJobFailsSession(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	oldHeartbeat := time.Now().UTC().Add(-queueRunningStaleAfter - time.Minute).Format(time.RFC3339Nano)
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_stale_linked",
		CreatedAt:        oldHeartbeat,
		Workdir:          t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  "parent_stale_linked",
		RootSessionID:    "parent_stale_linked",
		AgentName:        "stale-child",
		AgentRole:        "evaluator",
		QueueJobID:       "job_stale_linked",
		Depth:            1,
	}
	if err := store.Create(childMeta, State{Status: StatusRunning, Phase: "provider_call", UpdatedAt: oldHeartbeat}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := store.SaveJob(QueueJob{
		SchemaVersion:   1,
		ID:              childMeta.QueueJobID,
		CreatedAt:       oldHeartbeat,
		Status:          QueueStatusRunning,
		ClaimedAt:       oldHeartbeat,
		HeartbeatAt:     oldHeartbeat,
		ParentSessionID: childMeta.ParentSessionID,
		RootSessionID:   childMeta.RootSessionID,
		AgentName:       childMeta.AgentName,
		AgentRole:       childMeta.AgentRole,
		Prompt:          "stale",
		Mode:            ModeExec,
		Background:      true,
	}); err != nil {
		t.Fatalf("save stale running job: %v", err)
	}

	reconciled, err := store.LoadJob(childMeta.QueueJobID)
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if reconciled.Status != QueueStatusFailed || reconciled.SessionStatus != StatusFailed || !strings.Contains(reconciled.LastError, "linked running session heartbeat is stale") {
		t.Fatalf("expected stale linked job to fail, got %#v", reconciled)
	}
	loadedState, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state: %v", err)
	}
	if loadedState.Status != StatusFailed || !strings.Contains(loadedState.LastError, "linked running session heartbeat is stale") {
		t.Fatalf("expected stale linked child session to fail, got %#v", loadedState)
	}
}

func TestReconcileCompletedSessionCompletesJob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)

	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_session",
	}
	parentState := State{
		Status:    StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	requestedWorkdir := t.TempDir()
	stale := QueueJob{
		SchemaVersion:    1,
		ID:               "job_repair",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Status:           QueueStatusRunning,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "child-two",
		AgentRole:        "evaluator",
		Prompt:           "Finish child two.",
		Mode:             ModeExec,
		RequestedWorkdir: requestedWorkdir,
		Background:       true,
		IsolationMode:    "auto",
	}
	if err := store.SaveJob(stale); err != nil {
		t.Fatalf("save stale job: %v", err)
	}

	effectiveWorkdir := t.TempDir()
	childMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "child_session",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          effectiveWorkdir,
		RequestedWorkdir: requestedWorkdir,
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		ParentSessionID:  parentMeta.ID,
		RootSessionID:    parentMeta.ID,
		AgentName:        "child-two",
		AgentRole:        "evaluator",
		QueueJobID:       stale.ID,
		Depth:            1,
	}
	childState := State{
		Status:               StatusCompleted,
		Phase:                "turn_decide",
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		LastAssistantExcerpt: "Done.",
	}
	if err := store.Create(childMeta, childState); err != nil {
		t.Fatalf("create child: %v", err)
	}
	outputPath := filepath.Join(effectiveWorkdir, "reports", "child-two.md")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("CHILD_TWO_OK\n"), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if err := store.AppendMessage(childMeta.ID, NewToolMessage([]ToolResult{{
		Name:          "write_file",
		DisplayOutput: "ok",
		Metadata:      map[string]any{"path": outputPath},
	}})); err != nil {
		t.Fatalf("append tool message: %v", err)
	}

	repaired, err := store.LoadJob(stale.ID)
	if err != nil {
		t.Fatalf("load repaired job: %v", err)
	}
	if repaired.Status != QueueStatusCompleted {
		t.Fatalf("expected completed repaired job, got %#v", repaired)
	}
	if repaired.SessionID != childMeta.ID {
		t.Fatalf("expected repaired session id %s, got %#v", childMeta.ID, repaired)
	}
	if repaired.EffectiveWorkdir != effectiveWorkdir {
		t.Fatalf("expected effective workdir %s, got %#v", effectiveWorkdir, repaired)
	}
	if len(repaired.VisiblePaths) != 1 || repaired.VisiblePaths[0] != "reports/child-two.md" {
		t.Fatalf("unexpected visible paths: %#v", repaired.VisiblePaths)
	}
	syncedBytes, err := os.ReadFile(filepath.Join(requestedWorkdir, "reports", "child-two.md"))
	if err != nil {
		t.Fatalf("read synced output: %v", err)
	}
	if string(syncedBytes) != "CHILD_TWO_OK\n" {
		t.Fatalf("unexpected synced output: %q", string(syncedBytes))
	}
	notifications, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].QueueJobID != stale.ID {
		t.Fatalf("unexpected notifications: %#v", notifications)
	}
	eventsList, err := store.LoadEvents(parentMeta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	completedCount := 0
	for _, evt := range eventsList {
		if evt.Type != "queue.job.completed" {
			continue
		}
		jobID, _ := evt.Data["job_id"].(string)
		if jobID == stale.ID {
			completedCount++
		}
	}
	if completedCount != 1 {
		t.Fatalf("expected one repaired completion event, got %d", completedCount)
	}
	reloaded, err := store.LoadJob(stale.ID)
	if err != nil {
		t.Fatalf("reload repaired job: %v", err)
	}
	if reloaded.Status != QueueStatusCompleted {
		t.Fatalf("expected persisted completed job, got %#v", reloaded)
	}
}

func TestLoadJobRepairsMissingTerminalBackgroundNotification(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(root)
	parentMeta := SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_missing_notification",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          t.TempDir(),
		RequestedWorkdir: t.TempDir(),
		Mode:             ModeExec,
		Provider:         "openai",
		Model:            "gpt-5.4",
		CompletionPolicy: CompletionPolicyAutonomous,
		RootSessionID:    "parent_missing_notification",
	}
	parentState := State{
		Status:    StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.Create(parentMeta, parentState); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	job := QueueJob{
		SchemaVersion:   1,
		ID:              "job_missing_notification",
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Status:          QueueStatusCompleted,
		ParentSessionID: parentMeta.ID,
		RootSessionID:   parentMeta.ID,
		SessionID:       "child_missing_notification",
		SessionStatus:   StatusCompleted,
		AgentName:       "child",
		AgentRole:       "generator",
		Prompt:          "already completed",
		Mode:            ModeExec,
		Background:      true,
		FinalText:       "done",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save terminal job: %v", err)
	}
	before, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications before repair: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("expected missing notification before repair, got %#v", before)
	}
	if _, err := store.LoadJob(job.ID); err != nil {
		t.Fatalf("load terminal job: %v", err)
	}
	notifications, err := store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("load notifications after repair: %v", err)
	}
	if len(notifications) != 1 || notifications[0].QueueJobID != job.ID {
		t.Fatalf("expected repaired notification for %s, got %#v", job.ID, notifications)
	}
	eventsList, err := store.LoadEvents(parentMeta.ID)
	if err != nil {
		t.Fatalf("load events after repair: %v", err)
	}
	seen := map[string]bool{}
	for _, evt := range eventsList {
		jobID, _ := evt.Data["job_id"].(string)
		if jobID == job.ID {
			seen[evt.Type] = true
		}
	}
	if !seen["queue.job.notified"] || !seen["queue.job.completed"] {
		t.Fatalf("expected repaired lifecycle events, got %#v", seen)
	}
	if _, err := store.LoadJob(job.ID); err != nil {
		t.Fatalf("reload terminal job: %v", err)
	}
	notifications, err = store.LoadBackgroundNotifications(parentMeta.ID)
	if err != nil {
		t.Fatalf("reload notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected repair to stay idempotent, got %#v", notifications)
	}
}

func TestSyncQueueVisiblePathsRejectsRequestedSymlinkEscape(t *testing.T) {
	requestedWorkdir := t.TempDir()
	effectiveWorkdir := t.TempDir()
	outside := t.TempDir()

	outputPath := filepath.Join(effectiveWorkdir, "reports", "child-two.md")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		t.Fatalf("mkdir effective reports: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("CHILD_TWO_OK\n"), 0o600); err != nil {
		t.Fatalf("write effective output: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(requestedWorkdir, "reports")); err != nil {
		t.Fatalf("symlink requested reports: %v", err)
	}

	visible := syncQueueVisiblePaths(requestedWorkdir, effectiveWorkdir, []string{"reports/child-two.md"})
	if len(visible) != 0 {
		t.Fatalf("expected symlink escape copy to be skipped, got %#v", visible)
	}
	if _, err := os.Stat(filepath.Join(outside, "child-two.md")); !os.IsNotExist(err) {
		t.Fatalf("outside symlink target should not be written, stat err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(requestedWorkdir, "reports")); err != nil {
		t.Fatalf("requested symlink should be left untouched: %v", err)
	}
}

func TestCollectQueueVisiblePathsAllowsDotPrefixedDirectory(t *testing.T) {
	workdir := t.TempDir()
	outputPath := filepath.Join(workdir, "..reports", "child-two.md")
	visible := collectQueueVisiblePaths(workdir, []Message{NewToolMessage([]ToolResult{{
		Name:     "write_file",
		Metadata: map[string]any{"path": outputPath},
	}})})

	if len(visible) != 1 || visible[0] != filepath.ToSlash(filepath.Join("..reports", "child-two.md")) {
		t.Fatalf("expected dot-prefixed child path to stay visible, got %#v", visible)
	}
}

func TestLoadJobPreservesResumableChildAsBlocked(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job := QueueJob{
		ID:        "job-resumable",
		Status:    QueueStatusBlocked,
		Prompt:    "continue later",
		Mode:      ModeRun,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save blocked job: %v", err)
	}
	childMeta := SessionMetadata{
		ID:         "child-resumable",
		Mode:       ModeRun,
		Workdir:    t.TempDir(),
		QueueJobID: job.ID,
		CreatedAt:  now,
	}
	if err := store.Create(childMeta, State{Status: StatusAwaitingInput, Phase: "turn_decide", UpdatedAt: now}); err != nil {
		t.Fatalf("create child: %v", err)
	}

	repaired, err := store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if repaired.Status != QueueStatusBlocked {
		t.Fatalf("expected blocked job for resumable child, got %#v", repaired)
	}
	if repaired.SessionStatus != StatusAwaitingInput {
		t.Fatalf("expected awaiting child status, got %#v", repaired)
	}
	state, err := store.LoadState(childMeta.ID)
	if err != nil {
		t.Fatalf("load child state: %v", err)
	}
	if state.Status != StatusAwaitingInput {
		t.Fatalf("resumable child state should not be forced terminal, got %#v", state)
	}
}

func stringSliceContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
