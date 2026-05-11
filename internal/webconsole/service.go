package webconsole

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/extensions"
	"go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var webconsoleProcessOwner = newProcessOwner()

const (
	maskedAPIKey                    = "••••••••••••••••"
	settingsThinkingStandardBudget  = 1024
	settingsThinkingMaxBudget       = 32000
	settingsThinkingMaxOutputTokens = 32768
	settingsProbeTimeout            = 90 * time.Second
)

type processOwner struct {
	pid            int
	processStartID string
	startedAt      string
}

func newProcessOwner() processOwner {
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	return processOwner{
		pid:            os.Getpid(),
		processStartID: strconv.Itoa(os.Getpid()) + ":" + startedAt,
		startedAt:      startedAt,
	}
}

type Options struct {
	WorkerCount int
	ConfigPath  string
}

type Service struct {
	cfg        *config.Config
	configPath string
	store      *session.Store
	staticFS   fs.FS
	workers    *workerPool

	mu      sync.RWMutex
	handles map[string]*launchHandle
}

type launchHandle struct {
	sessionID      string
	runner         *runtime.Runner
	cancel         context.CancelFunc
	startedAt      string
	processStartID string
	pid            int
}

type workerPool struct {
	cfg *config.Config

	mu      sync.RWMutex
	nextID  int
	desired int
	workers map[int]*workerHandle
}

type workerHandle struct {
	id     int
	runner *runtime.Runner
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.RWMutex
	status WorkerStatus
}

type ProviderMeta struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url"`
}

type MetaResponse struct {
	SessionRoot              string         `json:"session_root"`
	WorkspaceRoot            string         `json:"workspace_root"`
	WorkspaceSwitchSupported bool           `json:"workspace_switch_supported"`
	DefaultMode              string         `json:"default_mode"`
	QueuePollMS              int            `json:"queue_poll_ms"`
	WorkerCount              int            `json:"worker_count"`
	Capabilities             []string       `json:"capabilities"`
	DefaultVendor            string         `json:"default_provider"`
	Providers                []ProviderMeta `json:"providers"`
}

type OverviewResponse struct {
	SessionCounters map[string]int           `json:"session_counters"`
	QueueCounters   map[string]int           `json:"queue_counters"`
	RecentSessions  []session.SessionSummary `json:"recent_sessions"`
	RecentJobs      []session.QueueJob       `json:"recent_jobs"`
	Workers         WorkerPoolSnapshot       `json:"workers"`
	RecentFailures  []FailureSummary         `json:"recent_failures"`
	Feed            []TimelineEntry          `json:"feed"`
}

type HistoryResponse struct {
	Items      []session.SessionSummary `json:"items"`
	Total      int                      `json:"total"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"page_size"`
	TotalPages int                      `json:"total_pages"`
}

type FailureSummary struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updated_at"`
}

type SessionDetailResponse struct {
	Metadata                session.SessionMetadata          `json:"metadata"`
	State                   session.State                    `json:"state"`
	Contract                *session.SessionContract         `json:"contract,omitempty"`
	RequiredArtifacts       []session.RequiredArtifact       `json:"required_artifacts,omitempty"`
	ProviderAttempts        []session.ProviderAttempt        `json:"provider_attempts,omitempty"`
	LongRunCheckpoint       *session.LongRunCheckpoint       `json:"longrun_checkpoint,omitempty"`
	ParentCoordination      *session.ParentCoordination      `json:"parent_coordination,omitempty"`
	TaskBoard               session.TaskBoard                `json:"task_board"`
	Children                ChildrenResponse                 `json:"children"`
	BackgroundNotifications []session.BackgroundNotification `json:"background_notifications"`
	SteerRequests           []session.SteerRequest           `json:"steer_requests"`
	Messages                []session.Message                `json:"messages"`
	HasMoreMessages         bool                             `json:"has_more_messages"`
	Events                  []events.Event                   `json:"events"`
	Timeline                []TimelineEntry                  `json:"timeline"`
	ActiveHandle            bool                             `json:"active_handle"`
	ActiveHandleOwner       ActiveHandleOwner                `json:"active_handle_owner"`
}

type ActiveHandleOwner struct {
	State                 string `json:"state"`
	OwnedByCurrentProcess bool   `json:"owned_by_current_process"`
	ProcessStartID        string `json:"process_start_id,omitempty"`
	PID                   int    `json:"pid,omitempty"`
	StartedAt             string `json:"started_at,omitempty"`
	ReleasedAt            string `json:"released_at,omitempty"`
	LastEventAt           string `json:"last_event_at,omitempty"`
	Action                string `json:"action,omitempty"`
}

type ChildrenResponse struct {
	Sessions []session.SessionSummary `json:"sessions"`
	Jobs     []session.QueueJob       `json:"jobs"`
}

type TimelineEntry struct {
	Time      string         `json:"time"`
	Kind      string         `json:"kind"`
	MessageID string         `json:"message_id,omitempty"`
	EventID   string         `json:"event_id,omitempty"`
	Role      string         `json:"role,omitempty"`
	Text      string         `json:"text,omitempty"`
	EventType string         `json:"event_type,omitempty"`
	Phase     string         `json:"phase,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

type LaunchResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type WorkerPoolSnapshot struct {
	DesiredCount int            `json:"desired_count"`
	ActiveCount  int            `json:"active_count"`
	PollInterval int            `json:"poll_interval_ms"`
	Workers      []WorkerStatus `json:"workers"`
}

type WorkerStatus struct {
	ID            int    `json:"id"`
	State         string `json:"state"`
	LastJobID     string `json:"last_job_id,omitempty"`
	LastJobStatus string `json:"last_job_status,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

func New(cfg *config.Config, opts Options) (*Service, error) {
	staticFS, err := assetFS()
	if err != nil {
		return nil, err
	}
	serviceCfg, err := config.Clone(cfg)
	if err != nil {
		return nil, err
	}
	store := runtime.NewStoreView(serviceCfg).Store()
	svc := &Service{
		cfg:        serviceCfg,
		configPath: opts.ConfigPath,
		store:      store,
		staticFS:   staticFS,
		handles:    map[string]*launchHandle{},
	}
	svc.workers = newWorkerPool(serviceCfg, opts.WorkerCount)
	return svc, nil
}

func (s *Service) Close() {
	s.workers.Close()
	s.mu.Lock()
	handles := make([]*launchHandle, 0, len(s.handles))
	for _, handle := range s.handles {
		handles = append(handles, handle)
	}
	s.handles = map[string]*launchHandle{}
	s.mu.Unlock()
	for _, handle := range handles {
		handle.cancel()
		_ = s.recordLaunchHandleEvent(handle, "webconsole.handle.released", map[string]any{"reason": "service_close"})
	}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" || r.URL.Path == "/api/ws" {
		s.serveAPI(w, r)
		return
	}
	s.serveUI(w, r)
}

func (s *Service) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/meta":
		meta, err := s.meta()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, meta)
	case r.Method == http.MethodGet && r.URL.Path == "/api/overview":
		resp, err := s.overview()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case r.Method == http.MethodGet && r.URL.Path == "/api/history":
		s.handleHistory(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/sessions":
		s.handleListSessions(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/clear":
		s.handleClearSessions(w)
	case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/start":
		s.handleStartSession(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/sessions/"):
		s.handleSessionRoute(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/queue/jobs":
		s.handleListJobs(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/queue/jobs/"):
		s.handleShowJob(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/queue/jobs":
		s.handleCreateJob(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/workers":
		writeJSON(w, http.StatusOK, s.workers.Snapshot())
	case r.Method == http.MethodPost && r.URL.Path == "/api/workers":
		s.handleScaleWorkers(w, r)
	case r.URL.Path == "/api/ws" || r.URL.Path == "/ws":
		s.handleWebSocket(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/config":
		s.handleGetConfig(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/config":
		s.handleUpdateConfig(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/config/test":
		s.handleTestConfig(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/files":
		s.handleListFiles(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/file/read":
		s.handleReadFile(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/skills":
		s.handleListSkills(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/skills/upload":
		s.handleUploadSkill(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/skills/") && strings.HasSuffix(r.URL.Path, "/install"):
		s.handleInstallSkill(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/skills/") && strings.HasSuffix(r.URL.Path, "/uninstall"):
		s.handleUninstallSkill(w, r)
	default:
		writeError(w, http.StatusNotFound, errors.New("route not found"))
	}
}

func (s *Service) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		serveEmbeddedFile(w, s.staticFS, "index.html")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if _, err := fs.Stat(s.staticFS, name); err == nil {
		serveEmbeddedFile(w, s.staticFS, name)
		return
	}
	serveEmbeddedFile(w, s.staticFS, "index.html")
}

func (s *Service) configSnapshot() (*config.Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return config.Clone(s.cfg)
}

func (s *Service) meta() (MetaResponse, error) {
	cfg, err := s.configSnapshot()
	if err != nil {
		return MetaResponse{}, err
	}
	providers := make([]ProviderMeta, 0, len(cfg.Providers))
	for name, provider := range cfg.Providers {
		providers = append(providers, ProviderMeta{
			Name:    name,
			Model:   provider.Model,
			BaseURL: provider.BaseURL,
		})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	workspaceRoot, _ := currentServerWorkspaceRoot()
	return MetaResponse{
		SessionRoot:              s.store.Root(),
		WorkspaceRoot:            workspaceRoot,
		WorkspaceSwitchSupported: true,
		DefaultMode:              cfg.Runtime.Isolation.DefaultMode,
		QueuePollMS:              cfg.Runtime.Queue.PollIntervalMS,
		WorkerCount:              s.workers.Snapshot().DesiredCount,
		Capabilities:             []string{"start", "steer", "continue", "interrupt", "stop", "queue", "children", "tasks"},
		DefaultVendor:            cfg.DefaultProvider,
		Providers:                providers,
	}, nil
}

func (s *Service) overview() (OverviewResponse, error) {
	sessions, err := s.store.List(50)
	if err != nil {
		return OverviewResponse{}, err
	}
	jobs, err := s.store.ListJobs(50)
	if err != nil {
		return OverviewResponse{}, err
	}
	resp := OverviewResponse{
		SessionCounters: map[string]int{},
		QueueCounters:   map[string]int{},
		RecentSessions:  []session.SessionSummary{},
		RecentJobs:      []session.QueueJob{},
		Workers:         s.workers.Snapshot(),
		RecentFailures:  []FailureSummary{},
		Feed:            []TimelineEntry{},
	}
	if sessions != nil {
		resp.RecentSessions = sessions
	}
	if jobs != nil {
		resp.RecentJobs = jobs
	}
	for _, item := range sessions {
		resp.SessionCounters[item.Status]++
	}
	for _, job := range jobs {
		resp.QueueCounters[job.Status]++
	}
	for _, item := range sessions {
		if item.Status == session.StatusFailed {
			resp.RecentFailures = append(resp.RecentFailures, FailureSummary{
				Kind:      "session",
				ID:        item.ID,
				Status:    item.Status,
				Message:   firstNonEmpty(item.LastError, item.Phase),
				UpdatedAt: item.UpdatedAt,
			})
		}
	}
	for _, job := range jobs {
		if job.Status == session.QueueStatusFailed {
			resp.RecentFailures = append(resp.RecentFailures, FailureSummary{
				Kind:      "queue_job",
				ID:        job.ID,
				Status:    job.Status,
				Message:   firstNonEmpty(job.LastError, job.FinalText),
				UpdatedAt: job.UpdatedAt,
			})
		}
	}
	sort.Slice(resp.RecentFailures, func(i, j int) bool { return resp.RecentFailures[i].UpdatedAt > resp.RecentFailures[j].UpdatedAt })
	if len(resp.RecentFailures) > 8 {
		resp.RecentFailures = resp.RecentFailures[:8]
	}
	for _, item := range sessions {
		resp.Feed = append(resp.Feed, TimelineEntry{
			Time:      item.UpdatedAt,
			Kind:      "session_summary",
			Text:      item.ID,
			EventType: item.Status,
			Data: map[string]any{
				"provider": item.Provider,
				"model":    item.Model,
				"phase":    item.Phase,
			},
		})
	}
	for _, job := range jobs {
		resp.Feed = append(resp.Feed, TimelineEntry{
			Time:      job.UpdatedAt,
			Kind:      "queue_job",
			Text:      job.ID,
			EventType: job.Status,
			Data: map[string]any{
				"agent_name": job.AgentName,
				"agent_role": job.AgentRole,
				"session_id": job.SessionID,
			},
		})
	}
	sort.Slice(resp.Feed, func(i, j int) bool { return resp.Feed[i].Time > resp.Feed[j].Time })
	if len(resp.Feed) > 16 {
		resp.Feed = resp.Feed[:16]
	}
	return resp, nil
}

func (s *Service) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	items, err := s.store.List(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []session.SessionSummary{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Service) handleHistory(w http.ResponseWriter, r *http.Request) {
	pageSize := queryInt(r, "page_size", 10)
	if pageSize <= 0 {
		pageSize = 10
	}
	page := queryInt(r, "page", 1)
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize
	items, total, err := s.store.ListPage(pageSize, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	writeJSON(w, http.StatusOK, HistoryResponse{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	})
}

func (s *Service) handleSessionRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, errors.New("session route not found"))
		return
	}
	sessionID := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			resp, err := s.sessionDetail(sessionID, queryBoundedInt(r, "limit", 40, 1, 200))
			if err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, fs.ErrNotExist) {
					status = http.StatusNotFound
				}
				writeError(w, status, err)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		case http.MethodDelete:
			s.handleDeleteSession(w, sessionID)
		default:
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		}
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, errors.New("session route not found"))
		return
	}
	switch parts[1] {
	case "continue":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handleContinueSession(w, r, sessionID)
	case "steer":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handleSteerSession(w, r, sessionID)
	case "interrupt":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handleInterruptSession(w, sessionID)
	case "stop":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handleStopSession(w, sessionID)
	case "children":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handleChildren(w, sessionID, queryInt(r, "limit", 50))
	case "messages":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handleSessionMessages(w, sessionID, r)
	case "tasks":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handleTaskBoard(w, sessionID)
	default:
		writeError(w, http.StatusNotFound, errors.New("session route not found"))
	}
}

func (s *Service) handleDeleteSession(w http.ResponseWriter, sessionID string) {
	if s.hasActiveDescendantHandle(sessionID) {
		writeError(w, http.StatusConflict, errors.New("cannot delete an active session tree"))
		return
	}
	if err := s.ensureSessionTreeNotLive(sessionID); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err := s.store.DeleteSessionTree(sessionID); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	if err := s.appendAuditEvent("web.session.delete", map[string]any{
		"session_id": sessionID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "deleted": true})
}

func (s *Service) handleClearSessions(w http.ResponseWriter) {
	if s.hasAnyActiveHandle() {
		writeError(w, http.StatusConflict, errors.New("cannot clear history while sessions are active in this web console"))
		return
	}
	hasRunningJobs, err := s.hasRunningQueueJobs("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if hasRunningJobs {
		writeError(w, http.StatusConflict, errors.New("cannot clear history while queue jobs are still running"))
		return
	}
	if err := s.store.ClearHistory(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.appendAuditEvent("web.sessions.clear", nil); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

func (s *Service) sessionDetail(sessionID string, limit int) (SessionDetailResponse, error) {
	meta, err := s.store.LoadMetadata(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	if strings.TrimSpace(meta.QueueJobID) != "" {
		_, _ = s.store.LoadJob(meta.QueueJobID)
	}
	state, err := s.store.LoadState(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	todo, err := s.store.LoadTodo(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	tasks, err := s.store.ListTasks(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	children, err := s.children(sessionID, 50)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	messages, err := s.store.LoadMessages(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	eventsList, err := s.store.LoadEvents(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	background, err := s.store.LoadBackgroundNotifications(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	steers, err := s.store.LoadSteerRequests(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
	}
	var contractPtr *session.SessionContract
	if contract, err := s.store.LoadContract(sessionID); err == nil && contract.ContractID != "" {
		contractPtr = &contract
	}
	requiredArtifacts, _ := s.store.LoadArtifactTracker(sessionID)
	providerAttempts, _ := s.store.LoadProviderAttempts(sessionID)
	var checkpointPtr *session.LongRunCheckpoint
	if checkpoint, err := s.store.LoadLongRunCheckpoint(sessionID); err == nil && checkpoint.SessionID != "" {
		checkpointPtr = &checkpoint
	}
	var parentCoordinationPtr *session.ParentCoordination
	if coordination, err := s.store.LoadParentCoordination(sessionID); err == nil && coordination.ParentSessionID != "" {
		parentCoordinationPtr = &coordination
	}
	ownerEvents := eventsList
	hasMoreMessages := limit > 0 && len(messages) > limit
	messages = tailMessages(messages, limit)
	eventsList = tailEvents(eventsList, limit)
	background = tailBackground(dedupeBackgroundNotifications(background), limit)
	steers = tailSteers(steers, limit)
	providerAttempts = tailProviderAttempts(providerAttempts, limit)
	if messages == nil {
		messages = []session.Message{}
	}
	if eventsList == nil {
		eventsList = []events.Event{}
	}
	if background == nil {
		background = []session.BackgroundNotification{}
	}
	if steers == nil {
		steers = []session.SteerRequest{}
	}
	timeline := buildTimeline(messages, eventsList)
	if timeline == nil {
		timeline = []TimelineEntry{}
	}
	activeOwner := s.activeHandleOwner(sessionID, state.Status, ownerEvents)
	return SessionDetailResponse{
		Metadata:                meta,
		State:                   state,
		Contract:                contractPtr,
		RequiredArtifacts:       requiredArtifacts,
		ProviderAttempts:        providerAttempts,
		LongRunCheckpoint:       checkpointPtr,
		ParentCoordination:      parentCoordinationPtr,
		TaskBoard:               session.BuildTaskBoard(todo, tasks),
		Children:                children,
		BackgroundNotifications: background,
		SteerRequests:           steers,
		Messages:                messages,
		HasMoreMessages:         hasMoreMessages,
		Events:                  eventsList,
		Timeline:                timeline,
		ActiveHandle:            activeOwner.OwnedByCurrentProcess,
		ActiveHandleOwner:       activeOwner,
	}, nil
}

func (s *Service) handleChildren(w http.ResponseWriter, sessionID string, limit int) {
	resp, err := s.children(sessionID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) children(sessionID string, limit int) (ChildrenResponse, error) {
	items, err := s.store.ListChildren(sessionID, limit)
	if err != nil {
		return ChildrenResponse{}, err
	}
	jobs, err := s.store.ListJobsByParent(sessionID, limit)
	if err != nil {
		return ChildrenResponse{}, err
	}
	if items == nil {
		items = []session.SessionSummary{}
	}
	if jobs == nil {
		jobs = []session.QueueJob{}
	}
	return ChildrenResponse{Sessions: items, Jobs: jobs}, nil
}

func (s *Service) handleTaskBoard(w http.ResponseWriter, sessionID string) {
	todo, err := s.store.LoadTodo(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tasks, err := s.store.ListTasks(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, session.BuildTaskBoard(todo, tasks))
}

func (s *Service) handleSessionMessages(w http.ResponseWriter, sessionID string, r *http.Request) {
	messages, err := s.store.LoadMessages(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if messages == nil {
		messages = []session.Message{}
	}

	limit := queryBoundedInt(r, "limit", 40, 1, 200)
	beforeID := strings.TrimSpace(r.URL.Query().Get("before_id"))

	var page []session.Message
	hasMore := false

	if beforeID == "" {
		page = tailMessages(messages, limit)
		hasMore = len(messages) > limit
	} else {
		var beforeIdx int = -1
		for i := range messages {
			if messages[i].ID == beforeID {
				beforeIdx = i
				break
			}
		}
		if beforeIdx < 0 {
			page = []session.Message{}
		} else {
			start := beforeIdx - limit
			if start < 0 {
				start = 0
			} else {
				hasMore = start > 0
			}
			page = messages[start:beforeIdx]
		}
	}

	if page == nil {
		page = []session.Message{}
	}

	writeJSON(w, http.StatusOK, MessagesResponse{
		Messages: page,
		HasMore:  hasMore,
	})
}

func (s *Service) handleStartSession(w http.ResponseWriter, r *http.Request) {
	var req StartSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}
	resp, err := s.startSession(runtime.StartRequest{
		Prompt:         req.Prompt,
		AgentName:      req.AgentName,
		AgentRole:      req.AgentRole,
		Provider:       req.Provider,
		Model:          req.Model,
		Workdir:        req.Workdir,
		Mode:           req.Mode,
		SystemOverride: req.SystemOverride,
		IsolationMode:  req.IsolationMode,
		IsolationRoot:  req.IsolationRoot,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if isClientStartError(err) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func isClientStartError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "unsupported agent role") ||
		strings.Contains(message, "isolation target must not be inside source workdir")
}

func (s *Service) startSession(req runtime.StartRequest) (LaunchResponse, error) {
	cfg, err := s.configSnapshot()
	if err != nil {
		return LaunchResponse{}, err
	}
	runner := runtime.NewRunner(cfg)
	sub := runner.Bus().Subscribe(32)
	runCtx, cancel := context.WithCancel(context.Background())
	outcomeCh := make(chan launchOutcome, 1)
	go func() {
		result, err := runner.Start(runCtx, req)
		outcomeCh <- launchOutcome{result: result, err: err}
	}()

	sessionID, early, err := waitForSessionID(sub, outcomeCh)
	if err != nil {
		cancel()
		return LaunchResponse{}, err
	}
	handle := newLaunchHandle(sessionID, runner, cancel)
	s.addHandle(handle)
	if early != nil {
		go s.finishHandle(handle, *early)
	} else {
		go func() {
			s.finishHandle(handle, <-outcomeCh)
		}()
	}
	return LaunchResponse{SessionID: sessionID, Status: "accepted"}, nil
}

func (s *Service) handleContinueSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req ContinueSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state, err := s.store.LoadState(sessionID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	switch state.Status {
	case session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed:
	default:
		writeError(w, http.StatusConflict, newWebError(
			errorCodeSessionNotResumable,
			"session is not resumable",
			"only paused, awaiting_input, and failed sessions can be continued",
			"start a new session or choose a resumable session",
		))
		return
	}
	if s.hasActiveHandle(sessionID) {
		writeError(w, http.StatusConflict, errors.New("session is already active in this web console"))
		return
	}
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runner := runtime.NewRunner(cfg)
	runCtx, cancel := context.WithCancel(context.Background())
	handle := newLaunchHandle(sessionID, runner, cancel)
	s.addHandle(handle)
	go func() {
		result, err := runner.Continue(runCtx, runtime.ContinueRequest{
			SessionID:      sessionID,
			Message:        req.Message,
			Provider:       req.Provider,
			Model:          req.Model,
			SystemOverride: req.SystemOverride,
		})
		s.finishHandle(handle, launchOutcome{result: result, err: err})
	}()
	writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
}

func (s *Service) handleSteerSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req SteerSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runner := runtime.NewRunner(cfg)
	result, err := runner.Steer(r.Context(), runtime.SteerRequest{
		SessionID: sessionID,
		Message:   req.Message,
		Interrupt: req.Interrupt,
		Source:    "web",
	})
	if err != nil {
		status := http.StatusBadRequest
		var sizeErr runtime.SteerValidationError
		if errors.As(err, &sizeErr) {
			status = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "not running") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Service) handleInterruptSession(w http.ResponseWriter, sessionID string) {
	s.mu.RLock()
	handle, ok := s.handles[sessionID]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusConflict, newWebError(
			errorCodeActiveHandleNotOwned,
			"session is not actively owned by this web console; use steer with interrupt instead",
			"this server process does not own an in-memory cancel handle for the session",
			"send POST /api/sessions/{id}/steer with interrupt=true, or continue after the active run settles",
		))
		return
	}
	if err := handle.runner.InterruptWithReason(sessionID, "manual_interrupt"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"session_id": sessionID, "status": "interrupt_requested"})
}

func (s *Service) handleStopSession(w http.ResponseWriter, sessionID string) {
	s.mu.RLock()
	handle, ok := s.handles[sessionID]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusConflict, newWebError(
			errorCodeActiveHandleNotOwned,
			"session is not actively owned by this web console; it may already be settled",
			"this server process does not own an in-memory cancel handle for the session",
			"refresh the session; if it is still running, send interrupt steer or wait for the run to settle",
		))
		return
	}
	if err := handle.runner.InterruptWithReason(sessionID, "manual_stop"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"session_id": sessionID, "status": "stop_requested"})
}

func (s *Service) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	jobs, err := s.store.ListJobs(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if jobs == nil {
		jobs = []session.QueueJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Service) handleShowJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	jobID := strings.TrimPrefix(r.URL.Path, "/api/queue/jobs/")
	if strings.TrimSpace(jobID) == "" {
		writeError(w, http.StatusNotFound, errors.New("job route not found"))
		return
	}
	job, err := s.store.LoadJob(jobID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Service) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req QueueJobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runner := runtime.NewRunner(cfg)
	job, err := runner.QueueSubmit(r.Context(), runtime.QueueSubmitRequest{
		ParentSessionID: req.ParentSessionID,
		Prompt:          req.Prompt,
		AgentName:       req.AgentName,
		AgentRole:       req.AgentRole,
		Provider:        req.Provider,
		Model:           req.Model,
		Workdir:         req.Workdir,
		SystemOverride:  req.SystemOverride,
		Mode:            req.Mode,
		WaitMode:        req.WaitMode,
		IsolationMode:   req.IsolationMode,
		IsolationRoot:   req.IsolationRoot,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Service) handleScaleWorkers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DesiredCount int `json:"desired_count"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.DesiredCount < 0 {
		writeError(w, http.StatusBadRequest, errors.New("desired_count must be >= 0"))
		return
	}
	s.workers.Scale(req.DesiredCount)
	writeJSON(w, http.StatusAccepted, s.workers.Snapshot())
}

func (s *Service) addHandle(handle *launchHandle) {
	if handle.startedAt == "" {
		handle.startedAt = nowString()
	}
	if handle.processStartID == "" {
		handle.processStartID = webconsoleProcessOwner.processStartID
	}
	if handle.pid == 0 {
		handle.pid = webconsoleProcessOwner.pid
	}
	s.mu.Lock()
	s.handles[handle.sessionID] = handle
	s.mu.Unlock()
	_ = s.recordLaunchHandleEvent(handle, "webconsole.handle.acquired", nil)
}

func (s *Service) finishHandle(handle *launchHandle, outcome launchOutcome) {
	handle.cancel()
	data := map[string]any{}
	if outcome.result.SessionID != "" {
		data["result_session_id"] = outcome.result.SessionID
	}
	if outcome.err != nil {
		data["error"] = outcome.err.Error()
	}
	_ = s.recordLaunchHandleEvent(handle, "webconsole.handle.released", data)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.handles, handle.sessionID)
}

func newLaunchHandle(sessionID string, runner *runtime.Runner, cancel context.CancelFunc) *launchHandle {
	return &launchHandle{
		sessionID:      sessionID,
		runner:         runner,
		cancel:         cancel,
		startedAt:      nowString(),
		processStartID: webconsoleProcessOwner.processStartID,
		pid:            webconsoleProcessOwner.pid,
	}
}

func (s *Service) recordLaunchHandleEvent(handle *launchHandle, eventType string, extra map[string]any) error {
	data := map[string]any{
		"source":           "webconsole",
		"process_start_id": handle.processStartID,
		"pid":              handle.pid,
		"started_at":       handle.startedAt,
	}
	if eventType == "webconsole.handle.released" {
		data["released_at"] = nowString()
	}
	for key, value := range extra {
		data[key] = value
	}
	return s.store.AppendEvent(handle.sessionID, events.New(handle.sessionID, eventType, "webconsole", data))
}

func (s *Service) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	send := func(msg map[string]any) {
		_ = conn.WriteJSON(msg)
	}

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var data struct {
			Type      string `json:"type"`
			Message   string `json:"message"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(message, &data); err != nil {
			send(map[string]any{
				"type": "error",
				"payload": map[string]any{
					"content": "invalid websocket payload",
				},
			})
			continue
		}

		switch data.Type {
		case "chat":
			sendWebSocketControlDeprecated(send, data.SessionID, "chat")
		case "reset_session":
			// Client-local reset only; WebSocket is no longer a session control path.
		case "stop":
			sendWebSocketControlDeprecated(send, data.SessionID, "stop")
		default:
			send(map[string]any{
				"type": "error",
				"payload": map[string]any{
					"code":    errorCodeWebSocketControlDeprecated,
					"content": "websocket messages are relay-only; use the REST API for session control",
					"action":  "call /api/sessions/start, /api/sessions/{id}/continue, /api/sessions/{id}/steer, /api/sessions/{id}/interrupt, or /api/sessions/{id}/stop",
				},
			})
		}
	}
}

func sendWebSocketControlDeprecated(send func(map[string]any), sessionID, messageType string) {
	payload := map[string]any{
		"code":    errorCodeWebSocketControlDeprecated,
		"content": "websocket session control is deprecated; use the REST API for start, continue, steer, interrupt, and stop",
		"action":  "use POST /api/sessions/start, /api/sessions/{id}/continue, /api/sessions/{id}/steer, /api/sessions/{id}/interrupt, or /api/sessions/{id}/stop",
		"type":    messageType,
	}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	send(map[string]any{
		"type":    "error",
		"payload": payload,
	})
}

func (s *Service) handleListFiles(w http.ResponseWriter, r *http.Request) {
	root, err := currentServerWorkspaceRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	browseRoot, err := currentServerBrowseRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	target := root
	requestedPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if requestedPath != "" && requestedPath != "." {
		target, err = resolveWorkspaceBrowserPath(root, browseRoot, requestedPath)
		if err != nil {
			writeError(w, http.StatusForbidden, errors.New("access denied"))
			return
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("path is not a directory"))
		return
	}
	tree, err := s.listDirectory(root, browseRoot, target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

func (s *Service) handleReadFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	root, err := currentServerWorkspaceRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	browseRoot, err := currentServerBrowseRoot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	fullPath, err := resolveWorkspaceBrowserPath(root, browseRoot, path)
	if err != nil {
		writeError(w, http.StatusForbidden, errors.New("access denied"))
		return
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": string(content)})
}

func currentServerWorkspaceRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cwd, "workspace")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return tools.ResolveWorkspacePath(root, ".")
}

func currentServerBrowseRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return tools.ResolveWorkspacePath(cwd, ".")
}

func resolveWorkspaceBrowserPath(workspaceRoot, browseRoot, requestedPath string) (string, error) {
	workspaceRel, err := filepath.Rel(browseRoot, workspaceRoot)
	if err != nil {
		return "", err
	}
	return tools.ResolveWorkspacePath(browseRoot, filepath.Join(workspaceRel, requestedPath))
}

func (s *Service) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	provs := map[string]any{}
	for name, p := range cfg.Providers {
		effectiveAPIProvider, _ := config.EffectiveAPIProvider(name, p)
		provs[name] = map[string]any{
			"api_provider":            strings.TrimSpace(p.APIProvider),
			"effective_api_provider":  effectiveAPIProvider,
			"base_url":                p.BaseURL,
			"model":                   p.Model,
			"has_key":                 cfg.APIKey(name) != "",
			"reasoning_mode":          providerReasoningMode(name, p),
			"reasoning_modes":         providerReasoningModes(name, p),
			"reasoning_summary":       providerReasoningSummary(p),
			"reasoning_summary_modes": providerReasoningSummaryModes(name, p),
			"reasoning_effort":        strings.TrimSpace(p.ReasoningEffort),
			"thinking_budget":         p.ThinkingBudget,
			"include_thoughts":        p.IncludeThoughts,
			"max_output_tokens":       p.MaxOutputTokens,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_provider":        cfg.DefaultProvider,
		"guardrails_mode":         cfg.Runtime.GuardrailsMode,
		"max_turns_hard":          cfg.Runtime.MaxTurnsHard,
		"disable_hard_turn_limit": cfg.Runtime.MaxTurnsHard <= 0,
		"providers":               provs,
	})
}

func (s *Service) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	updatedCfg, err := config.Clone(s.cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.Provider != "" {
		if _, ok := updatedCfg.Providers[req.Provider]; !ok {
			writeError(w, http.StatusBadRequest, newWebError(
				errorCodeUnknownProvider,
				"unknown provider",
				"provider "+req.Provider+" is not configured",
				"choose one of the configured providers before saving settings",
			))
			return
		}
		updatedCfg.DefaultProvider = req.Provider
	}
	if strings.TrimSpace(req.GuardrailsMode) != "" {
		updatedCfg.Runtime.GuardrailsMode = configMode(req.GuardrailsMode)
	}
	if req.DisableHardTurnLimit {
		updatedCfg.Runtime.MaxTurnsHard = -1
	} else if req.MaxTurnsHard != nil {
		if *req.MaxTurnsHard <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("max_turns_hard must be positive unless hard turn limit is disabled"))
			return
		}
		updatedCfg.Runtime.MaxTurnsHard = *req.MaxTurnsHard
	}

	var apiKeyAudit map[string]any
	if p, ok := updatedCfg.Providers[req.Provider]; ok {
		if req.BaseURL != "" {
			p.BaseURL = req.BaseURL
		}
		if req.Model != "" {
			p.Model = req.Model
		}
		if strings.TrimSpace(req.APIProvider) != "" {
			p.APIProvider = strings.TrimSpace(req.APIProvider)
		}
		if strings.TrimSpace(req.ReasoningMode) != "" {
			if err := applyProviderReasoningMode(req.Provider, &p, req.ReasoningMode); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		if strings.TrimSpace(req.ReasoningSummary) != "" {
			if err := applyProviderReasoningSummary(req.Provider, &p, req.ReasoningSummary); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		if _, err := config.EffectiveAPIProvider(req.Provider, p); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.APIKey != "" && req.APIKey != maskedAPIKey {
			if err := os.Setenv(p.APIKeyEnv, req.APIKey); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			cwd, _ := os.Getwd()
			envPath := config.DefaultEnvFilePath(cwd)
			if err := config.UpsertEnvFile(envPath, p.APIKeyEnv, req.APIKey); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			apiKeyAudit = map[string]any{
				"provider": req.Provider,
				"env_key":  p.APIKeyEnv,
				"env_file": envPath,
			}
		}
		updatedCfg.Providers[req.Provider] = p
	}

	cwd, _ := os.Getwd()
	configPath := s.configPath
	if strings.TrimSpace(configPath) == "" {
		configPath = config.PersistPath("", cwd)
	}
	if err := config.WriteFile(configPath, updatedCfg); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.cfg = updatedCfg
	if err := s.appendAuditEvent("web.config.write", map[string]any{
		"provider":               updatedCfg.DefaultProvider,
		"config_path":            configPath,
		"guardrails_mode":        updatedCfg.Runtime.GuardrailsMode,
		"max_turns_hard":         updatedCfg.Runtime.MaxTurnsHard,
		"hard_turn_limit_active": updatedCfg.Runtime.MaxTurnsHard > 0,
		"api_provider":           updatedCfg.Providers[updatedCfg.DefaultProvider].APIProvider,
		"reasoning_mode":         providerReasoningMode(updatedCfg.DefaultProvider, updatedCfg.Providers[updatedCfg.DefaultProvider]),
		"reasoning_summary":      providerReasoningSummary(updatedCfg.Providers[updatedCfg.DefaultProvider]),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if apiKeyAudit != nil {
		if err := s.appendAuditEvent("web.config.api_key_write", apiKeyAudit); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Service) handleTestConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	testCfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	providerName := strings.TrimSpace(req.Provider)
	if providerName == "" {
		providerName = testCfg.DefaultProvider
	}
	p, ok := testCfg.Providers[providerName]
	if !ok {
		writeError(w, http.StatusBadRequest, newWebError(
			errorCodeUnknownProvider,
			"unknown provider",
			"provider "+providerName+" is not configured",
			"choose one of the configured providers before testing settings",
		))
		return
	}
	testCfg.DefaultProvider = providerName
	if strings.TrimSpace(req.BaseURL) != "" {
		p.BaseURL = req.BaseURL
	}
	if strings.TrimSpace(req.Model) != "" {
		p.Model = req.Model
	}
	if strings.TrimSpace(req.APIProvider) != "" {
		p.APIProvider = strings.TrimSpace(req.APIProvider)
	}
	if strings.TrimSpace(req.ReasoningMode) != "" {
		if err := applyProviderReasoningMode(providerName, &p, req.ReasoningMode); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if strings.TrimSpace(req.ReasoningSummary) != "" {
		if err := applyProviderReasoningSummary(providerName, &p, req.ReasoningSummary); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	effectiveAPIProvider, err := config.EffectiveAPIProvider(providerName, p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	probeReq := runtime.ProbeRequest{Provider: providerName, APIProvider: p.APIProvider, ThinkingProbe: true, ReasoningSummary: p.ReasoningSummary}
	if req.APIKey != "" && req.APIKey != maskedAPIKey {
		apiKeyEnv := fmt.Sprintf("GO_CLI_AGENT_SETTINGS_TEST_API_KEY_%d", time.Now().UnixNano())
		if err := os.Setenv(apiKeyEnv, req.APIKey); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		defer os.Unsetenv(apiKeyEnv)
		probeReq.APIKeyEnv = apiKeyEnv
	}
	testCfg.Providers[providerName] = p

	ctx, cancel := context.WithTimeout(r.Context(), settingsProbeTimeout)
	defer cancel()
	result, err := runtime.NewRunner(testCfg).Probe(ctx, probeReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, TestConfigResponse{
		Success:                    true,
		Provider:                   result.Provider,
		APIProvider:                strings.TrimSpace(p.APIProvider),
		EffectiveAPIProvider:       effectiveAPIProvider,
		Model:                      result.Model,
		ReasoningMode:              providerReasoningMode(providerName, p),
		ReasoningSummary:           providerReasoningSummary(p),
		StopReason:                 result.StopReason,
		FinishMessage:              result.FinishMessage,
		ReasoningEffort:            strings.TrimSpace(p.ReasoningEffort),
		ReasoningSummaryObserved:   result.ReasoningSummaryObserved,
		ReasoningEncryptedObserved: result.ReasoningEncryptedObserved,
		ReasoningTokens:            result.ReasoningTokens,
		ThinkingBudget:             p.ThinkingBudget,
		ThinkingVisibleObserved:    result.ThinkingVisibleObserved,
		ThinkingReplayObserved:     result.ThinkingReplayObserved,
		ThinkingDetail:             result.ThinkingDetail,
		ThinkingStrategy:           result.ThinkingStrategy,
		MaxOutputTokens:            p.MaxOutputTokens,
		IncludeThoughts:            p.IncludeThoughts,
	})
}

func configMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yolo":
		return "yolo"
	default:
		return "standard"
	}
}

func providerReasoningModes(providerName string, provider config.Provider) []string {
	switch providerReasoningFamily(providerName, provider) {
	case "openai":
		return []string{"default", "low", "medium", "high", "xhigh"}
	case "thinking":
		return []string{"default", "standard", "max", "off"}
	default:
		return []string{"default"}
	}
}

func providerReasoningMode(providerName string, provider config.Provider) string {
	switch providerReasoningFamily(providerName, provider) {
	case "openai":
		switch strings.ToLower(strings.TrimSpace(provider.ReasoningEffort)) {
		case "low", "medium", "high", "xhigh":
			return strings.ToLower(strings.TrimSpace(provider.ReasoningEffort))
		default:
			return "default"
		}
	case "thinking":
		if provider.IncludeThoughts != nil && !*provider.IncludeThoughts {
			return "off"
		}
		if provider.ThinkingBudget >= settingsThinkingMaxBudget {
			return "max"
		}
		if provider.ThinkingBudget > 0 || (provider.IncludeThoughts != nil && *provider.IncludeThoughts) {
			return "standard"
		}
		return "default"
	default:
		return "default"
	}
}

func applyProviderReasoningMode(providerName string, provider *config.Provider, mode string) error {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" {
		return nil
	}
	switch providerReasoningFamily(providerName, *provider) {
	case "openai":
		switch normalized {
		case "default", "off":
			provider.ReasoningEffort = ""
		case "low", "medium", "high", "xhigh":
			provider.ReasoningEffort = normalized
		default:
			return fmt.Errorf("unsupported reasoning mode for %s: %s", providerName, mode)
		}
	case "thinking":
		switch normalized {
		case "default":
			provider.ThinkingBudget = 0
			provider.IncludeThoughts = nil
		case "off":
			value := false
			provider.ThinkingBudget = 0
			provider.IncludeThoughts = &value
		case "standard":
			value := true
			provider.ThinkingBudget = settingsThinkingStandardBudget
			provider.IncludeThoughts = &value
			if provider.MaxOutputTokens > 0 && provider.MaxOutputTokens <= settingsThinkingStandardBudget {
				provider.MaxOutputTokens = settingsThinkingStandardBudget + 1024
			}
		case "max":
			value := true
			provider.ThinkingBudget = settingsThinkingMaxBudget
			provider.IncludeThoughts = &value
			if provider.MaxOutputTokens < settingsThinkingMaxOutputTokens {
				provider.MaxOutputTokens = settingsThinkingMaxOutputTokens
			}
		default:
			return fmt.Errorf("unsupported thinking mode for %s: %s", providerName, mode)
		}
	default:
		if normalized != "default" {
			return fmt.Errorf("provider %s does not support reasoning mode %s", providerName, mode)
		}
	}
	return nil
}

func providerReasoningSummaryModes(providerName string, provider config.Provider) []string {
	if providerReasoningFamily(providerName, provider) == "openai" {
		return []string{"default", "auto", "concise", "detailed", "off"}
	}
	return []string{"default"}
}

func providerReasoningSummary(provider config.Provider) string {
	switch strings.ToLower(strings.TrimSpace(provider.ReasoningSummary)) {
	case "auto", "concise", "detailed":
		return strings.ToLower(strings.TrimSpace(provider.ReasoningSummary))
	case "none":
		return "off"
	default:
		return "default"
	}
}

func applyProviderReasoningSummary(providerName string, provider *config.Provider, summary string) error {
	normalized := strings.ToLower(strings.TrimSpace(summary))
	if normalized == "" {
		return nil
	}
	if providerReasoningFamily(providerName, *provider) != "openai" {
		if normalized == "default" {
			provider.ReasoningSummary = ""
			return nil
		}
		return fmt.Errorf("provider %s does not support reasoning summary %s", providerName, summary)
	}
	switch normalized {
	case "default":
		provider.ReasoningSummary = ""
	case "auto", "concise", "detailed":
		provider.ReasoningSummary = normalized
	case "off", "none":
		provider.ReasoningSummary = "none"
	default:
		return fmt.Errorf("unsupported reasoning summary for %s: %s", providerName, summary)
	}
	return nil
}

func providerReasoningFamily(providerName string, provider config.Provider) string {
	apiProvider, err := config.EffectiveAPIProvider(providerName, provider)
	if err != nil {
		return ""
	}
	switch apiProvider {
	case "openai-compatible":
		return "openai"
	case "anthropic-compatible", "google":
		return "thinking"
	default:
		return ""
	}
}

func (s *Service) handleListSkills(w http.ResponseWriter, r *http.Request) {
	type skillMeta struct {
		ID             string   `json:"id"`
		Name           string   `json:"name"`
		Author         string   `json:"author"`
		Description    string   `json:"description"`
		Icon           string   `json:"icon"`
		Tags           []string `json:"tags"`
		Downloads      int      `json:"downloads"`
		Installed      bool     `json:"installed"`
		ReadOnly       bool     `json:"read_only,omitempty"`
		Trust          string   `json:"trust,omitempty"`
		ExtensionPath  string   `json:"extension_path,omitempty"`
		DiscoveryPath  string   `json:"discovery_path,omitempty"`
		DisabledReason string   `json:"disabled_reason,omitempty"`
	}

	var skills []skillMeta

	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, rawDir := range cfg.Skills.Dirs {
		dir, err := resolveSkillDir(rawDir)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillDir := filepath.Join(dir, entry.Name())
			mdData, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
			desc := "Local skill"
			name := entry.Name()
			if err == nil {
				// Simple frontmatter extraction
				lines := strings.Split(string(mdData), "\n")
				inFront := false
				for _, l := range lines {
					l = strings.TrimSpace(l)
					if l == "---" {
						inFront = !inFront
						continue
					}
					if inFront && strings.HasPrefix(l, "description:") {
						desc = strings.TrimSpace(strings.TrimPrefix(l, "description:"))
					}
					if inFront && strings.HasPrefix(l, "name:") {
						name = strings.TrimSpace(strings.TrimPrefix(l, "name:"))
					}
				}
			}
			skills = append(skills, skillMeta{
				ID:          entry.Name(),
				Name:        name,
				Author:      "Local",
				Description: desc,
				Icon:        "Box",
				Tags:        []string{"local", entry.Name()},
				Downloads:   1,
				Installed:   true,
			})
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if discovery, err := extensions.Discover(cwd, false); err == nil {
			for _, candidate := range discovery.Candidates {
				skills = append(skills, skillMeta{
					ID:             candidate.QualifiedName,
					Name:           candidate.Name,
					Author:         "Workspace extension",
					Description:    "Discovery-only workspace extension.",
					Icon:           "folder-git-2",
					Tags:           []string{"workspace-extension", string(candidate.Trust)},
					Downloads:      0,
					Installed:      false,
					ReadOnly:       true,
					Trust:          string(candidate.Trust),
					ExtensionPath:  candidate.Path,
					DiscoveryPath:  discovery.DiscoveryPath,
					DisabledReason: candidate.DisabledReason,
				})
			}
		}
	}

	if len(skills) == 0 {
		skills = make([]skillMeta, 0)
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *Service) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	writeError(w, http.StatusNotImplemented, errors.New("installing marketplace skills is not supported; upload a .zip skill instead"))
}

func processSkillZip(src string, globalDest string) (int, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	var skillRoots []string
	cleanNames := make(map[*zip.File]string, len(r.File))
	for _, f := range r.File {
		cleaned, err := cleanZipEntryName(f.Name)
		if err != nil {
			return 0, err
		}
		cleanNames[f] = cleaned
		if path.Base(cleaned) == "SKILL.md" {
			dir := path.Dir(cleaned)
			skillRoots = append(skillRoots, dir)
		}
	}

	if len(skillRoots) == 0 {
		return 0, errors.New("no SKILL.md found in zip, not a valid skill package")
	}

	extractedCount := 0

	for _, root := range skillRoots {
		var targetDirName string
		mdPath := "SKILL.md"
		if root != "." && root != "/" && root != "" {
			mdPath = root + "/" + "SKILL.md"
		} else {
			root = "."
		}

		for _, f := range r.File {
			if cleanNames[f] == path.Clean(mdPath) {
				rc, err := f.Open()
				if err == nil {
					data, _ := io.ReadAll(rc)
					rc.Close()
					targetDirName = extractSkillNameFromMd(data)
				}
				break
			}
		}

		if targetDirName == "" {
			if root == "." {
				targetDirName = "skill_" + strconv.FormatInt(time.Now().UnixNano(), 10)
			} else {
				targetDirName = filepath.Base(root)
			}
		}

		targetDirName = sanitizeDirName(targetDirName)
		targetPath := filepath.Join(globalDest, targetDirName)

		if err := os.RemoveAll(targetPath); err != nil {
			return extractedCount, err
		}
		if err := os.MkdirAll(targetPath, 0o755); err != nil {
			return extractedCount, err
		}

		for _, f := range r.File {
			cleanedName := cleanNames[f]
			cleanRoot := path.Clean(root)

			var rel string
			var isInRoot bool

			if cleanRoot == "." {
				rel = cleanedName
				isInRoot = true
			} else {
				if strings.HasPrefix(cleanedName, cleanRoot+"/") {
					rel = strings.TrimPrefix(cleanedName, cleanRoot+"/")
					isInRoot = true
				} else if cleanedName == cleanRoot {
					continue
				}
			}

			if !isInRoot {
				continue
			}

			if rel == "." || rel == "" {
				continue
			}
			outPath := filepath.Join(targetPath, filepath.FromSlash(rel))
			if !pathWithinRoot(targetPath, outPath) {
				return extractedCount, fmt.Errorf("zip entry escapes skill target: %s", f.Name)
			}

			if f.FileInfo().IsDir() {
				if err := os.MkdirAll(outPath, f.Mode()); err != nil {
					return extractedCount, err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return extractedCount, err
			}

			rc, err := f.Open()
			if err != nil {
				return extractedCount, err
			}

			destFile, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				rc.Close()
				return extractedCount, err
			}
			_, copyErr := io.Copy(destFile, rc)
			closeDestErr := destFile.Close()
			closeSrcErr := rc.Close()
			if copyErr != nil {
				return extractedCount, copyErr
			}
			if closeDestErr != nil {
				return extractedCount, closeDestErr
			}
			if closeSrcErr != nil {
				return extractedCount, closeSrcErr
			}
		}
		extractedCount++
	}
	return extractedCount, nil
}

func cleanZipEntryName(name string) (string, error) {
	original := name
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "", fmt.Errorf("invalid empty zip entry name")
	}
	if strings.HasPrefix(name, "/") || (len(name) >= 2 && name[1] == ':') {
		return "", fmt.Errorf("zip entry uses absolute path: %s", original)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("zip entry uses path traversal: %s", original)
	}
	return cleaned, nil
}

func pathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}

func sanitizeDirName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func extractSkillNameFromMd(data []byte) string {
	lines := strings.Split(string(data), "\n")
	inFront := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "---" {
			inFront = !inFront
			continue
		}
		if inFront && strings.HasPrefix(l, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "name:"))
		}
	}
	return ""
}

func (s *Service) handleUploadSkill(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(cfg.Skills.Dirs) == 0 {
		writeError(w, http.StatusInternalServerError, errors.New("no skill directory configured"))
		return
	}
	if err := r.ParseMultipartForm(50 << 20); err != nil { // limit 50MB
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer file.Close()

	dest, err := resolveSkillDir(cfg.Skills.Dirs[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	os.MkdirAll(dest, 0755)

	tmpFile, err := os.CreateTemp("", "skill-upload-*.zip")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(tmpFile.Name())
	io.Copy(tmpFile, file)
	tmpFile.Close()

	count, err := processSkillZip(tmpFile.Name(), dest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.appendAuditEvent("web.skill.install", map[string]any{
		"skill_dir":       dest,
		"installed_count": count,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "installed_count": count})
}

func (s *Service) handleUninstallSkill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(cfg.Skills.Dirs) == 0 {
		writeError(w, http.StatusInternalServerError, errors.New("no skill directory configured"))
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		writeError(w, http.StatusBadRequest, errors.New("invalid path format"))
		return
	}
	skillID := parts[3]

	rootDir, err := resolveSkillDir(cfg.Skills.Dirs[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	targetDir := filepath.Join(rootDir, skillID)
	// simple protection against directory traversal
	if !pathWithinRoot(rootDir, targetDir) {
		writeError(w, http.StatusForbidden, errors.New("access denied"))
		return
	}

	if err := os.RemoveAll(targetDir); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.appendAuditEvent("web.skill.uninstall", map[string]any{
		"skill_id":  skillID,
		"skill_dir": targetDir,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Service) listDirectory(root, browseRoot, current string) ([]any, error) {
	entries, err := os.ReadDir(current)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	tree := []any{}
	if current != browseRoot {
		parent := filepath.Dir(current)
		parentRel, _ := filepath.Rel(root, parent)
		if parentRel == "." {
			parentRel = ""
		}
		tree = append(tree, map[string]any{
			"name":       "..",
			"path":       parentRel,
			"type":       "directory",
			"navigation": "parent",
		})
	}
	for _, entry := range entries {
		if entry.Name() == "node_modules" || entry.Name() == ".git" || entry.Name() == ".go-cli-agent" {
			continue
		}
		fullPath := filepath.Join(current, entry.Name())
		relPath, _ := filepath.Rel(root, fullPath)
		if relPath == "." {
			relPath = ""
		}
		node := map[string]any{
			"name": entry.Name(),
			"path": relPath,
		}
		if entry.IsDir() {
			node["type"] = "directory"
		} else {
			node["type"] = "file"
		}
		tree = append(tree, node)
	}
	return tree, nil
}

func (s *Service) hasActiveHandle(sessionID string) bool {
	s.pruneInactiveHandles()
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.handles[sessionID]
	return ok
}

func (s *Service) activeHandleOwner(sessionID, sessionStatus string, eventsList []events.Event) ActiveHandleOwner {
	s.pruneInactiveHandles()
	s.mu.RLock()
	handle, ok := s.handles[sessionID]
	s.mu.RUnlock()
	if ok {
		return ActiveHandleOwner{
			State:                 "current_process",
			OwnedByCurrentProcess: true,
			ProcessStartID:        handle.processStartID,
			PID:                   handle.pid,
			StartedAt:             handle.startedAt,
			Action:                "interrupt and stop are available from this web console process",
		}
	}

	owner := latestActiveOwnerFromEvents(eventsList)
	if sessionStatus == session.StatusRunning {
		owner.State = "running_not_owned"
		owner.OwnedByCurrentProcess = false
		owner.Action = "send POST /api/sessions/{id}/steer with interrupt=true, or continue after the active run settles"
		return owner
	}
	owner.State = "settled"
	owner.OwnedByCurrentProcess = false
	owner.Action = "refresh the session or continue it if the current state is resumable"
	return owner
}

func latestActiveOwnerFromEvents(eventsList []events.Event) ActiveHandleOwner {
	for i := len(eventsList) - 1; i >= 0; i-- {
		evt := eventsList[i]
		if evt.Type != "webconsole.handle.acquired" && evt.Type != "webconsole.handle.released" {
			continue
		}
		owner := ActiveHandleOwner{
			ProcessStartID: eventString(evt.Data, "process_start_id"),
			PID:            eventInt(evt.Data, "pid"),
			StartedAt:      eventString(evt.Data, "started_at"),
			ReleasedAt:     eventString(evt.Data, "released_at"),
			LastEventAt:    evt.Time,
		}
		return owner
	}
	return ActiveHandleOwner{}
}

func (s *Service) hasAnyActiveHandle() bool {
	s.pruneInactiveHandles()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.handles) > 0
}

func (s *Service) hasActiveDescendantHandle(sessionID string) bool {
	s.pruneInactiveHandles()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.handles[sessionID]; ok {
		return true
	}
	for id := range s.handles {
		meta, err := s.store.LoadMetadata(id)
		if err != nil {
			continue
		}
		if meta.ParentSessionID == sessionID || meta.RootSessionID == sessionID {
			return true
		}
	}
	return false
}

func (s *Service) pruneInactiveHandles() {
	s.mu.RLock()
	ids := make([]string, 0, len(s.handles))
	for id := range s.handles {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	if len(ids) == 0 {
		return
	}
	stale := make([]string, 0, len(ids))
	for _, id := range ids {
		state, err := s.store.LoadState(id)
		if err != nil || state.Status != session.StatusRunning {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range stale {
		delete(s.handles, id)
	}
}

func (s *Service) ensureSessionTreeNotLive(sessionID string) error {
	hasRunningJobs, err := s.hasRunningQueueJobs(sessionID)
	if err != nil {
		return err
	}
	if hasRunningJobs {
		return errors.New("cannot delete a running session tree")
	}
	return nil
}

func (s *Service) hasRunningQueueJobs(sessionID string) (bool, error) {
	jobs, _, err := s.store.ListJobsPage(1000000, 0)
	if err != nil {
		return false, err
	}
	if sessionID == "" {
		for _, job := range jobs {
			if job.Status == session.QueueStatusRunning {
				return true, nil
			}
		}
		return false, nil
	}

	items, _, err := s.store.ListPage(1000000, 0)
	if err != nil {
		return false, err
	}
	targets := map[string]struct{}{sessionID: {}}
	changed := true
	for changed {
		changed = false
		for _, item := range items {
			if _, ok := targets[item.ID]; ok {
				continue
			}
			if _, ok := targets[item.ParentSessionID]; ok {
				targets[item.ID] = struct{}{}
				changed = true
			}
		}
	}
	for _, job := range jobs {
		if job.Status != session.QueueStatusRunning {
			continue
		}
		if _, ok := targets[job.ParentSessionID]; ok {
			return true, nil
		}
		if _, ok := targets[job.SessionID]; ok {
			return true, nil
		}
		if _, ok := targets[job.RootSessionID]; ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) handleForSession(sessionID string) (*launchHandle, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	handle, ok := s.handles[sessionID]
	return handle, ok
}

type launchOutcome struct {
	result runtime.RunResult
	err    error
}

func waitForSessionID(sub <-chan events.Event, outcomeCh <-chan launchOutcome) (string, *launchOutcome, error) {
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case evt := <-sub:
			if evt.Type == "session.created" || evt.Type == "session.started" {
				return evt.SessionID, nil, nil
			}
		case outcome := <-outcomeCh:
			if outcome.result.SessionID != "" {
				return outcome.result.SessionID, &outcome, nil
			}
			if outcome.err != nil {
				return "", &outcome, outcome.err
			}
			return "", &outcome, errors.New("session start returned without session id")
		case <-timeout.C:
			return "", nil, errors.New("timed out waiting for session creation")
		}
	}
}

func (s *Service) relayWebSocketEvents(sub <-chan events.Event, sessionID string, send func(map[string]any), done <-chan struct{}) {
	go func() {
		for {
			select {
			case evt := <-sub:
				if sessionID != "" && evt.SessionID != "" && evt.SessionID != sessionID {
					continue
				}
				for _, msg := range s.translateWebSocketEvent(evt) {
					send(msg)
				}
			case <-done:
				return
			}
		}
	}()
}

func (s *Service) translateWebSocketEvent(evt events.Event) []map[string]any {
	messages := []map[string]any{
		{
			"type": "engine_event",
			"payload": map[string]any{
				"id":        evt.ID,
				"sessionId": evt.SessionID,
				"type":      evt.Type,
				"phase":     evt.Phase,
				"data":      evt.Data,
				"time":      evt.Time,
			},
		},
	}

	switch evt.Type {
	case "assistant.message":
		agentName := "Agent"
		if meta, err := s.store.LoadMetadata(evt.SessionID); err == nil {
			if strings.TrimSpace(meta.AgentName) != "" {
				agentName = meta.AgentName
			}
		}
		messages = append(messages, map[string]any{
			"type": "message",
			"payload": map[string]any{
				"sessionId": evt.SessionID,
				"role":      "assistant",
				"content":   stringValue(evt.Data["text"]),
				"agentName": agentName,
			},
		})
	case "session.started":
		messages = append(messages, map[string]any{
			"type": "status",
			"payload": map[string]any{
				"sessionId": evt.SessionID,
				"status":    session.StatusRunning,
				"phase":     evt.Phase,
			},
		})
	case "session.awaiting_input":
		messages = append(messages, map[string]any{
			"type": "status",
			"payload": map[string]any{
				"sessionId": evt.SessionID,
				"status":    session.StatusAwaitingInput,
				"phase":     evt.Phase,
			},
		})
	case "session.paused":
		messages = append(messages, map[string]any{
			"type": "status",
			"payload": map[string]any{
				"sessionId": evt.SessionID,
				"status":    session.StatusPaused,
				"phase":     evt.Phase,
			},
		})
	case "session.completed":
		messages = append(messages, map[string]any{
			"type": "status",
			"payload": map[string]any{
				"sessionId": evt.SessionID,
				"status":    session.StatusCompleted,
				"phase":     evt.Phase,
			},
		})
	case "session.failed":
		errText := firstNonEmpty(stringValue(evt.Data["error"]), "session failed")
		messages = append(messages,
			map[string]any{
				"type": "status",
				"payload": map[string]any{
					"sessionId": evt.SessionID,
					"status":    session.StatusFailed,
					"phase":     evt.Phase,
				},
			},
			map[string]any{
				"type": "error",
				"payload": map[string]any{
					"sessionId": evt.SessionID,
					"content":   errText,
				},
			},
		)
	}

	return messages
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func resolveSkillDir(rawDir string) (string, error) {
	if filepath.IsAbs(rawDir) {
		return filepath.Clean(rawDir), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, rawDir), nil
}

func newWorkerPool(cfg *config.Config, desired int) *workerPool {
	pool := &workerPool{
		cfg:     cfg,
		workers: map[int]*workerHandle{},
	}
	pool.Scale(desired)
	return pool
}

func (p *workerPool) Close() {
	p.Scale(0)
}

func (p *workerPool) Scale(desired int) {
	if desired < 0 {
		desired = 0
	}
	var toStop []*workerHandle
	p.mu.Lock()
	p.desired = desired
	for len(p.workers) < desired {
		p.startWorkerLocked()
	}
	if len(p.workers) > desired {
		ids := make([]int, 0, len(p.workers))
		for id := range p.workers {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for len(p.workers) > desired {
			id := ids[len(ids)-1]
			ids = ids[:len(ids)-1]
			worker := p.workers[id]
			delete(p.workers, id)
			toStop = append(toStop, worker)
		}
	}
	p.mu.Unlock()
	for _, worker := range toStop {
		worker.cancel()
		<-worker.done
	}
}

func (p *workerPool) startWorkerLocked() {
	p.nextID++
	id := p.nextID
	ctx, cancel := context.WithCancel(context.Background())
	worker := &workerHandle{
		id:     id,
		runner: runtime.NewRunner(p.cfg),
		cancel: cancel,
		done:   make(chan struct{}),
		status: WorkerStatus{
			ID:        id,
			State:     "starting",
			UpdatedAt: nowString(),
		},
	}
	p.workers[id] = worker
	go func() {
		defer close(worker.done)
		p.runWorker(ctx, worker)
	}()
}

func (p *workerPool) runWorker(ctx context.Context, worker *workerHandle) {
	poll := time.Duration(p.cfg.Runtime.Queue.PollIntervalMS) * time.Millisecond
	if poll <= 0 {
		poll = time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			worker.setStatus(WorkerStatus{
				ID:        worker.id,
				State:     "stopped",
				UpdatedAt: nowString(),
			})
			return
		case <-timer.C:
		}
		current := worker.snapshot()
		worker.setStatus(WorkerStatus{
			ID:            worker.id,
			State:         "polling",
			LastJobID:     current.LastJobID,
			LastJobStatus: current.LastJobStatus,
			LastError:     current.LastError,
			UpdatedAt:     nowString(),
		})
		job, ok, err := worker.runner.ProcessNextJob(ctx)
		if err != nil {
			current = worker.snapshot()
			worker.setStatus(WorkerStatus{
				ID:            worker.id,
				State:         "error",
				LastJobID:     current.LastJobID,
				LastJobStatus: current.LastJobStatus,
				LastError:     err.Error(),
				UpdatedAt:     nowString(),
			})
			timer.Reset(poll)
			continue
		}
		if ok {
			worker.setStatus(WorkerStatus{
				ID:            worker.id,
				State:         "processed",
				LastJobID:     job.ID,
				LastJobStatus: job.Status,
				LastError:     firstNonEmpty(job.LastError, ""),
				UpdatedAt:     nowString(),
			})
			timer.Reset(0)
			continue
		}
		current = worker.snapshot()
		worker.setStatus(WorkerStatus{
			ID:            worker.id,
			State:         "idle",
			LastJobID:     current.LastJobID,
			LastJobStatus: current.LastJobStatus,
			LastError:     current.LastError,
			UpdatedAt:     nowString(),
		})
		timer.Reset(poll)
	}
}

func (p *workerPool) Snapshot() WorkerPoolSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	snapshot := WorkerPoolSnapshot{
		DesiredCount: p.desired,
		ActiveCount:  len(p.workers),
		PollInterval: p.cfg.Runtime.Queue.PollIntervalMS,
		Workers:      []WorkerStatus{},
	}
	for _, worker := range p.workers {
		snapshot.Workers = append(snapshot.Workers, worker.snapshot())
	}
	sort.Slice(snapshot.Workers, func(i, j int) bool { return snapshot.Workers[i].ID < snapshot.Workers[j].ID })
	return snapshot
}

func (w *workerHandle) snapshot() WorkerStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

func (w *workerHandle) setStatus(status WorkerStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
}

func buildTimeline(messages []session.Message, eventsList []events.Event) []TimelineEntry {
	items := make([]TimelineEntry, 0, len(messages)+len(eventsList))
	for _, msg := range messages {
		text := strings.TrimSpace(msg.Text)
		if text == "" && len(msg.ToolResults) > 0 {
			text = msg.ToolResults[0].DisplayOutput
		}
		items = append(items, TimelineEntry{
			Time:      msg.CreatedAt,
			Kind:      "message",
			MessageID: msg.ID,
			Role:      msg.Role,
			Text:      text,
			Data:      msg.Meta,
		})
	}
	for _, evt := range eventsList {
		items = append(items, TimelineEntry{
			Time:      evt.Time,
			Kind:      "event",
			EventID:   evt.ID,
			EventType: evt.Type,
			Phase:     evt.Phase,
			Data:      evt.Data,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Time > items[j].Time })
	return items
}

func tailMessages(items []session.Message, limit int) []session.Message {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func tailEvents(items []events.Event, limit int) []events.Event {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func dedupeBackgroundNotifications(items []session.BackgroundNotification) []session.BackgroundNotification {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]session.BackgroundNotification, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		key := item.QueueJobID
		if key == "" {
			key = item.ID
		}
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, item)
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

func tailBackground(items []session.BackgroundNotification, limit int) []session.BackgroundNotification {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func tailSteers(items []session.SteerRequest, limit int) []session.SteerRequest {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func tailProviderAttempts(items []session.ProviderAttempt, limit int) []session.ProviderAttempt {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func serveEmbeddedFile(w http.ResponseWriter, files fs.FS, name string) {
	data, err := fs.ReadFile(files, filepath.Clean(name))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")

	_, _ = w.Write(data)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	resp := ErrorResponse{Error: err.Error()}
	var coded webError
	if errors.As(err, &coded) {
		resp.Code = coded.code
		resp.Detail = coded.detail
		resp.Action = coded.action
	}
	writeJSON(w, status, resp)
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func queryBoundedInt(r *http.Request, key string, fallback, minValue, maxValue int) int {
	value := queryInt(r, key, fallback)
	if value < minValue || value > maxValue {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func eventString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func eventInt(data map[string]any, key string) int {
	if data == nil {
		return 0
	}
	switch value := data[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := strconv.Atoi(value.String())
		return parsed
	default:
		return 0
	}
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
