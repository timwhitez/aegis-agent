package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go-cli-agent/internal/events"
)

type Store struct {
	root     string
	dirMode  fs.FileMode
	fileMode fs.FileMode
	mu       sync.Mutex
}

const QueueRunningStaleAfter = 15 * time.Minute

const queueRunningStaleAfter = QueueRunningStaleAfter

var queueProcessStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
var queueProcessStartID = fmt.Sprintf("%d:%s", os.Getpid(), queueProcessStartedAt)

func NewStore(root string) *Store {
	return NewStoreWithDirMode(root, 0o700)
}

func NewStoreWithDirMode(root string, dirMode fs.FileMode) *Store {
	dirMode = normalizeDirMode(dirMode)
	return &Store{
		root:     root,
		dirMode:  dirMode,
		fileMode: deriveFileMode(dirMode),
	}
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) EnsureRoot() error {
	return s.ensureDir(s.root)
}

func (s *Store) SessionDir(sessionID string) string {
	return filepath.Join(s.root, sessionID)
}

func (s *Store) Create(meta SessionMetadata, state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.EnsureRoot(); err != nil {
		return err
	}
	dir := s.SessionDir(meta.ID)
	for _, path := range []string{
		dir,
		filepath.Join(dir, "control"),
		filepath.Join(dir, "tasks"),
		filepath.Join(dir, "artifacts"),
		filepath.Join(dir, "artifacts", "compactions"),
		filepath.Join(dir, "artifacts", "transcripts"),
		filepath.Join(dir, "checkpoints"),
	} {
		if err := s.ensureDir(path); err != nil {
			return err
		}
	}
	if err := s.writeJSONFile(filepath.Join(dir, "session.json"), meta); err != nil {
		return err
	}
	if err := s.writeJSONFile(filepath.Join(dir, "state.json"), state); err != nil {
		return err
	}
	for _, name := range []string{"messages.jsonl", "events.jsonl", "control/steer.jsonl", "control/background.jsonl"} {
		path := filepath.Join(dir, name)
		if err := s.writeBytesFile(path, nil); err != nil {
			return err
		}
	}
	return s.writeJSONFile(filepath.Join(dir, "todo.json"), []TodoItem{})
}

func (s *Store) LoadMetadata(sessionID string) (SessionMetadata, error) {
	var meta SessionMetadata
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "session.json"), &meta)
	return meta, err
}

func (s *Store) SaveMetadata(sessionID string, meta SessionMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(filepath.Join(s.SessionDir(sessionID), "session.json"), meta)
}

func (s *Store) LoadState(sessionID string) (State, error) {
	var state State
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "state.json"), &state)
	return state, err
}

func (s *Store) SaveState(sessionID string, state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.writeJSONFile(filepath.Join(s.SessionDir(sessionID), "state.json"), state)
}

func (s *Store) AppendMessage(sessionID string, message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendJSONL(filepath.Join(s.SessionDir(sessionID), "messages.jsonl"), message)
}

func (s *Store) LoadMessages(sessionID string) ([]Message, error) {
	path := filepath.Join(s.SessionDir(sessionID), "messages.jsonl")
	var out []Message
	err := readJSONL(path, &out)
	return out, err
}

func (s *Store) LoadEvents(sessionID string) ([]events.Event, error) {
	path := filepath.Join(s.SessionDir(sessionID), "events.jsonl")
	var out []events.Event
	err := readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []events.Event{}, nil
	}
	return out, err
}

func (s *Store) LoadContract(sessionID string) (SessionContract, error) {
	var contract SessionContract
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "contract.json"), &contract)
	return contract, err
}

func (s *Store) SaveContract(sessionID string, contract SessionContract) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(filepath.Join(s.SessionDir(sessionID), "contract.json"), contract)
}

func (s *Store) AppendContractHistory(sessionID string, contract SessionContract) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendJSONL(filepath.Join(s.SessionDir(sessionID), "artifacts", "contract-history.jsonl"), contract)
}

func (s *Store) LoadArtifactTracker(sessionID string) ([]RequiredArtifact, error) {
	var artifacts []RequiredArtifact
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "artifact-tracker.json"), &artifacts)
	if errors.Is(err, os.ErrNotExist) {
		return []RequiredArtifact{}, nil
	}
	return artifacts, err
}

func (s *Store) SaveArtifactTracker(sessionID string, artifacts []RequiredArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(filepath.Join(s.SessionDir(sessionID), "artifact-tracker.json"), artifacts)
}

func (s *Store) AppendProviderAttempt(sessionID string, attempt ProviderAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendJSONL(filepath.Join(s.SessionDir(sessionID), "provider-attempts.jsonl"), attempt)
}

func (s *Store) LoadProviderAttempts(sessionID string) ([]ProviderAttempt, error) {
	path := filepath.Join(s.SessionDir(sessionID), "provider-attempts.jsonl")
	var out []ProviderAttempt
	err := readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []ProviderAttempt{}, nil
	}
	return out, err
}

func (s *Store) LoadLongRunCheckpoint(sessionID string) (LongRunCheckpoint, error) {
	var checkpoint LongRunCheckpoint
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "checkpoints", "longrun-latest.json"), &checkpoint)
	return checkpoint, err
}

func (s *Store) SaveLongRunCheckpoint(sessionID string, checkpoint LongRunCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(filepath.Join(s.SessionDir(sessionID), "checkpoints", "longrun-latest.json"), checkpoint)
}

func (s *Store) LoadParentCoordination(sessionID string) (ParentCoordination, error) {
	var coordination ParentCoordination
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "parent-coordination.json"), &coordination)
	return coordination, err
}

func (s *Store) SaveParentCoordination(sessionID string, coordination ParentCoordination) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(filepath.Join(s.SessionDir(sessionID), "parent-coordination.json"), coordination)
}

func (s *Store) WriteSessionMarkdown(sessionID string, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeBytesFile(filepath.Join(s.SessionDir(sessionID), "session.md"), []byte(content))
}

func (s *Store) AppendEvent(sessionID string, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendJSONL(filepath.Join(s.SessionDir(sessionID), "events.jsonl"), event)
}

func (s *Store) LoadTodo(sessionID string) ([]TodoItem, error) {
	var todo []TodoItem
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "todo.json"), &todo)
	if errors.Is(err, os.ErrNotExist) {
		return []TodoItem{}, nil
	}
	return todo, err
}

func (s *Store) SaveTodo(sessionID string, todo []TodoItem) error {
	if err := validateTodo(todo); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONFile(filepath.Join(s.SessionDir(sessionID), "todo.json"), todo)
}

func (s *Store) AppendSteerRequest(sessionID string, request SteerRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendJSONL(filepath.Join(s.SessionDir(sessionID), "control", "steer.jsonl"), request)
}

func (s *Store) LoadSteerRequests(sessionID string) ([]SteerRequest, error) {
	path := filepath.Join(s.SessionDir(sessionID), "control", "steer.jsonl")
	var out []SteerRequest
	err := readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []SteerRequest{}, nil
	}
	return out, err
}

func (s *Store) UpdateSteerRequests(sessionID string, requests []SteerRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONL(filepath.Join(s.SessionDir(sessionID), "control", "steer.jsonl"), requests)
}

func (s *Store) AppendBackgroundNotification(sessionID string, notification BackgroundNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendJSONL(filepath.Join(s.SessionDir(sessionID), "control", "background.jsonl"), notification)
}

func (s *Store) EnsureBackgroundNotification(sessionID string, notification BackgroundNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.SessionDir(sessionID), "control", "background.jsonl")
	var existing []BackgroundNotification
	err := readJSONL(path, &existing)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if strings.TrimSpace(notification.QueueJobID) != "" {
		for _, item := range existing {
			if item.QueueJobID == notification.QueueJobID {
				return nil
			}
		}
	}
	return s.appendJSONL(path, notification)
}

func (s *Store) LoadBackgroundNotifications(sessionID string) ([]BackgroundNotification, error) {
	path := filepath.Join(s.SessionDir(sessionID), "control", "background.jsonl")
	var out []BackgroundNotification
	err := readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []BackgroundNotification{}, nil
	}
	return out, err
}

func (s *Store) UpdateBackgroundNotifications(sessionID string, notifications []BackgroundNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONL(filepath.Join(s.SessionDir(sessionID), "control", "background.jsonl"), notifications)
}

func (s *Store) PendingBackgroundNotifications(sessionID string) ([]BackgroundNotification, error) {
	notifications, err := s.LoadBackgroundNotifications(sessionID)
	if err != nil {
		return nil, err
	}
	var out []BackgroundNotification
	for _, notification := range notifications {
		if notification.DeliveryStatus == BackgroundNotificationPending {
			out = append(out, notification)
		}
	}
	return out, nil
}

func (s *Store) PendingSteerRequests(sessionID string) ([]SteerRequest, error) {
	requests, err := s.LoadSteerRequests(sessionID)
	if err != nil {
		return nil, err
	}
	var out []SteerRequest
	for _, req := range requests {
		if req.Status == SteerStatusPending {
			out = append(out, req)
		}
	}
	return out, nil
}

func (s *Store) List(limit int) ([]SessionSummary, error) {
	result, err := s.listAllSessions()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) ListPage(limit, offset int) ([]SessionSummary, int, error) {
	result, err := s.listAllSessions()
	if err != nil {
		return nil, 0, err
	}
	total := len(result)
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []SessionSummary{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return result[offset:end], total, nil
}

func (s *Store) listAllSessions() ([]SessionSummary, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []SessionSummary{}, nil
		}
		return nil, err
	}
	result := []SessionSummary{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := s.LoadMetadata(entry.Name())
		if err != nil {
			continue
		}
		state, err := s.LoadState(entry.Name())
		if err != nil {
			continue
		}
		result = append(result, SessionSummary{
			ID:              meta.ID,
			Status:          state.Status,
			Provider:        meta.Provider,
			Model:           meta.Model,
			CreatedAt:       meta.CreatedAt,
			UpdatedAt:       state.UpdatedAt,
			Phase:           state.Phase,
			LastError:       state.LastError,
			Workdir:         meta.Workdir,
			ParentSessionID: meta.ParentSessionID,
			RootSessionID:   meta.RootSessionID,
			AgentName:       meta.AgentName,
			AgentRole:       meta.AgentRole,
			Depth:           meta.Depth,
			QueueJobID:      meta.QueueJobID,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt > result[j].UpdatedAt
	})
	return result, nil
}

func (s *Store) ListChildren(parentSessionID string, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []SessionSummary{}, nil
		}
		return nil, err
	}
	result := []SessionSummary{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := s.LoadMetadata(entry.Name())
		if err != nil || meta.ParentSessionID != parentSessionID {
			continue
		}
		state, err := s.LoadState(entry.Name())
		if err != nil {
			continue
		}
		result = append(result, SessionSummary{
			ID:              meta.ID,
			Status:          state.Status,
			Provider:        meta.Provider,
			Model:           meta.Model,
			CreatedAt:       meta.CreatedAt,
			UpdatedAt:       state.UpdatedAt,
			Phase:           state.Phase,
			LastError:       state.LastError,
			Workdir:         meta.Workdir,
			ParentSessionID: meta.ParentSessionID,
			RootSessionID:   meta.RootSessionID,
			AgentName:       meta.AgentName,
			AgentRole:       meta.AgentRole,
			Depth:           meta.Depth,
			QueueJobID:      meta.QueueJobID,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt > result[j].UpdatedAt
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) NextTaskID(sessionID string) (string, error) {
	tasks, err := s.ListTasks(sessionID)
	if err != nil {
		return "", err
	}
	maxID := 0
	for _, task := range tasks {
		var value int
		if _, err := fmt.Sscanf(task.ID, "task_%04d", &value); err == nil && value > maxID {
			maxID = value
		}
	}
	return fmt.Sprintf("task_%04d", maxID+1), nil
}

func (s *Store) SaveTask(sessionID string, task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.SessionDir(sessionID), "tasks", task.ID+".json")
	return s.writeJSONFile(path, task)
}

func (s *Store) GetTask(sessionID, taskID string) (Task, error) {
	var task Task
	err := readJSONFile(filepath.Join(s.SessionDir(sessionID), "tasks", taskID+".json"), &task)
	return task, err
}

func (s *Store) ListTasks(sessionID string) ([]Task, error) {
	dir := filepath.Join(s.SessionDir(sessionID), "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Task{}, nil
		}
		return nil, err
	}
	var tasks []Task
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var task Task
		if err := readJSONFile(filepath.Join(dir, entry.Name()), &task); err == nil {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks, nil
}

func (s *Store) SaveTasks(sessionID string, tasks []Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.SessionDir(sessionID), "tasks")
	if err := s.ensureDir(dir); err != nil {
		return err
	}
	for _, task := range tasks {
		path := filepath.Join(dir, task.ID+".json")
		if err := s.writeJSONFile(path, task); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EnqueueJob(job QueueJob) error {
	if strings.TrimSpace(job.Status) == "" {
		job.Status = QueueStatusQueued
	}
	return s.SaveJob(job)
}

func (s *Store) SaveJob(job QueueJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveJobLocked(job)
}

func (s *Store) saveJobLocked(job QueueJob) error {
	if strings.TrimSpace(job.ID) == "" {
		return errors.New("job id is required")
	}
	if strings.TrimSpace(job.Status) == "" {
		job.Status = QueueStatusQueued
	}
	if !isQueueStatus(job.Status) {
		return fmt.Errorf("invalid queue job status: %s", job.Status)
	}
	if err := s.ensureQueueDirs(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if job.SchemaVersion == 0 {
		job.SchemaVersion = 1
	}
	if job.CreatedAt == "" {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	target := s.queueJobPath(job.Status, job.ID)
	if err := s.writeJSONFile(target, job); err != nil {
		return err
	}
	for _, status := range queueStatuses() {
		path := s.queueJobPath(status, job.ID)
		if path == target {
			continue
		}
		_ = os.Remove(path)
	}
	return nil
}

func (s *Store) LoadJob(jobID string) (QueueJob, error) {
	var job QueueJob
	for _, status := range queueStatuses() {
		path := s.queueJobPath(status, jobID)
		err := readJSONFile(path, &job)
		if err == nil {
			if repaired, changed := s.reconcileStaleRunningJob(job); changed {
				job = repaired
			}
			return job, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return QueueJob{}, err
		}
	}
	return QueueJob{}, os.ErrNotExist
}

func (s *Store) ListJobs(limit int) ([]QueueJob, error) {
	return s.listJobs(limit, "")
}

func (s *Store) ListJobsPage(limit, offset int) ([]QueueJob, int, error) {
	items, err := s.listJobs(0, "")
	if err != nil {
		return nil, 0, err
	}
	total := len(items)
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []QueueJob{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return items[offset:end], total, nil
}

func (s *Store) ListJobsByParent(parentSessionID string, limit int) ([]QueueJob, error) {
	return s.listJobs(limit, parentSessionID)
}

func (s *Store) listJobs(limit int, parentSessionID string) ([]QueueJob, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []QueueJob
	for _, status := range queueStatuses() {
		dir := s.queueStatusDir(status)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var job QueueJob
			if err := readJSONFile(filepath.Join(dir, entry.Name()), &job); err != nil {
				continue
			}
			if parentSessionID != "" && job.ParentSessionID != parentSessionID {
				continue
			}
			if repaired, changed := s.reconcileStaleRunningJob(job); changed {
				job = repaired
			}
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) DeleteSessionTree(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.LoadMetadata(sessionID); err != nil {
		return err
	}

	summaries, err := s.listAllSessions()
	if err != nil {
		return err
	}
	targets := map[string]struct{}{sessionID: {}}
	changed := true
	for changed {
		changed = false
		for _, item := range summaries {
			if _, ok := targets[item.ID]; ok {
				continue
			}
			if _, ok := targets[item.ParentSessionID]; ok {
				targets[item.ID] = struct{}{}
				changed = true
			}
		}
	}
	for id := range targets {
		if err := os.RemoveAll(s.SessionDir(id)); err != nil {
			return err
		}
	}
	jobs, err := s.listJobs(0, "")
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if _, ok := targets[job.ParentSessionID]; ok {
			if err := s.deleteJobLocked(job.ID); err != nil {
				return err
			}
			continue
		}
		if _, ok := targets[job.SessionID]; ok {
			if err := s.deleteJobLocked(job.ID); err != nil {
				return err
			}
			continue
		}
		if _, ok := targets[job.RootSessionID]; ok {
			if err := s.deleteJobLocked(job.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.EnsureRoot(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(s.root, entry.Name())); err != nil {
			return err
		}
	}
	return s.EnsureRoot()
}

func (s *Store) deleteJobLocked(jobID string) error {
	for _, status := range queueStatuses() {
		path := s.queueJobPath(status, jobID)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) ClaimNextQueuedJob() (QueueJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return QueueJob{}, false, err
	}
	dir := s.queueStatusDir(QueueStatusQueued)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return QueueJob{}, false, nil
		}
		return QueueJob{}, false, err
	}
	type candidate struct {
		name string
		job  QueueJob
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var job QueueJob
		if err := readJSONFile(filepath.Join(dir, entry.Name()), &job); err != nil {
			continue
		}
		candidates = append(candidates, candidate{name: entry.Name(), job: job})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].job.CreatedAt == candidates[j].job.CreatedAt {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].job.CreatedAt < candidates[j].job.CreatedAt
	})
	for _, candidate := range candidates {
		from := filepath.Join(dir, candidate.name)
		to := filepath.Join(s.queueStatusDir(QueueStatusRunning), candidate.name)
		if err := os.Rename(from, to); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return QueueJob{}, false, err
		}
		job := candidate.job
		now := time.Now().UTC().Format(time.RFC3339Nano)
		job.Status = QueueStatusRunning
		job.UpdatedAt = now
		applyQueueLease(&job, now)
		if err := s.writeJSONFile(to, job); err != nil {
			return QueueJob{}, false, err
		}
		return job, true, nil
	}
	return QueueJob{}, false, nil
}

func (s *Store) RefreshQueueJobHeartbeat(jobID string) (QueueJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureQueueDirs(); err != nil {
		return QueueJob{}, err
	}
	path := s.queueJobPath(QueueStatusRunning, jobID)
	var job QueueJob
	if err := readJSONFile(path, &job); err != nil {
		return QueueJob{}, err
	}
	if job.Status != QueueStatusRunning {
		return QueueJob{}, fmt.Errorf("queue job %s is not running", jobID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	job.UpdatedAt = now
	applyQueueLease(&job, now)
	if err := s.writeJSONFile(path, job); err != nil {
		return QueueJob{}, err
	}
	return job, nil
}

func (s *Store) WriteArtifact(sessionID, relativePath string, payload any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.SessionDir(sessionID), "artifacts", relativePath)
	if err := s.writeJSONFile(path, payload); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) WriteTranscript(sessionID, name string, messages []Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.SessionDir(sessionID), "artifacts", "transcripts", name)
	if err := s.writeJSONL(path, messages); err != nil {
		return "", err
	}
	return path, nil
}

func NewSessionID() string {
	now := time.Now().UTC().Format("20060102-150405")
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return now + "-fallback"
	}
	return now + "-" + hex.EncodeToString(buf)
}

func NewQueueJobID() string {
	return newRecordID("job")
}

func applyQueueLease(job *QueueJob, now string) {
	if job.ClaimedBy == "" {
		job.ClaimedBy = "process:" + queueProcessStartID
	}
	if job.ClaimedAt == "" {
		job.ClaimedAt = now
	}
	job.HeartbeatAt = now
	if job.WorkerPID == 0 {
		job.WorkerPID = os.Getpid()
	}
	if job.ProcessStartID == "" {
		job.ProcessStartID = queueProcessStartID
	}
}

func NewMessage(role, text string) Message {
	return Message{
		ID:        newRecordID("msg"),
		Role:      role,
		Text:      text,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func NewAssistantMessage(text string, toolCalls []ToolCall) Message {
	return Message{
		ID:        newRecordID("msg"),
		Role:      "assistant",
		Text:      text,
		ToolCalls: toolCalls,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func NewToolMessage(results []ToolResult) Message {
	return Message{
		ID:          newRecordID("msg"),
		Role:        "tool",
		ToolResults: results,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func NewSteerRequest(text string, interrupt bool) SteerRequest {
	return NewSteerRequestWithSource("cli", text, interrupt)
}

func NewSteerRequestWithSource(source, text string, interrupt bool) SteerRequest {
	if strings.TrimSpace(source) == "" {
		source = "cli"
	}
	return SteerRequest{
		ID:        newRecordID("steer"),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Source:    source,
		Text:      text,
		Interrupt: interrupt,
		Status:    SteerStatusPending,
	}
}

func NewBackgroundNotification(job QueueJob) BackgroundNotification {
	return BackgroundNotification{
		ID:               newRecordID("bg"),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Source:           "queue",
		QueueJobID:       job.ID,
		SessionID:        job.SessionID,
		AgentName:        job.AgentName,
		AgentRole:        job.AgentRole,
		Status:           job.Status,
		SessionStatus:    job.SessionStatus,
		RequestedWorkdir: job.RequestedWorkdir,
		EffectiveWorkdir: job.EffectiveWorkdir,
		VisiblePaths:     append([]string(nil), job.VisiblePaths...),
		FinalText:        job.FinalText,
		LastError:        job.LastError,
		DeliveryStatus:   BackgroundNotificationPending,
	}
}

func (s *Store) writeJSONFile(path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return s.writeBytesFile(path, data)
}

func (s *Store) writeBytesFile(path string, data []byte) error {
	if err := s.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, s.fileMode); err != nil {
		return err
	}
	chmodBestEffort(tmp, s.fileMode)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	chmodBestEffort(path, s.fileMode)
	return nil
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (s *Store) appendJSONL(path string, payload any) error {
	if err := s.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, s.fileMode)
	if err != nil {
		return err
	}
	defer file.Close()
	chmodBestEffort(path, s.fileMode)
	enc := json.NewEncoder(file)
	return enc.Encode(payload)
}

func (s *Store) writeJSONL(path string, payload any) error {
	if err := s.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, s.fileMode)
	if err != nil {
		return err
	}
	chmodBestEffort(tmp, s.fileMode)
	enc := json.NewEncoder(file)
	switch items := payload.(type) {
	case []Message:
		for _, item := range items {
			if err := enc.Encode(item); err != nil {
				file.Close()
				return err
			}
		}
	case []SteerRequest:
		for _, item := range items {
			if err := enc.Encode(item); err != nil {
				file.Close()
				return err
			}
		}
	case []BackgroundNotification:
		for _, item := range items {
			if err := enc.Encode(item); err != nil {
				file.Close()
				return err
			}
		}
	default:
		file.Close()
		return fmt.Errorf("unsupported jsonl payload %T", payload)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	chmodBestEffort(path, s.fileMode)
	return nil
}

func (s *Store) reconcileStaleRunningJob(job QueueJob) (QueueJob, bool) {
	if job.Status != QueueStatusRunning {
		return job, false
	}
	meta, state, messages, ok := s.findSessionForQueueJob(job.ID)
	if !ok {
		if !queueJobIsStale(job, time.Now().UTC()) {
			return job, false
		}
		job.Status = QueueStatusFailed
		job.SessionStatus = StatusFailed
		job.LastError = "queue job stale: running job has no linked session and heartbeat is stale"
		_ = s.SaveJob(job)
		if job.ParentSessionID != "" {
			s.ensureBackgroundNotification(job)
			s.ensureQueueLifecycleEvent(job, "queue.job.notified")
			s.ensureQueueLifecycleEvent(job, "queue.job.failed")
		}
		return job, true
	}
	if changed := syncRunningQueueJobSession(&job, meta, state, messages); changed && state.Status != StatusCompleted && state.Status != StatusFailed {
		_ = s.SaveJob(job)
		return job, true
	}
	switch state.Status {
	case StatusCompleted, StatusFailed:
	default:
		return job, false
	}
	if state.Status == StatusFailed {
		job.Status = QueueStatusFailed
	} else {
		job.Status = QueueStatusCompleted
	}
	_ = s.SaveJob(job)
	if job.ParentSessionID != "" {
		s.ensureBackgroundNotification(job)
		s.ensureQueueLifecycleEvent(job, "queue.job.notified")
		if job.Status == QueueStatusFailed {
			s.ensureQueueLifecycleEvent(job, "queue.job.failed")
		} else {
			s.ensureQueueLifecycleEvent(job, "queue.job.completed")
		}
	}
	return job, true
}

func syncRunningQueueJobSession(job *QueueJob, meta SessionMetadata, state State, messages []Message) bool {
	changed := false
	if job.SessionID != meta.ID {
		job.SessionID = meta.ID
		changed = true
	}
	if job.SessionStatus != state.Status {
		job.SessionStatus = state.Status
		changed = true
	}
	if job.FinalText != state.LastAssistantExcerpt {
		job.FinalText = state.LastAssistantExcerpt
		changed = true
	}
	if job.LastError != state.LastError {
		job.LastError = state.LastError
		changed = true
	}
	if job.EffectiveWorkdir != meta.Workdir {
		job.EffectiveWorkdir = meta.Workdir
		changed = true
	}
	visiblePaths := collectQueueVisiblePaths(meta.Workdir, messages)
	visiblePaths = syncQueueVisiblePaths(job.RequestedWorkdir, meta.Workdir, visiblePaths)
	if !equalStringSlices(job.VisiblePaths, visiblePaths) {
		job.VisiblePaths = visiblePaths
		changed = true
	}
	return changed
}

func queueJobIsStale(job QueueJob, now time.Time) bool {
	reference := firstNonEmpty(job.HeartbeatAt, job.ClaimedAt, job.UpdatedAt)
	if reference == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, reference)
	if err != nil {
		return false
	}
	return now.Sub(parsed) > queueRunningStaleAfter
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) findSessionForQueueJob(jobID string) (SessionMetadata, State, []Message, bool) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return SessionMetadata{}, State{}, nil, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionID := entry.Name()
		meta, err := s.LoadMetadata(sessionID)
		if err != nil || meta.QueueJobID != jobID {
			continue
		}
		state, err := s.LoadState(sessionID)
		if err != nil {
			return SessionMetadata{}, State{}, nil, false
		}
		messages, err := s.LoadMessages(sessionID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return SessionMetadata{}, State{}, nil, false
		}
		return meta, state, messages, true
	}
	return SessionMetadata{}, State{}, nil, false
}

func (s *Store) ensureBackgroundNotification(job QueueJob) {
	_ = s.EnsureBackgroundNotification(job.ParentSessionID, NewBackgroundNotification(job))
}

func (s *Store) ensureQueueLifecycleEvent(job QueueJob, eventType string) {
	eventsList, err := s.LoadEvents(job.ParentSessionID)
	if err != nil {
		return
	}
	for _, evt := range eventsList {
		if evt.Type != eventType {
			continue
		}
		jobID, _ := evt.Data["job_id"].(string)
		if jobID == job.ID {
			return
		}
	}
	data := map[string]any{
		"job_id":     job.ID,
		"session_id": job.SessionID,
		"status":     job.Status,
		"agent_role": job.AgentRole,
	}
	_ = s.AppendEvent(job.ParentSessionID, events.New(job.ParentSessionID, eventType, "queue", data))
}

func collectQueueVisiblePaths(effectiveWorkdir string, messages []Message) []string {
	base := strings.TrimSpace(effectiveWorkdir)
	if base == "" {
		return nil
	}
	base = filepath.Clean(base)
	seen := map[string]struct{}{}
	var out []string
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.IsError || (result.Name != "write_file" && result.Name != "edit_file") {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			if strings.TrimSpace(path) == "" {
				continue
			}
			rel, ok := relativePathWithinRoot(base, path)
			if !ok {
				continue
			}
			rel = filepath.ToSlash(rel)
			if _, exists := seen[rel]; exists {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func syncQueueVisiblePaths(requestedWorkdir, effectiveWorkdir string, visiblePaths []string) []string {
	requestedRoot := strings.TrimSpace(requestedWorkdir)
	effectiveRoot := strings.TrimSpace(effectiveWorkdir)
	if requestedRoot == "" || effectiveRoot == "" || len(visiblePaths) == 0 {
		return visiblePaths
	}
	requestedRoot = filepath.Clean(requestedRoot)
	effectiveRoot = filepath.Clean(effectiveRoot)
	if requestedRoot == effectiveRoot {
		return visiblePaths
	}
	var out []string
	for _, rel := range visiblePaths {
		src := filepath.Join(effectiveRoot, filepath.FromSlash(rel))
		dst := filepath.Join(requestedRoot, filepath.FromSlash(rel))
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			continue
		}
		out = append(out, rel)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func relativePathWithinRoot(root, target string) (string, bool) {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}

func readJSONL[T any](path string, out *[]T) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var item T
			if unmarshalErr := json.Unmarshal(line, &item); unmarshalErr != nil {
				return unmarshalErr
			}
			*out = append(*out, item)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func validateTodo(todo []TodoItem) error {
	inProgress := 0
	for _, item := range todo {
		switch item.Status {
		case "pending", "in_progress", "completed", "cancelled":
		default:
			return fmt.Errorf("invalid todo status %q", item.Status)
		}
		if item.Status == "in_progress" {
			inProgress++
		}
	}
	if inProgress > 1 {
		return errors.New("only one todo may be in_progress")
	}
	return nil
}

func newRecordID(prefix string) string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return prefix + "-fallback"
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func (s *Store) queueRoot() string {
	return filepath.Join(s.root, "_queue")
}

func (s *Store) queueStatusDir(status string) string {
	return filepath.Join(s.queueRoot(), status)
}

func (s *Store) queueJobPath(status, jobID string) string {
	return filepath.Join(s.queueStatusDir(status), jobID+".json")
}

func (s *Store) ensureQueueDirs() error {
	if err := s.EnsureRoot(); err != nil {
		return err
	}
	for _, status := range queueStatuses() {
		if err := s.ensureDir(s.queueStatusDir(status)); err != nil {
			return err
		}
	}
	return nil
}

func queueStatuses() []string {
	return []string{
		QueueStatusQueued,
		QueueStatusRunning,
		QueueStatusCompleted,
		QueueStatusFailed,
	}
}

func isQueueStatus(status string) bool {
	for _, allowed := range queueStatuses() {
		if status == allowed {
			return true
		}
	}
	return false
}

func normalizeDirMode(mode fs.FileMode) fs.FileMode {
	mode &= 0o777
	if mode == 0 {
		return 0o700
	}
	return mode
}

func deriveFileMode(dirMode fs.FileMode) fs.FileMode {
	mode := dirMode & 0o666
	if mode == 0 {
		return 0o600
	}
	return mode
}

func (s *Store) ensureDir(path string) error {
	path = filepath.Clean(path)
	targets := modeTargets(s.root, path)
	for _, target := range targets {
		if err := os.MkdirAll(target, s.dirMode); err != nil {
			return err
		}
		chmodBestEffort(target, s.dirMode)
	}
	return nil
}

func modeTargets(root, path string) []string {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		targets := []string{root}
		if rel == "." {
			return targets
		}
		current := root
		for _, part := range strings.Split(rel, string(os.PathSeparator)) {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, part)
			targets = append(targets, current)
		}
		return targets
	}
	return []string{path}
}

func chmodBestEffort(path string, mode fs.FileMode) {
	_ = os.Chmod(path, mode)
}
