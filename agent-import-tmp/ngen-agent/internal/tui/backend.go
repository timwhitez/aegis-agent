package tui

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	ngenrt "ngen/internal/runtime"
	"ngen/internal/task"
)

type Backend struct {
	Service           *ngenrt.Service
	PromptReturnDelay time.Duration
	TaskOpenDelay     time.Duration
}

type RefreshOptions struct {
	EventLimit int
	LoadMemory bool
}

type Snapshot struct {
	TaskView        task.TaskView
	Project         task.Project
	Criteria        task.CriteriaSnapshot
	Continuity      task.ContinuitySnapshot
	Sprint          task.SprintSnapshot
	Session         task.Session
	SessionSnapshot task.SessionSnapshot
	Messages        []task.SessionMessage
	Events          []task.Event
	Approvals       []task.ApprovalRecord
	OwnedApprovals  []task.ApprovalRecord
	Inputs          []task.InputRequestRecord
	Workers         []task.WorkerContract
	MemoryMarkdown  string
}

func NewBackend(service *ngenrt.Service) *Backend {
	return &Backend{
		Service:           service,
		PromptReturnDelay: testPromptReturnDelayFromEnv(),
		TaskOpenDelay:     testTaskOpenDelayFromEnv(),
	}
}

func (b *Backend) ListTasks(ctx context.Context) ([]task.TaskListEntry, error) {
	return b.Service.ListTasks(ctx)
}

func (b *Backend) StartSession(ctx context.Context, taskID string) (task.Session, error) {
	return b.Service.StartSession(ctx, taskID, "tui")
}

func (b *Backend) Refresh(ctx context.Context, taskID, sessionID string, opts RefreshOptions) (Snapshot, error) {
	taskView, err := b.Service.GetTask(ctx, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	projectView, err := b.Service.GetProject(ctx)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	criteria, err := b.Service.Store.LoadCriteria(taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	continuity, err := b.Service.Store.LoadContinuity(taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	sprint, err := b.Service.Store.LoadSprint(taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	session, messages, err := b.Service.ReadSession(ctx, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	sessionSnapshot, err := b.Service.SessionSnapshot(ctx, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	events, err := b.Service.TailEvents(taskID, opts.EventLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	approvals, err := b.Service.ListApprovals(ctx, taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	ownedApprovals, err := b.Service.ListOwnedApprovals(ctx, taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	inputs, err := b.Service.ListInputRequests(ctx, taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	workers, err := b.Service.ListWorkers(ctx, taskID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	memoryMarkdown := ""
	if opts.LoadMemory {
		data, err := b.Service.MemoryMarkdown(ctx)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, err
		}
		memoryMarkdown = string(data)
	}
	return Snapshot{
		TaskView:        taskView,
		Project:         projectView.Project,
		Criteria:        criteria,
		Continuity:      continuity,
		Sprint:          sprint,
		Session:         session,
		SessionSnapshot: sessionSnapshot,
		Messages:        messages,
		Events:          events,
		Approvals:       approvals,
		OwnedApprovals:  ownedApprovals,
		Inputs:          inputs,
		Workers:         workers,
		MemoryMarkdown:  memoryMarkdown,
	}, nil
}

func (b *Backend) PromptSession(ctx context.Context, sessionID, prompt string) error {
	_, _, _, err := b.Service.PromptSession(ctx, sessionID, prompt)
	if b.PromptReturnDelay > 0 {
		timer := time.NewTimer(b.PromptReturnDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func testPromptReturnDelayFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("NGEN_TUI_TEST_PROMPT_RETURN_DELAY_MS"))
	if raw == "" {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func testTaskOpenDelayFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("NGEN_TUI_TEST_TASK_OPEN_DELAY_MS"))
	if raw == "" {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func (b *Backend) Review(ctx context.Context, taskID string) error {
	_, err := b.Service.Review(ctx, taskID)
	return err
}

func (b *Backend) Approve(ctx context.Context, taskID, approvalID string) error {
	_, err := b.Service.DecideApproval(ctx, taskID, approvalID, "approved")
	return err
}

func (b *Backend) Deny(ctx context.Context, taskID, approvalID string) error {
	_, err := b.Service.DecideApproval(ctx, taskID, approvalID, "denied")
	return err
}

func (b *Backend) RespondInput(ctx context.Context, taskID, requestID, value string) error {
	_, err := b.Service.RespondInput(ctx, taskID, requestID, value)
	return err
}

func (b *Backend) ContinueWorker(ctx context.Context, parentTaskID, workerID string) error {
	_, err := b.Service.ContinueWorker(ctx, parentTaskID, workerID)
	return err
}

func (b *Backend) CancelSession(ctx context.Context, sessionID string) error {
	_, err := b.Service.CancelSession(ctx, sessionID)
	return err
}

func pendingApprovalRecords(records []task.ApprovalRecord) []task.ApprovalRecord {
	latest := make(map[string]task.ApprovalRecord)
	for _, record := range records {
		latest[record.ApprovalID] = record
	}
	pending := make([]task.ApprovalRecord, 0, len(latest))
	for _, record := range latest {
		if record.Status == "pending" {
			pending = append(pending, record)
		}
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].TS == pending[j].TS {
			return pending[i].ApprovalID < pending[j].ApprovalID
		}
		return pending[i].TS < pending[j].TS
	})
	return pending
}

func pendingInputRecord(records []task.InputRequestRecord) (task.InputRequestRecord, bool) {
	latest := make(map[string]task.InputRequestRecord)
	for _, record := range records {
		latest[record.RequestID] = record
	}
	var pending []task.InputRequestRecord
	for _, record := range latest {
		if strings.TrimSpace(record.Status) == "pending" {
			pending = append(pending, record)
		}
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].TS == pending[j].TS {
			return pending[i].RequestID < pending[j].RequestID
		}
		return pending[i].TS < pending[j].TS
	})
	if len(pending) == 0 {
		return task.InputRequestRecord{}, false
	}
	return pending[len(pending)-1], true
}
