package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestTaskCreateRejectsBlankSubject(t *testing.T) {
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
	if _, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: " \n\t "}); err == nil || !strings.Contains(err.Error(), "subject is required") {
		t.Fatalf("expected blank subject error, got %v", err)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("blank subject created task: %#v", tasks)
	}
}

func TestTaskCreateRejectsInvalidPriority(t *testing.T) {
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
	if _, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "existing", Priority: "urgent"}); err == nil || !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("expected invalid priority error, got %v", err)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("invalid priority created task: %#v", tasks)
	}
}

func TestTaskUpdateRejectsBlankTaskID(t *testing.T) {
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
	task, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "existing"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := UpdateTask(store, meta.ID, TaskUpdateInput{TaskID: " \n\t ", Status: "completed"}); err == nil || !strings.Contains(err.Error(), "task_id is required") {
		t.Fatalf("expected blank task_id error, got %v", err)
	}
	unchanged, err := store.GetTask(meta.ID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if unchanged.Status != "pending" {
		t.Fatalf("blank task_id update mutated task: %#v", unchanged)
	}
}

func TestTaskUpdateRejectsBlankSubject(t *testing.T) {
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
	task, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "existing"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := UpdateTask(store, meta.ID, TaskUpdateInput{TaskID: task.ID, Subject: " \n\t "}); err == nil || !strings.Contains(err.Error(), "subject is required") {
		t.Fatalf("expected blank subject error, got %v", err)
	}
	unchanged, err := store.GetTask(meta.ID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if unchanged.Subject != "existing" {
		t.Fatalf("blank subject update mutated task: %#v", unchanged)
	}
}

func TestTaskUpdateRejectsInvalidPriority(t *testing.T) {
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
	task, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "existing", Priority: "high"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := UpdateTask(store, meta.ID, TaskUpdateInput{TaskID: task.ID, Priority: "urgent"}); err == nil || !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("expected invalid priority error, got %v", err)
	}
	unchanged, err := store.GetTask(meta.ID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if unchanged.Priority != "high" {
		t.Fatalf("invalid priority update mutated task: %#v", unchanged)
	}
}

func TestSaveTodoRejectsInvalidItems(t *testing.T) {
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
	cases := []struct {
		name string
		todo []TodoItem
		want string
	}{
		{
			name: "blank content",
			todo: []TodoItem{{Content: " \n\t ", Status: "pending", Priority: "high"}},
			want: "content is required",
		},
		{
			name: "missing status",
			todo: []TodoItem{{Content: "Do work", Priority: "high"}},
			want: "status is required",
		},
		{
			name: "invalid priority",
			todo: []TodoItem{{Content: "Do work", Status: "pending", Priority: "urgent"}},
			want: `invalid todo priority "urgent"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.SaveTodo(meta.ID, tc.todo); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			todo, err := store.LoadTodo(meta.ID)
			if err != nil {
				t.Fatalf("load todo: %v", err)
			}
			if len(todo) != 0 {
				t.Fatalf("invalid todo item was persisted: %#v", todo)
			}
		})
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

func TestTaskMutationsReadLatestGraphUnderLock(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
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

	entered := make(chan struct{})
	release := make(chan struct{})
	var heldErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		heldErr = store.MutateTasks(meta.ID, func(tasks []Task) ([]Task, error) {
			close(entered)
			<-release
			now := time.Now().UTC().Format(time.RFC3339Nano)
			tasks = append(tasks, Task{
				ID:        "task_0001",
				Subject:   "held mutation",
				Status:    "pending",
				Priority:  "medium",
				CreatedAt: now,
				UpdatedAt: now,
			})
			return tasks, nil
		})
	}()
	<-entered

	otherStore := NewStore(root)
	resultCh := make(chan struct {
		task Task
		err  error
	}, 1)
	go func() {
		task, err := CreateTask(otherStore, meta.ID, TaskCreateInput{Subject: "concurrent create"})
		resultCh <- struct {
			task Task
			err  error
		}{task: task, err: err}
	}()

	close(release)
	wg.Wait()
	if heldErr != nil {
		t.Fatalf("held mutation: %v", heldErr)
	}
	result := <-resultCh
	if result.err != nil {
		t.Fatalf("concurrent create: %v", result.err)
	}
	if result.task.ID != "task_0002" {
		t.Fatalf("expected concurrent create to allocate after held mutation, got %#v", result.task)
	}
	tasks, err := store.ListTasks(meta.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 2 || tasks[0].ID != "task_0001" || tasks[1].ID != "task_0002" {
		t.Fatalf("expected both task mutations to persist in order, got %#v", tasks)
	}
}

func TestTaskListAndMutationReportCorruptTaskFiles(t *testing.T) {
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
	if _, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "existing"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	corruptPath := filepath.Join(store.SessionDir(meta.ID), "tasks", "task_0002.json")
	if err := os.WriteFile(corruptPath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write corrupt task file: %v", err)
	}

	if _, err := store.ListTasks(meta.ID); err == nil || !strings.Contains(err.Error(), "task_0002.json") {
		t.Fatalf("expected corrupt task file from ListTasks, got %v", err)
	}
	if _, err := CreateTask(store, meta.ID, TaskCreateInput{Subject: "new"}); err == nil || !strings.Contains(err.Error(), "task_0002.json") {
		t.Fatalf("expected corrupt task file from CreateTask, got %v", err)
	}
	if _, err := os.Stat(corruptPath); err != nil {
		t.Fatalf("corrupt task file should remain for recovery, got %v", err)
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

func TestBuildTaskBoardSeparatesCompletedAndCancelled(t *testing.T) {
	board := BuildTaskBoard(nil, []Task{
		{ID: "task_0001", Subject: "done", Status: "completed"},
		{ID: "task_0002", Subject: "stopped", Status: "cancelled"},
	})
	if board.Counters["completed"] != 1 || board.Counters["cancelled"] != 1 || board.Counters["done"] != 2 {
		t.Fatalf("expected separate completed/cancelled counters, got %#v", board.Counters)
	}
	if len(board.Groups["completed"]) != 1 || board.Groups["completed"][0].ID != "task_0001" {
		t.Fatalf("expected completed group to contain only completed tasks, got %#v", board.Groups["completed"])
	}
	if len(board.Groups["cancelled"]) != 1 || board.Groups["cancelled"][0].ID != "task_0002" {
		t.Fatalf("expected cancelled group to contain cancelled tasks, got %#v", board.Groups["cancelled"])
	}
	if len(board.Groups["done"]) != 2 {
		t.Fatalf("expected done group to include completed and cancelled tasks, got %#v", board.Groups["done"])
	}
}
