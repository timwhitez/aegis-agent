package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

func TestStoreLoadJobRepairsCompletedChildSessionState(t *testing.T) {
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
