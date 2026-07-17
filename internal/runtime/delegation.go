package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-cli-agent/internal/events"
	"go-cli-agent/internal/fileutil"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/tools"
)

type DelegateRequest struct {
	ParentSessionID string
	Prompt          string
	AgentName       string
	AgentRole       string
	Provider        string
	Model           string
	Workdir         string
	SystemOverride  string
	Background      bool
	WaitMode        string
	ResumeParent    bool
	Mode            string
	IsolationMode   string
	IsolationRoot   string
}

type DelegateResult struct {
	SessionID    string   `json:"session_id,omitempty"`
	QueueJobID   string   `json:"queue_job_id,omitempty"`
	Status       string   `json:"status,omitempty"`
	FinalText    string   `json:"final_text,omitempty"`
	LastError    string   `json:"last_error,omitempty"`
	Workdir      string   `json:"workdir,omitempty"`
	VisiblePaths []string `json:"visible_paths,omitempty"`
	AgentRole    string   `json:"agent_role,omitempty"`
}

type ChildrenResult struct {
	Sessions []session.SessionSummary `json:"sessions"`
	Jobs     []session.QueueJob       `json:"jobs"`
}

func (r *Runner) Delegate(ctx context.Context, req DelegateRequest) (DelegateResult, error) {
	result, err := r.SpawnAgent(ctx, tools.AgentSpawnRequest{
		ParentSessionID: req.ParentSessionID,
		Prompt:          req.Prompt,
		AgentName:       req.AgentName,
		AgentRole:       req.AgentRole,
		Provider:        normalizeProviderOverride(req.Provider),
		Model:           normalizeModelOverride(req.Model),
		Workdir:         req.Workdir,
		SystemOverride:  req.SystemOverride,
		Background:      req.Background,
		WaitMode:        req.WaitMode,
		ResumeParent:    req.ResumeParent,
		Mode:            req.Mode,
		IsolationMode:   req.IsolationMode,
		IsolationRoot:   req.IsolationRoot,
	})
	return DelegateResult{
		SessionID:    result.SessionID,
		QueueJobID:   result.QueueJobID,
		Status:       result.Status,
		FinalText:    result.FinalText,
		LastError:    result.LastError,
		Workdir:      result.Workdir,
		VisiblePaths: append([]string(nil), result.VisiblePaths...),
		AgentRole:    result.AgentRole,
	}, err
}

func (r *Runner) SpawnAgent(ctx context.Context, req tools.AgentSpawnRequest) (tools.AgentSpawnResult, error) {
	if strings.TrimSpace(req.ParentSessionID) == "" {
		return tools.AgentSpawnResult{}, errors.New("parent session id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return tools.AgentSpawnResult{}, errors.New("prompt is required")
	}
	if err := r.rejectParentLinkedActionDuringPendingPlanMode(req.ParentSessionID, "agent_spawn"); err != nil {
		return tools.AgentSpawnResult{}, err
	}
	agentRole, err := normalizeAgentRole(req.AgentRole, req.AgentName)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	req.AgentRole = agentRole
	parentMeta, err := r.store.LoadMetadata(req.ParentSessionID)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	if isNestedAgentParent(parentMeta) {
		return tools.AgentSpawnResult{}, errors.New("nested sub-agents are not allowed; only the root master session can create sub-agents")
	}
	workdir, err := resolveRequestedWorkdir(req.Workdir, &parentMeta)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	mode, err := normalizeAndValidateRunMode(req.Mode, session.ModeExec)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	isolationMode, err := normalizeAndValidateIsolationMode(req.IsolationMode, "auto")
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	waitMode, err := normalizeAndValidateParentWaitMode(req.WaitMode)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	providerName, modelName, providerCfg, err := resolveProviderAndModel(r.cfg, &parentMeta, req.Provider, req.Model, req.AgentRole)
	if err != nil {
		return tools.AgentSpawnResult{}, WrapConfigError(err)
	}
	providerOptions := providerOptionsFromConfig(providerName, providerCfg)
	if req.Background {
		job, err := r.QueueSubmit(ctx, QueueSubmitRequest{
			ParentSessionID: req.ParentSessionID,
			Prompt:          req.Prompt,
			AgentName:       req.AgentName,
			AgentRole:       req.AgentRole,
			Provider:        providerName,
			Model:           modelName,
			ProviderOptions: providerOptions,
			Workdir:         workdir,
			SystemOverride:  req.SystemOverride,
			Mode:            mode,
			WaitMode:        waitMode,
			ResumeParent:    req.ResumeParent,
			IsolationMode:   isolationMode,
			IsolationRoot:   req.IsolationRoot,
		})
		if err != nil {
			return tools.AgentSpawnResult{}, err
		}
		return tools.AgentSpawnResult{
			QueueJobID: job.ID,
			Status:     job.Status,
			Workdir:    job.RequestedWorkdir,
			AgentRole:  job.AgentRole,
		}, nil
	}
	childSessionID := session.NewSessionID()
	rootSessionID := strings.TrimSpace(parentMeta.RootSessionID)
	if rootSessionID == "" {
		rootSessionID = parentMeta.ID
	}
	limit := r.cfg.Runtime.MultiAgent.MaxActiveChildren
	acquired, err := r.store.AcquireDirectChildSlot(parentMeta.ID, rootSessionID, childSessionID, limit)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	if !acquired {
		return tools.AgentSpawnResult{}, fmt.Errorf("max active children reached: %d", limit)
	}
	childRunner := NewRunner(r.cfg)
	childRunner.SetRunLifecycleHooks(r.lifecycleHooksSnapshot())
	result, err := childRunner.Start(ctx, StartRequest{
		SessionID:       childSessionID,
		Prompt:          req.Prompt,
		Provider:        providerName,
		Model:           modelName,
		ProviderOptions: providerOptions,
		Workdir:         workdir,
		Mode:            mode,
		SystemOverride:  req.SystemOverride,
		ParentSessionID: req.ParentSessionID,
		AgentName:       req.AgentName,
		AgentRole:       req.AgentRole,
		IsolationMode:   isolationMode,
		IsolationRoot:   req.IsolationRoot,
	})
	if releaseErr := r.store.ReleaseDirectChildSlot(childSessionID); releaseErr != nil {
		err = errors.Join(err, fmt.Errorf("release active child slot for %s: %w", childSessionID, releaseErr))
	}
	out := tools.AgentSpawnResult{
		SessionID: result.SessionID,
		Status:    result.Status,
		FinalText: result.FinalText,
		LastError: result.LastError,
		AgentRole: req.AgentRole,
	}
	requestedChildWorkdir := workdir
	coordinationStatus := result.Status
	var handoffErr error
	if result.SessionID != "" {
		if meta, loadErr := childRunner.store.LoadMetadata(result.SessionID); loadErr == nil {
			out.Workdir = meta.Workdir
			out.AgentRole = meta.AgentRole
			requestedChildWorkdir = firstNonEmpty(meta.RequestedWorkdir, requestedChildWorkdir)
		} else {
			handoffErr = fmt.Errorf("load child session metadata for delegate handoff %s: %w", result.SessionID, loadErr)
		}
		if handoffErr == nil {
			if messages, loadErr := childRunner.store.LoadMessages(result.SessionID); loadErr == nil {
				effectiveWorkdir := firstNonEmpty(out.Workdir, requestedChildWorkdir)
				out.VisiblePaths = collectVisibleSessionOutputs(effectiveWorkdir, messages)
				var syncErr error
				out.VisiblePaths, syncErr = syncVisibleSessionOutputs(requestedChildWorkdir, effectiveWorkdir, out.VisiblePaths)
				if syncErr != nil {
					handoffErr = fmt.Errorf("sync child visible outputs for delegate handoff %s: %w", result.SessionID, syncErr)
				}
			} else {
				handoffErr = fmt.Errorf("load child session messages.jsonl for delegate handoff %s: %w", result.SessionID, loadErr)
			}
		}
	}
	if handoffErr != nil {
		coordinationStatus = session.StatusFailed
		out.Status = session.StatusFailed
		out.LastError = handoffErr.Error()
	}
	if result.SessionID != "" {
		coordinationSnapshot, snapshotErr := r.store.SnapshotParentCoordination(req.ParentSessionID)
		if snapshotErr != nil {
			return out, errors.Join(err, fmt.Errorf("snapshot parent coordination for child session %s: %w", result.SessionID, snapshotErr))
		}
		eventsSnapshot, snapshotErr := r.store.LoadEvents(req.ParentSessionID)
		if eventErr := r.appendEvent(req.ParentSessionID, "session.child.spawned", "delegate", map[string]any{
			"session_id": result.SessionID,
			"status":     out.Status,
			"agent_name": req.AgentName,
			"agent_role": out.AgentRole,
			"wait_mode":  waitMode,
		}); eventErr != nil {
			return out, errors.Join(err, fmt.Errorf("append session.child.spawned event for child session %s: %w", result.SessionID, eventErr))
		}
		if snapshotErr != nil {
			return out, errors.Join(err, fmt.Errorf("snapshot parent events for child session %s: %w", result.SessionID, snapshotErr))
		}
		// A concurrently issued agent_stop can cancel the child before this
		// synchronous spawn call reaches its post-run handoff. Engine.cancel has
		// already written the terminal parent fact in that case; do not re-add the
		// same child to unresolved before resolving it again.
		if !parentCoordinationSnapshotHasResolvedChild(coordinationSnapshot, result.SessionID) {
			if coordinationErr := addParentChildSession(r.store, req.ParentSessionID, result.SessionID, waitMode); coordinationErr != nil {
				if restoreErr := r.restoreDirectChildParentFacts(req.ParentSessionID, coordinationSnapshot, eventsSnapshot); restoreErr != nil {
					coordinationErr = fmt.Errorf("persist parent coordination for child session %s failed with %v; restore parent child facts: %w", result.SessionID, coordinationErr, restoreErr)
				} else {
					coordinationErr = fmt.Errorf("persist parent coordination for child session %s: %w", result.SessionID, coordinationErr)
				}
				return out, errors.Join(err, coordinationErr)
			}
		}
		if coordinationErr := resolveParentChildSession(r.store, req.ParentSessionID, result.SessionID, coordinationStatus); coordinationErr != nil {
			if restoreErr := r.restoreDirectChildParentFacts(req.ParentSessionID, coordinationSnapshot, eventsSnapshot); restoreErr != nil {
				coordinationErr = fmt.Errorf("resolve parent coordination for child session %s failed with %v; restore parent child facts: %w", result.SessionID, coordinationErr, restoreErr)
			} else {
				coordinationErr = fmt.Errorf("resolve parent coordination for child session %s: %w", result.SessionID, coordinationErr)
			}
			return out, errors.Join(err, coordinationErr)
		}
		_ = writeSessionSummary(r.store, req.ParentSessionID)
		_ = writeLongRunCheckpoint(r.store, req.ParentSessionID)
	}
	return out, err
}

func parentCoordinationSnapshotHasResolvedChild(snapshot session.ParentCoordinationSnapshot, childSessionID string) bool {
	if !snapshot.HasCoordination || strings.TrimSpace(childSessionID) == "" {
		return false
	}
	coordination := snapshot.Coordination
	return containsStringValue(coordination.CompletedChildSessions, childSessionID) ||
		containsStringValue(coordination.FailedChildSessions, childSessionID) ||
		containsStringValue(coordination.CancelledChildSessions, childSessionID)
}

func containsStringValue(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (r *Runner) activeChildCount(parentSessionID string) (int, error) {
	children, err := r.store.ListChildren(parentSessionID, -1)
	if err != nil {
		return 0, err
	}
	runningSessions := map[string]struct{}{}
	for _, child := range children {
		if child.Status == session.StatusRunning {
			runningSessions[child.ID] = struct{}{}
		}
	}
	jobs, err := r.store.ListJobsByParent(parentSessionID, -1)
	if err != nil {
		return 0, err
	}
	count := len(runningSessions)
	for _, job := range jobs {
		if job.Status != session.QueueStatusRunning {
			continue
		}
		if _, exists := runningSessions[strings.TrimSpace(job.SessionID)]; exists && strings.TrimSpace(job.SessionID) != "" {
			continue
		}
		count++
	}
	return count, nil
}

func (r *Runner) restoreDirectChildParentFacts(parentSessionID string, coordinationSnapshot session.ParentCoordinationSnapshot, eventsSnapshot []events.Event) error {
	var restoreErrs []error
	if err := r.store.RestoreParentCoordination(parentSessionID, coordinationSnapshot); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore parent coordination: %w", err))
	}
	if err := r.store.RestoreEvents(parentSessionID, eventsSnapshot); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("restore parent events: %w", err))
	}
	return errors.Join(restoreErrs...)
}

func (r *Runner) AgentStatus(_ context.Context, req tools.AgentStatusRequest) (tools.AgentStatusResult, error) {
	parentSessionID := strings.TrimSpace(req.ParentSessionID)
	if parentSessionID == "" {
		return tools.AgentStatusResult{}, errors.New("parent session id is required")
	}
	parentMeta, err := r.store.LoadMetadata(parentSessionID)
	if err != nil {
		return tools.AgentStatusResult{}, err
	}
	parentSessionID = parentMeta.ID
	if strings.TrimSpace(req.QueueJobID) != "" {
		job, err := r.store.LoadJob(req.QueueJobID)
		if err != nil {
			return tools.AgentStatusResult{}, err
		}
		if strings.TrimSpace(job.ParentSessionID) != parentSessionID {
			return tools.AgentStatusResult{}, fmt.Errorf("queue job %s is not linked to parent session %s", job.ID, parentSessionID)
		}
		return tools.AgentStatusResult{
			QueueJobID:      job.ID,
			Status:          job.Status,
			SessionID:       job.SessionID,
			SessionStatus:   job.SessionStatus,
			FinalText:       job.FinalText,
			StopReason:      job.StopReason,
			LastError:       job.LastError,
			Workdir:         firstNonEmpty(job.EffectiveWorkdir, job.RequestedWorkdir),
			AgentName:       job.AgentName,
			AgentRole:       job.AgentRole,
			EffectiveBudget: session.CloneEffectiveBudget(job.EffectiveBudget),
		}, nil
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return tools.AgentStatusResult{}, errors.New("session_id or queue_job_id is required")
	}
	meta, err := r.store.LoadMetadata(sessionID)
	if err != nil {
		return tools.AgentStatusResult{}, err
	}
	if strings.TrimSpace(meta.ParentSessionID) != parentSessionID {
		return tools.AgentStatusResult{}, fmt.Errorf("child session %s is not linked to parent session %s", meta.ID, parentSessionID)
	}
	state, err := r.store.LoadState(sessionID)
	if err != nil {
		return tools.AgentStatusResult{}, err
	}
	return tools.AgentStatusResult{
		SessionID:       meta.ID,
		Status:          state.Status,
		SessionStatus:   state.Status,
		FinalText:       state.LastAssistantExcerpt,
		StopReason:      state.PauseReason,
		LastError:       state.LastError,
		Workdir:         meta.Workdir,
		AgentName:       meta.AgentName,
		AgentRole:       meta.AgentRole,
		EffectiveBudget: session.CloneEffectiveBudget(meta.EffectiveBudget),
	}, nil
}

func (r *Runner) StopAgent(ctx context.Context, req tools.AgentStopRequest) (tools.AgentStopResult, error) {
	parentSessionID := strings.TrimSpace(req.ParentSessionID)
	if parentSessionID == "" {
		return tools.AgentStopResult{}, errors.New("parent session id is required")
	}
	parentMeta, err := r.store.LoadMetadata(parentSessionID)
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	childSessionID, job, err := r.resolveStopTarget(parentMeta.ID, req)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && job.Status == session.QueueStatusRunning && strings.TrimSpace(job.SessionID) != "" {
			request, created, requestErr := r.store.RequestSessionCancel(job.SessionID, parentMeta.ID, job.ID, agentCancelRequestedReason)
			if requestErr != nil {
				return tools.AgentStopResult{}, requestErr
			}
			if created {
				if eventErr := r.appendEvent(parentMeta.ID, "session.child.cancel_requested", "delegate", map[string]any{
					"request_id": request.ID,
					"session_id": job.SessionID,
					"job_id":     job.ID,
				}); eventErr != nil {
					return tools.AgentStopResult{}, eventErr
				}
			}
			return tools.AgentStopResult{
				SessionID:  job.SessionID,
				QueueJobID: job.ID,
				Status:     session.CancelRequestStatusRequested,
				Accepted:   true,
				Behavior:   "cancel_requested_before_session_create",
			}, nil
		}
		return tools.AgentStopResult{}, err
	}
	if childSessionID == "" {
		if job.Status == session.QueueStatusCancelled {
			return tools.AgentStopResult{QueueJobID: job.ID, Status: job.Status, Accepted: true, Behavior: "already_cancelled", LastError: job.LastError}, nil
		}
		if job.Status != session.QueueStatusQueued {
			return tools.AgentStopResult{}, fmt.Errorf("queue job %s is %s and has no cancellable child session", job.ID, job.Status)
		}
		cancelled, err := r.store.StopQueuedJob(job.ID, parentMeta.ID, "cancelled by parent agent before worker claim")
		if err != nil {
			return tools.AgentStopResult{}, err
		}
		if err := r.finalizeCancelledJob(parentMeta.ID, job, cancelled); err != nil {
			return tools.AgentStopResult{}, err
		}
		return tools.AgentStopResult{QueueJobID: cancelled.ID, Status: cancelled.Status, Accepted: true, Behavior: "cancelled_queued_job", LastError: cancelled.LastError}, nil
	}

	state, err := r.store.LoadState(childSessionID)
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	if state.Status == session.StatusCancelled {
		return tools.AgentStopResult{SessionID: childSessionID, QueueJobID: job.ID, Status: state.Status, Accepted: true, Behavior: "already_cancelled"}, nil
	}
	if job.Status == session.QueueStatusCancelled && state.Status == session.StatusPaused && session.IsChildBudgetPauseReason(state.PauseReason) {
		return tools.AgentStopResult{SessionID: childSessionID, QueueJobID: job.ID, Status: job.Status, Accepted: true, Behavior: "already_settled_budget_paused_job", LastError: job.LastError}, nil
	}
	if state.Status == session.StatusCompleted || state.Status == session.StatusFailed {
		return tools.AgentStopResult{}, fmt.Errorf("child session %s is already terminal: %s", childSessionID, state.Status)
	}
	if state.Status == session.StatusPaused && session.IsChildBudgetPauseReason(state.PauseReason) && job.ID != "" && job.Status == session.QueueStatusBlocked {
		cancelled, err := r.store.StopBudgetPausedJob(job.ID, parentMeta.ID, "cancelled by parent after child budget pause: "+state.PauseReason)
		if err != nil {
			return tools.AgentStopResult{}, err
		}
		if err := r.finalizeCancelledJob(parentMeta.ID, job, cancelled); err != nil {
			return tools.AgentStopResult{}, err
		}
		return tools.AgentStopResult{SessionID: childSessionID, QueueJobID: cancelled.ID, Status: cancelled.Status, Accepted: true, Behavior: "settled_budget_paused_job", LastError: cancelled.LastError}, nil
	}

	request, created, err := r.store.RequestSessionCancel(childSessionID, parentMeta.ID, job.ID, agentCancelRequestedReason)
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	if created {
		if err := r.appendEvent(childSessionID, "session.cancel_requested", "control", map[string]any{
			"request_id":        request.ID,
			"parent_session_id": parentMeta.ID,
			"queue_job_id":      job.ID,
		}); err != nil {
			return tools.AgentStopResult{}, err
		}
		if err := r.appendEvent(parentMeta.ID, "session.child.cancel_requested", "delegate", map[string]any{
			"request_id": request.ID,
			"session_id": childSessionID,
			"job_id":     job.ID,
		}); err != nil {
			return tools.AgentStopResult{}, err
		}
	}
	interrupted := interruptRegisteredChild(r.store, childSessionID)
	if state.Status != session.StatusRunning && !interrupted {
		if err := r.cancelInactiveChild(parentMeta.ID, childSessionID, job, request); err != nil {
			return tools.AgentStopResult{}, err
		}
		return tools.AgentStopResult{SessionID: childSessionID, QueueJobID: job.ID, Status: session.StatusCancelled, Accepted: true, Behavior: "cancelled_inactive_child"}, nil
	}

	deadline := time.Now().Add(time.Duration(r.cfg.Runtime.MultiAgent.CancelGraceSec) * time.Second)
	for time.Now().Before(deadline) {
		current, loadErr := r.store.LoadState(childSessionID)
		if loadErr == nil && current.Status == session.StatusCancelled {
			return tools.AgentStopResult{SessionID: childSessionID, QueueJobID: job.ID, Status: current.Status, Accepted: true, Behavior: "cancelled_running_child"}, nil
		}
		select {
		case <-ctx.Done():
			return tools.AgentStopResult{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	behavior := "cancel_requested_durable"
	if interrupted {
		behavior = "cancel_requested_active_handle"
	}
	return tools.AgentStopResult{SessionID: childSessionID, QueueJobID: job.ID, Status: session.CancelRequestStatusRequested, Accepted: true, Behavior: behavior}, nil
}

func (r *Runner) resolveStopTarget(parentSessionID string, req tools.AgentStopRequest) (string, session.QueueJob, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	jobID := strings.TrimSpace(req.QueueJobID)
	if sessionID == "" && jobID == "" {
		return "", session.QueueJob{}, errors.New("session_id or queue_job_id is required")
	}
	var job session.QueueJob
	if jobID != "" {
		loaded, err := r.store.LoadJob(jobID)
		if err != nil {
			return "", job, err
		}
		if strings.TrimSpace(loaded.ParentSessionID) != parentSessionID {
			return "", job, fmt.Errorf("queue job %s is not linked to parent session %s", loaded.ID, parentSessionID)
		}
		job = loaded
		if sessionID == "" {
			sessionID = strings.TrimSpace(job.SessionID)
		}
	}
	if sessionID == "" {
		return "", job, nil
	}
	meta, err := r.store.LoadMetadata(sessionID)
	if err != nil {
		return "", job, err
	}
	if strings.TrimSpace(meta.ParentSessionID) != parentSessionID {
		return "", job, fmt.Errorf("child session %s is not linked to parent session %s", meta.ID, parentSessionID)
	}
	if strings.TrimSpace(meta.QueueJobID) != "" {
		if job.ID == "" {
			loaded, err := r.store.LoadJob(meta.QueueJobID)
			if err != nil {
				return "", job, err
			}
			job = loaded
		}
		if job.ID != meta.QueueJobID {
			return "", job, fmt.Errorf("child session %s queue_job_id mismatch: got %q, want %q", meta.ID, meta.QueueJobID, job.ID)
		}
	}
	if job.ID != "" && strings.TrimSpace(job.SessionID) != "" && strings.TrimSpace(job.SessionID) != meta.ID {
		return "", job, fmt.Errorf("queue job %s session_id mismatch: got %q, want %q", job.ID, job.SessionID, meta.ID)
	}
	return meta.ID, job, nil
}

func (r *Runner) finalizeCancelledJob(parentSessionID string, previous, cancelled session.QueueJob) error {
	coordinationSnapshot, err := r.store.SnapshotParentCoordination(parentSessionID)
	if err != nil {
		return err
	}
	notificationSnapshot, err := r.store.SnapshotBackgroundNotification(parentSessionID, cancelled.ID)
	if err != nil {
		return err
	}
	if err := resolveParentQueueJob(r.store, parentSessionID, cancelled.ID, cancelled.Status); err != nil {
		_ = r.store.SaveJob(previous)
		return err
	}
	if err := r.store.EnsureBackgroundNotification(parentSessionID, session.NewBackgroundNotification(cancelled)); err != nil {
		_ = r.store.RestoreParentCoordination(parentSessionID, coordinationSnapshot)
		_ = r.store.SaveJob(previous)
		return err
	}
	if err := r.appendEvent(parentSessionID, "queue.job.cancelled", "delegate", map[string]any{
		"job_id":           cancelled.ID,
		"session_id":       cancelled.SessionID,
		"previous_status":  previous.Status,
		"status":           cancelled.Status,
		"stop_reason":      cancelled.StopReason,
		"last_error":       cancelled.LastError,
		"effective_budget": effectiveBudgetEventData(cancelled.EffectiveBudget),
	}); err != nil {
		_ = r.store.RestoreParentCoordination(parentSessionID, coordinationSnapshot)
		_ = r.store.RestoreBackgroundNotification(parentSessionID, notificationSnapshot)
		_ = r.store.SaveJob(previous)
		return err
	}
	_ = writeSessionSummary(r.store, parentSessionID)
	_ = writeLongRunCheckpoint(r.store, parentSessionID)
	return nil
}

func (r *Runner) cancelInactiveChild(parentSessionID, childSessionID string, job session.QueueJob, request session.CancelRequest) error {
	state, err := r.store.LoadState(childSessionID)
	if err != nil {
		return err
	}
	state.Status = session.StatusCancelled
	state.Phase = "cancelled"
	state.PauseReason = ""
	state.LastError = ""
	state.IncompleteReason = ""
	if err := r.store.SaveState(childSessionID, state); err != nil {
		return err
	}
	meta, err := r.store.LoadMetadata(childSessionID)
	if err != nil {
		return err
	}
	var effectiveBudget *session.EffectiveBudget
	if meta.EffectiveBudget != nil {
		budget := session.CloneEffectiveBudget(meta.EffectiveBudget)
		budget.Status = session.BudgetStatusCancelled
		budget.LastReason = agentCancelRequestedReason
		session.RefreshEffectiveBudget(budget, state.Turn)
		if err := persistEffectiveBudget(r.store, meta, budget); err != nil {
			return err
		}
		effectiveBudget = budget
	}
	if _, err := r.store.MarkSessionCancelApplied(childSessionID, request.ID); err != nil {
		return err
	}
	if err := r.appendEvent(childSessionID, "session.cancelled", "cancelled", map[string]any{
		"reason":            agentCancelRequestedReason,
		"request_id":        request.ID,
		"parent_session_id": parentSessionID,
		"queue_job_id":      job.ID,
		"effective_budget":  effectiveBudgetEventData(effectiveBudget),
	}); err != nil {
		return err
	}
	if job.ID != "" {
		previous := job
		job.Status = session.QueueStatusCancelled
		job.SessionStatus = session.StatusCancelled
		job.StopReason = session.QueueStopReasonAgentStop
		job.LastError = "cancelled by parent agent"
		job.ClaimedBy = ""
		job.ClaimedAt = ""
		job.HeartbeatAt = ""
		job.WorkerPID = 0
		job.ProcessStartID = ""
		if err := r.store.SaveJob(job); err != nil {
			return err
		}
		if err := r.finalizeCancelledJob(parentSessionID, previous, job); err != nil {
			return err
		}
	} else if err := resolveParentChildSession(r.store, parentSessionID, childSessionID, session.StatusCancelled); err != nil {
		return err
	}
	if err := r.appendEvent(parentSessionID, "session.child.cancelled", "delegate", map[string]any{
		"session_id":       childSessionID,
		"job_id":           job.ID,
		"request_id":       request.ID,
		"effective_budget": effectiveBudgetEventData(effectiveBudget),
	}); err != nil {
		return err
	}
	_ = writeSessionSummary(r.store, childSessionID)
	_ = writeLongRunCheckpoint(r.store, childSessionID)
	_ = writeSessionSummary(r.store, parentSessionID)
	_ = writeLongRunCheckpoint(r.store, parentSessionID)
	return nil
}

func (r *Runner) PromptAgent(ctx context.Context, req tools.AgentPromptRequest) (tools.AgentPromptResult, error) {
	parentSessionID := strings.TrimSpace(req.ParentSessionID)
	if parentSessionID == "" {
		return tools.AgentPromptResult{}, errors.New("parent session id is required")
	}
	parentMeta, err := r.store.LoadMetadata(parentSessionID)
	if err != nil {
		return tools.AgentPromptResult{}, err
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return tools.AgentPromptResult{}, errors.New("message is required")
	}
	if strings.TrimSpace(req.QueueJobID) != "" && strings.TrimSpace(req.SessionID) == "" {
		job, err := r.store.LoadJob(strings.TrimSpace(req.QueueJobID))
		if err != nil {
			return tools.AgentPromptResult{}, err
		}
		if strings.TrimSpace(job.ParentSessionID) != parentMeta.ID {
			return tools.AgentPromptResult{}, fmt.Errorf("queue job %s is not linked to parent session %s", job.ID, parentMeta.ID)
		}
		if isParentStoppedPreClaimJob(job) {
			requeued, err := r.store.RequeueParentStoppedJob(job.ID, parentMeta.ID, message)
			if err != nil {
				return tools.AgentPromptResult{}, err
			}
			if err := r.appendEvent(parentMeta.ID, "queue.job.requeued", "delegate", map[string]any{
				"job_id":      requeued.ID,
				"status":      requeued.Status,
				"resume_from": session.QueueStopReasonParentStop,
			}); err != nil {
				return tools.AgentPromptResult{}, err
			}
			_ = writeSessionSummary(r.store, parentMeta.ID)
			_ = writeLongRunCheckpoint(r.store, parentMeta.ID)
			return tools.AgentPromptResult{QueueJobID: requeued.ID, Accepted: true, Behavior: "requeued_parent_stopped_job"}, nil
		}
	}
	childSessionID, queueJobID, err := r.resolvePromptTarget(parentMeta.ID, req)
	if err != nil {
		return tools.AgentPromptResult{}, err
	}
	state, err := r.store.LoadState(childSessionID)
	if err != nil {
		return tools.AgentPromptResult{}, err
	}
	if state.Status != session.StatusRunning {
		behavior := ""
		var effectiveBudget *session.EffectiveBudget
		budgetResumeAuthorized := false
		if state.Status == session.StatusPaused && session.IsChildBudgetPauseReason(state.PauseReason) {
			if strings.TrimSpace(queueJobID) != "" {
				linkedJob, loadErr := r.store.LoadJob(queueJobID)
				if loadErr != nil {
					return tools.AgentPromptResult{}, loadErr
				}
				if linkedJob.Status != session.QueueStatusBlocked {
					return tools.AgentPromptResult{}, fmt.Errorf("queue job %s is %s and cannot resume its budget-paused child", linkedJob.ID, linkedJob.Status)
				}
			}
			if req.BudgetExtension == nil {
				childMeta, loadErr := r.store.LoadMetadata(childSessionID)
				if loadErr != nil {
					return tools.AgentPromptResult{}, loadErr
				}
				effectiveBudget = session.CloneEffectiveBudget(childMeta.EffectiveBudget)
				if effectiveBudget == nil || effectiveBudget.Status != session.BudgetStatusActive || session.EffectiveBudgetExceededReason(effectiveBudget, state.Turn, time.Now().UTC()) != "" {
					return tools.AgentPromptResult{}, fmt.Errorf("child session %s is paused by %s; provide budget_extension that adds or clears the exhausted dimension before resuming", childSessionID, state.PauseReason)
				}
				behavior = "continued_previously_extended_child"
			} else {
				behavior = "continued_budget_extended_child"
			}
			budgetResumeAuthorized = true
		} else {
			if req.BudgetExtension != nil {
				return tools.AgentPromptResult{}, errors.New("budget_extension is only valid for a child paused by child budget exhaustion")
			}
			var ok bool
			behavior, ok = childPromptContinueBehavior(r.store, childSessionID, queueJobID, state)
			if !ok {
				return tools.AgentPromptResult{}, fmt.Errorf("child session %s is %s and is not a resumable child state for agent_prompt", childSessionID, state.Status)
			}
		}
		previousJob, directSlotReserved, err := r.acquirePromptedChildRunSlot(parentMeta, childSessionID, queueJobID)
		if err != nil {
			return tools.AgentPromptResult{}, err
		}
		stopHeartbeat := func() {}
		if previousJob.ID != "" {
			stopHeartbeat = r.startQueueJobHeartbeat(ctx, previousJob.ID)
		}
		if state.Status == session.StatusPaused && session.IsChildBudgetPauseReason(state.PauseReason) && req.BudgetExtension != nil {
			effectiveBudget, err = r.extendChildBudget(parentMeta.ID, childSessionID, queueJobID, state, *req.BudgetExtension)
			if err != nil {
				stopHeartbeat()
				return tools.AgentPromptResult{}, errors.Join(err, r.releasePromptedChildRunSlot(childSessionID, previousJob, directSlotReserved))
			}
		}
		var directCoordinationSnapshot session.ParentCoordinationSnapshot
		var directParentEventsSnapshot []events.Event
		directCoordinationReopened := previousJob.ID == ""
		if directCoordinationReopened {
			directCoordinationSnapshot, err = r.store.SnapshotParentCoordination(parentMeta.ID)
			if err == nil {
				directParentEventsSnapshot, err = r.store.LoadEvents(parentMeta.ID)
			}
			if err == nil {
				err = addParentChildSession(r.store, parentMeta.ID, childSessionID, parentWaitAll)
			}
			if err != nil {
				stopHeartbeat()
				return tools.AgentPromptResult{}, errors.Join(err, r.releasePromptedChildRunSlot(childSessionID, previousJob, directSlotReserved))
			}
		}
		childRunner := NewRunner(r.cfg)
		childRunner.SetRunLifecycleHooks(r.lifecycleHooksSnapshot())
		result, continueErr := childRunner.Continue(ctx, ContinueRequest{
			SessionID:              childSessionID,
			Message:                message,
			Source:                 "agent",
			BudgetExtensionApplied: budgetResumeAuthorized,
		})
		stopHeartbeat()
		var settleErr error
		if directSlotReserved {
			settleErr = r.store.ReleaseDirectChildSlot(childSessionID)
		}
		if previousJob.ID != "" {
			if result.SessionID != "" && result.Status != "" {
				if reconcileErr := r.reconcilePromptedChildJob(parentMeta.ID, previousJob, result); reconcileErr != nil {
					settleErr = errors.Join(settleErr, reconcileErr)
				}
			} else {
				if continueErr == nil {
					continueErr = errors.New("child continue returned without a durable session result")
				}
				settleErr = errors.Join(settleErr, r.store.SaveJob(previousJob))
			}
		} else if result.SessionID != "" && result.Status != "" {
			if reconcileErr := resolveParentChildSession(r.store, parentMeta.ID, result.SessionID, result.Status); reconcileErr != nil {
				settleErr = errors.Join(settleErr, reconcileErr)
			}
		} else if continueErr == nil {
			continueErr = errors.New("child continue returned without a durable session result")
		}
		if directCoordinationReopened && (result.SessionID == "" || result.Status == "") {
			if restoreErr := r.store.RestoreParentCoordination(parentMeta.ID, directCoordinationSnapshot); restoreErr != nil {
				settleErr = errors.Join(settleErr, fmt.Errorf("restore parent coordination after failed direct child resume: %w", restoreErr))
			}
			if restoreErr := r.store.RestoreEvents(parentMeta.ID, directParentEventsSnapshot); restoreErr != nil {
				settleErr = errors.Join(settleErr, fmt.Errorf("restore parent events after failed direct child resume: %w", restoreErr))
			}
		}
		if continueErr = errors.Join(continueErr, settleErr); continueErr != nil {
			return tools.AgentPromptResult{}, continueErr
		}
		if childMeta, loadErr := r.store.LoadMetadata(childSessionID); loadErr == nil {
			effectiveBudget = session.CloneEffectiveBudget(childMeta.EffectiveBudget)
		}
		if err := r.appendEvent(parentMeta.ID, "session.child.prompted", "delegate", map[string]any{
			"session_id":   childSessionID,
			"queue_job_id": queueJobID,
			"behavior":     behavior,
			"status":       result.Status,
		}); err != nil {
			return tools.AgentPromptResult{}, err
		}
		_ = writeSessionSummary(r.store, parentMeta.ID)
		_ = writeLongRunCheckpoint(r.store, parentMeta.ID)
		return tools.AgentPromptResult{
			SessionID:       childSessionID,
			QueueJobID:      queueJobID,
			Accepted:        true,
			Behavior:        behavior,
			EffectiveBudget: effectiveBudget,
		}, nil
	}
	if req.BudgetExtension != nil {
		return tools.AgentPromptResult{}, errors.New("budget_extension cannot mutate a running child; wait for a budget pause or cancel the child")
	}
	interrupt := false
	if req.Interrupt != nil {
		interrupt = *req.Interrupt
	}
	result, err := r.Steer(ctx, SteerRequest{
		SessionID: childSessionID,
		Message:   message,
		Interrupt: interrupt,
		Source:    "agent",
	})
	if err != nil {
		return tools.AgentPromptResult{}, err
	}
	if err := r.appendEvent(parentMeta.ID, "session.child.prompted", "delegate", map[string]any{
		"session_id":   childSessionID,
		"queue_job_id": queueJobID,
		"interrupt":    interrupt,
	}); err != nil {
		return tools.AgentPromptResult{}, err
	}
	_ = writeSessionSummary(r.store, parentMeta.ID)
	_ = writeLongRunCheckpoint(r.store, parentMeta.ID)
	return tools.AgentPromptResult{
		SessionID:  childSessionID,
		QueueJobID: queueJobID,
		Accepted:   result.Accepted,
		Behavior:   result.Behavior,
		EffectiveBudget: func() *session.EffectiveBudget {
			meta, loadErr := r.store.LoadMetadata(childSessionID)
			if loadErr != nil {
				return nil
			}
			return session.CloneEffectiveBudget(meta.EffectiveBudget)
		}(),
	}, nil
}

func isParentStoppedPreClaimJob(job session.QueueJob) bool {
	return job.Status == session.QueueStatusBlocked &&
		job.StopReason == session.QueueStopReasonParentStop &&
		strings.TrimSpace(job.SessionID) == ""
}

func childPromptContinueBehavior(store *session.Store, childSessionID, queueJobID string, state session.State) (string, bool) {
	switch state.Status {
	case session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed:
	default:
		return "", false
	}
	if state.Status == session.StatusPaused && session.IsChildBudgetPauseReason(state.PauseReason) {
		return "", false
	}
	if strings.TrimSpace(queueJobID) != "" {
		job, err := store.LoadJob(queueJobID)
		if err != nil {
			return "", false
		}
		if strings.TrimSpace(job.SessionID) != childSessionID {
			return "", false
		}
		if job.StopReason == session.QueueStopReasonParentStop {
			return "continued_parent_stopped_child", true
		}
		if job.Status == session.QueueStatusBlocked {
			return "continued_blocked_child", true
		}
		return "", false
	}
	switch state.Status {
	case session.StatusAwaitingInput:
		return "continued_awaiting_input_child", true
	case session.StatusFailed:
		return "continued_failed_child", true
	case session.StatusPaused:
		if state.PauseReason == agentCancelRequestedReason {
			return "", false
		}
		if state.PauseReason == "manual_stop" {
			return "continued_parent_stopped_child", true
		}
		return "continued_paused_child", true
	}
	return "", false
}

func (r *Runner) extendChildBudget(parentSessionID, childSessionID, queueJobID string, state session.State, extension session.BudgetExtension) (*session.EffectiveBudget, error) {
	meta, err := r.store.LoadMetadata(childSessionID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(meta.ParentSessionID) != parentSessionID {
		return nil, fmt.Errorf("child session %s is not linked to parent session %s", childSessionID, parentSessionID)
	}
	if ensureChildEffectiveBudget(r.cfg, &meta, state, session.BudgetSourceLegacyResume) {
		if err := r.store.SaveMetadata(meta.ID, meta); err != nil {
			return nil, err
		}
	}
	previous := session.CloneEffectiveBudget(meta.EffectiveBudget)
	next, err := session.ExtendEffectiveBudget(previous, extension, state.Turn, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	childEvents, err := r.store.LoadEvents(childSessionID)
	if err != nil {
		return nil, err
	}
	parentEvents, err := r.store.LoadEvents(parentSessionID)
	if err != nil {
		return nil, err
	}
	if err := persistEffectiveBudget(r.store, meta, next); err != nil {
		return nil, err
	}
	rollback := func(cause error) error {
		var rollbackErrs []error
		if err := persistEffectiveBudget(r.store, meta, previous); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore effective budget: %w", err))
		}
		if err := r.store.RestoreEvents(childSessionID, childEvents); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore child events: %w", err))
		}
		if err := r.store.RestoreEvents(parentSessionID, parentEvents); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore parent events: %w", err))
		}
		return errors.Join(append([]error{cause}, rollbackErrs...)...)
	}
	childData := map[string]any{
		"parent_session_id": parentSessionID,
		"queue_job_id":      queueJobID,
		"previous_budget":   effectiveBudgetEventData(previous),
		"effective_budget":  effectiveBudgetEventData(next),
		"extension":         extension,
		"reason":            strings.TrimSpace(extension.Reason),
	}
	if err := r.appendEvent(childSessionID, "session.child_budget.extended", "control", childData); err != nil {
		return nil, rollback(err)
	}
	if err := r.appendEvent(parentSessionID, "session.child.budget_extended", "delegate", map[string]any{
		"session_id":       childSessionID,
		"queue_job_id":     queueJobID,
		"effective_budget": effectiveBudgetEventData(next),
		"extension":        extension,
	}); err != nil {
		return nil, rollback(err)
	}
	return next, nil
}

func (r *Runner) acquirePromptedChildRunSlot(parent session.SessionMetadata, childSessionID, queueJobID string) (session.QueueJob, bool, error) {
	limit := r.cfg.Runtime.MultiAgent.MaxActiveChildren
	if strings.TrimSpace(queueJobID) != "" {
		previous, acquired, err := r.store.AcquireQueueChildResumeSlot(parent.ID, queueJobID, childSessionID, limit)
		if err != nil {
			return session.QueueJob{}, false, fmt.Errorf("acquire queue child resume slot for %s: %w", queueJobID, err)
		}
		if !acquired {
			return session.QueueJob{}, false, fmt.Errorf("max active children reached: %d", limit)
		}
		return previous, false, nil
	}
	child, err := r.store.LoadMetadata(childSessionID)
	if err != nil {
		return session.QueueJob{}, false, err
	}
	if strings.TrimSpace(child.ParentSessionID) != parent.ID {
		return session.QueueJob{}, false, fmt.Errorf("child session %s is not linked to parent session %s", childSessionID, parent.ID)
	}
	rootSessionID := strings.TrimSpace(child.RootSessionID)
	if rootSessionID == "" {
		rootSessionID = strings.TrimSpace(parent.RootSessionID)
	}
	if rootSessionID == "" {
		rootSessionID = parent.ID
	}
	acquired, err := r.store.AcquireDirectChildSlot(parent.ID, rootSessionID, childSessionID, limit)
	if err != nil {
		return session.QueueJob{}, false, err
	}
	if !acquired {
		return session.QueueJob{}, false, fmt.Errorf("max active children reached: %d", limit)
	}
	return session.QueueJob{}, true, nil
}

func (r *Runner) releasePromptedChildRunSlot(childSessionID string, previousJob session.QueueJob, directSlotReserved bool) error {
	if previousJob.ID != "" {
		return r.store.SaveJob(previousJob)
	}
	if directSlotReserved {
		return r.store.ReleaseDirectChildSlot(childSessionID)
	}
	return nil
}

func (r *Runner) reconcilePromptedChildJob(parentSessionID string, previous session.QueueJob, result RunResult) error {
	job, err := r.store.LoadJob(previous.ID)
	if err != nil {
		return err
	}
	job.SessionID = result.SessionID
	job.SessionStatus = result.Status
	job.FinalText = result.FinalText
	job.LastError = result.LastError
	job.ClaimedBy = ""
	job.ClaimedAt = ""
	job.HeartbeatAt = ""
	job.WorkerPID = 0
	job.ProcessStartID = ""
	switch result.Status {
	case session.StatusCompleted:
		job.Status = session.QueueStatusCompleted
		job.StopReason = ""
	case session.StatusCancelled:
		job.Status = session.QueueStatusCancelled
		job.StopReason = session.QueueStopReasonAgentStop
	case session.StatusFailed:
		job.Status = session.QueueStatusFailed
		job.StopReason = ""
	default:
		job.Status = session.QueueStatusBlocked
		job.StopReason = ""
		if strings.TrimSpace(job.LastError) == "" {
			job.LastError = "child session is resumable: " + result.Status
		}
	}
	if meta, loadErr := r.store.LoadMetadata(result.SessionID); loadErr == nil {
		job.EffectiveWorkdir = meta.Workdir
		job.EffectiveBudget = session.CloneEffectiveBudget(meta.EffectiveBudget)
	}
	if err := r.store.SaveJob(job); err != nil {
		return err
	}
	if isTerminalQueueStatus(job.Status) {
		if err := resolveParentQueueJob(r.store, parentSessionID, job.ID, job.Status); err != nil {
			return err
		}
	}
	if err := r.store.EnsureBackgroundNotification(parentSessionID, session.NewBackgroundNotification(job)); err != nil {
		return err
	}
	eventType := "queue.job.blocked"
	switch job.Status {
	case session.QueueStatusCompleted:
		eventType = "queue.job.completed"
	case session.QueueStatusCancelled:
		eventType = "queue.job.cancelled"
	case session.QueueStatusFailed:
		eventType = "queue.job.failed"
	}
	if _, err := r.appendQueueJobEventOnce(parentSessionID, eventType, job); err != nil {
		return err
	}
	_ = writeSessionSummary(r.store, parentSessionID)
	_ = writeLongRunCheckpoint(r.store, parentSessionID)
	return nil
}

func (r *Runner) resolvePromptTarget(parentSessionID string, req tools.AgentPromptRequest) (string, string, error) {
	if strings.TrimSpace(req.SessionID) != "" {
		meta, err := r.store.LoadMetadata(strings.TrimSpace(req.SessionID))
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(meta.ParentSessionID) != parentSessionID {
			return "", "", fmt.Errorf("child session %s is not linked to parent session %s", meta.ID, parentSessionID)
		}
		return meta.ID, strings.TrimSpace(meta.QueueJobID), nil
	}
	jobID := strings.TrimSpace(req.QueueJobID)
	if jobID == "" {
		return "", "", errors.New("session_id or queue_job_id is required")
	}
	job, err := r.store.LoadJob(jobID)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(job.ParentSessionID) != parentSessionID {
		return "", "", fmt.Errorf("queue job %s is not linked to parent session %s", job.ID, parentSessionID)
	}
	if strings.TrimSpace(job.SessionID) != "" {
		meta, err := r.store.LoadMetadata(strings.TrimSpace(job.SessionID))
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(meta.ParentSessionID) != parentSessionID {
			return "", "", fmt.Errorf("child session %s is not linked to parent session %s", meta.ID, parentSessionID)
		}
		if strings.TrimSpace(meta.QueueJobID) != "" && strings.TrimSpace(meta.QueueJobID) != job.ID {
			return "", "", fmt.Errorf("child session %s queue_job_id mismatch: got %q, want %q", meta.ID, meta.QueueJobID, job.ID)
		}
		return meta.ID, job.ID, nil
	}
	children, err := r.store.ListChildren(parentSessionID, -1)
	if err != nil {
		return "", "", err
	}
	for _, child := range children {
		if strings.TrimSpace(child.QueueJobID) == job.ID {
			return child.ID, job.ID, nil
		}
	}
	return "", "", fmt.Errorf("queue job %s has no linked running child session yet; use agent_status/agent_list and retry after the worker starts it", job.ID)
}

func (r *Runner) AgentList(_ context.Context, parentSessionID string) (tools.AgentListResult, error) {
	if _, err := r.store.LoadMetadata(parentSessionID); err != nil {
		return tools.AgentListResult{}, err
	}
	sessions, err := r.store.ListChildren(parentSessionID, -1)
	if err != nil {
		return tools.AgentListResult{}, err
	}
	jobs, err := r.store.ListJobsByParent(parentSessionID, -1)
	if err != nil {
		return tools.AgentListResult{}, err
	}
	return tools.AgentListResult{Sessions: sessions, Jobs: jobs}, nil
}

type QueueSubmitRequest struct {
	ParentSessionID string
	Prompt          string
	AgentName       string
	AgentRole       string
	Provider        string
	Model           string
	ProviderOptions session.ProviderOptions
	Workdir         string
	SystemOverride  string
	Mode            string
	WaitMode        string
	ResumeParent    bool
	IsolationMode   string
	IsolationRoot   string
}

func (r *Runner) QueueSubmit(_ context.Context, req QueueSubmitRequest) (session.QueueJob, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return session.QueueJob{}, errors.New("prompt is required")
	}
	if err := r.rejectParentLinkedActionDuringPendingPlanMode(req.ParentSessionID, "queue submission"); err != nil {
		return session.QueueJob{}, err
	}
	agentRole, err := normalizeAgentRole(req.AgentRole, req.AgentName)
	if err != nil {
		return session.QueueJob{}, err
	}
	req.AgentRole = agentRole
	mode, err := normalizeAndValidateRunMode(req.Mode, session.ModeExec)
	if err != nil {
		return session.QueueJob{}, err
	}
	waitMode, err := normalizeAndValidateParentWaitMode(req.WaitMode)
	if err != nil {
		return session.QueueJob{}, err
	}
	isolationMode, err := normalizeAndValidateIsolationMode(req.IsolationMode, "auto")
	if err != nil {
		return session.QueueJob{}, err
	}
	rootSessionID := req.ParentSessionID
	var parentMeta *session.SessionMetadata
	if req.ParentSessionID != "" {
		loadedParentMeta, err := r.store.LoadMetadata(req.ParentSessionID)
		if err != nil {
			return session.QueueJob{}, err
		}
		if isNestedAgentParent(loadedParentMeta) {
			return session.QueueJob{}, errors.New("nested sub-agents are not allowed; only the root master session can create sub-agents")
		}
		parentMeta = &loadedParentMeta
		if parentMeta.RootSessionID != "" {
			rootSessionID = parentMeta.RootSessionID
		} else {
			rootSessionID = parentMeta.ID
		}
	}
	workdir, err := resolveRequestedWorkdir(req.Workdir, parentMeta)
	if err != nil {
		return session.QueueJob{}, err
	}
	providerName, modelName, providerCfg, err := resolveProviderAndModel(r.cfg, parentMeta, req.Provider, req.Model, req.AgentRole)
	if err != nil {
		return session.QueueJob{}, WrapConfigError(err)
	}
	providerOptions, err := resolvedProviderOptions(providerName, providerCfg, req.ProviderOptions)
	if err != nil {
		return session.QueueJob{}, err
	}
	createdAt := time.Now().UTC()
	job := session.QueueJob{
		SchemaVersion:    1,
		ID:               session.NewQueueJobID(),
		CreatedAt:        createdAt.Format(time.RFC3339Nano),
		UpdatedAt:        createdAt.Format(time.RFC3339Nano),
		Status:           session.QueueStatusQueued,
		ParentSessionID:  req.ParentSessionID,
		RootSessionID:    rootSessionID,
		AgentName:        req.AgentName,
		AgentRole:        req.AgentRole,
		Prompt:           req.Prompt,
		Mode:             mode,
		Provider:         providerName,
		Model:            modelName,
		ProviderOptions:  providerOptions,
		RequestedWorkdir: workdir,
		SystemOverride:   req.SystemOverride,
		Background:       true,
		WaitMode:         waitMode,
		ResumeParent:     req.ResumeParent,
		IsolationMode:    isolationMode,
		IsolationRoot:    req.IsolationRoot,
		EffectiveBudget:  newEffectiveChildBudget(r.cfg, session.BudgetSourceRuntimeChild, 0, createdAt),
	}
	if err := r.store.EnqueueJob(job); err != nil {
		return session.QueueJob{}, err
	}
	if job.ParentSessionID != "" {
		previousCoordination, err := r.store.SnapshotParentCoordination(job.ParentSessionID)
		if err != nil {
			if deleteErr := r.store.DeleteJob(job.ID); deleteErr != nil {
				return session.QueueJob{}, fmt.Errorf("snapshot parent coordination for queue job %s failed with %v; delete queued job after failed parent coordination snapshot: %w", job.ID, err, deleteErr)
			}
			return session.QueueJob{}, err
		}
		if err := addParentQueueJob(r.store, job.ParentSessionID, job.ID, job.WaitMode); err != nil {
			if deleteErr := r.store.DeleteJob(job.ID); deleteErr != nil {
				return session.QueueJob{}, fmt.Errorf("persist parent coordination for queue job %s failed with %v; delete queued job after failed parent coordination: %w", job.ID, err, deleteErr)
			}
			return session.QueueJob{}, err
		}
		if err := retryQueuePersistence("append session.child.queued event for job "+job.ID, func() error {
			return r.appendEvent(job.ParentSessionID, "session.child.queued", "delegate", map[string]any{
				"job_id":        job.ID,
				"agent_name":    job.AgentName,
				"agent_role":    job.AgentRole,
				"wait_mode":     job.WaitMode,
				"resume_parent": job.ResumeParent,
			})
		}); err != nil {
			if restoreErr := r.store.RestoreParentCoordination(job.ParentSessionID, previousCoordination); restoreErr != nil {
				return session.QueueJob{}, fmt.Errorf("append session.child.queued event for queue job %s failed with %v; restore parent coordination after failed child queued event: %w", job.ID, err, restoreErr)
			}
			if deleteErr := r.store.DeleteJob(job.ID); deleteErr != nil {
				return session.QueueJob{}, fmt.Errorf("append session.child.queued event for queue job %s failed with %v; delete queued job after failed child queued event: %w", job.ID, err, deleteErr)
			}
			return session.QueueJob{}, err
		}
		_ = writeSessionSummary(r.store, job.ParentSessionID)
		_ = writeLongRunCheckpoint(r.store, job.ParentSessionID)
	}
	return job, nil
}

func isNestedAgentParent(meta session.SessionMetadata) bool {
	return meta.Depth > 0 || strings.TrimSpace(meta.ParentSessionID) != ""
}

func (r *Runner) QueueShow(jobID string) (session.QueueJob, error) {
	return r.store.LoadJob(jobID)
}

func (r *Runner) QueueList(limit int) ([]session.QueueJob, error) {
	return r.store.ListJobs(limit)
}

func (r *Runner) ProcessNextJob(ctx context.Context) (session.QueueJob, bool, error) {
	job, ok, err := r.store.ClaimNextQueuedJobWithLimit(r.cfg.Runtime.MultiAgent.MaxActiveChildren)
	if err != nil || !ok {
		return job, ok, err
	}
	if job.ParentSessionID != "" {
		if err := retryQueuePersistence("append queue.job.claimed event for job "+job.ID, func() error {
			return r.appendEvent(job.ParentSessionID, "queue.job.claimed", "queue", map[string]any{
				"job_id":           job.ID,
				"claimed_by":       job.ClaimedBy,
				"process_start_id": job.ProcessStartID,
				"worker_pid":       job.WorkerPID,
			})
		}); err != nil {
			restored := clearQueueClaim(job)
			if restoreErr := r.store.SaveJob(restored); restoreErr != nil {
				return job, ok, fmt.Errorf("append queue.job.claimed event for queue job %s failed with %v; restore queued job after failed queue claim: %w", job.ID, err, restoreErr)
			}
			return restored, ok, err
		}
	}
	childRunner := NewRunner(r.cfg)
	childRunner.SetRunLifecycleHooks(r.lifecycleHooksSnapshot())
	stopHeartbeat := r.startQueueJobHeartbeat(ctx, job.ID)
	childSessionID := session.NewSessionID()
	job.SessionID = childSessionID
	job.SessionStatus = session.StatusRunning
	if err := r.store.SaveJob(job); err != nil {
		stopHeartbeat()
		return job, true, err
	}
	result, runErr := childRunner.Start(ctx, StartRequest{
		SessionID:       childSessionID,
		Prompt:          job.Prompt,
		Provider:        job.Provider,
		Model:           job.Model,
		ProviderOptions: job.ProviderOptions,
		Workdir:         job.RequestedWorkdir,
		Mode:            job.Mode,
		SystemOverride:  job.SystemOverride,
		ParentSessionID: job.ParentSessionID,
		AgentName:       job.AgentName,
		AgentRole:       job.AgentRole,
		QueueJobID:      job.ID,
		IsolationMode:   job.IsolationMode,
		IsolationRoot:   job.IsolationRoot,
		EffectiveBudget: session.CloneEffectiveBudget(job.EffectiveBudget),
	})
	stopHeartbeat()
	if heartbeatJob, heartbeatErr := r.store.RefreshQueueJobHeartbeat(job.ID); heartbeatErr == nil {
		copyQueueLeaseFields(&job, heartbeatJob)
	}
	job.SessionID = result.SessionID
	job.SessionStatus = result.Status
	job.FinalText = result.FinalText
	job.LastError = result.LastError
	queueReconcileErr := runErr != nil && result.SessionID != "" && result.Status != "" && isLinkedQueueJobReconcileError(runErr)
	var handoffErr error
	if result.SessionID != "" {
		if meta, err := childRunner.store.LoadMetadata(result.SessionID); err == nil {
			job.EffectiveWorkdir = meta.Workdir
			job.EffectiveBudget = session.CloneEffectiveBudget(meta.EffectiveBudget)
		} else {
			handoffErr = fmt.Errorf("load child session metadata for queue job %s: %w", job.ID, err)
		}
		if handoffErr == nil {
			if messages, err := childRunner.store.LoadMessages(result.SessionID); err == nil {
				job.VisiblePaths = collectVisibleSessionOutputs(job.EffectiveWorkdir, messages)
				var syncErr error
				job.VisiblePaths, syncErr = syncVisibleSessionOutputs(job.RequestedWorkdir, job.EffectiveWorkdir, job.VisiblePaths)
				if syncErr != nil {
					handoffErr = fmt.Errorf("sync child visible outputs for queue job %s: %w", job.ID, syncErr)
				}
			} else {
				handoffErr = fmt.Errorf("load child session messages.jsonl for queue job %s: %w", job.ID, err)
			}
		}
	}
	if handoffErr != nil {
		job.Status = session.QueueStatusFailed
		job.LastError = handoffErr.Error()
	} else if (runErr != nil && !queueReconcileErr) || result.Status == session.StatusFailed {
		job.Status = session.QueueStatusFailed
		if job.LastError == "" && runErr != nil {
			job.LastError = runErr.Error()
		}
	} else if result.Status == session.StatusCompleted {
		job.Status = session.QueueStatusCompleted
	} else if result.Status == session.StatusCancelled {
		job.Status = session.QueueStatusCancelled
		job.StopReason = session.QueueStopReasonAgentStop
		if job.LastError == "" {
			job.LastError = "cancelled by parent agent"
		}
	} else {
		job.Status = session.QueueStatusBlocked
		if job.LastError == "" {
			job.LastError = "child session is resumable: " + result.Status
		}
	}
	clearQueueLeaseFields(&job)
	if err := retryQueuePersistence("persist queue job "+job.ID, func() error {
		return r.store.SaveJob(job)
	}); err != nil {
		return job, true, err
	}
	if job.ParentSessionID != "" {
		var terminalCoordinationSnapshot session.ParentCoordinationSnapshot
		var restoreTerminalCoordination bool
		if isTerminalQueueStatus(job.Status) {
			var snapshotErr error
			terminalCoordinationSnapshot, snapshotErr = r.store.SnapshotParentCoordination(job.ParentSessionID)
			if snapshotErr != nil {
				return job, true, snapshotErr
			}
			restoreTerminalCoordination = true
			if err := resolveParentQueueJob(r.store, job.ParentSessionID, job.ID, job.Status); err != nil {
				return job, true, err
			}
		}
		notification := session.NewBackgroundNotification(job)
		notificationSnapshot, snapshotErr := r.store.SnapshotBackgroundNotification(job.ParentSessionID, job.ID)
		if snapshotErr != nil {
			if restoreTerminalCoordination {
				if restoreErr := r.store.RestoreParentCoordination(job.ParentSessionID, terminalCoordinationSnapshot); restoreErr != nil {
					return job, true, fmt.Errorf("snapshot background notification for job %s failed with %v; restore parent coordination: %w", job.ID, snapshotErr, restoreErr)
				}
			}
			return job, true, snapshotErr
		}
		if err := retryQueuePersistence("append background notification for job "+job.ID, func() error {
			return r.store.EnsureBackgroundNotification(job.ParentSessionID, notification)
		}); err != nil {
			if restoreTerminalCoordination {
				if restoreErr := r.store.RestoreParentCoordination(job.ParentSessionID, terminalCoordinationSnapshot); restoreErr != nil {
					return job, true, fmt.Errorf("append background notification for job %s failed with %v; restore parent coordination: %w", job.ID, err, restoreErr)
				}
			}
			return job, true, err
		}
		if err := retryQueuePersistence("append queue notified event for job "+job.ID, func() error {
			_, err := r.appendQueueJobEventOnce(job.ParentSessionID, "queue.job.notified", job)
			return err
		}); err != nil {
			if restoreTerminalCoordination {
				if restoreErr := r.store.RestoreParentCoordination(job.ParentSessionID, terminalCoordinationSnapshot); restoreErr != nil {
					return job, true, fmt.Errorf("append queue notified event for job %s failed with %v; restore parent coordination after failed queue notified event: %w", job.ID, err, restoreErr)
				}
			}
			if restoreErr := r.store.RestoreBackgroundNotification(job.ParentSessionID, notificationSnapshot); restoreErr != nil {
				return job, true, fmt.Errorf("append queue notified event for job %s failed with %v; restore background notification after failed queue notified event: %w", job.ID, err, restoreErr)
			}
			return job, true, err
		}
		eventType := "queue.job.blocked"
		if job.Status == session.QueueStatusCompleted {
			eventType = "queue.job.completed"
		}
		if job.Status == session.QueueStatusCancelled {
			eventType = "queue.job.cancelled"
		}
		if job.Status == session.QueueStatusFailed {
			eventType = "queue.job.failed"
		}
		if err := retryQueuePersistence("append queue lifecycle event "+eventType+" for job "+job.ID, func() error {
			exists, err := r.queueJobEventExists(job.ParentSessionID, eventType, job.ID)
			if err != nil {
				return err
			}
			if exists {
				return nil
			}
			if r.beforeQueueLifecycleEvent != nil {
				r.beforeQueueLifecycleEvent(job, eventType)
			}
			return r.appendQueueJobEvent(job.ParentSessionID, eventType, job)
		}); err != nil {
			if restoreTerminalCoordination {
				if restoreErr := r.store.RestoreParentCoordination(job.ParentSessionID, terminalCoordinationSnapshot); restoreErr != nil {
					return job, true, fmt.Errorf("append queue lifecycle event %s for job %s failed with %v; restore parent coordination after failed queue lifecycle event: %w", eventType, job.ID, err, restoreErr)
				}
			}
			if restoreErr := r.store.RestoreBackgroundNotification(job.ParentSessionID, notificationSnapshot); restoreErr != nil {
				return job, true, fmt.Errorf("append queue lifecycle event %s for job %s failed with %v; restore background notification after failed queue lifecycle event: %w", eventType, job.ID, err, restoreErr)
			}
			return job, true, err
		}
		_ = writeSessionSummary(r.store, job.ParentSessionID)
		_ = writeLongRunCheckpoint(r.store, job.ParentSessionID)
	}
	// Failed jobs are part of normal queue lifecycle. Persist the failure on the
	// job record and let the worker keep polling unless queue I/O itself failed.
	return job, true, nil
}

func (r *Runner) startQueueJobHeartbeat(ctx context.Context, jobID string) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	interval := r.queueJobHeartbeatInterval()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			default:
			}
			_, _ = r.store.RefreshQueueJobHeartbeat(jobID)
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func isTerminalQueueStatus(status string) bool {
	return status == session.QueueStatusCompleted || status == session.QueueStatusCancelled || status == session.QueueStatusFailed
}

func (r *Runner) appendQueueJobEventOnce(parentSessionID, eventType string, job session.QueueJob) (bool, error) {
	exists, err := r.queueJobEventExists(parentSessionID, eventType, job.ID)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	return true, r.appendQueueJobEvent(parentSessionID, eventType, job)
}

func (r *Runner) queueJobEventExists(parentSessionID, eventType, jobID string) (bool, error) {
	eventsList, err := r.store.LoadEvents(parentSessionID)
	if err != nil {
		return false, err
	}
	for _, evt := range eventsList {
		if evt.Type != eventType {
			continue
		}
		eventJobID, _ := evt.Data["job_id"].(string)
		if eventJobID == jobID {
			return true, nil
		}
	}
	return false, nil
}

func (r *Runner) appendQueueJobEvent(parentSessionID, eventType string, job session.QueueJob) error {
	data := map[string]any{
		"job_id":      job.ID,
		"session_id":  job.SessionID,
		"status":      job.Status,
		"agent_role":  job.AgentRole,
		"stop_reason": job.StopReason,
		"last_error":  job.LastError,
	}
	if job.EffectiveBudget != nil {
		data["effective_budget"] = effectiveBudgetEventData(job.EffectiveBudget)
	}
	return r.appendEvent(parentSessionID, eventType, "queue", data)
}

func (r *Runner) queueJobHeartbeatInterval() time.Duration {
	interval := time.Duration(r.cfg.Runtime.Queue.PollIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	if interval < 50*time.Millisecond {
		return 50 * time.Millisecond
	}
	if interval > 5*time.Second {
		return 5 * time.Second
	}
	return interval
}

func copyQueueLeaseFields(target *session.QueueJob, source session.QueueJob) {
	target.ClaimedBy = source.ClaimedBy
	target.ClaimedAt = source.ClaimedAt
	target.HeartbeatAt = source.HeartbeatAt
	target.WorkerPID = source.WorkerPID
	target.ProcessStartID = source.ProcessStartID
}

func clearQueueLeaseFields(job *session.QueueJob) {
	if job == nil {
		return
	}
	job.ClaimedBy = ""
	job.ClaimedAt = ""
	job.HeartbeatAt = ""
	job.WorkerPID = 0
	job.ProcessStartID = ""
}

func clearQueueClaim(job session.QueueJob) session.QueueJob {
	job.Status = session.QueueStatusQueued
	clearQueueLeaseFields(&job)
	job.SessionID = ""
	job.SessionStatus = ""
	job.EffectiveWorkdir = ""
	job.VisiblePaths = nil
	job.LastError = ""
	job.FinalText = ""
	return job
}

func retryQueuePersistence(label string, fn func() error) error {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(attempt*25) * time.Millisecond)
	}
	return fmt.Errorf("%s: %w", label, lastErr)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func collectVisibleSessionOutputs(workdir string, messages []session.Message) []string {
	base, ok := resolvedExistingDir(workdir)
	if !ok {
		return nil
	}
	paths := make([]string, 0, 4)
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for _, result := range msg.ToolResults {
			if result.IsError || (result.Name != "write_file" && result.Name != "edit_file") {
				continue
			}
			path, _ := result.Metadata["path"].(string)
			target, ok := resolvedExistingPath(path)
			if !ok {
				continue
			}
			rel, ok := relativePathWithin(base, target)
			if !ok {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !isProjectMemoryPath(rel) {
				valid, _ := result.Metadata["review_artifact_valid"].(bool)
				if !valid && !looksFinalArtifactPath(rel) && !looksFinalArtifactPath(target) {
					continue
				}
			}
			paths = append(paths, rel)
		}
	}
	return uniqueRelativePaths(paths)
}

func syncVisibleSessionOutputs(requestedWorkdir, effectiveWorkdir string, visiblePaths []string) ([]string, error) {
	visiblePaths = uniqueRelativePaths(visiblePaths)
	if len(visiblePaths) == 0 {
		return nil, nil
	}
	requestedRoot, ok := resolvedExistingDir(requestedWorkdir)
	if !ok {
		return nil, fmt.Errorf("requested workdir is not an existing directory: %s", requestedWorkdir)
	}
	effectiveRoot, ok := resolvedExistingDir(effectiveWorkdir)
	if !ok {
		return nil, fmt.Errorf("effective workdir is not an existing directory: %s", effectiveWorkdir)
	}
	if requestedRoot == effectiveRoot {
		return visiblePaths, nil
	}
	out := make([]string, 0, len(visiblePaths))
	for _, rel := range visiblePaths {
		if err := tools.CheckWorkspaceWriteInputAllowed(requestedRoot, rel); err != nil {
			return out, fmt.Errorf("visible output %s is not allowed in requested workdir: %w", rel, err)
		}
		src, err := tools.ResolveWorkspacePath(effectiveRoot, rel)
		if err != nil {
			return out, fmt.Errorf("resolve visible output source %s: %w", rel, err)
		}
		dst, err := tools.ResolveWorkspacePath(requestedRoot, rel)
		if err != nil {
			return out, fmt.Errorf("resolve visible output destination %s: %w", rel, err)
		}
		if err := tools.CheckWorkspaceWriteAllowed(requestedRoot, dst); err != nil {
			return out, fmt.Errorf("visible output %s is not allowed in requested workdir: %w", rel, err)
		}
		data, _, err := fileutil.ReadRegularFileNoSymlink(src)
		if err != nil {
			return out, fmt.Errorf("read visible output %s from child workdir: %w", rel, err)
		}
		if err := fileutil.AtomicWriteFileNoSymlink(dst, data, 0o600); err != nil {
			return out, fmt.Errorf("write visible output %s to requested workdir: %w", rel, err)
		}
		out = append(out, rel)
	}
	return uniqueRelativePaths(out), nil
}

func resolvedExistingDir(path string) (string, bool) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return "", false
	}
	abs, err := filepath.Abs(cleaned)
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

func resolvedExistingPath(path string) (string, bool) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return "", false
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", false
	}
	return resolved, true
}

func relativePathWithin(base, target string) (string, bool) {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." {
		return "", false
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	return filepath.Clean(rel), true
}

func uniqueRelativePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
		if cleaned == "" || cleaned == "." {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}
