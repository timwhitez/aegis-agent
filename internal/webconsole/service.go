package webconsole

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
)

type Options struct {
	WorkerCount int
}

type Service struct {
	cfg      *config.Config
	store    *session.Store
	staticFS fs.FS
	workers  *workerPool

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
	SessionRoot   string         `json:"session_root"`
	DefaultMode   string         `json:"default_mode"`
	QueuePollMS   int            `json:"queue_poll_ms"`
	WorkerCount   int            `json:"worker_count"`
	Capabilities  []string       `json:"capabilities"`
	DefaultVendor string         `json:"default_provider"`
	Providers     []ProviderMeta `json:"providers"`
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
		cfg:      cfg,
		store:    store,
		staticFS: staticFS,
		handles:  map[string]*launchHandle{},
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
	if strings.HasPrefix(r.URL.Path, "/api/") {
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
	case r.Method == http.MethodGet && r.URL.Path == "/api/sessions":
		s.handleListSessions(w, r)
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
	default:
		writeError(w, http.StatusNotFound, errors.New("route not found"))
	}
}

func (s *Service) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		serveEmbeddedFile(w, s.staticFS, "index.html")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
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
	return MetaResponse{
		SessionRoot:   s.store.Root(),
		DefaultMode:   s.cfg.Runtime.Isolation.DefaultMode,
		QueuePollMS:   s.cfg.Runtime.Queue.PollIntervalMS,
		WorkerCount:   s.workers.Snapshot().DesiredCount,
		Capabilities:  []string{"start", "steer", "continue", "interrupt", "queue", "children", "tasks"},
		DefaultVendor: s.cfg.DefaultProvider,
		Providers:     providers,
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
				Message:   item.Phase,
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

func (s *Service) handleSessionRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, errors.New("session route not found"))
		return
	}
	sessionID := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
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
	messages = tailMessages(messages, limit)
	eventsList = tailEvents(eventsList, limit)
	background = tailBackground(background, limit)
	steers = tailSteers(steers, limit)
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
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
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

func (s *Service) hasActiveHandle(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.handles[sessionID]
	return ok
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
