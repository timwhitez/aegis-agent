package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/fileutil"
	"go-cli-agent/internal/hooks"
	"go-cli-agent/internal/isolation"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
	"go-cli-agent/internal/tools"
)

type Runner struct {
	cfg     *config.Config
	store   *session.Store
	bus     *events.Bus
	control *runControl
	engine  *Engine

	activeMu        sync.Mutex
	activeSessionID string
	activeDepth     int

	planInputMu       sync.Mutex
	planInputWaiters  map[string]chan planInputResponse
	planInputHandlers map[string]PlanInputHandler

	// beforeStartSessionCreatedEvent is set only by package tests to force
	// deterministic failures around the start-time lifecycle event boundary.
	beforeStartSessionCreatedEvent func(sessionID string)
	// beforeQueueLifecycleEvent is set only by package tests to force
	// deterministic failures around queue lifecycle event boundaries.
	beforeQueueLifecycleEvent func(job session.QueueJob, eventType string)
}

const defaultSteerMaxMessageChars = 12000
const defaultWorkspaceDirName = "workspace"

type SteerValidationError struct {
	Code        string
	MaxChars    int
	ActualChars int
}

func (e SteerValidationError) Error() string {
	return fmt.Sprintf("steer input exceeds the maximum length of %d characters", e.MaxChars)
}

func newSessionStore(cfg *config.Config) *session.Store {
	dirMode, err := config.ParseFileMode(cfg.Session.DirMode, 0o700)
	if err != nil {
		dirMode = 0o700
	}
	return session.NewStoreWithDirMode(cfg.Session.Dir, dirMode)
}

func NewRunner(cfg *config.Config) *Runner {
	store := newSessionStore(cfg)
	bus := events.NewBus()
	control := &runControl{}
	engine := NewEngine(cfg, store, bus, control)
	r := &Runner{
		cfg:               cfg,
		store:             store,
		bus:               bus,
		control:           control,
		engine:            engine,
		planInputWaiters:  map[string]chan planInputResponse{},
		planInputHandlers: map[string]PlanInputHandler{},
	}
	engine.SetRunner(r)
	return r
}

func (r *Runner) Bus() *events.Bus { return r.bus }

func planInputWaiterKey(sessionID, requestID string) string {
	return sessionID + ":" + requestID
}

func (r *Runner) setPlanInputHandler(sessionID string, handler PlanInputHandler) {
	r.planInputMu.Lock()
	defer r.planInputMu.Unlock()
	if handler == nil {
		delete(r.planInputHandlers, sessionID)
		return
	}
	r.planInputHandlers[sessionID] = handler
}

func (r *Runner) clearPlanInputHandler(sessionID string) {
	r.planInputMu.Lock()
	defer r.planInputMu.Unlock()
	delete(r.planInputHandlers, sessionID)
}

func (r *Runner) RequestPlanInput(ctx context.Context, sessionID string, request session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
	r.planInputMu.Lock()
	if handler := r.planInputHandlers[sessionID]; handler != nil {
		r.planInputMu.Unlock()
		return handler(ctx, request)
	}
	key := planInputWaiterKey(sessionID, request.RequestID)
	ch := make(chan planInputResponse, 1)
	r.planInputWaiters[key] = ch
	r.planInputMu.Unlock()
	defer func() {
		r.planInputMu.Lock()
		delete(r.planInputWaiters, key)
		r.planInputMu.Unlock()
	}()
	select {
	case response := <-ch:
		if response.err != nil {
			return nil, response.err
		}
		return response.answers, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Runner) AnswerActivePlanInput(sessionID, requestID string, answers []session.PlanModeInputAnswer) bool {
	r.planInputMu.Lock()
	key := planInputWaiterKey(sessionID, requestID)
	ch, ok := r.planInputWaiters[key]
	if !ok {
		r.planInputMu.Unlock()
		return false
	}
	delete(r.planInputWaiters, key)
	r.planInputMu.Unlock()
	ch <- planInputResponse{answers: append([]session.PlanModeInputAnswer(nil), answers...)}
	return true
}

func (r *Runner) CancelActivePlanInput(sessionID, requestID string) bool {
	r.planInputMu.Lock()
	key := planInputWaiterKey(sessionID, requestID)
	ch, ok := r.planInputWaiters[key]
	if !ok {
		r.planInputMu.Unlock()
		return false
	}
	delete(r.planInputWaiters, key)
	r.planInputMu.Unlock()
	ch <- planInputResponse{err: tools.ErrPlanInputCancelled}
	return true
}

func (r *Runner) acquireRunSlot(sessionID string) (func(), error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if r.activeSessionID != "" && r.activeSessionID != sessionID {
		return nil, fmt.Errorf("runner already has active session %s; create a separate runner for concurrent sessions", r.activeSessionID)
	}
	r.activeSessionID = sessionID
	r.activeDepth++
	return func() {
		r.activeMu.Lock()
		defer r.activeMu.Unlock()
		if r.activeSessionID != sessionID {
			return
		}
		r.activeDepth--
		if r.activeDepth <= 0 {
			r.activeSessionID = ""
			r.activeDepth = 0
		}
	}, nil
}

type StartRequest struct {
	Prompt           string
	Provider         string
	Model            string
	ProviderOptions  session.ProviderOptions
	Workdir          string
	Mode             string
	SystemOverride   string
	Goal             *session.GoalDraft
	PlanMode         *session.PlanModeDraft
	PlanInputHandler PlanInputHandler
	ParentSessionID  string
	AgentName        string
	AgentRole        string
	QueueJobID       string
	IsolationMode    string
	IsolationRoot    string
}

type ContinueRequest struct {
	SessionID            string
	Message              string
	Provider             string
	Model                string
	SystemOverride       string
	PlanMode             *session.PlanModeDraft
	PlanInputHandler     PlanInputHandler
	ApprovePlan          bool
	OverrideGoalCoverage bool
	CancelPlan           bool
	PlanInputRequestID   string
	PlanInputAnswers     []session.PlanModeInputAnswer
	Source               string
}

type PlanInputHandler func(context.Context, session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error)

type planInputResponse struct {
	answers []session.PlanModeInputAnswer
	err     error
}

type SteerRequest struct {
	SessionID string
	Message   string
	Interrupt bool
	Source    string
}

type SteerResult struct {
	SessionID string `json:"session_id"`
	Accepted  bool   `json:"accepted"`
	Behavior  string `json:"behavior"`
}

type ProbeRequest struct {
	Provider         string
	Model            string
	BaseURL          string
	APIKeyEnv        string
	APIProvider      string
	WireAPI          string
	Prompt           string
	ThinkingProbe    bool
	ReasoningSummary string
}

type ProbeResult struct {
	Provider                   string         `json:"provider"`
	Model                      string         `json:"model"`
	BaseURL                    string         `json:"base_url"`
	APIProvider                string         `json:"api_provider,omitempty"`
	WireAPI                    string         `json:"wire_api,omitempty"`
	StopReason                 string         `json:"stop_reason"`
	Text                       string         `json:"text,omitempty"`
	Thinking                   string         `json:"thinking,omitempty"`
	ThinkingVisibleObserved    bool           `json:"thinking_visible_observed,omitempty"`
	ThinkingReplayObserved     bool           `json:"thinking_replay_observed,omitempty"`
	ThinkingDetail             string         `json:"thinking_detail,omitempty"`
	ThinkingStrategy           string         `json:"thinking_strategy,omitempty"`
	ReasoningSummary           string         `json:"reasoning_summary,omitempty"`
	ReasoningSummaryObserved   bool           `json:"reasoning_summary_observed,omitempty"`
	ReasoningEncryptedObserved bool           `json:"reasoning_encrypted_observed,omitempty"`
	ReasoningTokens            int            `json:"reasoning_tokens,omitempty"`
	ToolCallNames              []string       `json:"tool_call_names,omitempty"`
	FinishMessage              string         `json:"finish_message,omitempty"`
	Usage                      provider.Usage `json:"usage,omitempty"`
}

func (r *Runner) Start(ctx context.Context, req StartRequest) (RunResult, error) {
	agentRole, err := normalizeAgentRole(req.AgentRole, req.AgentName)
	if err != nil {
		return RunResult{}, err
	}
	mode, err := normalizeAndValidateRunMode(req.Mode, session.ModeRun)
	if err != nil {
		return RunResult{}, err
	}
	sessionID := session.NewSessionID()
	req, err = prepareStartGoalAndPlanModeDrafts(sessionID, req)
	if err != nil {
		return RunResult{}, err
	}
	releaseRunSlot, err := r.acquireRunSlot(sessionID)
	if err != nil {
		return RunResult{}, err
	}
	defer releaseRunSlot()
	rootSessionID := sessionID
	depth := 0
	var parentMeta *session.SessionMetadata
	if req.ParentSessionID != "" {
		loadedParentMeta, err := r.store.LoadMetadata(req.ParentSessionID)
		if err != nil {
			return RunResult{}, err
		}
		parentMeta = &loadedParentMeta
		if parentMeta.RootSessionID != "" {
			rootSessionID = parentMeta.RootSessionID
		} else {
			rootSessionID = parentMeta.ID
		}
		depth = parentMeta.Depth + 1
		if depth > r.cfg.Runtime.MultiAgent.MaxDepth {
			return RunResult{}, fmt.Errorf("max agent depth exceeded: %d", r.cfg.Runtime.MultiAgent.MaxDepth)
		}
	}
	requestedWorkdir, err := resolveRequestedWorkdir(req.Workdir, parentMeta)
	if err != nil {
		return RunResult{}, err
	}
	providerName, model, providerCfg, err := resolveProviderAndModel(r.cfg, parentMeta, req.Provider, req.Model, agentRole)
	if err != nil {
		return RunResult{}, WrapConfigError(err)
	}
	if _, err := config.EffectiveAPIProvider(providerName, providerCfg); err != nil {
		return RunResult{}, WrapConfigError(err)
	}
	providerOptions, err := resolvedProviderOptions(providerName, providerCfg, req.ProviderOptions)
	if err != nil {
		return RunResult{}, err
	}
	effectiveWorkdir := requestedWorkdir
	isolationMode, err := normalizeAndValidateIsolationMode(req.IsolationMode, r.cfg.Runtime.Isolation.DefaultMode)
	if err != nil {
		return RunResult{}, err
	}
	var isolationInfo *session.IsolationInfo
	if isolationMode != "" && isolationMode != "off" {
		rootDir := req.IsolationRoot
		if strings.TrimSpace(rootDir) == "" {
			rootDir = r.cfg.Runtime.Isolation.RootDir
		}
		prepared, err := isolation.Prepare(isolation.Request{
			SessionID:     sessionID,
			ParentWorkdir: requestedWorkdir,
			RequestedMode: isolationMode,
			RootDir:       rootDir,
		})
		if err != nil {
			return RunResult{}, err
		}
		effectiveWorkdir = prepared.Workdir
		isolationInfo = &session.IsolationInfo{
			Mode:          prepared.Mode,
			RequestedMode: prepared.RequestedMode,
			ParentWorkdir: prepared.ParentWorkdir,
			Workdir:       prepared.Workdir,
			RootDir:       prepared.RootDir,
			GitRepoRoot:   prepared.GitRepoRoot,
		}
	}
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               sessionID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Workdir:          effectiveWorkdir,
		RequestedWorkdir: requestedWorkdir,
		Mode:             mode,
		Provider:         providerName,
		Model:            model,
		CompletionPolicy: completionPolicy(mode),
		ParentSessionID:  req.ParentSessionID,
		RootSessionID:    rootSessionID,
		AgentName:        req.AgentName,
		AgentRole:        agentRole,
		QueueJobID:       req.QueueJobID,
		Depth:            depth,
		Isolation:        isolationInfo,
		ProviderOptions:  providerOptions,
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := r.store.Create(meta, state); err != nil {
		return RunResult{}, err
	}
	if err := r.initializeStartGoalAndPlanMode(meta.ID, req); err != nil {
		return r.failBeforeRun(meta.ID, state, "prepare", err)
	}
	if r.beforeStartSessionCreatedEvent != nil {
		r.beforeStartSessionCreatedEvent(meta.ID)
	}
	if err := r.appendEvent(meta.ID, "session.created", "prepare", map[string]any{
		"provider": meta.Provider,
		"model":    meta.Model,
		"mode":     meta.Mode,
		"workdir":  meta.Workdir,
	}); err != nil {
		return r.failBeforeRun(meta.ID, state, "prepare", fmt.Errorf("record session.created event: %w", err))
	}
	_ = writeSessionSummary(r.store, meta.ID)
	if stringsTrim(req.Prompt) != "" {
		if err := r.appendUserMessage(ctx, meta, "prepare", req.Prompt, nil); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		if err := r.refreshContractFromMessages(meta, "prepare"); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
	}
	return r.runExisting(ctx, meta, state, req.SystemOverride, req.PlanInputHandler)
}

func prepareStartGoalAndPlanModeDrafts(sessionID string, req StartRequest) (StartRequest, error) {
	if req.Goal != nil && req.Goal.Enabled {
		draft := *req.Goal
		if strings.TrimSpace(draft.Source) == "" {
			draft.Source = session.GoalSourceCLI
		}
		goal, err := session.NewSessionGoalFromDraft(sessionID, draft)
		if err != nil {
			return req, err
		}
		req.Goal = &draft
		if (req.PlanMode == nil || !req.PlanMode.Enabled) && session.GoalRequiresPlanApproval(goal) {
			linkedDraft := session.PlanModeDraft{
				Enabled:   true,
				Objective: goal.Objective,
				Source:    goal.Source,
			}
			if _, err := session.NewPlanModeFromDraft(sessionID, linkedDraft, goal.GoalID); err != nil {
				return req, err
			}
		}
	}
	if req.PlanMode != nil && req.PlanMode.Enabled {
		draft := *req.PlanMode
		if strings.TrimSpace(draft.Source) == "" {
			draft.Source = session.PlanModeSourceCLI
		}
		if strings.TrimSpace(draft.Objective) == "" {
			draft.Objective = req.Prompt
		}
		if _, err := session.NewPlanModeFromDraft(sessionID, draft, ""); err != nil {
			return req, err
		}
		req.PlanMode = &draft
	}
	return req, nil
}

type startGoalPlanRollback struct {
	goalCreated         bool
	goalHistory         []session.GoalHistoryEntry
	tasks               []session.Task
	planModeTouched     bool
	planModeSnapshotted bool
	planModeSnapshot    session.PlanModeSnapshot
	planModeHistory     []session.PlanModeHistoryEntry
	requiredEventTypes  []string
}

func (r *Runner) initializeStartGoalAndPlanMode(sessionID string, req StartRequest) error {
	goalEnabled := req.Goal != nil && req.Goal.Enabled
	planModeEnabled := req.PlanMode != nil && req.PlanMode.Enabled
	if !goalEnabled && !planModeEnabled {
		return nil
	}
	rollback := startGoalPlanRollback{}
	if goalEnabled {
		history, err := r.store.LoadGoalHistory(sessionID)
		if err != nil {
			return err
		}
		tasks, err := r.store.ListTasks(sessionID)
		if err != nil {
			return err
		}
		rollback.goalHistory = history
		rollback.tasks = tasks
	}
	if planModeEnabled {
		if err := r.snapshotStartPlanModeRollback(sessionID, &rollback); err != nil {
			return err
		}
	}
	var requiredEvents []events.Event
	if goalEnabled {
		draft := *req.Goal
		if strings.TrimSpace(draft.Source) == "" {
			draft.Source = session.GoalSourceCLI
		}
		goal, err := r.store.CreateGoal(sessionID, draft)
		if err != nil {
			return err
		}
		rollback.goalCreated = true
		requiredEvents = append(requiredEvents, events.New(sessionID, "goal.created", "prepare", goalEventData(goal)))
		rollback.requiredEventTypes = append(rollback.requiredEventTypes, "goal.created")
		if !planModeEnabled && session.GoalRequiresPlanApproval(goal) {
			if err := r.snapshotStartPlanModeRollback(sessionID, &rollback); err != nil {
				return r.restoreStartGoalAndPlanMode(sessionID, rollback, err)
			}
			rollback.planModeTouched = true
			planMode, created, err := r.store.EnsurePlanModeForGoal(sessionID, goal, goal.Source)
			if err != nil {
				return r.restoreStartGoalAndPlanMode(sessionID, rollback, err)
			}
			if created {
				requiredEvents = append(requiredEvents, events.New(sessionID, "planmode.created", "prepare", planModeEventData(planMode)))
				rollback.requiredEventTypes = append(rollback.requiredEventTypes, "planmode.created")
			}
		}
	}
	if planModeEnabled {
		draft := *req.PlanMode
		if strings.TrimSpace(draft.Source) == "" {
			draft.Source = session.PlanModeSourceCLI
		}
		if strings.TrimSpace(draft.Objective) == "" {
			draft.Objective = req.Prompt
		}
		rollback.planModeTouched = true
		planMode, err := r.store.CreatePlanMode(sessionID, draft)
		if err != nil {
			return r.restoreStartGoalAndPlanMode(sessionID, rollback, err)
		}
		requiredEvents = append(requiredEvents, events.New(sessionID, "planmode.created", "prepare", planModeEventData(planMode)))
		rollback.requiredEventTypes = append(rollback.requiredEventTypes, "planmode.created")
	}
	if len(requiredEvents) == 0 {
		return nil
	}
	if err := r.appendEvents(sessionID, requiredEvents); err != nil {
		cause := fmt.Errorf("record start events [%s]: %w", strings.Join(rollback.requiredEventTypes, ", "), err)
		return r.restoreStartGoalAndPlanMode(sessionID, rollback, cause)
	}
	return nil
}

func (r *Runner) snapshotStartPlanModeRollback(sessionID string, rollback *startGoalPlanRollback) error {
	if rollback == nil || rollback.planModeSnapshotted {
		return nil
	}
	snapshot, err := r.store.SnapshotPlanMode(sessionID)
	if err != nil {
		return err
	}
	history, err := r.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		return err
	}
	rollback.planModeSnapshot = snapshot
	rollback.planModeHistory = history
	rollback.planModeSnapshotted = true
	return nil
}

func (r *Runner) restoreStartGoalAndPlanMode(sessionID string, rollback startGoalPlanRollback, cause error) error {
	if rollback.planModeTouched {
		if err := r.store.RestorePlanModeSnapshot(sessionID, rollback.planModeSnapshot); err != nil {
			return fmt.Errorf("restore plan mode after start initialization failure %v: %w", cause, err)
		}
		if err := r.store.RestorePlanModeHistory(sessionID, rollback.planModeHistory); err != nil {
			return fmt.Errorf("restore plan mode history after start initialization failure %v: %w", cause, err)
		}
	}
	if rollback.goalCreated {
		if _, err := r.store.ClearGoal(sessionID); err != nil {
			return fmt.Errorf("restore goal after start initialization failure %v: %w", cause, err)
		}
		if err := r.store.RestoreGoalHistory(sessionID, rollback.goalHistory); err != nil {
			return fmt.Errorf("restore goal history after start initialization failure %v: %w", cause, err)
		}
		if err := r.store.SaveTasks(sessionID, rollback.tasks); err != nil {
			return fmt.Errorf("restore tasks after start initialization failure %v: %w", cause, err)
		}
	}
	return cause
}

func resolveRequestedWorkdir(input string, parentMeta *session.SessionMetadata) (string, error) {
	workdir := strings.TrimSpace(input)
	usedDefaultWorkspace := false
	if workdir == "" {
		if parentMeta != nil {
			workdir = firstNonEmpty(parentMeta.RequestedWorkdir, parentMeta.Workdir)
		}
		if workdir == "" {
			current, err := os.Getwd()
			if err != nil {
				return "", err
			}
			workdir = filepath.Join(current, defaultWorkspaceDirName)
			usedDefaultWorkspace = true
		}
	} else if parentMeta != nil && !filepath.IsAbs(workdir) {
		if parentBase := firstNonEmpty(parentMeta.RequestedWorkdir, parentMeta.Workdir); parentBase != "" {
			parentRelative := filepath.Join(parentBase, workdir)
			cwdRelative, err := filepath.Abs(workdir)
			if err != nil {
				return "", err
			}
			if directoryExists(parentRelative) || !directoryExists(cwdRelative) {
				workdir = parentRelative
			} else {
				workdir = cwdRelative
			}
		}
	}
	resolved, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	if usedDefaultWorkspace {
		current, err := os.Getwd()
		if err != nil {
			return "", err
		}
		if info, err := os.Lstat(resolved); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("default workspace must not be a symlink: %s", resolved)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("default workspace path is not a directory: %s", resolved)
			}
		} else if os.IsNotExist(err) {
			if err := fileutil.MkdirAllNoSymlink(resolved, 0o700); err != nil {
				return "", err
			}
		} else {
			return "", err
		}
		currentReal, err := filepath.EvalSymlinks(current)
		if err != nil {
			return "", err
		}
		workspaceReal, err := filepath.EvalSymlinks(resolved)
		if err != nil {
			return "", err
		}
		if !pathWithin(currentReal, workspaceReal) {
			return "", fmt.Errorf("default workspace escapes current directory: %s", resolved)
		}
	}
	return resolved, nil
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func normalizeIsolationMode(value, fallback string) string {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" || mode == "default" {
		mode = strings.ToLower(strings.TrimSpace(fallback))
	}
	switch mode {
	case "none", "workspace-write", "workspace_write":
		return "off"
	default:
		return mode
	}
}

func normalizeAndValidateIsolationMode(value, fallback string) (string, error) {
	mode := normalizeIsolationMode(value, fallback)
	switch mode {
	case "", "off", "auto", "copy", "git":
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported isolation mode: %s", strings.TrimSpace(value))
	}
}

func normalizeRunMode(value, fallback string) string {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" || mode == "default" {
		mode = strings.ToLower(strings.TrimSpace(fallback))
	}
	switch mode {
	case "full-auto", "full_auto", "autonomous":
		return session.ModeExec
	case "interactive":
		return session.ModeRun
	case session.ModeInit:
		return session.ModeInit
	default:
		return mode
	}
}

func normalizeAndValidateRunMode(value, fallback string) (string, error) {
	mode := normalizeRunMode(value, fallback)
	switch mode {
	case session.ModeRun, session.ModeExec, session.ModeInit:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported run mode: %s", strings.TrimSpace(value))
	}
}

func normalizeProviderOverride(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "default") {
		return ""
	}
	return strings.ToLower(value)
}

func normalizeModelOverride(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "default") {
		return ""
	}
	return value
}

func resolveProviderAndModel(cfg *config.Config, parentMeta *session.SessionMetadata, providerOverride, modelOverride string, agentRole ...string) (string, string, config.Provider, error) {
	providerName := normalizeProviderOverride(providerOverride)
	model := normalizeModelOverride(modelOverride)
	explicitProvider := providerName != ""
	explicitModel := model != ""
	roleOverride := config.RoleProviderOverride{}
	if len(agentRole) > 0 && !explicitProvider {
		roleOverride = cfg.RoleProviderOverride(agentRole[0])
	}
	if explicitProvider {
		providerName = normalizeProviderOverride(providerOverride)
	} else if strings.TrimSpace(roleOverride.Provider) != "" {
		providerName = strings.TrimSpace(roleOverride.Provider)
	} else if parentMeta != nil && strings.TrimSpace(parentMeta.Provider) != "" {
		providerName = parentMeta.Provider
	} else if providerName == "" {
		providerName = cfg.DefaultProvider
	}
	providerCfg, err := cfg.ProviderConfig(providerName)
	if err != nil {
		return "", "", config.Provider{}, err
	}
	if strings.TrimSpace(roleOverride.APIProvider) != "" {
		providerCfg.APIProvider = strings.TrimSpace(roleOverride.APIProvider)
	}
	if strings.TrimSpace(roleOverride.BaseURL) != "" {
		providerCfg.BaseURL = strings.TrimSpace(roleOverride.BaseURL)
	}
	if explicitModel {
		model = normalizeModelOverride(modelOverride)
	} else if strings.TrimSpace(roleOverride.Model) != "" {
		model = strings.TrimSpace(roleOverride.Model)
	}
	if model == "" {
		if parentMeta != nil && providerName == parentMeta.Provider && strings.TrimSpace(parentMeta.Model) != "" {
			model = parentMeta.Model
		} else {
			model = providerCfg.Model
		}
	}
	return providerName, model, providerCfg, nil
}

func (r *Runner) Continue(ctx context.Context, req ContinueRequest) (RunResult, error) {
	meta, err := r.store.LoadMetadata(req.SessionID)
	if err != nil {
		return RunResult{}, err
	}
	state, err := r.store.LoadState(req.SessionID)
	if err != nil {
		return RunResult{}, err
	}
	switch state.Status {
	case session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed:
	default:
		return RunResult{}, errors.New("session is not resumable")
	}
	releaseRunSlot, err := r.acquireRunSlot(meta.ID)
	if err != nil {
		return RunResult{}, err
	}
	defer releaseRunSlot()
	providerNameOverride := normalizeProviderOverride(req.Provider)
	modelOverride := normalizeModelOverride(req.Model)
	if providerNameOverride != "" {
		providerName := providerNameOverride
		providerCfg, err := r.cfg.ProviderConfig(providerName)
		if err != nil {
			return RunResult{}, WrapConfigError(err)
		}
		if modelOverride != "" {
			providerCfg.Model = modelOverride
		}
		providerOptions, err := resolvedProviderOptions(providerName, providerCfg, session.ProviderOptions{})
		if err != nil {
			return RunResult{}, err
		}
		meta.Provider = providerName
		meta.Model = providerCfg.Model
		meta.ProviderOptions = providerOptions
	}
	if modelOverride != "" {
		meta.Model = modelOverride
	}
	req.PlanInputRequestID = strings.TrimSpace(req.PlanInputRequestID)
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = session.PlanModeSourceCLI
	}
	if err := r.preflightPlanModeControl(meta.ID, req); err != nil {
		return RunResult{}, err
	}
	planModeDraft, err := planModeDraftForContinue(meta.ID, req, source)
	if err != nil {
		return RunResult{}, err
	}
	if !req.CancelPlan {
		mergedProviderOptions, err := r.mergedSessionProviderOptions(meta.Provider, meta.ProviderOptions)
		if err != nil {
			return RunResult{}, err
		}
		if !reflect.DeepEqual(meta.ProviderOptions, mergedProviderOptions) {
			meta.ProviderOptions = mergedProviderOptions
		}
	}
	state, err = r.store.ClaimSessionRun(meta.ID, session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed)
	if err != nil {
		return RunResult{}, err
	}
	if err := r.store.SaveMetadata(meta.ID, meta); err != nil {
		return r.failBeforeRun(meta.ID, state, "prepare", err)
	}
	if len(req.PlanInputAnswers) > 0 {
		if err := r.appendPlanInputToolResult(meta.ID, req.PlanInputRequestID, source, req.PlanInputAnswers); err != nil {
			return r.failBeforeRun(meta.ID, state, "plan_input", err)
		}
	}
	if req.CancelPlan {
		if err := r.appendPlanInputCancelToolResult(meta.ID, source); err != nil {
			return r.failBeforeRun(meta.ID, state, "plan_input", err)
		}
		if _, err := r.ensurePlanModeCancelled(meta.ID, source); err != nil {
			return r.failBeforeRun(meta.ID, state, "plan_input", err)
		}
		state.Status = session.StatusAwaitingInput
		state.Phase = "plan_cancelled"
		if err := r.store.SaveState(meta.ID, state); err != nil {
			return RunResult{}, err
		}
		if err := r.appendEvent(meta.ID, "session.awaiting_input", state.Phase, map[string]any{"reason": "plan_cancelled"}); err != nil {
			return RunResult{}, fmt.Errorf("record session.awaiting_input event for plan_cancelled: %w", err)
		}
		_ = writeSessionSummary(r.store, meta.ID)
		return RunResult{SessionID: meta.ID, Status: state.Status, FinalText: "Plan Mode cancelled."}, nil
	}
	var extraUserMeta map[string]any
	if req.ApprovePlan {
		if err := r.checkPlanModeGoalCoverage(meta.ID, req.OverrideGoalCoverage); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		executing, err := r.ensurePlanModeExecutingForApproval(meta.ID, source)
		if err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		if err := r.approveLinkedMissionPlan(meta.ID, executing, source, req.OverrideGoalCoverage); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		req.Message = fmt.Sprintf("Implement the approved Plan Mode plan version %d.", executing.ApprovedVersion)
		extraUserMeta = map[string]any{
			"source":       "planmode_approval",
			"plan_mode_id": executing.PlanModeID,
			"plan_version": executing.ApprovedVersion,
		}
	}
	if planModeDraft != nil {
		if _, err := r.ensurePlanModeCreatedForContinue(meta.ID, *planModeDraft); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
	} else if !req.ApprovePlan && stringsTrim(req.Message) != "" {
		if planMode, err := r.store.LoadPlanMode(meta.ID); err == nil {
			revised, ok, err := r.ensurePlanModeRevisedForMessage(meta.ID, planMode, source, req.Message)
			if err != nil {
				return r.failBeforeRun(meta.ID, state, "prepare", err)
			}
			if ok {
				extraUserMeta = map[string]any{
					"source":       "planmode_revision",
					"plan_mode_id": revised.PlanModeID,
					"plan_version": revised.PlanVersion,
				}
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return r.failBeforeRun(meta.ID, state, "prepare", fmt.Errorf("load planmode.json: %w", err))
		}
	}
	checkpointHint, checkpointWarnings, checkpointMessageID, checkpointErr := appendCheckpointResumeHint(r.store, meta, meta.Provider, meta.Model)
	if checkpointErr != nil {
		return r.failBeforeRun(meta.ID, state, "prepare", checkpointErr)
	}
	if checkpointHint {
		if err := r.appendEvent(meta.ID, "checkpoint.resume_hint.injected", "prepare", map[string]any{
			"provider":       meta.Provider,
			"model":          meta.Model,
			"drift_warnings": append([]string(nil), checkpointWarnings...),
		}); err != nil {
			if rollbackErr := r.store.RemoveLastMessageIfID(meta.ID, checkpointMessageID); rollbackErr != nil {
				return r.failBeforeRun(meta.ID, state, "prepare", fmt.Errorf("record checkpoint.resume_hint.injected event after rolling back resume hint failed with %v: %w", rollbackErr, err))
			}
			return r.failBeforeRun(meta.ID, state, "prepare", fmt.Errorf("record checkpoint.resume_hint.injected event: %w", err))
		}
	}
	if stringsTrim(req.Message) != "" {
		if err := r.appendUserMessage(ctx, meta, "prepare", req.Message, extraUserMeta); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
	}
	if err := r.refreshContractFromMessages(meta, "prepare"); err != nil {
		return r.failBeforeRun(meta.ID, state, "prepare", err)
	}
	result, err := r.runExisting(ctx, meta, state, req.SystemOverride, req.PlanInputHandler)
	if err != nil && result.SessionID == "" {
		return r.failBeforeRun(meta.ID, state, "prepare", err)
	}
	return result, err
}

func planModeDraftForContinue(sessionID string, req ContinueRequest, source string) (*session.PlanModeDraft, error) {
	if req.PlanMode == nil || !req.PlanMode.Enabled {
		return nil, nil
	}
	draft := *req.PlanMode
	if strings.TrimSpace(draft.Source) == "" {
		draft.Source = source
	}
	if strings.TrimSpace(draft.Objective) == "" {
		draft.Objective = firstNonEmpty(req.Message, "Plan Mode continuation")
	}
	if _, err := session.NewPlanModeFromDraft(sessionID, draft, ""); err != nil {
		return nil, err
	}
	return &draft, nil
}

func (r *Runner) preflightPlanModeControl(sessionID string, req ContinueRequest) error {
	var controls []string
	if req.PlanMode != nil && req.PlanMode.Enabled {
		controls = append(controls, "start")
	}
	if req.ApprovePlan {
		controls = append(controls, "approve")
	}
	if req.CancelPlan {
		controls = append(controls, "cancel")
	}
	if len(req.PlanInputAnswers) > 0 {
		controls = append(controls, "answer_input")
	}
	if len(controls) > 1 {
		return fmt.Errorf("conflicting plan mode controls: %s", strings.Join(controls, ", "))
	}
	if req.ApprovePlan && stringsTrim(req.Message) != "" {
		return errors.New("plan mode approval cannot include ordinary message")
	}
	if len(req.PlanInputAnswers) > 0 && stringsTrim(req.Message) != "" {
		return errors.New("plan mode input answer cannot include ordinary message")
	}
	if req.CancelPlan {
		var executionInputs []string
		if stringsTrim(req.Message) != "" {
			executionInputs = append(executionInputs, "message")
		}
		if normalizeProviderOverride(req.Provider) != "" {
			executionInputs = append(executionInputs, "provider")
		}
		if normalizeModelOverride(req.Model) != "" {
			executionInputs = append(executionInputs, "model")
		}
		if strings.TrimSpace(req.SystemOverride) != "" {
			executionInputs = append(executionInputs, "system")
		}
		if len(executionInputs) > 0 {
			return fmt.Errorf("plan mode cancel cannot include execution inputs: %s", strings.Join(executionInputs, ", "))
		}
	}
	if req.ApprovePlan {
		planMode, err := r.store.LoadPlanMode(sessionID)
		if err != nil {
			return err
		}
		switch planMode.Status {
		case session.PlanModeStatusAwaitingApproval, session.PlanModeStatusApproved:
			if planMode.PlanVersion <= 0 || strings.TrimSpace(planMode.PlanMarkdown) == "" {
				return errors.New("plan mode has no submitted plan")
			}
		case session.PlanModeStatusExecuting:
			if planMode.ApprovedVersion <= 0 || strings.TrimSpace(planMode.PlanMarkdown) == "" {
				return errors.New("plan mode has no approved plan")
			}
		default:
			return fmt.Errorf("plan mode is not awaiting approval: %s", planMode.Status)
		}
	}
	if req.CancelPlan {
		if _, err := r.store.LoadPlanMode(sessionID); err != nil {
			return err
		}
	}
	if len(req.PlanInputAnswers) > 0 {
		if req.PlanInputRequestID == "" {
			return errors.New("plan input request_id is required")
		}
		planMode, err := r.store.LoadPlanMode(sessionID)
		if err != nil {
			return err
		}
		if planMode.PendingRequest == nil {
			recoverable, err := r.hasRecoverablePlanInputAnswer(sessionID, req.PlanInputRequestID, req.PlanInputAnswers)
			if err != nil {
				return err
			}
			if recoverable {
				return nil
			}
			return errors.New("plan mode has no pending input request")
		}
		if planMode.PendingRequest.RequestID != req.PlanInputRequestID {
			return fmt.Errorf("plan input request mismatch: %s", req.PlanInputRequestID)
		}
		if err := session.ValidatePlanModeAnswers(*planMode.PendingRequest, req.PlanInputAnswers); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) hasRecoverablePlanInputAnswer(sessionID, requestID string, answers []session.PlanModeInputAnswer) (bool, error) {
	answer, ok, err := r.matchingPlanInputAnswerHistory(sessionID, requestID, answers)
	if err != nil || !ok {
		return false, err
	}
	return r.hasToolResult(sessionID, answer.ToolCallID, "request_user_input")
}

func (r *Runner) ensurePlanModeCreatedForContinue(sessionID string, draft session.PlanModeDraft) (session.PlanModeState, error) {
	if current, err := r.store.LoadPlanMode(sessionID); err == nil && matchesPlanModeDraft(current, draft) {
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.created", current); err != nil {
			return session.PlanModeState{}, err
		}
		return current, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return session.PlanModeState{}, err
	}
	planMode, err := r.store.CreatePlanMode(sessionID, draft)
	if err != nil {
		return session.PlanModeState{}, err
	}
	if err := r.appendPlanModeEventOnce(sessionID, "planmode.created", planMode); err != nil {
		return session.PlanModeState{}, err
	}
	return planMode, nil
}

func matchesPlanModeDraft(planMode session.PlanModeState, draft session.PlanModeDraft) bool {
	if !draft.Enabled || !planMode.Enabled || planMode.Status != session.PlanModeStatusPlanning {
		return false
	}
	if strings.TrimSpace(planMode.Objective) != strings.TrimSpace(draft.Objective) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(planMode.Source), strings.TrimSpace(draft.Source))
}

func (r *Runner) ensurePlanModeCancelled(sessionID, source string) (session.PlanModeState, error) {
	planMode, err := r.store.LoadPlanMode(sessionID)
	if err != nil {
		return session.PlanModeState{}, err
	}
	if planMode.Status != session.PlanModeStatusCancelled {
		planMode, err = r.store.CancelPlanMode(sessionID, source)
		if err != nil {
			return session.PlanModeState{}, err
		}
	}
	if err := r.appendPlanModeEventOnce(sessionID, "planmode.cancelled", planMode); err != nil {
		return session.PlanModeState{}, err
	}
	return planMode, nil
}

func (r *Runner) ensurePlanModeRevisedForMessage(sessionID string, planMode session.PlanModeState, source, message string) (session.PlanModeState, bool, error) {
	switch planMode.Status {
	case session.PlanModeStatusAwaitingApproval, session.PlanModeStatusRejected, session.PlanModeStatusApproved:
		revised, err := r.store.RevisePlanMode(sessionID, source, message)
		if err != nil {
			return session.PlanModeState{}, false, err
		}
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.plan_revised", revised); err != nil {
			return session.PlanModeState{}, false, err
		}
		return revised, true, nil
	case session.PlanModeStatusPlanning:
		matchesHistory, err := r.hasMatchingPlanModeRevisionHistory(sessionID, planMode, message)
		if err != nil {
			return session.PlanModeState{}, false, err
		}
		if !matchesHistory {
			return planMode, false, nil
		}
		recorded, err := r.hasPlanModeRevisionMessage(sessionID, planMode)
		if err != nil {
			return session.PlanModeState{}, false, err
		}
		if recorded {
			return planMode, false, nil
		}
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.plan_revised", planMode); err != nil {
			return session.PlanModeState{}, false, err
		}
		return planMode, true, nil
	default:
		return planMode, false, nil
	}
}

func (r *Runner) hasMatchingPlanModeRevisionHistory(sessionID string, planMode session.PlanModeState, message string) (bool, error) {
	history, err := r.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		return false, fmt.Errorf("load planmode-history.jsonl: %w", err)
	}
	message = strings.TrimSpace(message)
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if item.Type != "planmode.plan_revised" {
			continue
		}
		if item.PlanModeID != planMode.PlanModeID || item.PlanVersion != planMode.PlanVersion {
			return false, nil
		}
		return strings.TrimSpace(fmt.Sprint(item.Data["message"])) == message, nil
	}
	return false, nil
}

func (r *Runner) hasPlanModeRevisionMessage(sessionID string, planMode session.PlanModeState) (bool, error) {
	messages, err := r.store.LoadMessages(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	planModeID := strings.TrimSpace(planMode.PlanModeID)
	if planModeID == "" || planMode.PlanVersion <= 0 {
		return false, nil
	}
	for _, msg := range messages {
		if msg.Role != "user" || msg.Meta == nil {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(msg.Meta["source"])) != "planmode_revision" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(msg.Meta["plan_mode_id"])) != planModeID {
			continue
		}
		if intFromEventData(msg.Meta, "plan_version") != planMode.PlanVersion {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (r *Runner) ensurePlanModeExecutingForApproval(sessionID, source string) (session.PlanModeState, error) {
	planMode, err := r.store.LoadPlanMode(sessionID)
	if err != nil {
		return session.PlanModeState{}, err
	}
	switch planMode.Status {
	case session.PlanModeStatusAwaitingApproval:
		approved, err := r.store.ApprovePlanMode(sessionID, source)
		if err != nil {
			return session.PlanModeState{}, err
		}
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.plan_approved", approved); err != nil {
			return session.PlanModeState{}, err
		}
		executing, err := r.store.MarkPlanModeExecuting(sessionID, source)
		if err != nil {
			return session.PlanModeState{}, err
		}
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.execution_started", executing); err != nil {
			return session.PlanModeState{}, err
		}
		return executing, nil
	case session.PlanModeStatusApproved:
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.plan_approved", planMode); err != nil {
			return session.PlanModeState{}, err
		}
		executing, err := r.store.MarkPlanModeExecuting(sessionID, source)
		if err != nil {
			return session.PlanModeState{}, err
		}
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.execution_started", executing); err != nil {
			return session.PlanModeState{}, err
		}
		return executing, nil
	case session.PlanModeStatusExecuting:
		if planMode.ApprovedVersion <= 0 || strings.TrimSpace(planMode.PlanMarkdown) == "" {
			return session.PlanModeState{}, errors.New("plan mode has no approved plan")
		}
		approvalEvent := planMode
		approvalEvent.Status = session.PlanModeStatusApproved
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.plan_approved", approvalEvent); err != nil {
			return session.PlanModeState{}, err
		}
		if err := r.appendPlanModeEventOnce(sessionID, "planmode.execution_started", planMode); err != nil {
			return session.PlanModeState{}, err
		}
		return planMode, nil
	default:
		return session.PlanModeState{}, fmt.Errorf("plan mode is not awaiting approval: %s", planMode.Status)
	}
}

func (r *Runner) appendPlanModeEventOnce(sessionID, eventType string, planMode session.PlanModeState) error {
	recorded, err := r.hasPlanModeEvent(sessionID, eventType, planMode)
	if err != nil {
		return err
	}
	if recorded {
		return nil
	}
	return r.appendEvent(sessionID, eventType, "planmode", planModeEventData(planMode))
}

func (r *Runner) hasPlanModeEvent(sessionID, eventType string, planMode session.PlanModeState) (bool, error) {
	eventType = strings.TrimSpace(eventType)
	planModeID := strings.TrimSpace(planMode.PlanModeID)
	if eventType == "" || planModeID == "" {
		return false, nil
	}
	items, err := r.store.LoadEvents(sessionID)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Type != eventType {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["plan_mode_id"])) != planModeID {
			continue
		}
		if eventType == "planmode.plan_approved" && intFromEventData(item.Data, "approved_version") != planMode.ApprovedVersion {
			continue
		}
		if eventType == "planmode.execution_started" && intFromEventData(item.Data, "approved_version") != planMode.ApprovedVersion {
			continue
		}
		if eventType == "planmode.plan_revised" && intFromEventData(item.Data, "plan_version") != planMode.PlanVersion {
			continue
		}
		return true, nil
	}
	return false, nil
}

func intFromEventData(data map[string]any, key string) int {
	switch value := data[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		out, _ := value.Int64()
		return int(out)
	default:
		return 0
	}
}

func (r *Runner) checkPlanModeGoalCoverage(sessionID string, override bool) error {
	planMode, err := r.store.LoadPlanMode(sessionID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(planMode.LinkedGoalID) == "" {
		return nil
	}
	goal, err := r.store.LoadGoal(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if goal.GoalID != planMode.LinkedGoalID || goal.Mission == nil {
		return nil
	}
	return ensureMissionCoverageForApproval(goal, override)
}

func ensureMissionCoverageForApproval(goal session.SessionGoal, override bool) error {
	coverage := session.CheckMissionPlanCoverage(goal)
	if !coverage.ApprovalBlocked || override {
		return nil
	}
	return fmt.Errorf("mission validation coverage blocks approval: %s", coverage.BlockingSummary())
}

func (r *Runner) approveLinkedMissionPlan(sessionID string, planMode session.PlanModeState, source string, overrideCoverage bool) error {
	if strings.TrimSpace(planMode.LinkedGoalID) == "" {
		return nil
	}
	goal, err := r.store.LoadGoal(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if goal.GoalID != planMode.LinkedGoalID || goal.Mission == nil {
		return nil
	}
	if err := ensureMissionCoverageForApproval(goal, overrideCoverage); err != nil {
		return err
	}
	approvedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if missionPlanApprovalMatches(goal, planMode) {
		hasApprovalHistory, err := r.hasMissionPlanApprovedHistory(sessionID, goal.GoalID, planMode)
		if err != nil {
			return err
		}
		if hasApprovalHistory {
			return r.appendMissionPlanApprovedEventOnce(sessionID, goal.GoalID, planMode, goal.Mission.ApprovedAt, overrideCoverage)
		}
	}
	goal, err = r.store.ApproveMissionPlan(sessionID, session.MissionPlanApprovalInput{
		Source:           session.GoalSourceSystem,
		ApprovedSource:   source,
		ApprovedAt:       approvedAt,
		CoverageOverride: overrideCoverage,
		PlanModeID:       planMode.PlanModeID,
		ApprovedVersion:  planMode.ApprovedVersion,
	})
	if err != nil {
		return err
	}
	return r.appendMissionPlanApprovedEventOnce(sessionID, goal.GoalID, planMode, approvedAt, overrideCoverage)
}

func missionPlanApprovalMatches(goal session.SessionGoal, planMode session.PlanModeState) bool {
	if goal.Mission == nil || session.NormalizeMissionPlanStatus(goal.Mission.PlanStatus) != session.MissionPlanStatusApproved {
		return false
	}
	if strings.TrimSpace(goal.Mission.ApprovedAt) == "" {
		return false
	}
	return strings.TrimSpace(planMode.PlanModeID) != "" && planMode.ApprovedVersion > 0
}

func (r *Runner) hasMissionPlanApprovedHistory(sessionID, goalID string, planMode session.PlanModeState) (bool, error) {
	history, err := r.store.LoadGoalHistory(sessionID)
	if err != nil {
		return false, fmt.Errorf("load goal-history.jsonl: %w", err)
	}
	goalID = strings.TrimSpace(goalID)
	planModeID := strings.TrimSpace(planMode.PlanModeID)
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if item.Type != "mission.plan.approved" {
			continue
		}
		if strings.TrimSpace(item.GoalID) != goalID {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["plan_mode_id"])) != planModeID {
			continue
		}
		if intFromEventData(item.Data, "approved_version") != planMode.ApprovedVersion {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (r *Runner) appendMissionPlanApprovedEventOnce(sessionID, goalID string, planMode session.PlanModeState, approvedAt string, overrideCoverage bool) error {
	recorded, err := r.hasMissionPlanApprovedEvent(sessionID, goalID, planMode)
	if err != nil {
		return err
	}
	if recorded {
		return nil
	}
	return r.appendEvent(sessionID, "mission.plan.approved", "planmode", map[string]any{
		"goal_id":           goalID,
		"plan_mode_id":      planMode.PlanModeID,
		"approved_version":  planMode.ApprovedVersion,
		"approved_at":       approvedAt,
		"coverage_override": overrideCoverage,
	})
}

func (r *Runner) hasMissionPlanApprovedEvent(sessionID, goalID string, planMode session.PlanModeState) (bool, error) {
	items, err := r.store.LoadEvents(sessionID)
	if err != nil {
		return false, err
	}
	goalID = strings.TrimSpace(goalID)
	planModeID := strings.TrimSpace(planMode.PlanModeID)
	for _, item := range items {
		if item.Type != "mission.plan.approved" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["goal_id"])) != goalID {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["plan_mode_id"])) != planModeID {
			continue
		}
		if intFromEventData(item.Data, "approved_version") != planMode.ApprovedVersion {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (r *Runner) appendPlanInputCancelToolResult(sessionID, source string) error {
	planMode, err := r.store.LoadPlanMode(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if planMode.PendingRequest == nil || strings.TrimSpace(planMode.PendingRequest.ToolCallID) == "" {
		return nil
	}
	request := *planMode.PendingRequest
	toolResultExists, err := r.hasToolResult(sessionID, request.ToolCallID, "request_user_input")
	if err != nil {
		return err
	}
	if !toolResultExists {
		result := session.ToolResult{
			ToolCallID:    request.ToolCallID,
			Name:          "request_user_input",
			LLMOutput:     "Error: Plan Mode input was cancelled by the user.",
			DisplayOutput: "Error: Plan Mode input was cancelled by the user.",
			IsError:       true,
			Metadata: map[string]any{
				"planmode":          true,
				"planmode_terminal": planModeTerminalPlanCancelled,
				"request_id":        request.RequestID,
				"cancelled":         true,
				"recovered":         true,
				"plan_mode_id":      planMode.PlanModeID,
			},
		}
		if err := r.store.AppendMessage(sessionID, session.NewToolMessage([]session.ToolResult{result})); err != nil {
			return err
		}
	}
	historyExists, err := r.hasPlanModeInputCancelHistory(sessionID, planMode.PlanModeID, request.RequestID, request.ToolCallID)
	if err != nil {
		return err
	}
	if !historyExists {
		if err := r.store.AppendPlanModeHistory(sessionID, session.PlanModeHistoryEntry{
			PlanModeID: planMode.PlanModeID,
			Type:       "planmode.input_cancelled",
			Source:     source,
			Status:     planMode.Status,
			Data: map[string]any{
				"request_id":   request.RequestID,
				"tool_call_id": request.ToolCallID,
			},
		}); err != nil {
			return err
		}
	}
	if err := r.appendPlanInputCancelledEventOnce(sessionID, planMode.PlanModeID, request.RequestID); err != nil {
		return err
	}
	return nil
}

func (r *Runner) appendPlanInputCancelledEventOnce(sessionID, planModeID, requestID string) error {
	recorded, err := r.hasPlanModeInputCancelEvent(sessionID, planModeID, requestID)
	if err != nil {
		return err
	}
	if recorded {
		return nil
	}
	return r.appendEvent(sessionID, "planmode.input_cancelled", "plan_input", map[string]any{
		"plan_mode_id": planModeID,
		"request_id":   requestID,
		"recovered":    true,
	})
}

func (r *Runner) hasPlanModeInputCancelHistory(sessionID, planModeID, requestID, toolCallID string) (bool, error) {
	history, err := r.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		return false, err
	}
	planModeID = strings.TrimSpace(planModeID)
	requestID = strings.TrimSpace(requestID)
	toolCallID = strings.TrimSpace(toolCallID)
	for _, item := range history {
		if item.Type != "planmode.input_cancelled" {
			continue
		}
		if strings.TrimSpace(item.PlanModeID) != planModeID {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["request_id"])) != requestID {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["tool_call_id"])) != toolCallID {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (r *Runner) hasPlanModeInputCancelEvent(sessionID, planModeID, requestID string) (bool, error) {
	events, err := r.store.LoadEvents(sessionID)
	if err != nil {
		return false, err
	}
	planModeID = strings.TrimSpace(planModeID)
	requestID = strings.TrimSpace(requestID)
	for _, item := range events {
		if item.Type != "planmode.input_cancelled" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["plan_mode_id"])) != planModeID {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["request_id"])) != requestID {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (r *Runner) hasToolResult(sessionID, toolCallID, name string) (bool, error) {
	toolCallID = strings.TrimSpace(toolCallID)
	name = strings.TrimSpace(name)
	if toolCallID == "" || name == "" {
		return false, nil
	}
	messages, err := r.store.LoadMessages(sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, msg := range messages {
		for _, result := range msg.ToolResults {
			if result.ToolCallID == toolCallID && result.Name == name {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *Runner) appendPlanInputToolResult(sessionID, requestID, source string, answers []session.PlanModeInputAnswer) error {
	planModeSnapshot, err := r.store.SnapshotPlanMode(sessionID)
	if err != nil {
		return err
	}
	planModeHistory, err := r.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		return err
	}
	planMode, request, err := r.store.AnswerPlanModeInput(sessionID, requestID, source, answers)
	if err != nil {
		if recovered, recoverErr := r.recoverPlanInputAnswerEvent(sessionID, requestID, answers); recoverErr != nil || recovered {
			return recoverErr
		}
		return err
	}
	result := session.ToolResult{
		ToolCallID:    request.ToolCallID,
		Name:          "request_user_input",
		LLMOutput:     session.PlanModeAnswersJSON(answers),
		DisplayOutput: session.PlanModeAnswersJSON(answers),
		Metadata: map[string]any{
			"planmode":     true,
			"request_id":   request.RequestID,
			"recovered":    true,
			"plan_mode_id": planMode.PlanModeID,
		},
	}
	if err := r.store.AppendMessage(sessionID, session.NewToolMessage([]session.ToolResult{result})); err != nil {
		if restoreErr := r.restorePlanInputAnswerAfterMessageError(sessionID, planModeSnapshot, planModeHistory, err); restoreErr != nil {
			return restoreErr
		}
		return err
	}
	return r.appendPlanInputAnsweredEventOnce(sessionID, planMode.PlanModeID, request.RequestID, answers)
}

func (r *Runner) recoverPlanInputAnswerEvent(sessionID, requestID string, answers []session.PlanModeInputAnswer) (bool, error) {
	answer, ok, err := r.matchingPlanInputAnswerHistory(sessionID, requestID, answers)
	if err != nil || !ok {
		return false, err
	}
	exists, err := r.hasToolResult(sessionID, answer.ToolCallID, "request_user_input")
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if err := r.appendPlanInputAnsweredEventOnce(sessionID, answer.PlanModeID, answer.RequestID, answers); err != nil {
		return false, err
	}
	return true, nil
}

type planInputAnswerRecord struct {
	PlanModeID string
	RequestID  string
	ToolCallID string
}

func (r *Runner) matchingPlanInputAnswerHistory(sessionID, requestID string, answers []session.PlanModeInputAnswer) (planInputAnswerRecord, bool, error) {
	history, err := r.store.LoadPlanModeHistory(sessionID)
	if err != nil {
		return planInputAnswerRecord{}, false, err
	}
	requestID = strings.TrimSpace(requestID)
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if item.Type != "planmode.input_answered" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["request_id"])) != requestID {
			continue
		}
		if !planInputAnswersEqual(item.Data["answers"], answers) {
			return planInputAnswerRecord{}, false, nil
		}
		toolCallID := matchingPlanInputToolCallID(history, item.PlanModeID, requestID)
		if strings.TrimSpace(toolCallID) == "" {
			return planInputAnswerRecord{}, false, nil
		}
		return planInputAnswerRecord{
			PlanModeID: strings.TrimSpace(item.PlanModeID),
			RequestID:  requestID,
			ToolCallID: toolCallID,
		}, true, nil
	}
	return planInputAnswerRecord{}, false, nil
}

func matchingPlanInputToolCallID(history []session.PlanModeHistoryEntry, planModeID, requestID string) string {
	planModeID = strings.TrimSpace(planModeID)
	requestID = strings.TrimSpace(requestID)
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if item.Type != "planmode.input_requested" {
			continue
		}
		if strings.TrimSpace(item.PlanModeID) != planModeID {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["request_id"])) != requestID {
			continue
		}
		return strings.TrimSpace(fmt.Sprint(item.Data["tool_call_id"]))
	}
	return ""
}

func planInputAnswersEqual(raw any, answers []session.PlanModeInputAnswer) bool {
	data, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	var recorded []session.PlanModeInputAnswer
	if err := json.Unmarshal(data, &recorded); err != nil {
		return false
	}
	return reflect.DeepEqual(recorded, answers)
}

func (r *Runner) appendPlanInputAnsweredEventOnce(sessionID, planModeID, requestID string, answers []session.PlanModeInputAnswer) error {
	recorded, err := r.hasPlanModeInputAnsweredEvent(sessionID, planModeID, requestID)
	if err != nil {
		return err
	}
	if recorded {
		return nil
	}
	return r.appendEvent(sessionID, "planmode.input_answered", "plan_input", map[string]any{
		"plan_mode_id": planModeID,
		"request_id":   requestID,
		"answers":      answers,
		"recovered":    true,
	})
}

func (r *Runner) hasPlanModeInputAnsweredEvent(sessionID, planModeID, requestID string) (bool, error) {
	events, err := r.store.LoadEvents(sessionID)
	if err != nil {
		return false, err
	}
	planModeID = strings.TrimSpace(planModeID)
	requestID = strings.TrimSpace(requestID)
	for _, item := range events {
		if item.Type != "planmode.input_answered" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["plan_mode_id"])) != planModeID {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(item.Data["request_id"])) != requestID {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (r *Runner) restorePlanInputAnswerAfterMessageError(sessionID string, snapshot session.PlanModeSnapshot, history []session.PlanModeHistoryEntry, cause error) error {
	if err := r.store.RestorePlanModeSnapshot(sessionID, snapshot); err != nil {
		return fmt.Errorf("restore plan mode after input tool result append error %v: %w", cause, err)
	}
	if err := r.store.RestorePlanModeHistory(sessionID, history); err != nil {
		return fmt.Errorf("restore plan mode history after input tool result append error %v: %w", cause, err)
	}
	return nil
}

func (r *Runner) runExisting(ctx context.Context, meta session.SessionMetadata, state session.State, systemOverride string, planInputHandler PlanInputHandler) (RunResult, error) {
	catalog, err := skills.Scan(r.cfg.Skills.Dirs)
	if err != nil {
		return RunResult{}, err
	}
	registry, err := tools.NewRegistry(r.cfg, catalog, r.store, r, meta.Workdir)
	if err != nil {
		return RunResult{}, err
	}
	hookManager := hooks.New(r.cfg.Hooks, meta.Workdir)
	adapter, err := r.adapterForSession(meta)
	if err != nil {
		return RunResult{}, err
	}
	if strings.TrimSpace(meta.QueueJobID) == "" {
		releaseAutoWorker := r.startAutoQueueWorker()
		defer releaseAutoWorker()
	}
	watcherCtx, cancelWatcher := context.WithCancel(ctx)
	watcherDone := make(chan struct{})
	defer func() {
		cancelWatcher()
		<-watcherDone
	}()
	go func() {
		defer close(watcherDone)
		r.watchSteer(watcherCtx, meta.ID)
	}()
	r.setPlanInputHandler(meta.ID, planInputHandler)
	defer r.clearPlanInputHandler(meta.ID)
	if err := r.appendEvent(meta.ID, "session.started", "prepare", map[string]any{
		"provider": meta.Provider,
		"model":    meta.Model,
	}); err != nil {
		return r.failBeforeRun(meta.ID, state, "prepare", fmt.Errorf("record session.started event: %w", err))
	}
	return r.engine.Run(ctx, meta, state, systemOverride, adapter, catalog, registry, hookManager)
}

func (r *Runner) Steer(_ context.Context, req SteerRequest) (SteerResult, error) {
	if stringsTrim(req.Message) == "" {
		return SteerResult{}, errors.New("steer message is required")
	}
	actualChars := utf8.RuneCountInString(req.Message)
	if actualChars > defaultSteerMaxMessageChars {
		return SteerResult{}, SteerValidationError{
			Code:        "steer_input_too_large",
			MaxChars:    defaultSteerMaxMessageChars,
			ActualChars: actualChars,
		}
	}
	meta, err := r.store.LoadMetadata(req.SessionID)
	if err != nil {
		return SteerResult{}, err
	}
	state, err := r.store.LoadState(req.SessionID)
	if err != nil {
		return SteerResult{}, err
	}
	if state.Status != session.StatusRunning {
		return SteerResult{}, errors.New("session is not running; use continue instead")
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "cli"
	}
	request := session.NewSteerRequestWithSource(source, req.Message, req.Interrupt)
	if err := r.store.AppendSteerRequest(req.SessionID, request); err != nil {
		return SteerResult{}, err
	}
	if _, err := r.store.RefreshPendingSteerCount(req.SessionID); err != nil {
		if rejectErr := r.rejectQueuedSteerRequest(req.SessionID, request.ID); rejectErr != nil {
			return SteerResult{}, fmt.Errorf("refresh pending steer count after rejecting queued steer failed with %v: %w", rejectErr, err)
		}
		return SteerResult{}, err
	}
	if err := r.appendEvent(req.SessionID, "session.steer.requested", "control", map[string]any{
		"id":        request.ID,
		"interrupt": request.Interrupt,
	}); err != nil {
		if rejectErr := r.rejectQueuedSteerRequest(req.SessionID, request.ID); rejectErr != nil {
			return SteerResult{}, fmt.Errorf("record steer requested event after rejecting queued steer failed with %v: %w", rejectErr, err)
		}
		return SteerResult{}, err
	}
	if err := r.appendEvent(req.SessionID, "session.steer.queued", "control", map[string]any{
		"id": request.ID,
	}); err != nil {
		if rejectErr := r.rejectQueuedSteerRequest(req.SessionID, request.ID); rejectErr != nil {
			return SteerResult{}, fmt.Errorf("record steer queued event after rejecting queued steer failed with %v: %w", rejectErr, err)
		}
		return SteerResult{}, err
	}
	_ = writeSessionSummary(r.store, meta.ID)
	return SteerResult{
		SessionID: req.SessionID,
		Accepted:  true,
		Behavior:  "queued",
	}, nil
}

func (r *Runner) rejectQueuedSteerRequest(sessionID, requestID string) error {
	requests, err := r.store.LoadSteerRequests(sessionID)
	if err != nil {
		return err
	}
	changed := false
	for i := range requests {
		if requests[i].ID != requestID {
			continue
		}
		requests[i].Status = session.SteerStatusRejected
		changed = true
		break
	}
	if !changed {
		return nil
	}
	if err := r.store.UpdateSteerRequests(sessionID, requests); err != nil {
		return err
	}
	_, err = r.store.RefreshPendingSteerCount(sessionID)
	return err
}

func (r *Runner) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	providerName := req.Provider
	if providerName == "" {
		providerName = r.cfg.DefaultProvider
	}
	providerCfg, err := r.providerConfig(providerName, req.BaseURL, req.APIKeyEnv, req.APIProvider, req.WireAPI, req.Model)
	if err != nil {
		return ProbeResult{}, WrapConfigError(err)
	}
	model := req.Model
	if model == "" {
		model = providerCfg.Model
	}
	adapter, err := r.adapterFromConfig(providerName, providerCfg)
	if err != nil {
		return ProbeResult{}, WrapConfigError(err)
	}
	prompt := req.Prompt
	if strings.TrimSpace(prompt) == "" {
		if req.ThinkingProbe {
			prompt = "Answer in one short sentence: what is 2+2? Use your configured reasoning summary or thinking mode if available."
		} else {
			prompt = "Return exactly one finish tool call with message: provider probe ok"
		}
	}
	tools := []provider.ToolSchema(nil)
	if !req.ThinkingProbe {
		tools = []provider.ToolSchema{
			{
				Name:        "finish",
				Description: "Explicitly mark the current task as complete.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"message": map[string]any{"type": "string"},
					},
					"required": []string{"message"},
				},
			},
		}
	}
	reasoningSummary := strings.TrimSpace(firstNonEmpty(req.ReasoningSummary, providerCfg.ReasoningSummary))
	apiProvider, _ := config.EffectiveAPIProvider(providerName, providerCfg)
	result, err := adapter.RunTurn(ctx, provider.TurnRequest{
		SessionID:    "probe",
		Model:        model,
		SystemPrompt: "You are a provider probe. Follow the user instruction exactly.",
		Messages: []session.Message{
			session.NewMessage("user", prompt),
		},
		Tools:            tools,
		Temperature:      providerCfg.Temperature,
		TopP:             providerCfg.TopP,
		MaxOutputTokens:  providerCfg.MaxOutputTokens,
		APIProvider:      apiProvider,
		ReasoningEffort:  strings.TrimSpace(providerCfg.ReasoningEffort),
		ReasoningSummary: reasoningSummary,
		TextVerbosity:    strings.TrimSpace(providerCfg.TextVerbosity),
		ThinkingBudget:   providerCfg.ThinkingBudget,
		IncludeThoughts:  providerCfg.IncludeThoughts,
		PromptCache:      defaultPromptCacheForAPIProvider(apiProvider, providerCfg.PromptCache),
		Store:            defaultStoreForAPIProvider(apiProvider, providerCfg.Store),
	}, func(string, map[string]any) {})
	if err != nil {
		return ProbeResult{}, WrapProviderError(err)
	}

	out := ProbeResult{
		Provider:         providerName,
		Model:            model,
		BaseURL:          providerCfg.BaseURL,
		APIProvider:      apiProvider,
		WireAPI:          providerCfg.WireAPI,
		StopReason:       result.StopReason,
		Text:             result.Text,
		Thinking:         result.Thinking,
		ReasoningSummary: reasoningSummary,
		Usage:            result.Usage,
	}
	annotateThinkingProbeResult(&out, result)
	if req.ThinkingProbe {
		return out, nil
	}
	for _, call := range result.ToolCalls {
		out.ToolCallNames = append(out.ToolCallNames, call.Name)
		if call.Name == "finish" {
			var payload struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(call.Arguments, &payload); err == nil {
				out.FinishMessage = payload.Message
			}
		}
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "finish" {
		return out, errors.New("probe failed: provider did not return exactly one finish tool call")
	}
	return out, nil
}

func annotateThinkingProbeResult(out *ProbeResult, result provider.TurnResult) {
	if out == nil {
		return
	}
	out.ThinkingVisibleObserved = strings.TrimSpace(result.Thinking) != ""
	out.ReasoningSummaryObserved = intFromRaw(result.RawProvider, "reasoning_summary_count") > 0 || intFromRaw(result.RawProvider, "reasoning_text_count") > 0
	out.ReasoningEncryptedObserved = intFromRaw(result.RawProvider, "reasoning_encrypted_count") > 0
	out.ReasoningTokens = intFromRaw(result.RawProvider, "reasoning_tokens")
	out.ThinkingStrategy = stringFromRaw(result.RawProvider, "thinking_strategy")
	out.ThinkingReplayObserved = out.ReasoningEncryptedObserved || boolFromRaw(result.RawProvider, "thinking_replay_observed")
	if boolFromRaw(result.RawProvider, "thinking_visible_observed") {
		out.ThinkingVisibleObserved = true
	}
	switch {
	case out.ThinkingVisibleObserved:
		out.ThinkingDetail = "readable thinking returned"
	case out.ThinkingReplayObserved:
		out.ThinkingDetail = "replay-only thinking returned"
	default:
		out.ThinkingDetail = "provider accepted request but returned no readable thinking in this probe"
	}
}

func stringFromRaw(raw map[string]any, key string) string {
	value, _ := raw[key].(string)
	return strings.TrimSpace(value)
}

func intFromRaw(raw map[string]any, key string) int {
	switch value := raw[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func boolFromRaw(raw map[string]any, key string) bool {
	value, _ := raw[key].(bool)
	return value
}

func (r *Runner) Interrupt(sessionID string) error {
	return r.InterruptWithReason(sessionID, "keyboard_interrupt")
}

func (r *Runner) InterruptWithReason(sessionID, reason string) error {
	if _, err := r.store.LoadMetadata(sessionID); err != nil {
		return err
	}
	state, err := r.store.LoadState(sessionID)
	if err != nil {
		return err
	}
	if state.Status != session.StatusRunning {
		return errors.New("session is not running")
	}
	r.control.requestPauseWithReason(reason)
	return nil
}

func (r *Runner) State(sessionID string) (session.State, error) {
	if _, err := r.store.LoadMetadata(sessionID); err != nil {
		return session.State{}, err
	}
	return r.store.LoadState(sessionID)
}

func (r *Runner) Tasks(sessionID string) (session.TaskBoard, error) {
	if _, err := r.store.LoadMetadata(sessionID); err != nil {
		return session.TaskBoard{}, err
	}
	todo, err := r.store.LoadTodo(sessionID)
	if err != nil {
		return session.TaskBoard{}, err
	}
	tasks, err := r.store.ListTasks(sessionID)
	if err != nil {
		return session.TaskBoard{}, err
	}
	return session.BuildTaskBoard(todo, tasks), nil
}

func (r *Runner) List(limit int) ([]session.SessionSummary, error) {
	return r.store.List(limit)
}

func (r *Runner) Store() *session.Store {
	return r.store
}

func (r *Runner) AutoContinue(ctx context.Context, sessionID string) (RunResult, error) {
	_, err := r.store.LoadMetadata(sessionID)
	if err != nil {
		return RunResult{}, err
	}
	state, err := r.store.LoadState(sessionID)
	if err != nil {
		return RunResult{}, err
	}
	if state.Status != session.StatusFailed {
		return RunResult{}, errors.New("auto-continue requires failed status")
	}
	if state.IncompleteReason != "incomplete_no_finish" {
		return RunResult{}, errors.New("auto-continue requires incomplete_no_finish reason")
	}
	if state.RalphLoopCount >= r.cfg.Runtime.RalphLoop.MaxIterations {
		if err := r.appendEvent(sessionID, events.EventRalphLoopExhausted, "ralph_loop", map[string]any{
			"count":          state.RalphLoopCount,
			"max_iterations": r.cfg.Runtime.RalphLoop.MaxIterations,
		}); err != nil {
			return RunResult{}, fmt.Errorf("record %s event: %w", events.EventRalphLoopExhausted, err)
		}
		return RunResult{}, fmt.Errorf("ralph loop exhausted after %d iterations", state.RalphLoopCount)
	}
	messages, err := r.store.LoadMessages(sessionID)
	if err != nil {
		return RunResult{}, err
	}
	if len(messages) == 0 {
		return RunResult{}, errors.New("no messages found for auto-continue")
	}
	originalPrompt := messages[0].Text
	previousState := state
	state.RalphLoopCount++
	if err := r.store.SaveState(sessionID, state); err != nil {
		return RunResult{}, err
	}
	if err := r.appendEvent(sessionID, events.EventRalphLoopTriggered, "ralph_loop", map[string]any{
		"count":          state.RalphLoopCount,
		"max_iterations": r.cfg.Runtime.RalphLoop.MaxIterations,
	}); err != nil {
		if rollbackErr := r.store.SaveState(sessionID, previousState); rollbackErr != nil {
			return RunResult{}, fmt.Errorf("restore state after %s event error %v: %w", events.EventRalphLoopTriggered, err, rollbackErr)
		}
		return RunResult{}, fmt.Errorf("record %s event: %w", events.EventRalphLoopTriggered, err)
	}
	result, err := r.Continue(ctx, ContinueRequest{
		SessionID: sessionID,
		Message:   originalPrompt,
	})
	if err == nil && result.Status == session.StatusCompleted {
		if appendErr := r.appendEvent(sessionID, events.EventRalphLoopCompleted, "ralph_loop", map[string]any{
			"count": state.RalphLoopCount,
		}); appendErr != nil {
			return RunResult{}, fmt.Errorf("record %s event: %w", events.EventRalphLoopCompleted, appendErr)
		}
	}
	return result, err
}

func (r *Runner) watchSteer(ctx context.Context, sessionID string) {
	interval := time.Duration(r.cfg.Runtime.Steer.PollIntervalMS) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	seenInterrupts := map[string]struct{}{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requests, err := r.store.PendingSteerRequests(sessionID)
			if err != nil {
				continue
			}
			for _, req := range requests {
				if !req.Interrupt {
					continue
				}
				if _, ok := seenInterrupts[req.ID]; ok {
					continue
				}
				if err := r.appendEvent(sessionID, "session.steer.interrupt_requested", "control", map[string]any{
					"id": req.ID,
				}); err != nil {
					continue
				}
				seenInterrupts[req.ID] = struct{}{}
				r.control.requestSteerInterrupt()
			}
		}
	}
}

func (r *Runner) appendUserMessage(ctx context.Context, meta session.SessionMetadata, phase, text string, extraMeta map[string]any) error {
	text, err := r.transformUserMessage(ctx, meta, phase, text)
	if err != nil {
		return err
	}
	if stringsTrim(text) == "" {
		return nil
	}
	msg := session.NewMessage("user", text)
	if len(extraMeta) > 0 {
		msg.Meta = extraMeta
	}
	if err := r.store.AppendMessage(meta.ID, msg); err != nil {
		return err
	}
	data := map[string]any{
		"text": text,
		"mode": meta.Mode,
	}
	for key, value := range extraMeta {
		data[key] = value
	}
	if err := r.appendEvent(meta.ID, "user.message", phase, data); err != nil {
		if rollbackErr := r.store.RemoveLastMessageIfID(meta.ID, msg.ID); rollbackErr != nil {
			return fmt.Errorf("record user.message event after rolling back message failed with %v: %w", rollbackErr, err)
		}
		return fmt.Errorf("record user.message event: %w", err)
	}
	return nil
}

func (r *Runner) transformUserMessage(ctx context.Context, meta session.SessionMetadata, phase, text string) (string, error) {
	hookManager := hooks.New(r.cfg.Hooks, meta.Workdir)
	hookManager.SetEmitter(func(eventType string, data map[string]any) error {
		return r.appendEvent(meta.ID, eventType, phase, data)
	})
	payload, err := hookManager.Trigger(ctx, "user.message", map[string]any{
		"session_id": meta.ID,
		"text":       text,
		"mode":       meta.Mode,
	})
	if err != nil {
		return "", err
	}
	if value, ok := payload["text"].(string); ok {
		return value, nil
	}
	return text, nil
}

func (r *Runner) emit(sessionID, eventType, phase string, data map[string]any) {
	evt := events.New(sessionID, eventType, phase, data)
	_ = r.store.AppendEvent(sessionID, evt)
	r.bus.Publish(evt)
}

func (r *Runner) failBeforeRun(sessionID string, state session.State, phase string, err error) (RunResult, error) {
	state.Status = session.StatusFailed
	state.Phase = phase
	state.LastError = err.Error()
	if saveErr := r.store.SaveState(sessionID, state); saveErr != nil {
		return RunResult{}, fmt.Errorf("record pre-run failure state after %v: %w", err, saveErr)
	}
	if appendErr := r.appendEvent(sessionID, "session.failed", phase, map[string]any{"error": err.Error()}); appendErr != nil {
		return RunResult{}, fmt.Errorf("record pre-run failure event after %v: %w", err, appendErr)
	}
	_ = writeSessionSummary(r.store, sessionID)
	_ = writeLongRunCheckpoint(r.store, sessionID)
	return RunResult{
		SessionID: sessionID,
		Status:    state.Status,
		LastError: state.LastError,
	}, err
}

func (r *Runner) appendEvent(sessionID, eventType, phase string, data map[string]any) error {
	evt := events.New(sessionID, eventType, phase, data)
	if err := r.store.AppendEvent(sessionID, evt); err != nil {
		return err
	}
	r.bus.Publish(evt)
	return nil
}

func (r *Runner) appendEvents(sessionID string, items []events.Event) error {
	if len(items) == 0 {
		return nil
	}
	if err := r.store.AppendEvents(sessionID, items); err != nil {
		return err
	}
	for _, evt := range items {
		r.bus.Publish(evt)
	}
	return nil
}

func (r *Runner) adapter(name string) (provider.Adapter, error) {
	cfg, err := r.cfg.ProviderConfig(name)
	if err != nil {
		return nil, WrapConfigError(err)
	}
	return r.adapterFromConfig(name, cfg)
}

func (r *Runner) adapterForSession(meta session.SessionMetadata) (provider.Adapter, error) {
	cfg, err := r.cfg.ProviderConfig(meta.Provider)
	if err != nil {
		return nil, WrapConfigError(err)
	}
	cfg = applySessionProviderOptions(cfg, meta.ProviderOptions)
	return r.adapterFromConfig(meta.Provider, cfg)
}

func (r *Runner) mergedSessionProviderOptions(providerName string, current session.ProviderOptions) (session.ProviderOptions, error) {
	providerCfg, err := r.cfg.ProviderConfig(providerName)
	if err != nil {
		return session.ProviderOptions{}, WrapConfigError(err)
	}
	merged, err := resolvedProviderOptions(providerName, providerCfg, current)
	if err != nil {
		return session.ProviderOptions{}, err
	}
	if current == (session.ProviderOptions{}) {
		return merged, nil
	}
	return preserveDurableProviderOptionDefaults(merged, current), nil
}

func (r *Runner) providerConfig(name, baseURL, apiKeyEnv, apiProvider, wireAPI, model string) (config.Provider, error) {
	cfg, err := r.cfg.ProviderConfig(name)
	if err != nil {
		return config.Provider{}, WrapConfigError(err)
	}
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if apiKeyEnv != "" {
		cfg.APIKeyEnv = apiKeyEnv
	}
	if apiProvider != "" {
		cfg.APIProvider = apiProvider
	}
	if wireAPI != "" {
		cfg.WireAPI = wireAPI
	}
	if model != "" {
		cfg.Model = model
	}
	return cfg, nil
}

func (r *Runner) adapterFromConfig(name string, cfg config.Provider) (provider.Adapter, error) {
	client := &http.Client{}
	retryCfg := providerRetryConfig(cfg)
	apiProvider, err := config.EffectiveAPIProvider(name, cfg)
	if err != nil {
		return nil, WrapConfigError(err)
	}
	switch apiProvider {
	case "openai-compatible":
		if cfg.WireAPI != "" && cfg.WireAPI != "responses" {
			return nil, WrapConfigError(errors.New("unsupported openai-compatible wire_api: " + cfg.WireAPI))
		}
		return provider.NewOpenAIWithRetry(cfg.BaseURL, cfg.ResolvedAPIKey(), client, retryCfg), nil
	case "anthropic-compatible":
		return provider.NewAnthropicWithRetry(cfg.BaseURL, cfg.ResolvedAPIKey(), cfg.AnthropicVersion, client, retryCfg), nil
	case "google":
		return provider.NewGoogleWithRetry(cfg.BaseURL, cfg.ResolvedAPIKey(), client, retryCfg), nil
	default:
		return nil, WrapConfigError(fmt.Errorf("unsupported api_provider for %s: %s", name, apiProvider))
	}
}

func completionPolicy(mode string) string {
	if mode == session.ModeExec || mode == session.ModeInit {
		return session.CompletionPolicyAutonomous
	}
	return session.CompletionPolicyInteractive
}

func providerRetryConfig(cfg config.Provider) provider.RetryConfig {
	baseDelay := time.Second
	if cfg.Retry.BaseDelayMS > 0 {
		baseDelay = time.Duration(cfg.Retry.BaseDelayMS) * time.Millisecond
	}
	maxAttempts := cfg.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	return provider.RetryConfig{
		MaxAttempts:       maxAttempts,
		BaseDelay:         baseDelay,
		Retry429:          cfg.Retry.Retry429,
		Retry5xx:          cfg.Retry.Retry5xx,
		RetryTransport:    cfg.Retry.RetryTransport,
		RequestTimeout:    time.Duration(providerRequestTimeout(cfg)) * time.Second,
		StreamIdleTimeout: time.Duration(providerStreamIdleTimeoutMS(cfg)) * time.Millisecond,
	}
}

func providerRequestTimeout(cfg config.Provider) int {
	if cfg.RequestTimeoutSec > 0 {
		return cfg.RequestTimeoutSec
	}
	if cfg.TimeoutSec > 0 {
		return cfg.TimeoutSec
	}
	return 300
}

func providerStreamIdleTimeoutMS(cfg config.Provider) int {
	if cfg.StreamIdleTimeoutMS > 0 {
		return cfg.StreamIdleTimeoutMS
	}
	return 300000
}

func providerRetryPolicy(cfg config.Provider) *session.ProviderRetryPolicy {
	retryCfg := providerRetryConfig(cfg)
	return &session.ProviderRetryPolicy{
		MaxAttempts:    retryCfg.MaxAttempts,
		BaseDelayMS:    int(retryCfg.BaseDelay / time.Millisecond),
		Retry429:       cfg.Retry.Retry429,
		Retry5xx:       cfg.Retry.Retry5xx,
		RetryTransport: cfg.Retry.RetryTransport,
	}
}

func providerTimeoutPolicy(cfg config.Provider) *session.ProviderTimeoutPolicy {
	return &session.ProviderTimeoutPolicy{
		TimeoutSec:          cfg.TimeoutSec,
		RequestTimeoutSec:   providerRequestTimeout(cfg),
		StreamIdleTimeoutMS: providerStreamIdleTimeoutMS(cfg),
	}
}

func applySessionProviderOptions(cfg config.Provider, opts session.ProviderOptions) config.Provider {
	if strings.TrimSpace(opts.APIProvider) != "" {
		cfg.APIProvider = opts.APIProvider
	}
	if strings.TrimSpace(opts.BaseURL) != "" {
		cfg.BaseURL = opts.BaseURL
	}
	cfg.Temperature = opts.Temperature
	cfg.TopP = opts.TopP
	if opts.MaxOutputTokens > 0 {
		cfg.MaxOutputTokens = opts.MaxOutputTokens
	}
	cfg.ReasoningEffort = opts.ReasoningEffort
	cfg.ReasoningSummary = opts.ReasoningSummary
	cfg.TextVerbosity = opts.TextVerbosity
	if opts.ThinkingBudget > 0 {
		cfg.ThinkingBudget = opts.ThinkingBudget
	}
	cfg.IncludeThoughts = opts.IncludeThoughts
	cfg.PromptCache = opts.PromptCache
	cfg.Store = opts.Store
	cfg.SendMetadata = opts.SendMetadata
	cfg.RawSidecar = opts.RawSidecar
	if opts.RetryPolicy != nil {
		cfg.Retry = config.Retry{
			MaxAttempts:    opts.RetryPolicy.MaxAttempts,
			BaseDelayMS:    opts.RetryPolicy.BaseDelayMS,
			Retry429:       opts.RetryPolicy.Retry429,
			Retry5xx:       opts.RetryPolicy.Retry5xx,
			RetryTransport: opts.RetryPolicy.RetryTransport,
		}
	}
	if opts.TimeoutPolicy != nil {
		cfg.TimeoutSec = opts.TimeoutPolicy.TimeoutSec
		cfg.RequestTimeoutSec = opts.TimeoutPolicy.RequestTimeoutSec
		cfg.StreamIdleTimeoutMS = opts.TimeoutPolicy.StreamIdleTimeoutMS
	}
	return cfg
}

func stringsTrim(value string) string {
	return strings.TrimSpace(value)
}

func providerOptionsFromConfig(name string, cfg config.Provider) session.ProviderOptions {
	apiProvider, _ := config.EffectiveAPIProvider(name, cfg)
	return session.ProviderOptions{
		APIProvider:      apiProvider,
		BaseURL:          strings.TrimSpace(cfg.BaseURL),
		Temperature:      cfg.Temperature,
		TopP:             cfg.TopP,
		MaxOutputTokens:  cfg.MaxOutputTokens,
		ReasoningEffort:  strings.TrimSpace(cfg.ReasoningEffort),
		ReasoningSummary: strings.TrimSpace(cfg.ReasoningSummary),
		TextVerbosity:    strings.TrimSpace(cfg.TextVerbosity),
		ThinkingBudget:   cfg.ThinkingBudget,
		IncludeThoughts:  cfg.IncludeThoughts,
		PromptCache:      defaultPromptCacheForAPIProvider(apiProvider, cfg.PromptCache),
		Store:            defaultStoreForAPIProvider(apiProvider, cfg.Store),
		SendMetadata:     cfg.SendMetadata,
		RawSidecar:       cfg.RawSidecar,
		RetryPolicy:      providerRetryPolicy(cfg),
		TimeoutPolicy:    providerTimeoutPolicy(cfg),
	}
}

func resolvedProviderOptions(name string, cfg config.Provider, override session.ProviderOptions) (session.ProviderOptions, error) {
	defaults := providerOptionsFromConfig(name, cfg)
	apiProvider, err := config.EffectiveAPIProvider(name, cfg)
	if err != nil {
		return session.ProviderOptions{}, WrapConfigError(err)
	}
	if err := validateSupportedAPIProvider(name, apiProvider); err != nil {
		return session.ProviderOptions{}, err
	}
	defaults.APIProvider = apiProvider
	if override == (session.ProviderOptions{}) {
		return defaults, nil
	}
	if strings.TrimSpace(override.APIProvider) != "" {
		apiProvider := strings.TrimSpace(override.APIProvider)
		if err := validateSupportedAPIProvider(name, apiProvider); err != nil {
			return session.ProviderOptions{}, err
		}
		defaults.APIProvider = apiProvider
		defaults.PromptCache = defaultPromptCacheForAPIProvider(defaults.APIProvider, cfg.PromptCache)
		defaults.Store = defaultStoreForAPIProvider(defaults.APIProvider, cfg.Store)
	}
	return mergeProviderOptions(defaults, override), nil
}

func validateSupportedAPIProvider(providerName, apiProvider string) error {
	switch strings.TrimSpace(apiProvider) {
	case "openai-compatible", "anthropic-compatible", "google":
		return nil
	case "":
		return nil
	default:
		return WrapConfigError(fmt.Errorf("unsupported api_provider for %s: %s", providerName, apiProvider))
	}
}

func mergeProviderOptions(defaults, override session.ProviderOptions) session.ProviderOptions {
	out := defaults
	if strings.TrimSpace(override.APIProvider) != "" {
		out.APIProvider = strings.TrimSpace(override.APIProvider)
	}
	if strings.TrimSpace(override.BaseURL) != "" {
		out.BaseURL = strings.TrimSpace(override.BaseURL)
	}
	if override.Temperature != nil {
		out.Temperature = override.Temperature
	}
	if override.TopP != nil {
		out.TopP = override.TopP
	}
	if override.MaxOutputTokens > 0 {
		out.MaxOutputTokens = override.MaxOutputTokens
	}
	if strings.TrimSpace(override.ReasoningEffort) != "" {
		out.ReasoningEffort = strings.TrimSpace(override.ReasoningEffort)
	}
	if strings.TrimSpace(override.ReasoningSummary) != "" {
		out.ReasoningSummary = strings.TrimSpace(override.ReasoningSummary)
	}
	if strings.TrimSpace(override.TextVerbosity) != "" {
		out.TextVerbosity = strings.TrimSpace(override.TextVerbosity)
	}
	if override.ThinkingBudget > 0 {
		out.ThinkingBudget = override.ThinkingBudget
	}
	if override.IncludeThoughts != nil {
		out.IncludeThoughts = override.IncludeThoughts
	}
	if override.PromptCache != nil {
		out.PromptCache = override.PromptCache
	}
	if override.Store != nil {
		out.Store = override.Store
	}
	if override.SendMetadata != nil {
		out.SendMetadata = override.SendMetadata
	}
	if override.RawSidecar != nil {
		out.RawSidecar = override.RawSidecar
	}
	if override.RetryPolicy != nil {
		out.RetryPolicy = override.RetryPolicy
	}
	if override.TimeoutPolicy != nil {
		out.TimeoutPolicy = override.TimeoutPolicy
	}
	return out
}

func preserveDurableProviderOptionDefaults(merged, current session.ProviderOptions) session.ProviderOptions {
	if current.Temperature == nil {
		merged.Temperature = nil
	}
	if current.TopP == nil {
		merged.TopP = nil
	}
	merged.MaxOutputTokens = current.MaxOutputTokens
	merged.ReasoningEffort = strings.TrimSpace(current.ReasoningEffort)
	merged.ReasoningSummary = strings.TrimSpace(current.ReasoningSummary)
	merged.TextVerbosity = strings.TrimSpace(current.TextVerbosity)
	merged.ThinkingBudget = current.ThinkingBudget
	if current.IncludeThoughts == nil {
		merged.IncludeThoughts = nil
	}
	if current.SendMetadata == nil {
		merged.SendMetadata = nil
	}
	if current.RawSidecar == nil {
		merged.RawSidecar = nil
	}
	return merged
}

func defaultStoreForAPIProvider(apiProvider string, configured *bool) *bool {
	if configured != nil {
		return configured
	}
	if strings.TrimSpace(apiProvider) == "openai-compatible" {
		value := false
		return &value
	}
	return nil
}

func defaultPromptCacheForAPIProvider(apiProvider string, configured *bool) *bool {
	if configured != nil {
		return configured
	}
	if strings.TrimSpace(apiProvider) == "anthropic-compatible" {
		value := true
		return &value
	}
	return nil
}
