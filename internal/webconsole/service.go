package webconsole

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
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
	"go-cli-agent/internal/fileutil"
	"go-cli-agent/internal/runtime"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			return true
		}
		originURL, err := url.Parse(origin)
		return err == nil && sameOriginHost(originURL, r.Host)
	},
}

var webconsoleProcessOwner = newProcessOwner()

const (
	maskedAPIKey                    = "••••••••••••••••"
	settingsThinkingStandardBudget  = 1024
	settingsThinkingMaxBudget       = 32000
	settingsThinkingMaxOutputTokens = 32768
	settingsProbeTimeout            = 90 * time.Second
	maxWorkerCount                  = 8
	webMutationHeader               = "X-Go-Cli-Agent-Web"
	maxSkillUploadBytes             = 50 << 20
	maxSkillZipFiles                = 2048
	maxSkillZipEntryBytes           = 10 << 20
	maxSkillZipTotalBytes           = 100 << 20
	maxWebJSONBodyBytes             = 4 << 20
	sessionStartObservationTimeout  = 15 * time.Second
)

var (
	errWebServiceClosing    = errors.New("web service is closing")
	errSessionAlreadyActive = errors.New("session is already active in this web console")
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
	closed  bool

	pendingStartSeq int
	pendingStarts   map[int]context.CancelFunc

	launchWG sync.WaitGroup
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
	Goal                    *session.SessionGoal             `json:"goal,omitempty"`
	GoalFacts               *GoalFactsResponse               `json:"goal_facts,omitempty"`
	PlanMode                *session.PlanModeState           `json:"plan_mode,omitempty"`
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

type GoalFactsResponse struct {
	Coverage                  session.MissionPlanCoverage  `json:"coverage"`
	LatestHistory             *session.GoalHistoryEntry    `json:"latest_history,omitempty"`
	History                   []session.GoalHistoryEntry   `json:"history,omitempty"`
	Progress                  []session.GoalProgressRecord `json:"progress,omitempty"`
	LinkedChildSessionIDs     []string                     `json:"linked_child_session_ids,omitempty"`
	LinkedQueueJobIDs         []string                     `json:"linked_queue_job_ids,omitempty"`
	UnresolvedChildSessionIDs []string                     `json:"unresolved_child_session_ids,omitempty"`
	UnresolvedQueueJobIDs     []string                     `json:"unresolved_queue_job_ids,omitempty"`
	EvaluatorEvidenceCount    int                          `json:"evaluator_evidence_count"`
	LatestBlocker             string                       `json:"latest_blocker,omitempty"`
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
	MaxCount     int            `json:"max_count"`
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
		cfg:           serviceCfg,
		configPath:    opts.ConfigPath,
		store:         store,
		staticFS:      staticFS,
		handles:       map[string]*launchHandle{},
		pendingStarts: map[int]context.CancelFunc{},
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
	pendingCancels := make([]context.CancelFunc, 0, len(s.pendingStarts))
	for _, cancel := range s.pendingStarts {
		pendingCancels = append(pendingCancels, cancel)
	}
	s.closed = true
	s.handles = map[string]*launchHandle{}
	s.pendingStarts = map[int]context.CancelFunc{}
	s.mu.Unlock()
	for _, cancel := range pendingCancels {
		cancel()
	}
	for _, handle := range handles {
		handle.cancel()
		_ = s.recordLaunchHandleEvent(handle, "webconsole.handle.released", map[string]any{"reason": "service_close"})
	}
	s.launchWG.Wait()
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" || r.URL.Path == "/api/ws" {
		s.serveAPI(w, r)
		return
	}
	s.serveUI(w, r)
}

func (s *Service) serveAPI(w http.ResponseWriter, r *http.Request) {
	if err := guardUnsafeAPIRequest(r); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if isUnsafeMethod(r.Method) && expectsJSONBody(r.URL.Path) {
		r.Body = http.MaxBytesReader(w, r.Body, maxWebJSONBodyBytes)
	}
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
		serveEmbeddedFileRequest(w, r, s.staticFS, "index.html")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if _, err := fs.Stat(s.staticFS, name); err == nil {
		serveEmbeddedFileRequest(w, r, s.staticFS, name)
		return
	}
	serveEmbeddedFileRequest(w, r, s.staticFS, "index.html")
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
		WorkspaceSwitchSupported: false,
		DefaultMode:              cfg.Runtime.Isolation.DefaultMode,
		QueuePollMS:              cfg.Runtime.Queue.PollIntervalMS,
		WorkerCount:              s.workers.Snapshot().DesiredCount,
		Capabilities:             []string{"start", "steer", "continue", "interrupt", "stop", "queue", "children", "tasks", "goals", "missions", "plan_mode"},
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
		if len(parts) == 3 && parts[1] == "planmode" {
			switch parts[2] {
			case "approve":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handlePlanModeApprove(w, r, sessionID)
			case "revise":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handlePlanModeRevise(w, r, sessionID)
			case "cancel":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handlePlanModeCancel(w, sessionID)
			case "input":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handlePlanModeInput(w, r, sessionID)
			default:
				writeError(w, http.StatusNotFound, errors.New("session route not found"))
			}
			return
		}
		if len(parts) == 3 && parts[1] == "goal" {
			switch parts[2] {
			case "complete":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handleGoalStatus(w, sessionID, session.GoalStatusComplete, "goal.completed")
			case "pause":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handleGoalStatus(w, sessionID, session.GoalStatusPaused, "goal.paused")
			case "resume":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handleGoalStatus(w, sessionID, session.GoalStatusActive, "goal.resumed")
			default:
				writeError(w, http.StatusNotFound, errors.New("session route not found"))
			}
			return
		}
		if len(parts) == 3 && parts[1] == "mission" {
			switch parts[2] {
			case "plan":
				if r.Method != http.MethodPatch {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handleMissionPlanPatch(w, r, sessionID)
			case "validation":
				if r.Method != http.MethodPatch {
					writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
					return
				}
				s.handleMissionValidationPatch(w, r, sessionID)
			default:
				writeError(w, http.StatusNotFound, errors.New("session route not found"))
			}
			return
		}
		if len(parts) == 4 && parts[1] == "mission" && parts[2] == "plan" && parts[3] == "approve" {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
				return
			}
			s.handleMissionPlanApprove(w, r, sessionID)
			return
		}
		writeError(w, http.StatusNotFound, errors.New("session route not found"))
		return
	}
	switch parts[1] {
	case "planmode":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		s.handlePlanModeGet(w, sessionID)
	case "goal":
		switch r.Method {
		case http.MethodGet:
			s.handleGoalGet(w, sessionID)
		case http.MethodPost:
			s.handleGoalCreate(w, r, sessionID)
		case http.MethodPatch:
			s.handleGoalPatch(w, r, sessionID)
		case http.MethodDelete:
			s.handleGoalClear(w, sessionID)
		default:
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		}
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
	hasRunningSessions, err := s.hasRunningSessions("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if hasRunningSessions {
		writeError(w, http.StatusConflict, errors.New("cannot clear history while sessions are still running"))
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
	var goalPtr *session.SessionGoal
	var goalFacts *GoalFactsResponse
	if goal, err := s.store.LoadGoal(sessionID); err == nil && goal.GoalID != "" {
		goalPtr = &goal
		goalFacts = s.goalFacts(sessionID, goal, children, background)
	}
	var planModePtr *session.PlanModeState
	if planMode, err := s.store.LoadPlanMode(sessionID); err == nil && planMode.PlanModeID != "" {
		planModePtr = &planMode
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
		Goal:                    goalPtr,
		GoalFacts:               goalFacts,
		PlanMode:                planModePtr,
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

func (s *Service) handleGoalGet(w http.ResponseWriter, sessionID string) {
	goal, err := s.store.LoadGoal(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, goal)
}

func (s *Service) handleGoalCreate(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req GoalDraftRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Enabled = true
	draft, err := goalDraftFromWebRequest(&req, session.GoalSourceWeb)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	goal, err := s.store.CreateGoal(sessionID, *draft)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "already has a current goal") {
			status = http.StatusConflict
		} else if isGoalClientError(err) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	if planMode, created, err := s.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceWeb); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if created {
		_ = s.store.AppendEvent(sessionID, events.New(sessionID, "planmode.created", "goal", map[string]any{
			"plan_mode_id":   planMode.PlanModeID,
			"status":         planMode.Status,
			"linked_goal_id": planMode.LinkedGoalID,
		}))
	}
	if err := s.store.AppendEvent(sessionID, events.New(sessionID, "goal.created", "goal", webGoalEventData(goal))); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, goal)
}

func (s *Service) handleGoalPatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req GoalPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, err := s.store.LoadGoal(sessionID)
	if err != nil {
		writeError(w, goalStoreStatus(err), err)
		return
	}
	var mission *session.MissionPlan
	if req.Mission != nil {
		wasApproved := current.Mission != nil && session.NormalizeMissionPlanStatus(current.Mission.PlanStatus) == session.MissionPlanStatusApproved
		missionCopy := *req.Mission
		if err := rejectMissionPlanApprovalByPatch(missionCopy.PlanStatus); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		missionCopy.PlanStatus = session.NormalizeMissionPlanStatus(missionCopy.PlanStatus)
		if wasApproved && missionCopy.PlanStatus != session.MissionPlanStatusNeedsApproval {
			missionCopy.PlanStatus = session.MissionPlanStatusNeedsApproval
		}
		if missionCopy.PlanStatus != session.MissionPlanStatusApproved {
			missionCopy.ApprovedAt = ""
		}
		mission = &missionCopy
	}
	goal, err := s.store.PatchGoal(sessionID, session.GoalPatchInput{
		SuccessCriteria: optionalGoalCriteria(req.SuccessCriteria),
		ValidationPlan:  optionalGoalValidations(req.ValidationPlan),
		Control:         req.Control,
		Mission:         mission,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	createdTasks := []session.Task{}
	if goal.Mission != nil && goal.Mission.CreateTasksFromPlan {
		syncedGoal, tasks, _, err := s.store.SyncMissionPlanTasks(sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		goal = syncedGoal
		createdTasks = tasks
	}
	if session.GoalRequiresPlanApproval(goal) {
		planMode, created, err := s.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceWeb)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if created {
			_ = s.store.AppendEvent(sessionID, events.New(sessionID, "planmode.created", "goal", map[string]any{
				"plan_mode_id":   planMode.PlanModeID,
				"status":         planMode.Status,
				"linked_goal_id": planMode.LinkedGoalID,
			}))
		}
	}
	if err := s.appendGoalMutation(sessionID, goal, "goal.updated", map[string]any{
		"created_task_ids": webTaskIDs(createdTasks),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, goal)
}

func (s *Service) handleGoalClear(w http.ResponseWriter, sessionID string) {
	goal, err := s.store.LoadGoal(sessionID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	cleared, err := s.store.ClearGoal(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if cleared {
		_ = s.store.AppendGoalHistory(sessionID, session.GoalHistoryEntry{
			GoalID: goal.GoalID,
			Type:   "goal.cleared",
			Source: session.GoalSourceWeb,
			Status: session.GoalStatusCleared,
			Data: map[string]any{
				"previous_status": goal.Status,
			},
		})
		_ = s.store.AppendEvent(sessionID, events.New(sessionID, "goal.cleared", "goal", map[string]any{
			"goal_id":         goal.GoalID,
			"previous_status": goal.Status,
		}))
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "cleared": cleared})
}

func (s *Service) handleGoalStatus(w http.ResponseWriter, sessionID, status, eventType string) {
	goal, err := s.store.SetGoalStatus(sessionID, status, session.GoalSourceWeb)
	if err != nil {
		writeError(w, goalStoreStatus(err), err)
		return
	}
	if err := s.appendGoalMutation(sessionID, goal, eventType, nil); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, goal)
}

func (s *Service) handleMissionPlanPatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req MissionPlanPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, err := s.store.LoadGoal(sessionID)
	if err != nil {
		writeError(w, goalStoreStatus(err), err)
		return
	}
	mission := ensureMissionPlan(current.Mission)
	approvalScopedPatch := missionPlanPatchTouchesApprovalScopedContent(req)
	if req.Requirements != nil {
		mission.Requirements = append([]session.MissionRequirement(nil), req.Requirements...)
	}
	if req.Features != nil {
		mission.Features = append([]session.MissionFeature(nil), req.Features...)
	}
	if req.Milestones != nil {
		mission.Milestones = append([]session.MissionMilestone(nil), req.Milestones...)
	}
	if req.ValidationContract != nil {
		mission.ValidationContract = append([]session.GoalValidation(nil), req.ValidationContract...)
	}
	if req.RolePlan != nil {
		mission.RolePlan = s.resolveMissionRolePlan(sessionID, req.RolePlan)
	}
	if req.SharedArtifacts != nil {
		mission.SharedArtifacts = append([]string(nil), req.SharedArtifacts...)
	}
	if req.KnowledgeArtifacts != nil {
		mission.KnowledgeArtifacts = append([]string(nil), req.KnowledgeArtifacts...)
	}
	if strings.TrimSpace(req.PlanStatus) != "" {
		status, err := normalizeMissionPlanPatchStatus(req.PlanStatus)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		mission.PlanStatus = status
		mission.ApprovedAt = ""
	} else if approvalScopedPatch && session.NormalizeMissionPlanStatus(mission.PlanStatus) == session.MissionPlanStatusApproved {
		mission.PlanStatus = session.MissionPlanStatusNeedsApproval
		mission.ApprovedAt = ""
	}
	if req.CreateTasksFromPlan != nil {
		mission.CreateTasksFromPlan = *req.CreateTasksFromPlan
	}
	goal, err := s.store.PatchGoal(sessionID, session.GoalPatchInput{Mission: mission})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	createdTasks := []session.Task{}
	if mission.CreateTasksFromPlan {
		syncedGoal, tasks, _, err := s.store.SyncMissionPlanTasks(sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		goal = syncedGoal
		createdTasks = tasks
		mission = ensureMissionPlan(goal.Mission)
	}
	planModeCreated := false
	if session.GoalRequiresPlanApproval(goal) {
		planMode, created, err := s.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceWeb)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		planModeCreated = created
		if created {
			_ = s.store.AppendEvent(sessionID, events.New(sessionID, "planmode.created", "goal", map[string]any{
				"plan_mode_id":   planMode.PlanModeID,
				"status":         planMode.Status,
				"linked_goal_id": planMode.LinkedGoalID,
			}))
		}
	}
	if err := s.appendGoalMutation(sessionID, goal, "mission.plan.updated", map[string]any{
		"plan_status":       mission.PlanStatus,
		"created_task_ids":  webTaskIDs(createdTasks),
		"plan_mode_created": planModeCreated,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, goal)
}

func (s *Service) handleMissionPlanApprove(w http.ResponseWriter, r *http.Request, sessionID string) {
	req, ok := decodeOptionalMissionPlanApproveRequest(w, r)
	if !ok {
		return
	}
	goal, err := s.store.LoadGoal(sessionID)
	if err != nil {
		writeError(w, goalStoreStatus(err), err)
		return
	}
	goal.Mode = session.GoalModeMission
	mission := ensureMissionPlan(goal.Mission)
	if err := ensureWebMissionCoverage(goal, req.OverrideCoverage); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if planMode, err := s.store.LoadPlanMode(sessionID); err == nil && planMode.Enabled && planMode.LinkedGoalID == goal.GoalID {
		switch planMode.Status {
		case session.PlanModeStatusAwaitingApproval, session.PlanModeStatusApproved:
			if s.hasActiveHandle(sessionID) {
				writeError(w, http.StatusConflict, errors.New("session is already active in this web console"))
				return
			}
			if err := s.launchPlanModeContinue(sessionID, runtime.ContinueRequest{
				SessionID:            sessionID,
				ApprovePlan:          true,
				OverrideGoalCoverage: req.OverrideCoverage,
				Source:               session.PlanModeSourceWeb,
			}); err != nil {
				writeError(w, planModeActionStatus(err), err)
				return
			}
			writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
			return
		case session.PlanModeStatusPlanning, session.PlanModeStatusAwaitingUserInput:
			writeError(w, http.StatusConflict, errors.New("linked Plan Mode is not awaiting approval; submit the plan before approving the mission plan"))
			return
		case session.PlanModeStatusExecuting:
			approvedAt := mission.ApprovedAt
			if approvedAt == "" {
				approvedAt = nowString()
			}
			goal, err = s.store.ApproveMissionPlan(sessionID, session.MissionPlanApprovalInput{
				Source:           session.GoalSourceWeb,
				ApprovedAt:       approvedAt,
				CoverageOverride: req.OverrideCoverage,
				PlanModeID:       planMode.PlanModeID,
				ApprovedVersion:  planMode.ApprovedVersion,
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if err := s.appendGoalEvent(sessionID, goal, "mission.plan.approved", map[string]any{
				"approved_at":       approvedAt,
				"plan_mode_id":      planMode.PlanModeID,
				"approved_version":  planMode.ApprovedVersion,
				"coverage_override": req.OverrideCoverage,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, goal)
			return
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if session.GoalRequiresPlanApproval(goal) {
		planMode, created, err := s.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceWeb)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if created {
			_ = s.store.AppendEvent(sessionID, events.New(sessionID, "planmode.created", "goal", map[string]any{
				"plan_mode_id":   planMode.PlanModeID,
				"status":         planMode.Status,
				"linked_goal_id": planMode.LinkedGoalID,
			}))
		}
		writeError(w, http.StatusConflict, errors.New("linked Plan Mode is not awaiting approval; submit the plan before approving the mission plan"))
		return
	}
	approvedAt := nowString()
	goal, err = s.store.ApproveMissionPlan(sessionID, session.MissionPlanApprovalInput{
		Source:           session.GoalSourceWeb,
		ApprovedAt:       approvedAt,
		CoverageOverride: req.OverrideCoverage,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.appendGoalEvent(sessionID, goal, "mission.plan.approved", map[string]any{
		"approved_at":       approvedAt,
		"coverage_override": req.OverrideCoverage,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, goal)
}

func (s *Service) handleMissionValidationPatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req struct {
		ValidationPlan     []session.GoalValidation `json:"validation_plan,omitempty"`
		ValidationContract []session.GoalValidation `json:"validation_contract,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	goal, err := s.store.LoadGoal(sessionID)
	if err != nil {
		writeError(w, goalStoreStatus(err), err)
		return
	}
	if req.ValidationPlan != nil {
		goal.ValidationPlan = append([]session.GoalValidation(nil), req.ValidationPlan...)
	}
	if req.ValidationContract != nil {
		goal.Mode = session.GoalModeMission
		mission := ensureMissionPlan(goal.Mission)
		mission.ValidationContract = append([]session.GoalValidation(nil), req.ValidationContract...)
		if session.NormalizeMissionPlanStatus(mission.PlanStatus) == session.MissionPlanStatusApproved {
			mission.PlanStatus = session.MissionPlanStatusNeedsApproval
			mission.ApprovedAt = ""
		}
		goal.Mission = mission
	}
	if err := s.store.SaveGoal(sessionID, goal); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planModeCreated := false
	if req.ValidationContract != nil && session.GoalRequiresPlanApproval(goal) {
		planMode, created, err := s.store.EnsurePlanModeForGoal(sessionID, goal, session.PlanModeSourceWeb)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		planModeCreated = created
		if created {
			_ = s.store.AppendEvent(sessionID, events.New(sessionID, "planmode.created", "goal", map[string]any{
				"plan_mode_id":   planMode.PlanModeID,
				"status":         planMode.Status,
				"linked_goal_id": planMode.LinkedGoalID,
			}))
		}
	}
	if err := s.appendGoalMutation(sessionID, goal, "mission.validation.updated", map[string]any{
		"plan_mode_created": planModeCreated,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, goal)
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
	if err := s.validateProviderOverride(req.Provider); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	goalDraft, err := goalDraftFromWebStartRequest(req.Goal, req.Prompt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planModeDraft := planModeDraftFromWebRequest(req.PlanMode, req.Prompt)
	resp, err := s.startSession(runtime.StartRequest{
		Prompt:         req.Prompt,
		AgentName:      req.AgentName,
		AgentRole:      req.AgentRole,
		Provider:       req.Provider,
		Model:          req.Model,
		Workdir:        req.Workdir,
		Mode:           req.Mode,
		SystemOverride: req.SystemOverride,
		Goal:           goalDraft,
		PlanMode:       planModeDraft,
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
	var configErr *runtime.ConfigError
	if errors.As(err, &configErr) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "unsupported agent role") ||
		strings.Contains(message, "isolation target must not be inside source workdir") ||
		strings.Contains(message, "unknown provider")
}

func (s *Service) validateProviderOverride(providerName string) error {
	cfg, err := s.configSnapshot()
	if err != nil {
		return err
	}
	return validateProviderOverrideInConfig(cfg, providerName)
}

func validateProviderOverrideInConfig(cfg *config.Config, providerName string) error {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" || strings.EqualFold(providerName, "default") {
		return nil
	}
	if _, err := cfg.ProviderConfig(providerName); err != nil {
		return newWebError(
			errorCodeUnknownProvider,
			"unknown provider",
			err.Error(),
			"choose one of the configured providers",
		)
	}
	return nil
}

func (s *Service) startSession(req runtime.StartRequest) (LaunchResponse, error) {
	cfg, err := s.configSnapshot()
	if err != nil {
		return LaunchResponse{}, err
	}
	runner := runtime.NewRunner(cfg)
	sub := runner.Bus().Subscribe(32)
	runCtx, cancel := context.WithCancel(context.Background())
	pendingStartID, err := s.registerPendingStart(cancel)
	if err != nil {
		cancel()
		return LaunchResponse{}, err
	}
	outcomeCh := make(chan launchOutcome, 1)
	s.trackLaunch(func() {
		result, err := runner.Start(runCtx, req)
		outcomeCh <- launchOutcome{result: result, err: err}
	})

	sessionID, early, err := waitForSessionID(sub, outcomeCh)
	if err != nil {
		s.removePendingStart(pendingStartID)
		cancel()
		return LaunchResponse{}, err
	}
	handle := newLaunchHandle(sessionID, runner, cancel)
	if err := s.promotePendingStart(pendingStartID, handle); err != nil {
		cancel()
		return LaunchResponse{}, err
	}
	if early != nil {
		s.trackLaunch(func() {
			s.finishHandle(handle, *early)
		})
	} else {
		s.trackLaunch(func() {
			s.finishHandle(handle, <-outcomeCh)
		})
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
	if err := validateProviderOverrideInConfig(cfg, req.Provider); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runner := runtime.NewRunner(cfg)
	runCtx, cancel := context.WithCancel(context.Background())
	handle := newLaunchHandle(sessionID, runner, cancel)
	if err := s.addHandle(handle); err != nil {
		cancel()
		status := http.StatusServiceUnavailable
		if errors.Is(err, errSessionAlreadyActive) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	s.trackLaunch(func() {
		result, err := runner.Continue(runCtx, runtime.ContinueRequest{
			SessionID:      sessionID,
			Message:        req.Message,
			Provider:       req.Provider,
			Model:          req.Model,
			SystemOverride: req.SystemOverride,
			PlanMode:       planModeDraftFromWebRequest(req.PlanMode, req.Message),
			Source:         session.PlanModeSourceWeb,
		})
		s.finishHandle(handle, launchOutcome{result: result, err: err})
	})
	writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
}

func (s *Service) trackLaunch(fn func()) {
	s.launchWG.Add(1)
	go func() {
		defer s.launchWG.Done()
		fn()
	}()
}

func (s *Service) handlePlanModeGet(w http.ResponseWriter, sessionID string) {
	planMode, err := s.store.LoadPlanMode(sessionID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, planMode)
}

func (s *Service) handlePlanModeApprove(w http.ResponseWriter, r *http.Request, sessionID string) {
	req, ok := decodeOptionalMissionPlanApproveRequest(w, r)
	if !ok {
		return
	}
	if s.hasActiveHandle(sessionID) {
		writeError(w, http.StatusConflict, errors.New("session is already active in this web console"))
		return
	}
	if err := s.ensurePlanModeApprovalPreflight(sessionID, req.OverrideCoverage); err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	if err := s.launchPlanModeContinue(sessionID, runtime.ContinueRequest{
		SessionID:            sessionID,
		ApprovePlan:          true,
		OverrideGoalCoverage: req.OverrideCoverage,
		Source:               session.PlanModeSourceWeb,
	}); err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
}

func (s *Service) handlePlanModeRevise(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req PlanModeReviseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, errors.New("revision message is required"))
		return
	}
	if s.hasActiveHandle(sessionID) {
		writeError(w, http.StatusConflict, errors.New("session is already active in this web console"))
		return
	}
	if err := s.launchPlanModeContinue(sessionID, runtime.ContinueRequest{
		SessionID: sessionID,
		Message:   strings.TrimSpace(req.Message),
		Source:    session.PlanModeSourceWeb,
	}); err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
}

func (s *Service) handlePlanModeCancel(w http.ResponseWriter, sessionID string) {
	if handle, ok := s.handleForSession(sessionID); ok {
		planMode, err := s.store.LoadPlanMode(sessionID)
		if err != nil {
			writeError(w, planModeActionStatus(err), err)
			return
		}
		if planMode.PendingRequest == nil {
			writeError(w, http.StatusConflict, errors.New("session is already active in this web console"))
			return
		}
		if !handle.runner.CancelActivePlanInput(sessionID, planMode.PendingRequest.RequestID) {
			writeError(w, http.StatusConflict, errors.New("plan input request is not waiting in this web console"))
			return
		}
		writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
		return
	}
	cfg, err := s.configSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runner := runtime.NewRunner(cfg)
	result, err := runner.Continue(context.Background(), runtime.ContinueRequest{
		SessionID:  sessionID,
		CancelPlan: true,
		Source:     session.PlanModeSourceWeb,
	})
	if err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handlePlanModeInput(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req PlanModeInputRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Answers) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("answers are required"))
		return
	}
	if strings.TrimSpace(req.RequestID) == "" {
		writeError(w, http.StatusBadRequest, errors.New("request_id is required"))
		return
	}
	planMode, err := s.store.LoadPlanMode(sessionID)
	if err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	if planMode.PendingRequest == nil {
		writeError(w, http.StatusConflict, errors.New("plan mode has no pending input request"))
		return
	}
	pendingRequest := *planMode.PendingRequest
	if pendingRequest.RequestID != req.RequestID {
		writeError(w, http.StatusConflict, fmt.Errorf("plan input request mismatch: %s", req.RequestID))
		return
	}
	if err := session.ValidatePlanModeAnswers(pendingRequest, req.Answers); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if handle, ok := s.handleForSession(sessionID); ok {
		if handle.runner.AnswerActivePlanInput(sessionID, req.RequestID, req.Answers) {
			writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
			return
		}
	}
	if err := s.launchPlanModeContinue(sessionID, runtime.ContinueRequest{
		SessionID:          sessionID,
		PlanInputRequestID: req.RequestID,
		PlanInputAnswers:   req.Answers,
		Source:             session.PlanModeSourceWeb,
	}); err != nil {
		writeError(w, planModeActionStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, LaunchResponse{SessionID: sessionID, Status: "accepted"})
}

func (s *Service) launchPlanModeContinue(sessionID string, req runtime.ContinueRequest) error {
	state, err := s.store.LoadState(sessionID)
	if err != nil {
		return err
	}
	switch state.Status {
	case session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed:
	default:
		return newWebError(errorCodeSessionNotResumable, "session is not resumable", "only paused, awaiting_input, and failed sessions can be continued", "wait for the active run or choose another action")
	}
	cfg, err := s.configSnapshot()
	if err != nil {
		return err
	}
	runner := runtime.NewRunner(cfg)
	runCtx, cancel := context.WithCancel(context.Background())
	handle := newLaunchHandle(sessionID, runner, cancel)
	if err := s.addHandle(handle); err != nil {
		cancel()
		return err
	}
	s.trackLaunch(func() {
		result, err := runner.Continue(runCtx, req)
		s.finishHandle(handle, launchOutcome{result: result, err: err})
	})
	return nil
}

func (s *Service) ensurePlanModeApprovalPreflight(sessionID string, overrideCoverage bool) error {
	planMode, err := s.store.LoadPlanMode(sessionID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(planMode.LinkedGoalID) == "" {
		return nil
	}
	goal, err := s.store.LoadGoal(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if goal.GoalID != planMode.LinkedGoalID || goal.Mission == nil {
		return nil
	}
	return ensureWebMissionCoverage(goal, overrideCoverage)
}

func planModeActionStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	if errors.Is(err, errSessionAlreadyActive) {
		return http.StatusConflict
	}
	var webErr webError
	if errors.As(err, &webErr) {
		return http.StatusConflict
	}
	message := err.Error()
	if strings.Contains(message, "not resumable") ||
		strings.Contains(message, "not awaiting") ||
		strings.Contains(message, "no pending") ||
		strings.Contains(message, "coverage blocks approval") {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
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
	if req.DesiredCount > maxWorkerCount {
		writeError(w, http.StatusBadRequest, fmt.Errorf("desired_count must be <= %d", maxWorkerCount))
		return
	}
	s.workers.Scale(req.DesiredCount)
	writeJSON(w, http.StatusAccepted, s.workers.Snapshot())
}

func (s *Service) addHandle(handle *launchHandle) error {
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
	if s.closed {
		s.mu.Unlock()
		return errWebServiceClosing
	}
	if _, exists := s.handles[handle.sessionID]; exists {
		s.mu.Unlock()
		return errSessionAlreadyActive
	}
	s.handles[handle.sessionID] = handle
	s.mu.Unlock()
	_ = s.recordLaunchHandleEvent(handle, "webconsole.handle.acquired", nil)
	return nil
}

func (s *Service) registerPendingStart(cancel context.CancelFunc) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("web service is closing")
	}
	s.pendingStartSeq++
	id := s.pendingStartSeq
	s.pendingStarts[id] = cancel
	return id, nil
}

func (s *Service) removePendingStart(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pendingStarts, id)
}

func (s *Service) promotePendingStart(id int, handle *launchHandle) error {
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
	if s.closed {
		delete(s.pendingStarts, id)
		s.mu.Unlock()
		return errWebServiceClosing
	}
	if _, exists := s.handles[handle.sessionID]; exists {
		delete(s.pendingStarts, id)
		s.mu.Unlock()
		return errSessionAlreadyActive
	}
	delete(s.pendingStarts, id)
	s.handles[handle.sessionID] = handle
	s.mu.Unlock()
	_ = s.recordLaunchHandleEvent(handle, "webconsole.handle.acquired", nil)
	return nil
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
	if current, ok := s.handles[handle.sessionID]; ok && current == handle {
		delete(s.handles, handle.sessionID)
	}
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
	if webFileBrowserPathDenied(browseRoot, target) {
		writeError(w, http.StatusForbidden, errors.New("access denied"))
		return
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
	if webFileBrowserPathDenied(browseRoot, fullPath) {
		writeError(w, http.StatusForbidden, errors.New("access denied"))
		return
	}
	content, _, err := fileutil.ReadRegularFileNoSymlink(fullPath)
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
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("default workspace must not be a symlink: %s", root)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("default workspace path is not a directory: %s", root)
		}
	} else if os.IsNotExist(err) {
		if err := fileutil.MkdirAllNoSymlink(root, 0o700); err != nil {
			return "", err
		}
	} else {
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
		"role_providers":          roleProviderOverridesResponse(cfg),
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
	if req.RoleProviders != nil {
		roleProviders, err := roleProvidersFromRequest(updatedCfg, req.RoleProviders)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		updatedCfg.RoleProviders = roleProviders
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
		if req.BaseURL != nil {
			p.BaseURL = strings.TrimSpace(*req.BaseURL)
		}
		if req.Model != nil {
			p.Model = strings.TrimSpace(*req.Model)
		}
		if req.APIProvider != nil {
			p.APIProvider = strings.TrimSpace(*req.APIProvider)
		}
		if req.ReasoningMode != nil && strings.TrimSpace(*req.ReasoningMode) != "" {
			if err := applyProviderReasoningMode(req.Provider, &p, *req.ReasoningMode); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		if req.ReasoningSummary != nil && strings.TrimSpace(*req.ReasoningSummary) != "" {
			if err := applyProviderReasoningSummary(req.Provider, &p, *req.ReasoningSummary); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		if _, err := config.EffectiveAPIProvider(req.Provider, p); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.APIKey != nil && *req.APIKey != "" && *req.APIKey != maskedAPIKey {
			if err := os.Setenv(p.APIKeyEnv, *req.APIKey); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			cwd, _ := os.Getwd()
			envPath := config.DefaultEnvFilePath(cwd)
			if err := config.UpsertEnvFile(envPath, p.APIKeyEnv, *req.APIKey); err != nil {
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
	s.workers.UpdateConfig(updatedCfg)
	if err := s.appendAuditEvent("web.config.write", map[string]any{
		"provider":               updatedCfg.DefaultProvider,
		"config_path":            configPath,
		"guardrails_mode":        updatedCfg.Runtime.GuardrailsMode,
		"max_turns_hard":         updatedCfg.Runtime.MaxTurnsHard,
		"hard_turn_limit_active": updatedCfg.Runtime.MaxTurnsHard > 0,
		"api_provider":           updatedCfg.Providers[updatedCfg.DefaultProvider].APIProvider,
		"reasoning_mode":         providerReasoningMode(updatedCfg.DefaultProvider, updatedCfg.Providers[updatedCfg.DefaultProvider]),
		"reasoning_summary":      providerReasoningSummary(updatedCfg.Providers[updatedCfg.DefaultProvider]),
		"role_provider_count":    roleProviderOverrideCount(updatedCfg.RoleProviders),
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

func roleProviderOverridesResponse(cfg *config.Config) map[string]any {
	out := map[string]any{}
	for _, role := range []string{"planner", "generator", "evaluator"} {
		override := cfg.RoleProviderOverride(role)
		out[role] = map[string]any{
			"provider":     strings.TrimSpace(override.Provider),
			"api_provider": strings.TrimSpace(override.APIProvider),
			"base_url":     strings.TrimSpace(override.BaseURL),
			"model":        strings.TrimSpace(override.Model),
		}
	}
	return out
}

func roleProvidersFromRequest(cfg *config.Config, req map[string]RoleProviderOverrideRequest) (config.RoleProvidersConfig, error) {
	out := cfg.RoleProviders
	for role, value := range req {
		override, err := roleProviderOverrideFromRequest(cfg, role, value)
		if err != nil {
			return out, err
		}
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "planner":
			out.Planner = override
		case "generator":
			out.Generator = override
		case "evaluator":
			out.Evaluator = override
		default:
			return out, fmt.Errorf("unsupported role provider override: %s", role)
		}
	}
	return out, nil
}

func roleProviderOverrideFromRequest(cfg *config.Config, role string, req RoleProviderOverrideRequest) (config.RoleProviderOverride, error) {
	providerName := strings.TrimSpace(req.Provider)
	apiProvider := strings.TrimSpace(req.APIProvider)
	baseURL := strings.TrimSpace(req.BaseURL)
	model := strings.TrimSpace(req.Model)
	if providerName != "" {
		if _, ok := cfg.Providers[providerName]; !ok {
			return config.RoleProviderOverride{}, newWebError(
				errorCodeUnknownProvider,
				"unknown role provider",
				"provider "+providerName+" is not configured",
				"choose one of the configured providers or leave the role provider blank",
			)
		}
	}
	if apiProvider != "" {
		providerCfg := config.Provider{APIProvider: apiProvider}
		if providerName != "" {
			providerCfg = cfg.Providers[providerName]
			providerCfg.APIProvider = apiProvider
		}
		if _, err := config.EffectiveAPIProvider(firstNonEmpty(providerName, "role-"+strings.TrimSpace(role)), providerCfg); err != nil {
			return config.RoleProviderOverride{}, err
		}
	}
	return config.RoleProviderOverride{
		Provider:    providerName,
		APIProvider: apiProvider,
		BaseURL:     baseURL,
		Model:       model,
	}, nil
}

func roleProviderOverrideCount(cfg config.RoleProvidersConfig) int {
	count := 0
	for _, override := range []config.RoleProviderOverride{cfg.Planner, cfg.Generator, cfg.Evaluator} {
		if strings.TrimSpace(override.Provider) != "" ||
			strings.TrimSpace(override.APIProvider) != "" ||
			strings.TrimSpace(override.BaseURL) != "" ||
			strings.TrimSpace(override.Model) != "" {
			count++
		}
	}
	return count
}

func (s *Service) resolveMissionRolePlan(sessionID string, input []session.MissionRole) []session.MissionRole {
	if len(input) == 0 {
		return nil
	}
	cfg, err := s.configSnapshot()
	if err != nil {
		return append([]session.MissionRole(nil), input...)
	}
	baseProvider := cfg.DefaultProvider
	if meta, err := s.store.LoadMetadata(sessionID); err == nil && strings.TrimSpace(meta.Provider) != "" {
		baseProvider = strings.TrimSpace(meta.Provider)
	}
	out := make([]session.MissionRole, 0, len(input))
	for _, item := range input {
		role := strings.TrimSpace(item.Role)
		if !isConfigurableMissionRole(role) {
			out = append(out, item)
			continue
		}
		override := cfg.RoleProviderOverride(role)
		providerName := firstNonEmpty(strings.TrimSpace(override.Provider), baseProvider)
		if strings.TrimSpace(item.Provider) == "" {
			item.Provider = providerName
		}
		if strings.TrimSpace(item.Model) == "" {
			item.Model = strings.TrimSpace(override.Model)
			if strings.TrimSpace(item.Model) == "" {
				if providerCfg, ok := cfg.Providers[providerName]; ok {
					item.Model = strings.TrimSpace(providerCfg.Model)
				}
			}
		}
		out = append(out, item)
	}
	return out
}

func isConfigurableMissionRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "planner", "generator", "evaluator":
		return true
	default:
		return false
	}
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
	if req.BaseURL != nil {
		p.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.Model != nil {
		p.Model = strings.TrimSpace(*req.Model)
	}
	if req.APIProvider != nil {
		p.APIProvider = strings.TrimSpace(*req.APIProvider)
	}
	if req.ReasoningMode != nil && strings.TrimSpace(*req.ReasoningMode) != "" {
		if err := applyProviderReasoningMode(providerName, &p, *req.ReasoningMode); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if req.ReasoningSummary != nil && strings.TrimSpace(*req.ReasoningSummary) != "" {
		if err := applyProviderReasoningSummary(providerName, &p, *req.ReasoningSummary); err != nil {
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
	if req.APIKey != nil && *req.APIKey != "" && *req.APIKey != maskedAPIKey {
		apiKeyEnv := fmt.Sprintf("GO_CLI_AGENT_SETTINGS_TEST_API_KEY_%d", time.Now().UnixNano())
		if err := os.Setenv(apiKeyEnv, *req.APIKey); err != nil {
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
			mdData, _, err := fileutil.ReadRegularFileNoSymlink(filepath.Join(skillDir, "SKILL.md"))
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
	globalDest, err = prepareSkillZipDestination(globalDest)
	if err != nil {
		return 0, err
	}
	if len(r.File) > maxSkillZipFiles {
		return 0, fmt.Errorf("skill zip has too many entries: %d > %d", len(r.File), maxSkillZipFiles)
	}
	var totalUncompressed uint64
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > uint64(maxSkillZipEntryBytes) {
			return 0, fmt.Errorf("skill zip entry too large: %s exceeds %d bytes", f.Name, maxSkillZipEntryBytes)
		}
		if totalUncompressed > uint64(maxSkillZipTotalBytes)-f.UncompressedSize64 {
			return 0, fmt.Errorf("skill zip uncompressed size exceeds %d bytes", maxSkillZipTotalBytes)
		}
		totalUncompressed += f.UncompressedSize64
	}

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
	extractedBytes := int64(0)

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
				data, err := readZipFileLimited(f, maxSkillZipEntryBytes)
				if err != nil {
					return extractedCount, err
				}
				targetDirName = extractSkillNameFromMd(data)
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
		if info, err := os.Lstat(targetPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return extractedCount, fmt.Errorf("refusing to replace symlinked skill directory: %s", targetPath)
		} else if err != nil && !os.IsNotExist(err) {
			return extractedCount, err
		}

		if err := fileutil.RemoveDirAllNoSymlink(targetPath); err != nil {
			return extractedCount, err
		}
		if err := fileutil.MkdirAllNoSymlink(targetPath, 0o755); err != nil {
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
				if err := fileutil.MkdirAllNoSymlink(outPath, f.Mode()); err != nil {
					return extractedCount, err
				}
				continue
			}
			if err := fileutil.MkdirAllNoSymlink(filepath.Dir(outPath), 0o755); err != nil {
				return extractedCount, err
			}

			data, err := readZipFileLimited(f, maxSkillZipEntryBytes)
			if err != nil {
				return extractedCount, err
			}
			extractedBytes += int64(len(data))
			if extractedBytes > maxSkillZipTotalBytes {
				return extractedCount, fmt.Errorf("skill zip uncompressed size exceeds %d bytes", maxSkillZipTotalBytes)
			}
			mode := f.Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := fileutil.AtomicWriteFileNoSymlink(outPath, data, mode); err != nil {
				return extractedCount, err
			}
		}
		extractedCount++
	}
	return extractedCount, nil
}

func readZipFileLimited(f *zip.File, limit int) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("skill zip entry too large: %s exceeds %d bytes", f.Name, limit)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readAllWithLimit(rc, int64(limit), fmt.Sprintf("skill zip entry too large: %s exceeds %d bytes", f.Name, limit))
}

func readAllWithLimit(reader io.Reader, limit int64, message string) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New(message)
	}
	return data, nil
}

func prepareSkillZipDestination(globalDest string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(globalDest))
	if cleaned == "" || cleaned == "." {
		return "", errors.New("skill destination is required")
	}
	if info, err := os.Lstat(cleaned); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("skill destination must not be a symlink: %s", cleaned)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("skill destination is not a directory: %s", cleaned)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	resolved := canonicalManagedPath(cleaned)
	if err := fileutil.MkdirAllNoSymlink(resolved, 0o755); err != nil {
		return "", err
	}
	if info, err := os.Lstat(resolved); err != nil {
		return "", err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("skill destination must not be a symlink: %s", resolved)
	} else if !info.IsDir() {
		return "", fmt.Errorf("skill destination is not a directory: %s", resolved)
	}
	return resolved, nil
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
	r.Body = http.MaxBytesReader(w, r.Body, maxSkillUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer file.Close()

	dest, err := resolveManagedSkillDir(cfg.Skills.Dirs[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := fileutil.MkdirAllNoSymlink(dest, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	tmpFile, err := os.CreateTemp("", "skill-upload-*.zip")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(tmpFile.Name())
	written, err := io.Copy(tmpFile, io.LimitReader(file, maxSkillUploadBytes+1))
	if err != nil {
		_ = tmpFile.Close()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if written > maxSkillUploadBytes {
		_ = tmpFile.Close()
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("skill upload exceeds %d bytes", maxSkillUploadBytes))
		return
	}
	if err := tmpFile.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

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
	if len(parts) != 5 || parts[1] != "api" || parts[2] != "skills" || parts[4] != "uninstall" {
		writeError(w, http.StatusBadRequest, errors.New("invalid path format"))
		return
	}
	skillID := parts[3]
	if strings.TrimSpace(skillID) == "" || sanitizeDirName(skillID) != skillID {
		writeError(w, http.StatusBadRequest, errors.New("invalid skill id"))
		return
	}

	rootDir, err := resolveManagedSkillDir(cfg.Skills.Dirs[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	targetDir := filepath.Join(rootDir, skillID)
	if !pathWithinRoot(rootDir, targetDir) {
		writeError(w, http.StatusForbidden, errors.New("access denied"))
		return
	}
	if filepath.Dir(targetDir) != rootDir {
		writeError(w, http.StatusForbidden, errors.New("skill must be a direct child of the skill root"))
		return
	}
	if _, err := os.Stat(filepath.Join(targetDir, "SKILL.md")); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("target is not an installed skill"))
		return
	}

	if err := fileutil.RemoveDirAllNoSymlink(targetDir); err != nil {
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
		if entry.Name() == "node_modules" || webFileBrowserNameDenied(entry.Name()) {
			continue
		}
		fullPath := filepath.Join(current, entry.Name())
		if webFileBrowserPathDenied(browseRoot, fullPath) {
			continue
		}
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

func webFileBrowserPathDenied(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == "" {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if webFileBrowserNameDenied(part) {
			return true
		}
	}
	return false
}

func webFileBrowserNameDenied(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	switch name {
	case ".git", ".go-cli-agent", ".ssh", ".aws", ".gnupg", ".kube", ".docker",
		"id_rsa", "id_ed25519", "credentials":
		return true
	case ".env":
		return true
	case ".env.example", ".env.sample", ".env.template":
		return false
	default:
		return strings.HasPrefix(name, ".env.")
	}
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
		if err != nil || currentProcessHandleCanBePruned(state.Status) {
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

func currentProcessHandleCanBePruned(status string) bool {
	return status == session.StatusCompleted || status == session.StatusFailed
}

func (s *Service) ensureSessionTreeNotLive(sessionID string) error {
	hasRunningSessions, err := s.hasRunningSessions(sessionID)
	if err != nil {
		return err
	}
	if hasRunningSessions {
		return errors.New("cannot delete a running session tree")
	}
	hasRunningJobs, err := s.hasRunningQueueJobs(sessionID)
	if err != nil {
		return err
	}
	if hasRunningJobs {
		return errors.New("cannot delete a running session tree")
	}
	return nil
}

func (s *Service) hasRunningSessions(sessionID string) (bool, error) {
	items, _, err := s.store.ListPage(1000000, 0)
	if err != nil {
		return false, err
	}
	targets := map[string]struct{}{}
	if strings.TrimSpace(sessionID) != "" {
		targets[sessionID] = struct{}{}
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
				if _, ok := targets[item.RootSessionID]; ok {
					targets[item.ID] = struct{}{}
					changed = true
				}
			}
		}
	}
	for _, item := range items {
		if len(targets) > 0 {
			if _, ok := targets[item.ID]; !ok {
				continue
			}
		}
		state, err := s.store.LoadState(item.ID)
		if err != nil {
			continue
		}
		if state.Status == session.StatusRunning {
			return true, nil
		}
	}
	return false, nil
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
	return waitForSessionIDWithTimeout(sub, outcomeCh, sessionStartObservationTimeout)
}

func waitForSessionIDWithTimeout(sub <-chan events.Event, outcomeCh <-chan launchOutcome, timeoutDuration time.Duration) (string, *launchOutcome, error) {
	timeout := time.NewTimer(timeoutDuration)
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

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func goalDraftFromWebRequest(req *GoalDraftRequest, source string) (*session.GoalDraft, error) {
	if req == nil {
		return nil, nil
	}
	if !req.Enabled {
		return nil, nil
	}
	objective := strings.TrimSpace(req.Objective)
	if objective == "" {
		return nil, errors.New("goal objective is required")
	}
	var tokenBudget *int64
	if req.TokenBudget != nil {
		if *req.TokenBudget <= 0 {
			return nil, errors.New("goal token budget must be positive")
		}
		value := *req.TokenBudget
		tokenBudget = &value
	}
	var timeBudgetSeconds *int64
	if req.TimeBudgetMinutes != nil {
		if *req.TimeBudgetMinutes <= 0 {
			return nil, errors.New("goal time budget must be positive")
		}
		value := *req.TimeBudgetMinutes * 60
		timeBudgetSeconds = &value
	}
	return &session.GoalDraft{
		Enabled:                   true,
		Mode:                      req.Mode,
		Objective:                 objective,
		SuccessCriteria:           append([]string(nil), req.SuccessCriteria...),
		ValidationPlan:            append([]string(nil), req.ValidationPlan...),
		TokenBudget:               tokenBudget,
		TimeBudgetSeconds:         timeBudgetSeconds,
		Autonomy:                  req.Autonomy,
		RequirePlanApproval:       req.RequirePlanApproval,
		StopOnBudget:              req.StopOnBudget,
		CreateTasksFromPlan:       req.CreateTasksFromPlan,
		Features:                  append([]string(nil), req.Features...),
		Milestones:                append([]string(nil), req.Milestones...),
		Source:                    source,
		AskBeforeLargeChanges:     req.AskBeforeLargeChanges,
		AskBeforeDependencyChange: req.AskBeforeDependencyChange,
	}, nil
}

func isGoalClientError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "goal objective") ||
		strings.Contains(text, "objective exceeds") ||
		strings.Contains(text, "goal token budget") ||
		strings.Contains(text, "goal time budget") ||
		strings.Contains(text, "invalid goal mode") ||
		strings.Contains(text, "invalid goal status")
}

func goalDraftFromWebStartRequest(req *GoalDraftRequest, prompt string) (*session.GoalDraft, error) {
	if req == nil || !req.Enabled {
		return nil, nil
	}
	draftReq := *req
	if strings.TrimSpace(draftReq.Objective) == "" {
		draftReq.Objective = prompt
	}
	if strings.TrimSpace(draftReq.Mode) == "" {
		draftReq.Mode = session.GoalModeGoal
	}
	return goalDraftFromWebRequest(&draftReq, session.GoalSourceWeb)
}

func planModeDraftFromWebRequest(req *PlanModeDraftRequest, fallbackObjective string) *session.PlanModeDraft {
	if req == nil || !req.Enabled {
		return nil
	}
	objective := strings.TrimSpace(req.Objective)
	if objective == "" {
		objective = strings.TrimSpace(fallbackObjective)
	}
	return &session.PlanModeDraft{
		Enabled:   true,
		Objective: objective,
		Source:    session.PlanModeSourceWeb,
	}
}

func goalStoreStatus(err error) int {
	if errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	if isGoalClientError(err) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func ensureMissionPlan(plan *session.MissionPlan) *session.MissionPlan {
	if plan == nil {
		return &session.MissionPlan{PlanStatus: session.MissionPlanStatusDraft}
	}
	copyPlan := *plan
	copyPlan.PlanStatus = session.NormalizeMissionPlanStatus(copyPlan.PlanStatus)
	return &copyPlan
}

func normalizeMissionPlanPatchStatus(value string) (string, error) {
	status := session.NormalizeMissionPlanStatus(value)
	if !session.IsMissionPlanStatus(status) {
		return "", fmt.Errorf("invalid mission plan status: %s", strings.TrimSpace(value))
	}
	if status == session.MissionPlanStatusApproved {
		return "", errors.New("mission plan approval must use the mission plan approve endpoint")
	}
	return status, nil
}

func rejectMissionPlanApprovalByPatch(value string) error {
	status := session.NormalizeMissionPlanStatus(value)
	if !session.IsMissionPlanStatus(status) {
		return fmt.Errorf("invalid mission plan status: %s", strings.TrimSpace(value))
	}
	if status == session.MissionPlanStatusApproved {
		return errors.New("mission plan approval must use the mission plan approve endpoint")
	}
	return nil
}

func missionPlanPatchTouchesApprovalScopedContent(req MissionPlanPatchRequest) bool {
	return req.Requirements != nil ||
		req.Features != nil ||
		req.Milestones != nil ||
		req.ValidationContract != nil ||
		req.RolePlan != nil ||
		req.SharedArtifacts != nil ||
		req.KnowledgeArtifacts != nil
}

func decodeOptionalMissionPlanApproveRequest(w http.ResponseWriter, r *http.Request) (MissionPlanApproveRequest, bool) {
	var req MissionPlanApproveRequest
	if r.Body == nil || r.ContentLength == 0 {
		return req, true
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return MissionPlanApproveRequest{}, false
	}
	return req, true
}

func ensureWebMissionCoverage(goal session.SessionGoal, override bool) error {
	coverage := session.CheckMissionPlanCoverage(goal)
	if !coverage.ApprovalBlocked || override {
		return nil
	}
	return fmt.Errorf("mission validation coverage blocks approval: %s", coverage.BlockingSummary())
}

func optionalGoalCriteria(items []session.GoalCriterion) *[]session.GoalCriterion {
	if items == nil {
		return nil
	}
	copyItems := append([]session.GoalCriterion(nil), items...)
	return &copyItems
}

func optionalGoalValidations(items []session.GoalValidation) *[]session.GoalValidation {
	if items == nil {
		return nil
	}
	copyItems := append([]session.GoalValidation(nil), items...)
	return &copyItems
}

func (s *Service) appendGoalMutation(sessionID string, goal session.SessionGoal, eventType string, extra map[string]any) error {
	data := webGoalEventData(goal)
	for key, value := range extra {
		data[key] = value
	}
	if err := s.store.AppendGoalHistory(sessionID, session.GoalHistoryEntry{
		GoalID: goal.GoalID,
		Type:   eventType,
		Source: session.GoalSourceWeb,
		Status: goal.Status,
		Data:   data,
	}); err != nil {
		return err
	}
	return s.store.AppendEvent(sessionID, events.New(sessionID, eventType, "goal", data))
}

func (s *Service) appendGoalEvent(sessionID string, goal session.SessionGoal, eventType string, extra map[string]any) error {
	data := webGoalEventData(goal)
	for key, value := range extra {
		data[key] = value
	}
	return s.store.AppendEvent(sessionID, events.New(sessionID, eventType, "goal", data))
}

func (s *Service) goalFacts(sessionID string, goal session.SessionGoal, children ChildrenResponse, background []session.BackgroundNotification) *GoalFactsResponse {
	history, _ := s.store.LoadGoalHistory(sessionID)
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	var latest *session.GoalHistoryEntry
	if len(history) > 0 {
		copyLatest := history[len(history)-1]
		latest = &copyLatest
	}
	childIDs, queueIDs, evaluatorCount, latestBlocker := linkedGoalFacts(goal)
	unresolvedChildren := unresolvedLinkedChildren(childIDs, children.Sessions)
	unresolvedJobs := unresolvedLinkedJobs(queueIDs, children.Jobs)
	if len(unresolvedChildren) == 0 || len(unresolvedJobs) == 0 {
		for _, notification := range background {
			if notification.DeliveryStatus != session.BackgroundNotificationPending {
				continue
			}
			if notification.SessionID != "" {
				unresolvedChildren = appendUniqueString(unresolvedChildren, notification.SessionID)
			}
			if notification.QueueJobID != "" {
				unresolvedJobs = appendUniqueString(unresolvedJobs, notification.QueueJobID)
			}
		}
	}
	return &GoalFactsResponse{
		Coverage:                  session.CheckMissionPlanCoverage(goal),
		LatestHistory:             latest,
		History:                   history,
		Progress:                  append([]session.GoalProgressRecord(nil), goal.Progress...),
		LinkedChildSessionIDs:     childIDs,
		LinkedQueueJobIDs:         queueIDs,
		UnresolvedChildSessionIDs: unresolvedChildren,
		UnresolvedQueueJobIDs:     unresolvedJobs,
		EvaluatorEvidenceCount:    evaluatorCount,
		LatestBlocker:             latestBlocker,
	}
}

func linkedGoalFacts(goal session.SessionGoal) ([]string, []string, int, string) {
	childIDs := []string{}
	queueIDs := []string{}
	evaluatorCount := 0
	evaluatorKeys := map[string]struct{}{}
	latestBlocker := ""
	addValidation := func(validation session.GoalValidation) {
		childIDs = mergeUniqueStrings(childIDs, validation.ChildSessionIDs)
		queueIDs = mergeUniqueStrings(queueIDs, validation.QueueJobIDs)
		for _, evidence := range validation.EvaluatorEvidence {
			key := strings.Join([]string{evidence.ChildSessionID, evidence.QueueJobID, evidence.Artifact, evidence.Summary}, "\x00")
			if _, ok := evaluatorKeys[key]; !ok {
				evaluatorKeys[key] = struct{}{}
				evaluatorCount++
			}
			if evidence.ChildSessionID != "" {
				childIDs = appendUniqueString(childIDs, evidence.ChildSessionID)
			}
			if evidence.QueueJobID != "" {
				queueIDs = appendUniqueString(queueIDs, evidence.QueueJobID)
			}
		}
	}
	for _, validation := range goal.ValidationPlan {
		addValidation(validation)
	}
	if goal.Mission != nil {
		for _, validation := range goal.Mission.ValidationContract {
			addValidation(validation)
		}
		for _, feature := range goal.Mission.Features {
			childIDs = mergeUniqueStrings(childIDs, feature.ChildSessionIDs)
			queueIDs = mergeUniqueStrings(queueIDs, feature.QueueJobIDs)
		}
		for _, milestone := range goal.Mission.Milestones {
			childIDs = mergeUniqueStrings(childIDs, milestone.ChildSessionIDs)
			queueIDs = mergeUniqueStrings(queueIDs, milestone.QueueJobIDs)
		}
	}
	for _, record := range goal.Progress {
		childIDs = mergeUniqueStrings(childIDs, record.ChildSessionIDs)
		queueIDs = mergeUniqueStrings(queueIDs, record.QueueJobIDs)
		if len(record.Blockers) > 0 {
			latestBlocker = record.Blockers[len(record.Blockers)-1]
		}
	}
	return childIDs, queueIDs, evaluatorCount, latestBlocker
}

func unresolvedLinkedChildren(ids []string, children []session.SessionSummary) []string {
	statusByID := map[string]string{}
	for _, child := range children {
		statusByID[child.ID] = child.Status
	}
	out := []string{}
	for _, id := range ids {
		status := statusByID[id]
		if status == "" || (status != session.StatusCompleted && status != session.StatusFailed) {
			out = append(out, id)
		}
	}
	return out
}

func unresolvedLinkedJobs(ids []string, jobs []session.QueueJob) []string {
	statusByID := map[string]string{}
	for _, job := range jobs {
		statusByID[job.ID] = job.Status
	}
	out := []string{}
	for _, id := range ids {
		status := statusByID[id]
		if status == "" || (status != session.QueueStatusCompleted && status != session.QueueStatusFailed) {
			out = append(out, id)
		}
	}
	return out
}

func mergeUniqueStrings(existing []string, additions []string) []string {
	out := append([]string(nil), existing...)
	for _, value := range additions {
		out = appendUniqueString(out, value)
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func webGoalEventData(goal session.SessionGoal) map[string]any {
	data := map[string]any{
		"goal_id":           goal.GoalID,
		"mode":              goal.Mode,
		"status":            goal.Status,
		"objective":         goal.Objective,
		"tokens_used":       goal.TokensUsed,
		"time_used_seconds": goal.TimeUsedSeconds,
	}
	if goal.TokenBudget != nil {
		data["token_budget"] = *goal.TokenBudget
	}
	if goal.TimeBudgetSeconds != nil {
		data["time_budget_seconds"] = *goal.TimeBudgetSeconds
	}
	if goal.CompletedAt != "" {
		data["completed_at"] = goal.CompletedAt
	}
	if goal.CompletionAudit != nil {
		data["completion_evidence_count"] = len(goal.CompletionAudit.Evidence)
		if goal.CompletionAudit.Summary != "" {
			data["completion_summary"] = goal.CompletionAudit.Summary
		}
	}
	if goal.Mission != nil {
		data["mission_plan_status"] = goal.Mission.PlanStatus
		data["mission_feature_count"] = len(goal.Mission.Features)
		data["mission_milestone_count"] = len(goal.Mission.Milestones)
		coverage := session.CheckMissionPlanCoverage(goal)
		if coverage.ValidationTotal > 0 {
			data["mission_validation_total"] = coverage.ValidationTotal
			data["mission_validation_covered"] = coverage.CoveredAssertions
			data["mission_validation_approval_blocked"] = coverage.ApprovalBlocked
		}
	}
	if len(goal.Progress) > 0 {
		latest := goal.Progress[len(goal.Progress)-1]
		data["progress_id"] = latest.ID
		data["kind"] = latest.Kind
		data["summary"] = latest.Summary
	}
	return data
}

func webTaskIDs(tasks []session.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) != "" {
			ids = append(ids, task.ID)
		}
	}
	return ids
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

func resolveManagedSkillDir(rawDir string) (string, error) {
	resolved, err := resolveSkillDir(rawDir)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("skill root must not be a symlink: %s", resolved)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("skill root is not a directory: %s", resolved)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return canonicalManagedPath(resolved), nil
}

func canonicalManagedPath(path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(real)
	}
	parent := filepath.Dir(path)
	if parentReal, parentErr := filepath.EvalSymlinks(parent); parentErr == nil {
		return filepath.Join(parentReal, filepath.Base(path))
	}
	return filepath.Clean(path)
}

func newWorkerPool(cfg *config.Config, desired int) *workerPool {
	if desired > maxWorkerCount {
		desired = maxWorkerCount
	}
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
	if desired > maxWorkerCount {
		desired = maxWorkerCount
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
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		cfg := p.configSnapshot()
		poll := time.Duration(cfg.Runtime.Queue.PollIntervalMS) * time.Millisecond
		if poll <= 0 {
			poll = time.Second
		}
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
		worker.runner = runtime.NewRunner(cfg)
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
		MaxCount:     maxWorkerCount,
		PollInterval: p.cfg.Runtime.Queue.PollIntervalMS,
		Workers:      []WorkerStatus{},
	}
	for _, worker := range p.workers {
		snapshot.Workers = append(snapshot.Workers, worker.snapshot())
	}
	sort.Slice(snapshot.Workers, func(i, j int) bool { return snapshot.Workers[i].ID < snapshot.Workers[j].ID })
	return snapshot
}

func (p *workerPool) UpdateConfig(cfg *config.Config) {
	cloned, err := config.Clone(cfg)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.cfg = cloned
	p.mu.Unlock()
}

func (p *workerPool) configSnapshot() *config.Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cloned, err := config.Clone(p.cfg)
	if err != nil {
		return p.cfg
	}
	return cloned
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

var (
	embeddedAssetCache sync.Map // name -> *assetCacheEntry
)

type assetCacheEntry struct {
	body        []byte
	gzipBody    []byte
	etag        string
	contentType string
}

func loadEmbeddedAsset(files fs.FS, name string) (*assetCacheEntry, error) {
	if v, ok := embeddedAssetCache.Load(name); ok {
		return v.(*assetCacheEntry), nil
	}
	data, err := fs.ReadFile(files, filepath.Clean(name))
	if err != nil {
		return nil, err
	}
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "text/html; charset=utf-8"
	}
	sum := sha256.Sum256(data)
	entry := &assetCacheEntry{
		body:        data,
		etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
		contentType: contentType,
	}
	if shouldPrecompressContentType(contentType) && len(data) >= 1024 {
		var buf bytes.Buffer
		gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err == nil {
			if _, werr := gw.Write(data); werr == nil {
				if cerr := gw.Close(); cerr == nil && buf.Len() < len(data) {
					entry.gzipBody = buf.Bytes()
				}
			}
		}
	}
	if actual, loaded := embeddedAssetCache.LoadOrStore(name, entry); loaded {
		return actual.(*assetCacheEntry), nil
	}
	return entry, nil
}

func shouldPrecompressContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "text/"):
		return true
	case strings.Contains(ct, "javascript"):
		return true
	case strings.Contains(ct, "json"):
		return true
	case strings.Contains(ct, "svg"):
		return true
	case strings.Contains(ct, "wasm"):
		return false
	}
	return false
}

func clientAcceptsGzip(r *http.Request) bool {
	if r == nil {
		return false
	}
	gzipQ := -1.0
	wildcardQ := -1.0
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		token, q := parseAcceptEncodingPart(part)
		if token == "" {
			continue
		}
		if strings.EqualFold(token, "gzip") {
			gzipQ = q
			continue
		}
		if token == "*" {
			wildcardQ = q
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return wildcardQ > 0
}

func parseAcceptEncodingPart(part string) (string, float64) {
	q := 1.0
	fields := strings.Split(part, ";")
	token := strings.ToLower(strings.TrimSpace(fields[0]))
	for _, field := range fields[1:] {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return token, 0
		}
		if parsed < 0 {
			parsed = 0
		}
		if parsed > 1 {
			parsed = 1
		}
		q = parsed
	}
	return token, q
}

func serveEmbeddedFileRequest(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	entry, err := loadEmbeddedAsset(files, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	header := w.Header()
	header.Set("Content-Type", entry.contentType)
	header.Set("ETag", entry.etag)
	header.Set("Vary", "Accept-Encoding")
	// HTML is templated/published frequently; assets are content-stable but unhashed,
	// so we still validate via ETag rather than long-cache them.
	header.Set("Cache-Control", "no-cache")

	if r != nil {
		if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, entry.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	if r != nil && entry.gzipBody != nil && clientAcceptsGzip(r) {
		header.Set("Content-Encoding", "gzip")
		header.Set("Content-Length", strconv.Itoa(len(entry.gzipBody)))
		_, _ = w.Write(entry.gzipBody)
		return
	}
	header.Set("Content-Length", strconv.Itoa(len(entry.body)))
	_, _ = w.Write(entry.body)
}

func etagMatches(headerValue, etag string) bool {
	for _, part := range strings.Split(headerValue, ",") {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain a single JSON value")
		}
		return err
	}
	return nil
}

func guardUnsafeAPIRequest(r *http.Request) error {
	if !isUnsafeMethod(r.Method) {
		return nil
	}
	if expectsJSONBody(r.URL.Path) {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			return errors.New("JSON API mutation requires Content-Type: application/json")
		}
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		originURL, err := url.Parse(origin)
		if err != nil || !sameOriginHost(originURL, r.Host) {
			return errors.New("cross-origin API mutation rejected")
		}
	} else if strings.TrimSpace(r.Header.Get(webMutationHeader)) != "1" {
		return errors.New("API mutation requires same-origin Origin or X-Go-Cli-Agent-Web header")
	}
	return nil
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func sameOriginHost(origin *url.URL, host string) bool {
	if origin == nil || strings.TrimSpace(origin.Host) == "" {
		return false
	}
	return strings.EqualFold(origin.Host, host)
}

func expectsJSONBody(path string) bool {
	if path == "/api/skills/upload" {
		return false
	}
	return path == "/api/config" ||
		path == "/api/config/test" ||
		path == "/api/sessions/start" ||
		path == "/api/queue/jobs" ||
		path == "/api/workers" ||
		strings.HasSuffix(path, "/goal") ||
		strings.Contains(path, "/planmode/") ||
		strings.Contains(path, "/mission/") ||
		strings.Contains(path, "/continue") ||
		strings.Contains(path, "/steer")
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
