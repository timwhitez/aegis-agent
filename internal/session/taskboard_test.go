package session

import (
	"testing"
	"time"
)

func TestTaskLifecycleAutoUnlocksDependents(t *testing.T) {
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

	first, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "first"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "second", BlockedBy: []string{first.ID}})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if len(second.BlockedBy) != 1 {
		t.Fatalf("expected blocked_by, got %#v", second.BlockedBy)
	}
	second, err = UpdateTask(store, meta.ID, TaskUpdateInput{TaskID: first.ID, Status: "completed"})
	if err != nil {
		t.Fatalf("complete first: %v", err)
	}
	_ = second
	updated, err := store.GetTask(meta.ID, "task_0002")
	if err != nil {
		t.Fatalf("get second: %v", err)
	}
	if len(updated.BlockedBy) != 0 {
		t.Fatalf("expected second to be unblocked, got %#v", updated.BlockedBy)
	}
	completed, err := store.GetTask(meta.ID, first.ID)
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	if len(completed.Blocks) != 0 {
		t.Fatalf("expected completed task to drop unlocked dependents, got %#v", completed.Blocks)
	}
}

func TestTaskCycleRejected(t *testing.T) {
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

	first, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "first"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "second", BlockedBy: []string{first.ID}})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	_, err = UpdateTask(store, meta.ID, TaskUpdateInput{
		TaskID:       first.ID,
		AddBlockedBy: []string{second.ID},
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestTaskUpdateRemovesReverseEdges(t *testing.T) {
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

	first, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "first"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "second", BlockedBy: []string{first.ID}})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if _, err := UpdateTask(store, meta.ID, TaskUpdateInput{
		TaskID:          second.ID,
		RemoveBlockedBy: []string{first.ID},
	}); err != nil {
		t.Fatalf("remove blocked_by: %v", err)
	}

	updatedFirst, err := store.GetTask(meta.ID, first.ID)
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	if len(updatedFirst.Blocks) != 0 {
		t.Fatalf("expected reverse blocks edge to be removed, got %#v", updatedFirst.Blocks)
	}

	updatedSecond, err := store.GetTask(meta.ID, second.ID)
	if err != nil {
		t.Fatalf("get second: %v", err)
	}
	if len(updatedSecond.BlockedBy) != 0 {
		t.Fatalf("expected blocked_by edge to be removed, got %#v", updatedSecond.BlockedBy)
	}
}

func TestBuildTaskBoardIncludesInProgressGroup(t *testing.T) {
	board := BuildTaskBoard(nil, []Task{
		{ID: "task_0001", Subject: "active", Status: "in_progress"},
		{ID: "task_0002", Subject: "ready", Status: "pending"},
		{ID: "task_0003", Subject: "blocked", Status: "pending", BlockedBy: []string{"task_0001"}},
	})
	if board.Counters["in_progress"] != 1 {
		t.Fatalf("expected one in_progress task, got counters %#v", board.Counters)
	}
	if len(board.Groups["in_progress"]) != 1 || board.Groups["in_progress"][0].ID != "task_0001" {
		t.Fatalf("expected active task group, got %#v", board.Groups)
	}
}
