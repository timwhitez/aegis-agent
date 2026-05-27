package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	mode := strings.TrimSpace(req.Mode)
	mode = normalizeRunMode(mode, session.ModeExec)
	isolationMode := normalizeIsolationMode(req.IsolationMode, "auto")
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
			WaitMode:        req.WaitMode,
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
				out.VisiblePaths = syncVisibleSessionOutputs(requestedChildWorkdir, effectiveWorkdir, out.VisiblePaths)
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
		if eventErr := r.appendEvent(req.ParentSessionID, "session.child.spawned", "delegate", map[string]any{
			"session_id": result.SessionID,
			"status":     out.Status,
			"agent_name": req.AgentName,
			"agent_role": out.AgentRole,
			"wait_mode":  normalizeParentWaitMode(req.WaitMode),
		}); eventErr != nil {
			if err != nil {
				return out, err
			}
			return out, fmt.Errorf("append session.child.spawned event for child session %s: %w", result.SessionID, eventErr)
		}
		if coordinationErr := addParentChildSession(r.store, req.ParentSessionID, result.SessionID, req.WaitMode); coordinationErr != nil {
			if err != nil {
				return out, err
			}
			return out, coordinationErr
		}
		if coordinationErr := resolveParentChildSession(r.store, req.ParentSessionID, result.SessionID, coordinationStatus); coordinationErr != nil {
			if err != nil {
				return out, err
			}
			return out, coordinationErr
		}
		_ = writeSessionSummary(r.store, req.ParentSessionID)
		_ = writeLongRunCheckpoint(r.store, req.ParentSessionID)
	}
	return out, err
}

func (r *Runner) AgentStatus(_ context.Context, req tools.AgentStatusRequest) (tools.AgentStatusResult, error) {
	if strings.TrimSpace(req.QueueJobID) != "" {
		job, err := r.store.LoadJob(req.QueueJobID)
		if err != nil {
			return tools.AgentStatusResult{}, err
		}
		return tools.AgentStatusResult{
			QueueJobID:    job.ID,
			Status:        job.Status,
			SessionID:     job.SessionID,
			SessionStatus: job.SessionStatus,
			FinalText:     job.FinalText,
			LastError:     job.LastError,
			Workdir:       firstNonEmpty(job.EffectiveWorkdir, job.RequestedWorkdir),
			AgentName:     job.AgentName,
			AgentRole:     job.AgentRole,
		}, nil
	}
	meta, err := r.store.LoadMetadata(req.SessionID)
	if err != nil {
		return tools.AgentStatusResult{}, err
	}
	state, err := r.store.LoadState(req.SessionID)
	if err != nil {
		return tools.AgentStatusResult{}, err
	}
	return tools.AgentStatusResult{
		SessionID:     meta.ID,
		Status:        state.Status,
		SessionStatus: state.Status,
		FinalText:     state.LastAssistantExcerpt,
		LastError:     state.LastError,
		Workdir:       meta.Workdir,
		AgentName:     meta.AgentName,
		AgentRole:     meta.AgentRole,
	}, nil
}

func (r *Runner) AgentList(_ context.Context, parentSessionID string) (tools.AgentListResult, error) {
	sessions, err := r.store.ListChildren(parentSessionID, 100)
	if err != nil {
		return tools.AgentListResult{}, err
	}
	jobs, err := r.store.ListJobsByParent(parentSessionID, 100)
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
	mode := strings.TrimSpace(req.Mode)
	mode = normalizeRunMode(mode, session.ModeExec)
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
	providerOptions := req.ProviderOptions
	if providerOptions == (session.ProviderOptions{}) {
		providerOptions = providerOptionsFromConfig(providerName, providerCfg)
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
		WaitMode:         normalizeParentWaitMode(req.WaitMode),
		IsolationMode:    normalizeIsolationMode(req.IsolationMode, "auto"),
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
				"job_id":     job.ID,
				"agent_name": job.AgentName,
				"agent_role": job.AgentRole,
				"wait_mode":  job.WaitMode,
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
		if err := retryQueuePersistence("append queue claimed event for job "+job.ID, func() error {
			return r.appendEvent(job.ParentSessionID, "queue.job.claimed", "queue", map[string]any{
				"job_id":           job.ID,
				"claimed_by":       job.ClaimedBy,
				"process_start_id": job.ProcessStartID,
				"worker_pid":       job.WorkerPID,
			})
		}); err != nil {
			return job, ok, err
		}
	}
	childRunner := NewRunner(r.cfg)
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
				job.VisiblePaths = syncVisibleSessionOutputs(job.RequestedWorkdir, job.EffectiveWorkdir, job.VisiblePaths)
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
		notification := session.NewBackgroundNotification(job)
		if err := retryQueuePersistence("append background notification for job "+job.ID, func() error {
			return r.store.EnsureBackgroundNotification(job.ParentSessionID, notification)
		}); err != nil {
			return job, true, err
		}
		if err := retryQueuePersistence("append queue notified event for job "+job.ID, func() error {
			return r.appendEvent(job.ParentSessionID, "queue.job.notified", "queue", map[string]any{
				"job_id":     job.ID,
				"session_id": job.SessionID,
				"status":     job.Status,
				"agent_role": job.AgentRole,
			})
		}); err != nil {
			return job, true, err
		}
		if isTerminalQueueStatus(job.Status) {
			if err := resolveParentQueueJob(r.store, job.ParentSessionID, job.ID, job.Status); err != nil {
				return job, true, err
			}
		}
		_ = writeSessionSummary(r.store, job.ParentSessionID)
		_ = writeLongRunCheckpoint(r.store, job.ParentSessionID)
	}
	if job.ParentSessionID != "" {
		eventType := "queue.job.blocked"
		if job.Status == session.QueueStatusCompleted {
			eventType = "queue.job.completed"
		}
		if job.Status == session.QueueStatusFailed {
			eventType = "queue.job.failed"
		}
		if err := retryQueuePersistence("append queue lifecycle event for job "+job.ID, func() error {
			return r.appendEvent(job.ParentSessionID, eventType, "queue", map[string]any{
				"job_id":     job.ID,
				"session_id": job.SessionID,
				"status":     job.Status,
				"agent_role": job.AgentRole,
			})
		}); err != nil {
			return job, true, err
		}
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

func syncVisibleSessionOutputs(requestedWorkdir, effectiveWorkdir string, visiblePaths []string) []string {
	visiblePaths = uniqueRelativePaths(visiblePaths)
	if len(visiblePaths) == 0 {
		return nil
	}
	requestedRoot, ok := resolvedExistingDir(requestedWorkdir)
	if !ok {
		return nil
	}
	effectiveRoot, ok := resolvedExistingDir(effectiveWorkdir)
	if !ok {
		return nil
	}
	if requestedRoot == effectiveRoot {
		return visiblePaths
	}
	out := make([]string, 0, len(visiblePaths))
	for _, rel := range visiblePaths {
		if err := tools.CheckWorkspaceWriteInputAllowed(requestedRoot, rel); err != nil {
			continue
		}
		src, err := tools.ResolveWorkspacePath(effectiveRoot, rel)
		if err != nil {
			continue
		}
		dst, err := tools.ResolveWorkspacePath(requestedRoot, rel)
		if err != nil {
			continue
		}
		if err := tools.CheckWorkspaceWriteAllowed(requestedRoot, dst); err != nil {
			continue
		}
		data, _, err := fileutil.ReadRegularFileNoSymlink(src)
		if err != nil {
			continue
		}
		if err := fileutil.AtomicWriteFileNoSymlink(dst, data, 0o644); err != nil {
			continue
		}
		out = append(out, rel)
	}
	return uniqueRelativePaths(out)
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
