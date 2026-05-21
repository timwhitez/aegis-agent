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
	defer r.planInputMu.Unlock()
	key := planInputWaiterKey(sessionID, requestID)
	ch, ok := r.planInputWaiters[key]
	if !ok {
		return false
	}
	ch <- planInputResponse{answers: append([]session.PlanModeInputAnswer(nil), answers...)}
	return true
}

func (r *Runner) CancelActivePlanInput(sessionID, requestID string) bool {
	r.planInputMu.Lock()
	defer r.planInputMu.Unlock()
	key := planInputWaiterKey(sessionID, requestID)
	ch, ok := r.planInputWaiters[key]
	if !ok {
		return false
	}
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
	mode := normalizeRunMode(req.Mode, session.ModeRun)
	sessionID := session.NewSessionID()
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
	effectiveWorkdir := requestedWorkdir
	isolationMode := normalizeIsolationMode(req.IsolationMode, r.cfg.Runtime.Isolation.DefaultMode)
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
		ProviderOptions:  resolvedProviderOptions(providerName, providerCfg, req.ProviderOptions),
	}
	state := session.State{
		Status:    session.StatusRunning,
		Phase:     "prepare",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := r.store.Create(meta, state); err != nil {
		return RunResult{}, err
	}
	if req.Goal != nil && req.Goal.Enabled {
		draft := *req.Goal
		if strings.TrimSpace(draft.Source) == "" {
			draft.Source = session.GoalSourceCLI
		}
		goal, err := r.store.CreateGoal(meta.ID, draft)
		if err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		r.emit(meta.ID, "goal.created", "prepare", goalEventData(goal))
		if (req.PlanMode == nil || !req.PlanMode.Enabled) && session.GoalRequiresPlanApproval(goal) {
			planMode, created, err := r.store.EnsurePlanModeForGoal(meta.ID, goal, goal.Source)
			if err != nil {
				return r.failBeforeRun(meta.ID, state, "prepare", err)
			}
			if created {
				r.emit(meta.ID, "planmode.created", "prepare", planModeEventData(planMode))
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
		planMode, err := r.store.CreatePlanMode(meta.ID, draft)
		if err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		r.emit(meta.ID, "planmode.created", "prepare", planModeEventData(planMode))
	}
	r.emit(meta.ID, "session.created", "prepare", map[string]any{
		"provider": meta.Provider,
		"model":    meta.Model,
		"mode":     meta.Mode,
		"workdir":  meta.Workdir,
	})
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
	if providerName == "" && parentMeta != nil && strings.TrimSpace(parentMeta.Provider) != "" {
		providerName = parentMeta.Provider
	}
	if providerName == "" {
		providerName = cfg.DefaultProvider
	}
	providerCfg, err := cfg.ProviderConfig(providerName)
	if err != nil {
		return "", "", config.Provider{}, err
	}
	roleOverride := config.RoleProviderOverride{}
	if len(agentRole) > 0 && !explicitProvider && !explicitModel {
		roleOverride = cfg.RoleProviderOverride(agentRole[0])
	}
	if strings.TrimSpace(roleOverride.Provider) != "" {
		providerName = strings.TrimSpace(roleOverride.Provider)
		providerCfg, err = cfg.ProviderConfig(providerName)
		if err != nil {
			return "", "", config.Provider{}, err
		}
	}
	if strings.TrimSpace(roleOverride.APIProvider) != "" {
		providerCfg.APIProvider = strings.TrimSpace(roleOverride.APIProvider)
	}
	if strings.TrimSpace(roleOverride.BaseURL) != "" {
		providerCfg.BaseURL = strings.TrimSpace(roleOverride.BaseURL)
	}
	if strings.TrimSpace(roleOverride.Model) != "" {
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
	if req.Provider != "" {
		providerName := normalizeProviderOverride(req.Provider)
		providerCfg, err := r.cfg.ProviderConfig(providerName)
		if err != nil {
			return RunResult{}, WrapConfigError(err)
		}
		meta.Provider = providerName
		if req.Model != "" {
			providerCfg.Model = req.Model
		}
		meta.Model = providerCfg.Model
		meta.ProviderOptions = providerOptionsFromConfig(providerName, providerCfg)
	}
	if req.Model != "" {
		meta.Model = req.Model
	}
	if err := r.store.SaveMetadata(meta.ID, meta); err != nil {
		return RunResult{}, err
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = session.PlanModeSourceCLI
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
		planMode, err := r.store.CancelPlanMode(meta.ID, source)
		if err != nil {
			return RunResult{}, err
		}
		r.emit(meta.ID, "planmode.cancelled", "planmode", planModeEventData(planMode))
		state.Status = session.StatusAwaitingInput
		state.Phase = "plan_cancelled"
		if err := r.store.SaveState(meta.ID, state); err != nil {
			return RunResult{}, err
		}
		_ = writeSessionSummary(r.store, meta.ID)
		return RunResult{SessionID: meta.ID, Status: state.Status, FinalText: "Plan Mode cancelled."}, nil
	}
	var extraUserMeta map[string]any
	if req.ApprovePlan {
		if err := r.checkPlanModeGoalCoverage(meta.ID, req.OverrideGoalCoverage); err != nil {
			return RunResult{}, err
		}
		approved, err := r.store.ApprovePlanMode(meta.ID, source)
		if err != nil {
			return RunResult{}, err
		}
		r.emit(meta.ID, "planmode.plan_approved", "planmode", planModeEventData(approved))
		executing, err := r.store.MarkPlanModeExecuting(meta.ID, source)
		if err != nil {
			return RunResult{}, err
		}
		r.emit(meta.ID, "planmode.execution_started", "planmode", planModeEventData(executing))
		if err := r.approveLinkedMissionPlan(meta.ID, executing, source, req.OverrideGoalCoverage); err != nil {
			return RunResult{}, err
		}
		req.Message = fmt.Sprintf("Implement the approved Plan Mode plan version %d.", executing.ApprovedVersion)
		extraUserMeta = map[string]any{
			"source":       "planmode_approval",
			"plan_mode_id": executing.PlanModeID,
			"plan_version": executing.ApprovedVersion,
		}
	}
	if req.PlanMode != nil && req.PlanMode.Enabled {
		draft := *req.PlanMode
		if strings.TrimSpace(draft.Source) == "" {
			draft.Source = source
		}
		if strings.TrimSpace(draft.Objective) == "" {
			draft.Objective = firstNonEmpty(req.Message, "Plan Mode continuation")
		}
		planMode, err := r.store.CreatePlanMode(meta.ID, draft)
		if err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		r.emit(meta.ID, "planmode.created", "prepare", planModeEventData(planMode))
	} else if !req.ApprovePlan && stringsTrim(req.Message) != "" {
		if planMode, err := r.store.LoadPlanMode(meta.ID); err == nil && planMode.Status == session.PlanModeStatusAwaitingApproval {
			revised, err := r.store.RevisePlanMode(meta.ID, source, req.Message)
			if err != nil {
				return r.failBeforeRun(meta.ID, state, "prepare", err)
			}
			r.emit(meta.ID, "planmode.plan_revised", "prepare", planModeEventData(revised))
			extraUserMeta = map[string]any{
				"source":       "planmode_revision",
				"plan_mode_id": revised.PlanModeID,
				"plan_version": revised.PlanVersion,
			}
		}
	}
	checkpointHint, checkpointWarnings, checkpointErr := appendCheckpointResumeHint(r.store, meta, meta.Provider, meta.Model)
	if checkpointErr != nil {
		return RunResult{}, checkpointErr
	}
	if checkpointHint {
		r.emit(meta.ID, "checkpoint.resume_hint.injected", "prepare", map[string]any{
			"provider":       meta.Provider,
			"model":          meta.Model,
			"drift_warnings": append([]string(nil), checkpointWarnings...),
		})
	}
	if stringsTrim(req.Message) != "" {
		if err := r.appendUserMessage(ctx, meta, "prepare", req.Message, extraUserMeta); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
		if err := r.refreshContractFromMessages(meta, "prepare"); err != nil {
			return r.failBeforeRun(meta.ID, state, "prepare", err)
		}
	}
	state.PendingSteerCount = 0
	state.PauseReason = ""
	state.ProviderAutoResumeCount = 0
	state.Status = session.StatusRunning
	return r.runExisting(ctx, meta, state, req.SystemOverride, req.PlanInputHandler)
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
	goal.Mission.PlanStatus = "approved"
	goal.Mission.ApprovedAt = approvedAt
	if err := r.store.SaveGoal(sessionID, goal); err != nil {
		return err
	}
	_ = r.store.AppendGoalHistory(sessionID, session.GoalHistoryEntry{
		Type:   "mission.plan.approved",
		Source: session.GoalSourceSystem,
		Status: goal.Status,
		Data: map[string]any{
			"approved_at":       approvedAt,
			"approved_source":   source,
			"plan_mode_id":      planMode.PlanModeID,
			"approved_version":  planMode.ApprovedVersion,
			"coverage_override": overrideCoverage,
		},
	})
	r.emit(sessionID, "mission.plan.approved", "planmode", map[string]any{
		"goal_id":           goal.GoalID,
		"plan_mode_id":      planMode.PlanModeID,
		"approved_version":  planMode.ApprovedVersion,
		"approved_at":       approvedAt,
		"coverage_override": overrideCoverage,
	})
	return nil
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
	exists, err := r.hasToolResult(sessionID, request.ToolCallID, "request_user_input")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	result := session.ToolResult{
		ToolCallID:    request.ToolCallID,
		Name:          "request_user_input",
		LLMOutput:     "Error: Plan Mode input was cancelled by the user.",
		DisplayOutput: "Error: Plan Mode input was cancelled by the user.",
		IsError:       true,
		Metadata: map[string]any{
			"planmode":     true,
			"request_id":   request.RequestID,
			"cancelled":    true,
			"plan_mode_id": planMode.PlanModeID,
		},
	}
	if err := r.store.AppendMessage(sessionID, session.NewToolMessage([]session.ToolResult{result})); err != nil {
		return err
	}
	_ = r.store.AppendPlanModeHistory(sessionID, session.PlanModeHistoryEntry{
		PlanModeID: planMode.PlanModeID,
		Type:       "planmode.input_cancelled",
		Source:     source,
		Status:     planMode.Status,
		Data: map[string]any{
			"request_id":   request.RequestID,
			"tool_call_id": request.ToolCallID,
		},
	})
	r.emit(sessionID, "planmode.input_cancelled", "plan_input", map[string]any{
		"plan_mode_id": planMode.PlanModeID,
		"request_id":   request.RequestID,
		"recovered":    true,
	})
	return nil
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
	planMode, request, err := r.store.AnswerPlanModeInput(sessionID, requestID, source, answers)
	if err != nil {
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
		return err
	}
	r.emit(sessionID, "planmode.input_answered", "plan_input", map[string]any{
		"plan_mode_id": planMode.PlanModeID,
		"request_id":   request.RequestID,
		"answers":      answers,
		"recovered":    true,
	})
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
	r.emit(meta.ID, "session.started", "prepare", map[string]any{
		"provider": meta.Provider,
		"model":    meta.Model,
	})
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
	state.PendingSteerCount++
	if err := r.store.SaveState(req.SessionID, state); err != nil {
		return SteerResult{}, err
	}
	r.emit(req.SessionID, "session.steer.requested", "control", map[string]any{
		"id":        request.ID,
		"interrupt": request.Interrupt,
	})
	r.emit(req.SessionID, "session.steer.queued", "control", map[string]any{
		"id": request.ID,
	})
	if meta, err := r.store.LoadMetadata(req.SessionID); err == nil {
		_ = writeSessionSummary(r.store, meta.ID)
	}
	return SteerResult{
		SessionID: req.SessionID,
		Accepted:  true,
		Behavior:  "queued",
	}, nil
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
	return r.store.LoadState(sessionID)
}

func (r *Runner) Tasks(sessionID string) (session.TaskBoard, error) {
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
		r.emit(sessionID, events.EventRalphLoopExhausted, "ralph_loop", map[string]any{
			"count":          state.RalphLoopCount,
			"max_iterations": r.cfg.Runtime.RalphLoop.MaxIterations,
		})
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
	state.RalphLoopCount++
	if err := r.store.SaveState(sessionID, state); err != nil {
		return RunResult{}, err
	}
	r.emit(sessionID, events.EventRalphLoopTriggered, "ralph_loop", map[string]any{
		"count":          state.RalphLoopCount,
		"max_iterations": r.cfg.Runtime.RalphLoop.MaxIterations,
	})
	result, err := r.Continue(ctx, ContinueRequest{
		SessionID: sessionID,
		Message:   originalPrompt,
	})
	if err == nil && result.Status == session.StatusCompleted {
		r.emit(sessionID, events.EventRalphLoopCompleted, "ralph_loop", map[string]any{
			"count": state.RalphLoopCount,
		})
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
				seenInterrupts[req.ID] = struct{}{}
				if req.Interrupt {
					r.control.requestSteerInterrupt()
					r.emit(sessionID, "session.steer.interrupt_requested", "control", map[string]any{
						"id": req.ID,
					})
				}
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
	r.emit(meta.ID, "user.message", phase, data)
	return nil
}

func (r *Runner) transformUserMessage(ctx context.Context, meta session.SessionMetadata, phase, text string) (string, error) {
	hookManager := hooks.New(r.cfg.Hooks, meta.Workdir)
	hookManager.SetEmitter(func(eventType string, data map[string]any) {
		r.emit(meta.ID, eventType, phase, data)
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
	_ = r.store.SaveState(sessionID, state)
	r.emit(sessionID, "session.failed", phase, map[string]any{"error": err.Error()})
	_ = writeSessionSummary(r.store, sessionID)
	_ = writeLongRunCheckpoint(r.store, sessionID)
	return RunResult{
		SessionID: sessionID,
		Status:    state.Status,
		LastError: state.LastError,
	}, err
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

func resolvedProviderOptions(name string, cfg config.Provider, override session.ProviderOptions) session.ProviderOptions {
	if override == (session.ProviderOptions{}) {
		return providerOptionsFromConfig(name, cfg)
	}
	return override
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
