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
	childRunner := NewRunner(r.cfg)
	childRunner.SetRunLifecycleHooks(r.lifecycleHooksSnapshot())
	result, err := childRunner.Start(ctx, StartRequest{
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
		if coordinationErr := addParentChildSession(r.store, req.ParentSessionID, result.SessionID, waitMode); coordinationErr != nil {
			if restoreErr := r.restoreDirectChildParentFacts(req.ParentSessionID, coordinationSnapshot, eventsSnapshot); restoreErr != nil {
				coordinationErr = fmt.Errorf("persist parent coordination for child session %s failed with %v; restore parent child facts: %w", result.SessionID, coordinationErr, restoreErr)
			} else {
				coordinationErr = fmt.Errorf("persist parent coordination for child session %s: %w", result.SessionID, coordinationErr)
			}
			return out, errors.Join(err, coordinationErr)
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
			QueueJobID:    job.ID,
			Status:        job.Status,
			SessionID:     job.SessionID,
			SessionStatus: job.SessionStatus,
			FinalText:     job.FinalText,
			StopReason:    job.StopReason,
			LastError:     job.LastError,
			Workdir:       firstNonEmpty(job.EffectiveWorkdir, job.RequestedWorkdir),
			AgentName:     job.AgentName,
			AgentRole:     job.AgentRole,
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
		SessionID:     meta.ID,
		Status:        state.Status,
		SessionStatus: state.Status,
		FinalText:     state.LastAssistantExcerpt,
		StopReason:    state.PauseReason,
		LastError:     state.LastError,
		Workdir:       meta.Workdir,
		AgentName:     meta.AgentName,
		AgentRole:     meta.AgentRole,
	}, nil
}

func (r *Runner) StopAgent(_ context.Context, req tools.AgentStopRequest) (tools.AgentStopResult, error) {
	parentSessionID := strings.TrimSpace(req.ParentSessionID)
	if parentSessionID == "" {
		return tools.AgentStopResult{}, errors.New("parent session id is required")
	}
	parentMeta, err := r.store.LoadMetadata(parentSessionID)
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	jobID := strings.TrimSpace(req.QueueJobID)
	if jobID == "" {
		return tools.AgentStopResult{}, errors.New("queue_job_id is required")
	}
	job, err := r.store.LoadJob(jobID)
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	if strings.TrimSpace(job.ParentSessionID) != parentMeta.ID {
		return tools.AgentStopResult{}, fmt.Errorf("queue job %s is not linked to parent session %s", job.ID, parentMeta.ID)
	}
	if job.Status != session.QueueStatusQueued {
		return tools.AgentStopResult{}, fmt.Errorf("queue job %s is %s and cannot be safely stopped by parent; use agent_wait or inspect it with agent_status", job.ID, job.Status)
	}
	previousJob := job
	coordinationSnapshot, err := r.store.SnapshotParentCoordination(parentMeta.ID)
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	notificationSnapshot, err := r.store.SnapshotBackgroundNotification(parentMeta.ID, job.ID)
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	job, err = r.store.StopQueuedJob(job.ID, parentMeta.ID, "stopped by parent agent before worker claim")
	if err != nil {
		return tools.AgentStopResult{}, err
	}
	if err := resolveParentQueueJob(r.store, parentMeta.ID, job.ID, job.Status); err != nil {
		if restoreErr := r.store.SaveJob(previousJob); restoreErr != nil {
			return tools.AgentStopResult{}, fmt.Errorf("resolve parent coordination after stopping job %s failed with %v; restore job: %w", job.ID, err, restoreErr)
		}
		return tools.AgentStopResult{}, err
	}
	if err := r.store.EnsureBackgroundNotification(parentMeta.ID, session.NewBackgroundNotification(job)); err != nil {
		if restoreErr := r.store.RestoreParentCoordination(parentMeta.ID, coordinationSnapshot); restoreErr != nil {
			return tools.AgentStopResult{}, fmt.Errorf("append stop notification for job %s failed with %v; restore parent coordination: %w", job.ID, err, restoreErr)
		}
		if restoreErr := r.store.SaveJob(previousJob); restoreErr != nil {
			return tools.AgentStopResult{}, fmt.Errorf("append stop notification for job %s failed with %v; restore job: %w", job.ID, err, restoreErr)
		}
		return tools.AgentStopResult{}, err
	}
	if err := r.appendEvent(parentMeta.ID, "queue.job.stopped", "delegate", map[string]any{
		"job_id":     job.ID,
		"last_error": job.LastError,
	}); err != nil {
		if restoreErr := r.store.RestoreParentCoordination(parentMeta.ID, coordinationSnapshot); restoreErr != nil {
			return tools.AgentStopResult{}, fmt.Errorf("append queue.job.stopped event for job %s failed with %v; restore parent coordination: %w", job.ID, err, restoreErr)
		}
		if restoreErr := r.store.RestoreBackgroundNotification(parentMeta.ID, notificationSnapshot); restoreErr != nil {
			return tools.AgentStopResult{}, fmt.Errorf("append queue.job.stopped event for job %s failed with %v; restore notification: %w", job.ID, err, restoreErr)
		}
		if restoreErr := r.store.SaveJob(previousJob); restoreErr != nil {
			return tools.AgentStopResult{}, fmt.Errorf("append queue.job.stopped event for job %s failed with %v; restore job: %w", job.ID, err, restoreErr)
		}
		return tools.AgentStopResult{}, err
	}
	_ = writeSessionSummary(r.store, parentMeta.ID)
	_ = writeLongRunCheckpoint(r.store, parentMeta.ID)
	return tools.AgentStopResult{QueueJobID: job.ID, Status: job.Status, LastError: job.LastError}, nil
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
		if !childPromptCanContinueParentStopped(r.store, childSessionID, queueJobID, state) {
			return tools.AgentPromptResult{}, fmt.Errorf("child session %s is %s and is not a parent-stopped session that agent_prompt can restart", childSessionID, state.Status)
		}
		childRunner := NewRunner(r.cfg)
		childRunner.SetRunLifecycleHooks(r.lifecycleHooksSnapshot())
		result, err := childRunner.Continue(ctx, ContinueRequest{
			SessionID: childSessionID,
			Message:   message,
			Source:    "agent",
		})
		if err != nil {
			return tools.AgentPromptResult{}, err
		}
		if err := r.appendEvent(parentMeta.ID, "session.child.prompted", "delegate", map[string]any{
			"session_id":   childSessionID,
			"queue_job_id": queueJobID,
			"behavior":     "continued_parent_stopped_child",
			"status":       result.Status,
		}); err != nil {
			return tools.AgentPromptResult{}, err
		}
		_ = writeSessionSummary(r.store, parentMeta.ID)
		_ = writeLongRunCheckpoint(r.store, parentMeta.ID)
		return tools.AgentPromptResult{
			SessionID:  childSessionID,
			QueueJobID: queueJobID,
			Accepted:   true,
			Behavior:   "continued_parent_stopped_child",
		}, nil
	}
	interrupt := true
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
	}, nil
}

func isParentStoppedPreClaimJob(job session.QueueJob) bool {
	return job.Status == session.QueueStatusBlocked &&
		job.StopReason == session.QueueStopReasonParentStop &&
		strings.TrimSpace(job.SessionID) == ""
}

func childPromptCanContinueParentStopped(store *session.Store, childSessionID, queueJobID string, state session.State) bool {
	switch state.Status {
	case session.StatusPaused, session.StatusAwaitingInput, session.StatusFailed:
	default:
		return false
	}
	if strings.TrimSpace(queueJobID) != "" {
		job, err := store.LoadJob(queueJobID)
		if err != nil {
			return false
		}
		return job.StopReason == session.QueueStopReasonParentStop && strings.TrimSpace(job.SessionID) == childSessionID
	}
	return state.Status == session.StatusPaused && state.PauseReason == "manual_stop"
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
	job := session.QueueJob{
		SchemaVersion:    1,
		ID:               session.NewQueueJobID(),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
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

func (r *Runner) QueueShow(jobID string) (session.QueueJob, error) {
	return r.store.LoadJob(jobID)
}

func (r *Runner) QueueList(limit int) ([]session.QueueJob, error) {
	return r.store.ListJobs(limit)
}

func (r *Runner) ProcessNextJob(ctx context.Context) (session.QueueJob, bool, error) {
	job, ok, err := r.store.ClaimNextQueuedJob()
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
	result, runErr := childRunner.Start(ctx, StartRequest{
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
	} else {
		job.Status = session.QueueStatusBlocked
		if job.LastError == "" {
			job.LastError = "child session is resumable: " + result.Status
		}
	}
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
	return status == session.QueueStatusCompleted || status == session.QueueStatusFailed
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
	return r.appendEvent(parentSessionID, eventType, "queue", map[string]any{
		"job_id":     job.ID,
		"session_id": job.SessionID,
		"status":     job.Status,
		"agent_role": job.AgentRole,
	})
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

func clearQueueClaim(job session.QueueJob) session.QueueJob {
	job.Status = session.QueueStatusQueued
	job.ClaimedBy = ""
	job.ClaimedAt = ""
	job.HeartbeatAt = ""
	job.WorkerPID = 0
	job.ProcessStartID = ""
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
