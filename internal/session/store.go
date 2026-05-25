package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/fileutil"
	"golang.org/x/sys/unix"
)

type Store struct {
	root     string
	dirMode  fs.FileMode
	fileMode fs.FileMode
	mu       sync.Mutex
}

const QueueRunningStaleAfter = 15 * time.Minute

const queueRunningStaleAfter = QueueRunningStaleAfter
const invalidStoreIDPathSegment = ".invalid-id"

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
	if validateStoreID("session", sessionID) != nil {
		return filepath.Join(s.root, invalidStoreIDPathSegment)
	}
	return filepath.Join(s.root, sessionID)
}

func (s *Store) sessionPath(sessionID string, parts ...string) (string, error) {
	if err := validateStoreID("session", sessionID); err != nil {
		return "", err
	}
	components := append([]string{s.root, sessionID}, parts...)
	return filepath.Join(components...), nil
}

func (s *Store) withFileLock(lockPath string, fn func() error) error {
	if err := s.ensureDir(filepath.Dir(lockPath)); err != nil {
		return err
	}
	file, err := openNoSymlink(lockPath, unix.O_CREAT|unix.O_RDWR, s.fileMode)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}()
	return fn()
}

func (s *Store) Create(meta SessionMetadata, state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateStoreID("session", meta.ID); err != nil {
		return err
	}
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
	path, err := s.sessionPath(sessionID, "session.json")
	if err != nil {
		return meta, err
	}
	err = readJSONFile(path, &meta)
	return meta, err
}

func (s *Store) SaveMetadata(sessionID string, meta SessionMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "session.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, meta)
}

func (s *Store) LoadState(sessionID string) (State, error) {
	var state State
	path, err := s.sessionPath(sessionID, "state.json")
	if err != nil {
		return state, err
	}
	err = readJSONFile(path, &state)
	return state, err
}

func (s *Store) SaveState(sessionID string, state State) error {
	path, err := s.sessionPath(sessionID, "state.json")
	if err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	lockPath, err := s.sessionPath(sessionID, "state.lock")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if count, ok, err := s.pendingSteerCountLocked(sessionID); err != nil {
		return err
	} else if ok {
		state.PendingSteerCount = count
	}
	return s.withFileLock(lockPath, func() error {
		var current State
		if err := readJSONFile(path, &current); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		state.LoadedSkills = mergeLoadedSkills(current.LoadedSkills, state.LoadedSkills)
		return s.writeJSONFile(path, state)
	})
}

func (s *Store) pendingSteerCountLocked(sessionID string) (int, bool, error) {
	path, err := s.sessionPath(sessionID, "control", "steer.jsonl")
	if err != nil {
		return 0, false, err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "steer.lock")
	if err != nil {
		return 0, false, err
	}
	var requests []SteerRequest
	err = s.withFileLock(lockPath, func() error {
		err := readJSONL(path, &requests)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
	if err != nil {
		return 0, false, err
	}
	if requests == nil {
		return 0, false, nil
	}
	return CountOpenSteerRequests(requests), true, nil
}

func mergeLoadedSkills(current, next []string) []string {
	if len(current) == 0 && len(next) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(current)+len(next))
	merged := make([]string, 0, len(current)+len(next))
	for _, value := range current {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	for _, value := range next {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		merged = append(merged, name)
	}
	return merged
}

func (s *Store) ClaimSessionRun(sessionID string, allowedStatuses ...string) (State, error) {
	allowed := make(map[string]struct{}, len(allowedStatuses))
	for _, status := range allowedStatuses {
		allowed[status] = struct{}{}
	}
	path, err := s.sessionPath(sessionID, "state.json")
	if err != nil {
		return State{}, err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "run.lock")
	if err != nil {
		return State{}, err
	}
	var claimed State
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.withFileLock(lockPath, func() error {
		if err := readJSONFile(path, &claimed); err != nil {
			return err
		}
		if _, ok := allowed[claimed.Status]; !ok {
			return errors.New("session is not resumable")
		}
		claimed.Status = StatusRunning
		claimed.Phase = "prepare"
		claimed.PendingSteerCount = 0
		claimed.PauseReason = ""
		claimed.ProviderAutoResumeCount = 0
		claimed.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return s.writeJSONFile(path, claimed)
	})
	if err != nil {
		return State{}, err
	}
	return claimed, nil
}

func (s *Store) AppendMessage(sessionID string, message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "messages.jsonl")
	if err != nil {
		return err
	}
	return s.appendJSONL(path, message)
}

func (s *Store) LoadMessages(sessionID string) ([]Message, error) {
	path, err := s.sessionPath(sessionID, "messages.jsonl")
	if err != nil {
		return nil, err
	}
	var out []Message
	err = readJSONL(path, &out)
	return out, err
}

func (s *Store) LoadEvents(sessionID string) ([]events.Event, error) {
	path, err := s.sessionPath(sessionID, "events.jsonl")
	if err != nil {
		return nil, err
	}
	var out []events.Event
	err = readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []events.Event{}, nil
	}
	return out, err
}

func (s *Store) LoadContract(sessionID string) (SessionContract, error) {
	var contract SessionContract
	path, err := s.sessionPath(sessionID, "contract.json")
	if err != nil {
		return contract, err
	}
	err = readJSONFile(path, &contract)
	return contract, err
}

func (s *Store) SaveContract(sessionID string, contract SessionContract) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "contract.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, contract)
}

func (s *Store) AppendContractHistory(sessionID string, contract SessionContract) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "artifacts", "contract-history.jsonl")
	if err != nil {
		return err
	}
	return s.appendJSONL(path, contract)
}

func (s *Store) LoadArtifactTracker(sessionID string) ([]RequiredArtifact, error) {
	var artifacts []RequiredArtifact
	path, err := s.sessionPath(sessionID, "artifact-tracker.json")
	if err != nil {
		return nil, err
	}
	err = readJSONFile(path, &artifacts)
	if errors.Is(err, os.ErrNotExist) {
		return []RequiredArtifact{}, nil
	}
	return artifacts, err
}

func (s *Store) SaveArtifactTracker(sessionID string, artifacts []RequiredArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "artifact-tracker.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, artifacts)
}

func (s *Store) AppendProviderAttempt(sessionID string, attempt ProviderAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "provider-attempts.jsonl")
	if err != nil {
		return err
	}
	return s.appendJSONL(path, attempt)
}

func (s *Store) LoadProviderAttempts(sessionID string) ([]ProviderAttempt, error) {
	path, err := s.sessionPath(sessionID, "provider-attempts.jsonl")
	if err != nil {
		return nil, err
	}
	var out []ProviderAttempt
	err = readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []ProviderAttempt{}, nil
	}
	return out, err
}

func (s *Store) SaveProviderRawSidecar(sessionID string, sidecar ProviderRawSidecar) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "provider-raw", fmt.Sprintf("%d.json", sidecar.Turn))
	if err != nil {
		return err
	}
	if sidecar.SchemaVersion == 0 {
		sidecar.SchemaVersion = 1
	}
	if strings.TrimSpace(sidecar.Timestamp) == "" {
		sidecar.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return s.writeJSONFile(path, sidecar)
}

func (s *Store) LoadProviderRawSidecar(sessionID string, turn int) (ProviderRawSidecar, error) {
	var sidecar ProviderRawSidecar
	path, err := s.sessionPath(sessionID, "provider-raw", fmt.Sprintf("%d.json", turn))
	if err != nil {
		return sidecar, err
	}
	err = readJSONFile(path, &sidecar)
	return sidecar, err
}

func (s *Store) ProviderRawSidecarPath(sessionID string, turn int) string {
	return filepath.Join(s.SessionDir(sessionID), "provider-raw", fmt.Sprintf("%d.json", turn))
}

func (s *Store) LoadLongRunCheckpoint(sessionID string) (LongRunCheckpoint, error) {
	var checkpoint LongRunCheckpoint
	path, err := s.sessionPath(sessionID, "checkpoints", "longrun-latest.json")
	if err != nil {
		return checkpoint, err
	}
	err = readJSONFile(path, &checkpoint)
	return checkpoint, err
}

func (s *Store) SaveLongRunCheckpoint(sessionID string, checkpoint LongRunCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "checkpoints", "longrun-latest.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, checkpoint)
}

func (s *Store) LoadParentCoordination(sessionID string) (ParentCoordination, error) {
	var coordination ParentCoordination
	path, err := s.sessionPath(sessionID, "parent-coordination.json")
	if err != nil {
		return coordination, err
	}
	err = readJSONFile(path, &coordination)
	return coordination, err
}

func (s *Store) SaveParentCoordination(sessionID string, coordination ParentCoordination) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "parent-coordination.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, coordination)
}

func (s *Store) MutateParentCoordination(sessionID string, mutate func(*ParentCoordination) error) (ParentCoordination, bool, error) {
	path, err := s.sessionPath(sessionID, "parent-coordination.json")
	if err != nil {
		return ParentCoordination{}, false, err
	}
	lockPath, err := s.sessionPath(sessionID, "parent-coordination.lock")
	if err != nil {
		return ParentCoordination{}, false, err
	}
	var coordination ParentCoordination
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.withFileLock(lockPath, func() error {
		if err := readJSONFile(path, &coordination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if mutate != nil {
			if err := mutate(&coordination); err != nil {
				return err
			}
		}
		if strings.TrimSpace(coordination.ParentSessionID) == "" {
			return nil
		}
		if coordination.SchemaVersion == 0 {
			coordination.SchemaVersion = 1
		}
		coordination.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return s.writeJSONFile(path, coordination)
	})
	if err != nil {
		return ParentCoordination{}, false, err
	}
	if strings.TrimSpace(coordination.ParentSessionID) == "" {
		return coordination, false, nil
	}
	return coordination, true, nil
}

func (s *Store) WriteSessionMarkdown(sessionID string, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "session.md")
	if err != nil {
		return err
	}
	return s.writeBytesFile(path, []byte(content))
}

func (s *Store) AppendEvent(sessionID string, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "events.jsonl")
	if err != nil {
		return err
	}
	return s.appendJSONL(path, event)
}

func (s *Store) LoadTodo(sessionID string) ([]TodoItem, error) {
	var todo []TodoItem
	path, err := s.sessionPath(sessionID, "todo.json")
	if err != nil {
		return nil, err
	}
	err = readJSONFile(path, &todo)
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
	path, err := s.sessionPath(sessionID, "todo.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, todo)
}

func (s *Store) LoadFeatureList(sessionID string) (FeatureList, error) {
	var featureList FeatureList
	path, err := s.sessionPath(sessionID, "feature_list.json")
	if err != nil {
		return featureList, err
	}
	err = readJSONFile(path, &featureList)
	return featureList, err
}

func (s *Store) SaveFeatureList(sessionID string, featureList FeatureList) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "feature_list.json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, featureList)
}

func (s *Store) AppendSteerRequest(sessionID string, request SteerRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "control", "steer.jsonl")
	if err != nil {
		return err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "steer.lock")
	if err != nil {
		return err
	}
	return s.withFileLock(lockPath, func() error {
		return s.appendJSONL(path, request)
	})
}

func (s *Store) LoadSteerRequests(sessionID string) ([]SteerRequest, error) {
	path, err := s.sessionPath(sessionID, "control", "steer.jsonl")
	if err != nil {
		return nil, err
	}
	var out []SteerRequest
	err = readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []SteerRequest{}, nil
	}
	return out, err
}

func (s *Store) UpdateSteerRequests(sessionID string, requests []SteerRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "control", "steer.jsonl")
	if err != nil {
		return err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "steer.lock")
	if err != nil {
		return err
	}
	return s.withFileLock(lockPath, func() error {
		var current []SteerRequest
		err := readJSONL(path, &current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(current) > 0 {
			requests = mergeSteerRequests(requests, current)
		}
		return s.writeJSONL(path, requests)
	})
}

func (s *Store) RefreshPendingSteerCount(sessionID string) (State, error) {
	requests, err := s.LoadSteerRequests(sessionID)
	if err != nil {
		return State{}, err
	}
	state, err := s.LoadState(sessionID)
	if err != nil {
		return State{}, err
	}
	state.PendingSteerCount = CountOpenSteerRequests(requests)
	if err := s.SaveState(sessionID, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s *Store) AppendBackgroundNotification(sessionID string, notification BackgroundNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "control", "background.jsonl")
	if err != nil {
		return err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "background.lock")
	if err != nil {
		return err
	}
	return s.withFileLock(lockPath, func() error {
		return s.appendJSONL(path, notification)
	})
}

func (s *Store) EnsureBackgroundNotification(sessionID string, notification BackgroundNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "control", "background.jsonl")
	if err != nil {
		return err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "background.lock")
	if err != nil {
		return err
	}
	return s.withFileLock(lockPath, func() error {
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
	})
}

func (s *Store) LoadBackgroundNotifications(sessionID string) ([]BackgroundNotification, error) {
	path, err := s.sessionPath(sessionID, "control", "background.jsonl")
	if err != nil {
		return nil, err
	}
	var out []BackgroundNotification
	err = readJSONL(path, &out)
	if errors.Is(err, os.ErrNotExist) {
		return []BackgroundNotification{}, nil
	}
	return out, err
}

func (s *Store) UpdateBackgroundNotifications(sessionID string, notifications []BackgroundNotification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.sessionPath(sessionID, "control", "background.jsonl")
	if err != nil {
		return err
	}
	lockPath, err := s.sessionPath(sessionID, "control", "background.lock")
	if err != nil {
		return err
	}
	return s.withFileLock(lockPath, func() error {
		var current []BackgroundNotification
		err := readJSONL(path, &current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(current) > 0 {
			notifications = mergeBackgroundNotifications(notifications, current)
		}
		return s.writeJSONL(path, notifications)
	})
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
		s.reconcileSessionQueueJob(meta)
		state, err := s.LoadState(entry.Name())
		if err != nil {
			continue
		}
		summary := SessionSummary{
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
		}
		s.populateGoalSummary(&summary)
		s.populatePlanModeSummary(&summary)
		result = append(result, summary)
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
		s.reconcileSessionQueueJob(meta)
		state, err := s.LoadState(entry.Name())
		if err != nil {
			continue
		}
		summary := SessionSummary{
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
		}
		s.populateGoalSummary(&summary)
		s.populatePlanModeSummary(&summary)
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt == result[j].CreatedAt {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt < result[j].CreatedAt
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) populateGoalSummary(summary *SessionSummary) {
	if summary == nil || strings.TrimSpace(summary.ID) == "" {
		return
	}
	goal, err := s.LoadGoal(summary.ID)
	if err != nil || goal.GoalID == "" {
		return
	}
	summary.GoalStatus = goal.Status
	summary.GoalMode = goal.Mode
	summary.GoalObjective = goal.Objective
}

func (s *Store) populatePlanModeSummary(summary *SessionSummary) {
	if summary == nil || strings.TrimSpace(summary.ID) == "" {
		return
	}
	planMode, err := s.LoadPlanMode(summary.ID)
	if err != nil || planMode.PlanModeID == "" {
		return
	}
	summary.PlanModeStatus = planMode.Status
	summary.PlanModeVersion = planMode.PlanVersion
	summary.PlanModeSummary = planMode.Summary
}

func (s *Store) NextTaskID(sessionID string) (string, error) {
	tasks, err := s.ListTasks(sessionID)
	if err != nil {
		return "", err
	}
	return nextTaskID(tasks), nil
}

func (s *Store) SaveTask(sessionID string, task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateStoreID("task", task.ID); err != nil {
		return err
	}
	path, err := s.sessionPath(sessionID, "tasks", task.ID+".json")
	if err != nil {
		return err
	}
	return s.writeJSONFile(path, task)
}

func (s *Store) GetTask(sessionID, taskID string) (Task, error) {
	var task Task
	if err := validateStoreID("task", taskID); err != nil {
		return task, err
	}
	path, err := s.sessionPath(sessionID, "tasks", taskID+".json")
	if err != nil {
		return task, err
	}
	err = readJSONFile(path, &task)
	return task, err
}

func (s *Store) ListTasks(sessionID string) ([]Task, error) {
	dir, err := s.sessionPath(sessionID, "tasks")
	if err != nil {
		return nil, err
	}
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
	return s.saveTasksLocked(sessionID, tasks)
}

func (s *Store) MutateTasks(sessionID string, mutate func([]Task) ([]Task, error)) error {
	if mutate == nil {
		return nil
	}
	lockPath, err := s.sessionPath(sessionID, "tasks", "taskboard.lock")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withFileLock(lockPath, func() error {
		tasks, err := s.listTasksLocked(sessionID)
		if err != nil {
			return err
		}
		tasks, err = mutate(tasks)
		if err != nil {
			return err
		}
		tasks = normalizeTaskGraph(tasks)
		if err := ensureTaskReferences(tasks); err != nil {
			return err
		}
		if err := ensureAcyclic(tasks); err != nil {
			return err
		}
		return s.saveTasksLocked(sessionID, tasks)
	})
}

func (s *Store) listTasksLocked(sessionID string) ([]Task, error) {
	dir, err := s.sessionPath(sessionID, "tasks")
	if err != nil {
		return nil, err
	}
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

func (s *Store) saveTasksLocked(sessionID string, tasks []Task) error {
	dir, err := s.sessionPath(sessionID, "tasks")
	if err != nil {
		return err
	}
	if err := s.ensureDir(dir); err != nil {
		return err
	}
	for _, task := range tasks {
		if err := validateStoreID("task", task.ID); err != nil {
			return err
		}
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
	if err := validateStoreID("queue job", job.ID); err != nil {
		return err
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
	if err := validateStoreID("queue job", jobID); err != nil {
		return job, err
	}
	for _, status := range queueStatuses() {
		path := s.queueJobPath(status, jobID)
		err := readJSONFile(path, &job)
		if err == nil {
			if repaired, changed := s.reconcileQueueJobSession(job); changed {
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
			if repaired, changed := s.reconcileQueueJobSession(job); changed {
				job = repaired
			}
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if parentSessionID != "" {
			if out[i].CreatedAt == out[j].CreatedAt {
				return out[i].ID < out[j].ID
			}
			return out[i].CreatedAt < out[j].CreatedAt
		}
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
	jobs, err := s.listJobs(0, "")
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id := range targets {
		path, err := s.sessionPath(id)
		if err != nil {
			return err
		}
		if err := fileutil.RemoveDirAllNoSymlink(path); err != nil {
			return err
		}
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
		path := filepath.Join(s.root, entry.Name())
		if entry.Type().IsRegular() {
			if err := fileutil.RemoveFileNoSymlink(path); err != nil {
				return err
			}
			continue
		}
		if err := fileutil.RemoveDirAllNoSymlink(path); err != nil {
			return err
		}
	}
	return s.EnsureRoot()
}

func (s *Store) deleteJobLocked(jobID string) error {
	if err := validateStoreID("queue job", jobID); err != nil {
		return err
	}
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
		if err := validateStoreID("queue job", job.ID); err != nil || entry.Name() != job.ID+".json" {
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
	if err := validateStoreID("queue job", jobID); err != nil {
		return QueueJob{}, err
	}
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
	artifactPath, err := validateStoreRelativePath("artifact", relativePath)
	if err != nil {
		return "", err
	}
	path, err := s.sessionPath(sessionID, "artifacts", artifactPath)
	if err != nil {
		return "", err
	}
	if err := s.writeJSONFile(path, payload); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) ReadArtifact(sessionID, relativePath string, target any) error {
	artifactPath, err := validateStoreRelativePath("artifact", relativePath)
	if err != nil {
		return err
	}
	path, err := s.sessionPath(sessionID, "artifacts", artifactPath)
	if err != nil {
		return err
	}
	return readJSONFile(path, target)
}

func (s *Store) WriteTranscript(sessionID, name string, messages []Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transcriptPath, err := validateStoreRelativePath("transcript", name)
	if err != nil {
		return "", err
	}
	path, err := s.sessionPath(sessionID, "artifacts", "transcripts", transcriptPath)
	if err != nil {
		return "", err
	}
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

func NewAssistantMessage(text, thinking string, toolCalls []ToolCall) Message {
	return Message{
		ID:        newRecordID("msg"),
		Role:      "assistant",
		Text:      text,
		Thinking:  thinking,
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
	return fileutil.AtomicWriteFileNoSymlink(path, data, s.fileMode)
}

func readJSONFile(path string, target any) error {
	if err := rejectSymlinkPathAncestors(path); err != nil {
		return err
	}
	data, _, err := fileutil.ReadRegularFileNoSymlink(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (s *Store) appendJSONL(path string, payload any) error {
	if err := s.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := openAppendNoSymlink(path, s.fileMode)
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
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	switch items := payload.(type) {
	case []Message:
		for _, item := range items {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
	case []SteerRequest:
		for _, item := range items {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
	case []BackgroundNotification:
		for _, item := range items {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported jsonl payload %T", payload)
	}
	return fileutil.AtomicWriteFileNoSymlink(path, data.Bytes(), s.fileMode)
}

func openAppendNoSymlink(path string, mode fs.FileMode) (*os.File, error) {
	return openNoSymlink(path, unix.O_APPEND|unix.O_CREAT|unix.O_WRONLY, mode)
}

func openNoSymlink(path string, flags int, mode fs.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW, uint32(mode))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("failed to open session file")
	}
	return file, nil
}

func (s *Store) reconcileQueueJobSession(job QueueJob) (QueueJob, bool) {
	originalStatus := job.Status
	meta, state, messages, ok := s.findSessionForQueueJob(job.ID)
	if !ok {
		if job.Status != QueueStatusRunning {
			if isTerminalQueueStatus(job.Status) {
				s.ensureTerminalQueueJobParentState(job)
			}
			return job, false
		}
		if !queueJobIsStale(job, time.Now().UTC()) {
			return job, false
		}
		job.Status = QueueStatusFailed
		job.SessionStatus = StatusFailed
		job.LastError = "queue job stale: running job has no linked session and heartbeat is stale"
		_ = s.SaveJob(job)
		if isTerminalQueueStatus(job.Status) {
			s.ensureTerminalQueueJobParentState(job)
		} else if job.Status != originalStatus {
			s.reconcileParentQueueJobStatus(job)
		}
		return job, true
	}
	if job.Status == QueueStatusRunning && state.Status == StatusRunning && queueJobIsStale(job, time.Now().UTC()) {
		job.Status = QueueStatusFailed
		if strings.TrimSpace(job.LastError) == "" {
			job.LastError = "queue job stale: linked running session heartbeat is stale"
		}
		var stateChanged bool
		state, stateChanged = reconcileStateFromTerminalQueueJob(state, job)
		if stateChanged {
			_ = s.SaveState(meta.ID, state)
		}
	}
	state, stateChanged := reconcileStateFromTerminalQueueJob(state, job)
	if stateChanged {
		_ = s.SaveState(meta.ID, state)
	}
	changed := syncRunningQueueJobSession(&job, meta, state, messages)
	switch state.Status {
	case StatusCompleted, StatusFailed:
		if state.Status == StatusFailed {
			if job.Status != QueueStatusFailed {
				job.Status = QueueStatusFailed
				changed = true
			}
		} else if job.Status != QueueStatusCompleted {
			job.Status = QueueStatusCompleted
			changed = true
		}
	case StatusRunning:
		if job.Status != QueueStatusRunning {
			job.Status = QueueStatusRunning
			changed = true
		}
	case StatusPaused, StatusAwaitingInput:
		if job.Status != QueueStatusBlocked {
			job.Status = QueueStatusBlocked
			if strings.TrimSpace(job.LastError) == "" {
				job.LastError = "child session is resumable: " + state.Status
			}
			changed = true
		}
	default:
		if job.Status != QueueStatusBlocked {
			job.Status = QueueStatusBlocked
			if strings.TrimSpace(job.LastError) == "" {
				job.LastError = "child session is resumable: " + state.Status
			}
			changed = true
		}
	}
	if !changed {
		if isTerminalQueueStatus(job.Status) {
			s.ensureTerminalQueueJobParentState(job)
		}
		return job, false
	}
	_ = s.SaveJob(job)
	if isTerminalQueueStatus(job.Status) {
		s.ensureTerminalQueueJobParentState(job)
	} else if job.Status != originalStatus {
		s.reconcileParentQueueJobStatus(job)
	}
	return job, true
}

func (s *Store) reconcileSessionQueueJob(meta SessionMetadata) {
	if strings.TrimSpace(meta.QueueJobID) == "" {
		return
	}
	_, _ = s.LoadJob(meta.QueueJobID)
}

func isTerminalQueueStatus(status string) bool {
	return status == QueueStatusCompleted || status == QueueStatusFailed
}

func reconcileStateFromTerminalQueueJob(state State, job QueueJob) (State, bool) {
	switch job.Status {
	case QueueStatusFailed:
		if state.Status == StatusCompleted || state.Status == StatusFailed {
			return state, false
		}
		state.Status = StatusFailed
		if strings.TrimSpace(state.LastError) == "" {
			state.LastError = job.LastError
		}
		if strings.TrimSpace(state.Phase) == "" {
			state.Phase = "failed"
		}
		return state, true
	case QueueStatusCompleted:
		if state.Status == StatusCompleted || state.Status == StatusFailed {
			return state, false
		}
		state.Status = StatusCompleted
		if strings.TrimSpace(state.LastAssistantExcerpt) == "" {
			state.LastAssistantExcerpt = job.FinalText
		}
		return state, true
	default:
		return state, false
	}
}

func (s *Store) reconcileParentQueueJobStatus(job QueueJob) {
	if strings.TrimSpace(job.ParentSessionID) == "" || strings.TrimSpace(job.ID) == "" {
		return
	}
	_, _, _ = s.MutateParentCoordination(job.ParentSessionID, func(coordination *ParentCoordination) error {
		if coordination.ParentSessionID == "" {
			return nil
		}
		coordination.UnresolvedQueueJobs = removeStringValue(coordination.UnresolvedQueueJobs, job.ID)
		coordination.CompletedQueueJobs = removeStringValue(coordination.CompletedQueueJobs, job.ID)
		coordination.FailedQueueJobs = removeStringValue(coordination.FailedQueueJobs, job.ID)
		switch job.Status {
		case QueueStatusCompleted:
			coordination.CompletedQueueJobs = appendUniqueString(coordination.CompletedQueueJobs, job.ID)
		case QueueStatusFailed:
			coordination.FailedQueueJobs = appendUniqueString(coordination.FailedQueueJobs, job.ID)
		default:
			coordination.UnresolvedQueueJobs = appendUniqueString(coordination.UnresolvedQueueJobs, job.ID)
		}
		return nil
	})
}

func appendUniqueString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func removeStringValue(items []string, value string) []string {
	if len(items) == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
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

func (s *Store) ensureTerminalQueueJobParentState(job QueueJob) {
	if strings.TrimSpace(job.ParentSessionID) == "" || !isTerminalQueueStatus(job.Status) {
		return
	}
	s.reconcileParentQueueJobStatus(job)
	s.ensureBackgroundNotification(job)
	s.ensureQueueLifecycleEvent(job, "queue.job.notified")
	if job.Status == QueueStatusFailed {
		s.ensureQueueLifecycleEvent(job, "queue.job.failed")
		return
	}
	s.ensureQueueLifecycleEvent(job, "queue.job.completed")
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
	return out
}

func syncQueueVisiblePaths(requestedWorkdir, effectiveWorkdir string, visiblePaths []string) []string {
	if strings.TrimSpace(requestedWorkdir) == "" || strings.TrimSpace(effectiveWorkdir) == "" || len(visiblePaths) == 0 {
		return visiblePaths
	}
	requestedRoot, ok := resolveQueueExistingDir(requestedWorkdir)
	if !ok {
		return nil
	}
	effectiveRoot, ok := resolveQueueExistingDir(effectiveWorkdir)
	if !ok {
		return nil
	}
	if requestedRoot == effectiveRoot {
		return visiblePaths
	}
	var out []string
	for _, rel := range visiblePaths {
		src, ok := resolveQueueVisiblePath(effectiveRoot, rel)
		if !ok {
			continue
		}
		dst, ok := resolveQueueVisiblePath(requestedRoot, rel)
		if !ok {
			continue
		}
		data, _, err := fileutil.ReadRegularFileNoSymlink(src)
		if err != nil {
			continue
		}
		if err := fileutil.AtomicWriteFileNoSymlink(dst, data, 0o600); err != nil {
			continue
		}
		out = append(out, rel)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveQueueExistingDir(path string) (string, bool) {
	abs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return resolved, true
}

func resolveQueueVisiblePath(root, rel string) (string, bool) {
	relPath := filepath.Clean(filepath.FromSlash(rel))
	if relPath == "." || relPath == ".." || filepath.IsAbs(relPath) || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return "", false
	}
	target := filepath.Join(root, relPath)
	resolved, err := resolveQueuePathWithExistingParent(target)
	if err != nil || !pathWithinRoot(root, resolved) {
		return "", false
	}
	return resolved, true
}

func resolveQueuePathWithExistingParent(path string) (string, error) {
	var suffix []string
	current := filepath.Clean(path)
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("unable to resolve path")
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func relativePathWithinRoot(root, target string) (string, bool) {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == "" || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return rel, true
}

func readJSONL[T any](path string, out *[]T) error {
	if err := rejectSymlinkPathAncestors(path); err != nil {
		return err
	}
	file, err := openNoSymlink(path, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), int(fileutil.MaxRegularFileReadBytes))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		*out = append(*out, item)
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return fmt.Errorf("session JSONL record exceeds maximum readable size: %s (> %d bytes)", path, fileutil.MaxRegularFileReadBytes)
		}
		return err
	}
	return nil
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

func validateStoreID(kind, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s id is required", kind)
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("invalid %s id %q: leading or trailing whitespace is not allowed", kind, id)
	}
	lower := strings.ToLower(id)
	if id == "." || id == ".." || filepath.IsAbs(id) || strings.ContainsAny(id, `/\`) || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return fmt.Errorf("invalid %s id %q: path separators and traversal are not allowed", kind, id)
	}
	return nil
}

func validateStoreRelativePath(kind, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s path is required", kind)
	}
	if strings.TrimSpace(value) != value {
		return "", fmt.Errorf("invalid %s path %q: leading or trailing whitespace is not allowed", kind, value)
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if path.IsAbs(normalized) || filepath.IsAbs(value) {
		return "", fmt.Errorf("invalid %s path %q: absolute paths are not allowed", kind, value)
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid %s path %q: empty, current, and parent path segments are not allowed", kind, value)
		}
	}
	return filepath.FromSlash(path.Clean(normalized)), nil
}

func mergeSteerRequests(updated, current []SteerRequest) []SteerRequest {
	byID := make(map[string]SteerRequest, len(updated))
	for _, request := range updated {
		byID[request.ID] = request
	}
	seen := make(map[string]struct{}, len(current))
	merged := make([]SteerRequest, 0, len(current)+len(updated))
	for _, request := range current {
		if replacement, ok := byID[request.ID]; ok {
			merged = append(merged, replacement)
		} else {
			merged = append(merged, request)
		}
		seen[request.ID] = struct{}{}
	}
	for _, request := range updated {
		if _, ok := seen[request.ID]; ok {
			continue
		}
		merged = append(merged, request)
	}
	return merged
}

func CountOpenSteerRequests(requests []SteerRequest) int {
	pending := 0
	for _, request := range requests {
		if request.Status == SteerStatusPending || request.Status == SteerStatusDeferred {
			pending++
		}
	}
	return pending
}

func mergeBackgroundNotifications(updated, current []BackgroundNotification) []BackgroundNotification {
	byKey := make(map[string]BackgroundNotification, len(updated))
	for _, notification := range updated {
		key := backgroundNotificationMergeKey(notification)
		if key == "" {
			continue
		}
		byKey[key] = notification
	}
	seen := make(map[string]struct{}, len(current))
	merged := make([]BackgroundNotification, 0, len(current)+len(updated))
	for _, notification := range current {
		key := backgroundNotificationMergeKey(notification)
		if replacement, ok := byKey[key]; key != "" && ok {
			merged = append(merged, replacement)
			seen[key] = struct{}{}
			continue
		}
		merged = append(merged, notification)
		if key != "" {
			seen[key] = struct{}{}
		}
	}
	for _, notification := range updated {
		key := backgroundNotificationMergeKey(notification)
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		merged = append(merged, notification)
	}
	return merged
}

func backgroundNotificationMergeKey(notification BackgroundNotification) string {
	if strings.TrimSpace(notification.QueueJobID) != "" {
		return "queue:" + notification.QueueJobID
	}
	if strings.TrimSpace(notification.ID) != "" {
		return "id:" + notification.ID
	}
	return ""
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
		QueueStatusBlocked,
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
		if err := rejectSymlinkPathAncestors(target); err != nil {
			return err
		}
		if err := os.MkdirAll(target, s.dirMode); err != nil {
			return err
		}
		if err := rejectSymlinkPathAncestors(target); err != nil {
			return err
		}
		chmodBestEffort(target, s.dirMode)
	}
	return nil
}

func rejectSymlinkPathAncestors(path string) error {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, volume)
	separator := string(os.PathSeparator)
	current := volume
	if strings.HasPrefix(rest, separator) {
		current += separator
		rest = strings.TrimPrefix(rest, separator)
	}
	if current == "" {
		current = "."
	}
	for _, part := range strings.Split(rest, separator) {
		if part == "" {
			continue
		}
		if current == separator || strings.HasSuffix(current, separator) {
			current += part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to use symlinked session path: %s", current)
		}
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
