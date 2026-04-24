package webconsole

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
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
	sessionID string
	runner    *runtime.Runner
	cancel    context.CancelFunc
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
	Events                  []events.Event                   `json:"events"`
	Timeline                []TimelineEntry                  `json:"timeline"`
	ActiveHandle            bool                             `json:"active_handle"`
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
	store := runtime.NewStoreView(cfg).Store()
	svc := &Service{
		cfg:        cfg,
		configPath: opts.ConfigPath,
		store:      store,
		staticFS:   staticFS,
		handles:    map[string]*launchHandle{},
	}
	svc.workers = newWorkerPool(cfg, opts.WorkerCount)
	return svc, nil
}

func (s *Service) Close() {
	s.workers.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, handle := range s.handles {
		handle.cancel()
	}
	s.handles = map[string]*launchHandle{}
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
		writeJSON(w, http.StatusOK, s.meta())
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

func (s *Service) meta() MetaResponse {
	providers := make([]ProviderMeta, 0, len(s.cfg.Providers))
	for name, provider := range s.cfg.Providers {
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
		DefaultMode:              s.cfg.Runtime.Isolation.DefaultMode,
		QueuePollMS:              s.cfg.Runtime.Queue.PollIntervalMS,
		WorkerCount:              s.workers.Snapshot().DesiredCount,
		Capabilities:             []string{"start", "steer", "continue", "interrupt", "stop", "queue", "children", "tasks"},
		DefaultVendor:            s.cfg.DefaultProvider,
		Providers:                providers,
	}
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
			resp, err := s.sessionDetail(sessionID, queryInt(r, "limit", 40))
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
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

func (s *Service) sessionDetail(sessionID string, limit int) (SessionDetailResponse, error) {
	meta, err := s.store.LoadMetadata(sessionID)
	if err != nil {
		return SessionDetailResponse{}, err
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
		Events:                  eventsList,
		Timeline:                timeline,
		ActiveHandle:            s.hasActiveHandle(sessionID),
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

func (s *Service) handleStartSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt         string `json:"prompt"`
		AgentName      string `json:"agent_name"`
		AgentRole      string `json:"agent_role"`
		Provider       string `json:"provider"`
		Model          string `json:"model"`
		Workdir        string `json:"workdir"`
		Mode           string `json:"mode"`
		SystemOverride string `json:"system"`
		IsolationMode  string `json:"isolation_mode"`
		IsolationRoot  string `json:"isolation_root"`
	}
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
	runner := runtime.NewRunner(s.cfg)
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
	handle := &launchHandle{
		sessionID: sessionID,
		runner:    runner,
		cancel:    cancel,
	}
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
	var req struct {
		Message        string `json:"message"`
		Provider       string `json:"provider"`
		Model          string `json:"model"`
		SystemOverride string `json:"system"`
	}
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
		writeError(w, http.StatusConflict, errors.New("session is not resumable"))
		return
	}
	if s.hasActiveHandle(sessionID) {
		writeError(w, http.StatusConflict, errors.New("session is already active in this web console"))
		return
	}
	runner := runtime.NewRunner(s.cfg)
	runCtx, cancel := context.WithCancel(context.Background())
	handle := &launchHandle{
		sessionID: sessionID,
		runner:    runner,
		cancel:    cancel,
	}
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
	var req struct {
		Message   string `json:"message"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runner := runtime.NewRunner(s.cfg)
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
		writeError(w, http.StatusConflict, errors.New("session is not actively owned by this web console; use steer with interrupt instead"))
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
		writeError(w, http.StatusConflict, errors.New("session is not actively owned by this web console; it may already be settled"))
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
	var req struct {
		Prompt          string `json:"prompt"`
		ParentSessionID string `json:"parent_session_id"`
		AgentName       string `json:"agent_name"`
		AgentRole       string `json:"agent_role"`
		Provider        string `json:"provider"`
		Model           string `json:"model"`
		Workdir         string `json:"workdir"`
		SystemOverride  string `json:"system"`
		Mode            string `json:"mode"`
		WaitMode        string `json:"wait_mode"`
		IsolationMode   string `json:"isolation_mode"`
		IsolationRoot   string `json:"isolation_root"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, errors.New("prompt is required"))
		return
	}
	runner := runtime.NewRunner(s.cfg)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handles[handle.sessionID] = handle
}

func (s *Service) finishHandle(handle *launchHandle, _ launchOutcome) {
	handle.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.handles, handle.sessionID)
}

func (s *Service) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sendQueue := make(chan map[string]any, 128)
	stop := make(chan struct{})
	var stopped atomic.Bool
	var writeWG sync.WaitGroup
	writeWG.Add(1)
	go func() {
		defer writeWG.Done()
		for {
			select {
			case msg := <-sendQueue:
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}()

	send := func(msg map[string]any) {
		if stopped.Load() {
			return
		}
		select {
		case sendQueue <- msg:
		case <-stop:
		}
	}

	var watched sync.Map
	attachRunner := func(runner *runtime.Runner, sessionID string) {
		if runner == nil {
			return
		}
		if _, loaded := watched.LoadOrStore(runner, struct{}{}); loaded {
			return
		}
		sub := runner.Bus().Subscribe(64)
		s.relayWebSocketEvents(sub, sessionID, send, stop)
	}

	currentSessionID := ""

	processChat := func(frontendSessionID, text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			send(map[string]any{
				"type": "error",
				"payload": map[string]any{
					"content": "message is required",
				},
			})
			return
		}

		startNewSession := func() {
			sessionID, err := s.startWebSocketSession(frontendSessionID, text, send, stop)
			if err != nil {
				send(map[string]any{
					"type": "error",
					"payload": map[string]any{
						"content": err.Error(),
					},
				})
				return
			}
			currentSessionID = sessionID
		}

		if currentSessionID == "" {
			startNewSession()
			return
		}

		state, err := s.store.LoadState(currentSessionID)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				startNewSession()
				return
			}
			send(map[string]any{
				"type": "error",
				"payload": map[string]any{
					"content": err.Error(),
				},
			})
			return
		}
		state = s.settleWebSocketChatState(currentSessionID, state)

		switch state.Status {
		case session.StatusRunning:
			handle, ok := s.handleForSession(currentSessionID)
			if !ok {
				send(map[string]any{
					"type": "error",
					"payload": map[string]any{
						"content": "session is running but is not actively owned by this web console",
					},
				})
				return
			}
			attachRunner(handle.runner, currentSessionID)
			if _, err := handle.runner.Steer(context.Background(), runtime.SteerRequest{
				SessionID: currentSessionID,
				Message:   text,
				Source:    "web",
			}); err != nil {
				send(map[string]any{
					"type": "error",
					"payload": map[string]any{
						"sessionId": currentSessionID,
						"content":   err.Error(),
					},
				})
			}
		case session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed:
			s.continueWebSocketSession(currentSessionID, text, send, stop)
		default:
			startNewSession()
		}
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
			if data.SessionID != "" && currentSessionID == "" {
				currentSessionID = data.SessionID
			}
			processChat(data.SessionID, data.Message)
		case "reset_session":
			currentSessionID = ""
		case "stop":
			if currentSessionID == "" {
				send(map[string]any{
					"type": "error",
					"payload": map[string]any{
						"content": "no active session to interrupt",
					},
				})
				continue
			}
			handle, ok := s.handleForSession(currentSessionID)
			if !ok {
				send(map[string]any{
					"type": "error",
					"payload": map[string]any{
						"sessionId": currentSessionID,
						"content":   "session is not actively owned by this web console",
					},
				})
				continue
			}
			if err := handle.runner.InterruptWithReason(currentSessionID, "manual_interrupt"); err != nil {
				send(map[string]any{
					"type": "error",
					"payload": map[string]any{
						"sessionId": currentSessionID,
						"content":   err.Error(),
					},
				})
			}
		}
	}

	stopped.Store(true)
	close(stop)
	writeWG.Wait()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	provs := map[string]any{}
	for name, p := range s.cfg.Providers {
		provs[name] = map[string]any{
			"base_url": p.BaseURL,
			"model":    p.Model,
			"has_key":  s.cfg.APIKey(name) != "",
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_provider":        s.cfg.DefaultProvider,
		"guardrails_mode":         s.cfg.Runtime.GuardrailsMode,
		"max_turns_hard":          s.cfg.Runtime.MaxTurnsHard,
		"disable_hard_turn_limit": s.cfg.Runtime.MaxTurnsHard <= 0,
		"providers":               provs,
	})
}

func (s *Service) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider             string `json:"provider"`
		BaseURL              string `json:"base_url"`
		Model                string `json:"model"`
		APIKey               string `json:"api_key"`
		GuardrailsMode       string `json:"guardrails_mode"`
		MaxTurnsHard         *int   `json:"max_turns_hard"`
		DisableHardTurnLimit bool   `json:"disable_hard_turn_limit"`
	}
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

	if p, ok := updatedCfg.Providers[req.Provider]; ok {
		if req.BaseURL != "" {
			p.BaseURL = req.BaseURL
		}
		if req.Model != "" {
			p.Model = req.Model
		}
		if req.APIKey != "" && req.APIKey != "••••••••••••••••" {
			if err := os.Setenv(p.APIKeyEnv, req.APIKey); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			cwd, _ := os.Getwd()
			if err := config.UpsertEnvFile(config.DefaultEnvFilePath(cwd), p.APIKeyEnv, req.APIKey); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
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
	*s.cfg = *updatedCfg

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func configMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yolo":
		return "yolo"
	default:
		return "standard"
	}
}

func (s *Service) handleListSkills(w http.ResponseWriter, r *http.Request) {
	type skillMeta struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Author      string   `json:"author"`
		Description string   `json:"description"`
		Icon        string   `json:"icon"`
		Tags        []string `json:"tags"`
		Downloads   int      `json:"downloads"`
		Installed   bool     `json:"installed"`
	}

	var skills []skillMeta

	for _, rawDir := range s.cfg.Skills.Dirs {
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

	if len(skills) == 0 {
		skills = make([]skillMeta, 0)
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *Service) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, errors.New("installing marketplace skills is not supported; upload a .zip skill instead"))
}

func processSkillZip(src string, globalDest string) (int, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	var skillRoots []string
	for _, f := range r.File {
		if filepath.Base(f.Name) == "SKILL.md" {
			dir := filepath.Dir(f.Name)
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
			if filepath.ToSlash(filepath.Clean(f.Name)) == filepath.ToSlash(filepath.Clean(mdPath)) {
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

		os.RemoveAll(targetPath)
		os.MkdirAll(targetPath, 0755)

		for _, f := range r.File {
			cleanedName := filepath.ToSlash(filepath.Clean(f.Name))
			cleanRoot := filepath.ToSlash(filepath.Clean(root))

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

			outPath := filepath.Join(targetPath, rel)

			if f.FileInfo().IsDir() {
				os.MkdirAll(outPath, f.Mode())
				continue
			}
			os.MkdirAll(filepath.Dir(outPath), 0755)

			rc, err := f.Open()
			if err != nil {
				return extractedCount, err
			}

			destFile, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				rc.Close()
				return extractedCount, err
			}
			io.Copy(destFile, rc)
			destFile.Close()
			rc.Close()
		}
		extractedCount++
	}
	return extractedCount, nil
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
	if len(s.cfg.Skills.Dirs) == 0 {
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

	dest, err := resolveSkillDir(s.cfg.Skills.Dirs[0])
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

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "installed_count": count})
}

func (s *Service) handleUninstallSkill(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.Skills.Dirs) == 0 {
		writeError(w, http.StatusInternalServerError, errors.New("no skill directory configured"))
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		writeError(w, http.StatusBadRequest, errors.New("invalid path format"))
		return
	}
	skillID := parts[3]

	rootDir, err := resolveSkillDir(s.cfg.Skills.Dirs[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	targetDir := filepath.Join(rootDir, skillID)
	// simple protection against directory traversal
	if !strings.HasPrefix(targetDir, filepath.Clean(rootDir)+string(os.PathSeparator)) {
		writeError(w, http.StatusForbidden, errors.New("access denied"))
		return
	}

	os.RemoveAll(targetDir)
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

func (s *Service) startWebSocketSession(frontendSessionID, prompt string, send func(map[string]any), done <-chan struct{}) (string, error) {
	runner := runtime.NewRunner(s.cfg)
	sub := runner.Bus().Subscribe(64)
	runCtx, cancel := context.WithCancel(context.Background())
	outcomeCh := make(chan launchOutcome, 1)
	go func() {
		result, err := runner.Start(runCtx, runtime.StartRequest{
			Prompt: prompt,
		})
		outcomeCh <- launchOutcome{result: result, err: err}
	}()

	sessionID, early, err := waitForSessionID(sub, outcomeCh)
	if err != nil {
		cancel()
		return "", err
	}

	handle := &launchHandle{
		sessionID: sessionID,
		runner:    runner,
		cancel:    cancel,
	}
	s.addHandle(handle)
	send(map[string]any{
		"type": "session",
		"payload": map[string]any{
			"clientSessionId": frontendSessionID,
			"sessionId":       sessionID,
		},
	})
	s.relayWebSocketEvents(sub, sessionID, send, done)

	if early != nil {
		go s.finishHandle(handle, *early)
	} else {
		go func() {
			s.finishHandle(handle, <-outcomeCh)
		}()
	}
	return sessionID, nil
}

func (s *Service) continueWebSocketSession(sessionID, message string, send func(map[string]any), done <-chan struct{}) {
	runner := runtime.NewRunner(s.cfg)
	sub := runner.Bus().Subscribe(64)
	runCtx, cancel := context.WithCancel(context.Background())
	handle := &launchHandle{
		sessionID: sessionID,
		runner:    runner,
		cancel:    cancel,
	}
	s.addHandle(handle)
	s.relayWebSocketEvents(sub, sessionID, send, done)
	go func() {
		result, err := runner.Continue(runCtx, runtime.ContinueRequest{
			SessionID: sessionID,
			Message:   message,
		})
		s.finishHandle(handle, launchOutcome{result: result, err: err})
	}()
}

func (s *Service) settleWebSocketChatState(sessionID string, state session.State) session.State {
	if state.Status != session.StatusRunning {
		return state
	}
	switch state.Phase {
	case "provider_call", "assistant_output", "turn_decide":
	default:
		return state
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	current := state
	for time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
		next, err := s.store.LoadState(sessionID)
		if err != nil {
			return current
		}
		current = next
		if current.Status != session.StatusRunning {
			return current
		}
	}
	return current
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
	writeJSON(w, status, map[string]any{"error": err.Error()})
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nowString() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
