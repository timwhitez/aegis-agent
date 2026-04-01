package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		Provider:        req.Provider,
		Model:           req.Model,
		Workdir:         req.Workdir,
		SystemOverride:  req.SystemOverride,
		Background:      req.Background,
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
	agentRole, err := normalizeAgentRole(req.AgentRole, req.AgentName)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	req.AgentRole = agentRole
	parentMeta, err := r.store.LoadMetadata(req.ParentSessionID)
	if err != nil {
		return tools.AgentSpawnResult{}, err
	}
	workdir := strings.TrimSpace(req.Workdir)
	if workdir == "" {
		if parentMeta.RequestedWorkdir != "" {
			workdir = parentMeta.RequestedWorkdir
		} else {
			workdir = parentMeta.Workdir
		}
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = session.ModeExec
	}
	isolationMode := strings.TrimSpace(req.IsolationMode)
	if isolationMode == "" {
		isolationMode = "auto"
	}
	if req.Background {
		job, err := r.QueueSubmit(ctx, QueueSubmitRequest{
			ParentSessionID: req.ParentSessionID,
			Prompt:          req.Prompt,
			AgentName:       req.AgentName,
			AgentRole:       req.AgentRole,
			Provider:        req.Provider,
			Model:           req.Model,
			Workdir:         workdir,
			SystemOverride:  req.SystemOverride,
			Mode:            mode,
			IsolationMode:   isolationMode,
			IsolationRoot:   req.IsolationRoot,
		})
		if err != nil {
			return tools.AgentSpawnResult{}, err
		}
		r.emit(req.ParentSessionID, "session.child.queued", "delegate", map[string]any{
			"job_id":     job.ID,
			"agent_name": job.AgentName,
			"agent_role": job.AgentRole,
		})
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
		Provider:        req.Provider,
		Model:           req.Model,
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
	if result.SessionID != "" {
		if meta, loadErr := childRunner.store.LoadMetadata(result.SessionID); loadErr == nil {
			out.Workdir = meta.Workdir
			out.AgentRole = meta.AgentRole
			requestedChildWorkdir = firstNonEmpty(meta.RequestedWorkdir, requestedChildWorkdir)
		}
		if messages, loadErr := childRunner.store.LoadMessages(result.SessionID); loadErr == nil {
			effectiveWorkdir := firstNonEmpty(out.Workdir, requestedChildWorkdir)
			out.VisiblePaths = collectVisibleSessionOutputs(effectiveWorkdir, messages)
			out.VisiblePaths = syncVisibleSessionOutputs(requestedChildWorkdir, effectiveWorkdir, out.VisiblePaths)
		}
	}
	if result.SessionID != "" {
		r.emit(req.ParentSessionID, "session.child.spawned", "delegate", map[string]any{
			"session_id": result.SessionID,
			"status":     result.Status,
			"agent_name": req.AgentName,
			"agent_role": out.AgentRole,
		})
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
	Workdir         string
	SystemOverride  string
	Mode            string
	IsolationMode   string
	IsolationRoot   string
}

func (r *Runner) QueueSubmit(_ context.Context, req QueueSubmitRequest) (session.QueueJob, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return session.QueueJob{}, errors.New("prompt is required")
	}
	agentRole, err := normalizeAgentRole(req.AgentRole, req.AgentName)
	if err != nil {
		return session.QueueJob{}, err
	}
	req.AgentRole = agentRole
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = session.ModeExec
	}
	workdir := strings.TrimSpace(req.Workdir)
	rootSessionID := req.ParentSessionID
	if req.ParentSessionID != "" {
		parentMeta, err := r.store.LoadMetadata(req.ParentSessionID)
		if err != nil {
			return session.QueueJob{}, err
		}
		if parentMeta.RootSessionID != "" {
			rootSessionID = parentMeta.RootSessionID
		} else {
			rootSessionID = parentMeta.ID
		}
		if workdir == "" {
			workdir = firstNonEmpty(parentMeta.RequestedWorkdir, parentMeta.Workdir)
		}
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
		Provider:         req.Provider,
		Model:            req.Model,
		RequestedWorkdir: workdir,
		SystemOverride:   req.SystemOverride,
		Background:       true,
		IsolationMode:    firstNonEmpty(req.IsolationMode, "auto"),
		IsolationRoot:    req.IsolationRoot,
	}
	return job, r.store.EnqueueJob(job)
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
		r.emit(job.ParentSessionID, "queue.job.claimed", "queue", map[string]any{
			"job_id": job.ID,
		})
	}
	childRunner := NewRunner(r.cfg)
	result, runErr := childRunner.Start(ctx, StartRequest{
		Prompt:          job.Prompt,
		Provider:        job.Provider,
		Model:           job.Model,
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
	job.SessionID = result.SessionID
	job.SessionStatus = result.Status
	job.FinalText = result.FinalText
	job.LastError = result.LastError
	if result.SessionID != "" {
		if meta, err := childRunner.store.LoadMetadata(result.SessionID); err == nil {
			job.EffectiveWorkdir = meta.Workdir
		}
		if messages, err := childRunner.store.LoadMessages(result.SessionID); err == nil {
			job.VisiblePaths = collectVisibleSessionOutputs(job.EffectiveWorkdir, messages)
			job.VisiblePaths = syncVisibleSessionOutputs(job.RequestedWorkdir, job.EffectiveWorkdir, job.VisiblePaths)
		}
	}
	if runErr != nil || result.Status == session.StatusFailed {
		job.Status = session.QueueStatusFailed
		if job.LastError == "" && runErr != nil {
			job.LastError = runErr.Error()
		}
	} else {
		job.Status = session.QueueStatusCompleted
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
		r.emit(job.ParentSessionID, "queue.job.notified", "queue", map[string]any{
			"job_id":     job.ID,
			"session_id": job.SessionID,
			"status":     job.Status,
			"agent_role": job.AgentRole,
		})
	}
	if job.ParentSessionID != "" {
		eventType := "queue.job.completed"
		if job.Status == session.QueueStatusFailed {
			eventType = "queue.job.failed"
		}
		r.emit(job.ParentSessionID, eventType, "queue", map[string]any{
			"job_id":     job.ID,
			"session_id": job.SessionID,
			"status":     job.Status,
			"agent_role": job.AgentRole,
		})
	}
	// Failed jobs are part of normal queue lifecycle. Persist the failure on the
	// job record and let the worker keep polling unless queue I/O itself failed.
	return job, true, nil
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
		src, err := tools.ResolveWorkspacePath(effectiveRoot, rel)
		if err != nil {
			continue
		}
		dst, err := tools.ResolveWorkspacePath(requestedRoot, rel)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
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
