package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

func TestRunnerDelegateCreatesChildSessionWithIsolation(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		IsolationMode:   "auto",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed child, got %#v", result)
	}
	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.ParentSessionID != parentID || meta.RootSessionID != parentID {
		t.Fatalf("unexpected parent/root linkage: %#v", meta)
	}
	if meta.AgentName != "reviewer" {
		t.Fatalf("expected agent name, got %#v", meta.AgentName)
	}
	if meta.AgentRole != "evaluator" || result.AgentRole != "evaluator" {
		t.Fatalf("expected evaluator role to persist, meta=%#v result=%#v", meta.AgentRole, result.AgentRole)
	}
	if meta.Isolation == nil || meta.Isolation.Mode != "copy" {
		t.Fatalf("expected copy isolation, got %#v", meta.Isolation)
	}
	if meta.Workdir == parentWorkdir {
		t.Fatalf("expected isolated workdir, got parent workdir %s", meta.Workdir)
	}
}

func TestRunnerDelegateTreatsNoneIsolationModeAsOff(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		IsolationMode:   "none",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.Isolation != nil {
		t.Fatalf("expected no isolation metadata for none/off mode, got %#v", meta.Isolation)
	}
	if meta.Workdir != parentWorkdir {
		t.Fatalf("expected child to reuse parent workdir when isolation_mode=none, got %q want %q", meta.Workdir, parentWorkdir)
	}
}

func TestRunnerDelegateResolvesRelativeWorkdirAgainstParent(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		Workdir:         ".",
		IsolationMode:   "none",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Workdir != parentWorkdir {
		t.Fatalf("expected result workdir to resolve under parent, got %q want %q", result.Workdir, parentWorkdir)
	}
	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.RequestedWorkdir != parentWorkdir || meta.Workdir != parentWorkdir {
		t.Fatalf("expected relative child workdir to resolve to parent workspace, got %#v", meta)
	}
}

func TestRunnerDelegateKeepsExistingCwdRelativeWorkdir(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	root := t.TempDir()
	parentWorkdir := filepath.Join(root, "parent")
	cwdRelativeWorkdir := filepath.Join(root, "child-workspace")
	if err := os.MkdirAll(parentWorkdir, 0o755); err != nil {
		t.Fatalf("create parent workdir: %v", err)
	}
	if err := os.MkdirAll(cwdRelativeWorkdir, 0o755); err != nil {
		t.Fatalf("create cwd-relative workdir: %v", err)
	}
	t.Chdir(root)
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		Workdir:         "child-workspace",
		IsolationMode:   "none",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Workdir != cwdRelativeWorkdir {
		t.Fatalf("expected existing cwd-relative workdir, got %q want %q", result.Workdir, cwdRelativeWorkdir)
	}
}

func TestRunnerDelegateTreatsDefaultProviderAndModelAsInherited(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		Provider:        "default",
		Model:           "default",
		IsolationMode:   "none",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed child, got %#v", result)
	}
	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.Provider != cfg.DefaultProvider {
		t.Fatalf("expected provider %q, got %#v", cfg.DefaultProvider, meta.Provider)
	}
	if meta.Model != cfg.Providers[cfg.DefaultProvider].Model {
		t.Fatalf("expected model %q, got %#v", cfg.Providers[cfg.DefaultProvider].Model, meta.Model)
	}
}

func TestRunnerDelegateInheritsParentProviderAndModelWhenOmitted(t *testing.T) {
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"unexpected fallback provider"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(fallback.Close)

	cfg := testRuntimeConfig(t)
	cfg.DefaultProvider = "openai"
	cfg.Providers["openai"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    fallback.URL,
		Model:      "gpt-5",
		TimeoutSec: 5,
		WireAPI:    "responses",
	}
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		IsolationMode:   "none",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed child, got %#v", result)
	}
	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.Provider != "openai-compatible" {
		t.Fatalf("expected provider to inherit from parent, got %#v", meta.Provider)
	}
	if meta.Model != "gpt-5.4" {
		t.Fatalf("expected model to inherit from parent, got %#v", meta.Model)
	}
}

func TestRunnerDelegateTreatsDefaultIsolationModeAsAuto(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		IsolationMode:   "default",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.Isolation == nil || meta.Isolation.Mode != "copy" {
		t.Fatalf("expected default isolation mode to fall back to auto/copy, got %#v", meta.Isolation)
	}
}

func TestRunnerQueueSubmitAndWorkerCompletesJob(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		AgentRole:       "planner",
		IsolationMode:   "auto",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	if job.Status != session.QueueStatusQueued {
		t.Fatalf("expected queued job, got %#v", job)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil {
		t.Fatalf("process next job: %v", err)
	}
	if !ok {
		t.Fatal("expected a queued job to be processed")
	}
	if processed.Status != session.QueueStatusCompleted || processed.SessionID == "" {
		t.Fatalf("unexpected processed job: %#v", processed)
	}
	loaded, err := runner.store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loaded.Status != session.QueueStatusCompleted || loaded.SessionStatus != session.StatusCompleted {
		t.Fatalf("unexpected persisted job: %#v", loaded)
	}
	if loaded.AgentRole != "planner" {
		t.Fatalf("expected planner role on persisted job, got %#v", loaded.AgentRole)
	}
	meta, err := runner.store.LoadMetadata(loaded.SessionID)
	if err != nil {
		t.Fatalf("load child session metadata: %v", err)
	}
	if meta.ParentSessionID != parentID || meta.QueueJobID != job.ID {
		t.Fatalf("unexpected queued child metadata: %#v", meta)
	}
	if meta.AgentRole != "planner" {
		t.Fatalf("expected planner role on child metadata, got %#v", meta.AgentRole)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil {
		t.Fatalf("load background notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one background notification, got %#v", notifications)
	}
	if notifications[0].QueueJobID != job.ID || notifications[0].DeliveryStatus != session.BackgroundNotificationPending {
		t.Fatalf("unexpected background notification: %#v", notifications[0])
	}
	if notifications[0].AgentRole != "planner" {
		t.Fatalf("expected planner role on background notification, got %#v", notifications[0].AgentRole)
	}
}

func TestRunnerQueueSubmitResolvesRelativeWorkdirAgainstParent(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		AgentRole:       "planner",
		Workdir:         ".",
		IsolationMode:   "auto",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	if job.RequestedWorkdir != parentWorkdir {
		t.Fatalf("expected queued workdir to resolve under parent, got %q want %q", job.RequestedWorkdir, parentWorkdir)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil {
		t.Fatalf("process next job: %v", err)
	}
	if !ok {
		t.Fatal("expected a queued job to be processed")
	}
	if processed.RequestedWorkdir != parentWorkdir {
		t.Fatalf("expected processed workdir to stay parent-relative, got %#v", processed)
	}
	if processed.EffectiveWorkdir == "" || processed.EffectiveWorkdir == parentWorkdir {
		t.Fatalf("expected isolated effective workdir, got %#v", processed)
	}
	meta, err := runner.store.LoadMetadata(processed.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.RequestedWorkdir != parentWorkdir {
		t.Fatalf("expected child requested workdir to be parent workspace, got %#v", meta)
	}
	if meta.Isolation == nil || meta.Isolation.ParentWorkdir != parentWorkdir {
		t.Fatalf("expected isolation to copy from parent workspace, got %#v", meta.Isolation)
	}
}

func TestRunnerQueueSubmitKeepsExistingCwdRelativeWorkdir(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	root := t.TempDir()
	parentWorkdir := filepath.Join(root, "parent")
	cwdRelativeWorkdir := filepath.Join(root, "child-workspace")
	if err := os.MkdirAll(parentWorkdir, 0o755); err != nil {
		t.Fatalf("create parent workdir: %v", err)
	}
	if err := os.MkdirAll(cwdRelativeWorkdir, 0o755); err != nil {
		t.Fatalf("create cwd-relative workdir: %v", err)
	}
	t.Chdir(root)
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		AgentRole:       "planner",
		Workdir:         "child-workspace",
		IsolationMode:   "none",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	if job.RequestedWorkdir != cwdRelativeWorkdir {
		t.Fatalf("expected queued workdir to keep existing cwd-relative path, got %q want %q", job.RequestedWorkdir, cwdRelativeWorkdir)
	}
}

func TestRunnerQueueSubmitInheritsParentProviderAndModelWhenOmitted(t *testing.T) {
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"unexpected fallback provider"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(fallback.Close)

	cfg := testRuntimeConfig(t)
	cfg.DefaultProvider = "openai"
	cfg.Providers["openai"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    fallback.URL,
		Model:      "gpt-5",
		TimeoutSec: 5,
		WireAPI:    "responses",
	}
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	if job.Provider != "openai-compatible" {
		t.Fatalf("expected queued provider to inherit from parent, got %#v", job.Provider)
	}
	if job.Model != "gpt-5.4" {
		t.Fatalf("expected queued model to inherit from parent, got %#v", job.Model)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil {
		t.Fatalf("process next job: %v", err)
	}
	if !ok {
		t.Fatal("expected a queued job to be processed")
	}
	if processed.Status != session.QueueStatusCompleted {
		t.Fatalf("expected completed job, got %#v", processed)
	}
	if processed.Provider != "openai-compatible" {
		t.Fatalf("expected processed provider to inherit from parent, got %#v", processed.Provider)
	}
	if processed.Model != "gpt-5.4" {
		t.Fatalf("expected processed model to inherit from parent, got %#v", processed.Model)
	}
}

func TestRunnerQueueSubmitNormalizesFullAutoAndWorkspaceWriteAliases(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		Mode:            "full-auto",
		IsolationMode:   "workspace-write",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	if job.Mode != session.ModeExec {
		t.Fatalf("expected full-auto alias to normalize to exec, got %#v", job.Mode)
	}
	if job.IsolationMode != "off" {
		t.Fatalf("expected workspace-write alias to normalize to off, got %#v", job.IsolationMode)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil {
		t.Fatalf("process next job: %v", err)
	}
	if !ok {
		t.Fatal("expected a queued job to be processed")
	}
	if processed.Status != session.QueueStatusCompleted {
		t.Fatalf("expected completed job, got %#v", processed)
	}
	if processed.SessionID == "" {
		t.Fatalf("expected child session id, got %#v", processed)
	}
	meta, err := runner.store.LoadMetadata(processed.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.Mode != session.ModeExec || meta.CompletionPolicy != session.CompletionPolicyAutonomous {
		t.Fatalf("expected exec/autonomous child mode, got %#v", meta)
	}
	if meta.Isolation != nil {
		t.Fatalf("expected workspace-write alias to reuse parent workspace, got %#v", meta.Isolation)
	}
	if meta.Workdir != parentWorkdir {
		t.Fatalf("expected child workdir to reuse parent workspace, got %q want %q", meta.Workdir, parentWorkdir)
	}
}

func TestRunnerAutoQueueWorkerProcessesQueuedJobs(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.Runtime.Queue.PollIntervalMS = 10
	runner := NewRunner(cfg)
	release := runner.startAutoQueueWorker()
	defer release()

	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)
	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		IsolationMode:   "auto",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		loaded, err := runner.store.LoadJob(job.ID)
		if err != nil {
			t.Fatalf("load job: %v", err)
		}
		if loaded.Status == session.QueueStatusCompleted {
			if loaded.SessionID == "" {
				t.Fatalf("expected session id on completed job: %#v", loaded)
			}
			notifications, err := runner.store.LoadBackgroundNotifications(parentID)
			if err != nil {
				t.Fatalf("load background notifications: %v", err)
			}
			if len(notifications) == 0 {
				t.Fatalf("expected background notification for completed job")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for auto queue worker to complete job %s", job.ID)
}

func TestRunnerProcessNextJobCopiesVisibleOutputsIntoRequestedWorkspace(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		callCount++
		current := callCount
		mu.Unlock()
		if current == 1 {
			_, _ = w.Write([]byte(`{
				"id":"resp_1",
				"status":"completed",
				"output":[
					{"type":"function_call","call_id":"call_spec","name":"write_file","arguments":"{\"path\":\"reports/spec.md\",\"content\":\"# synced spec\"}"},
					{"type":"function_call","call_id":"call_output","name":"write_file","arguments":"{\"path\":\"reports/queue-output.md\",\"content\":\"# queued output\"}"}
				],
				"usage":{"input_tokens":10,"output_tokens":10}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"resp_2",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"queued done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	t.Cleanup(server.Close)

	cfg := config.Default()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    server.URL,
		Model:      "gpt-5.4",
		TimeoutSec: 30,
		WireAPI:    "responses",
	}
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.Isolation.RootDir = t.TempDir()

	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "Write reports/spec.md and reports/queue-output.md, then finish.",
		AgentName:       "batch",
		IsolationMode:   "auto",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil {
		t.Fatalf("process next job: %v", err)
	}
	if !ok {
		t.Fatal("expected queued job to be processed")
	}
	if processed.Status != session.QueueStatusCompleted {
		t.Fatalf("expected completed job, got %#v", processed)
	}
	if processed.EffectiveWorkdir == "" || processed.EffectiveWorkdir == parentWorkdir {
		t.Fatalf("expected isolated effective workdir, got %#v", processed)
	}
	if !slices.Equal(processed.VisiblePaths, []string{"reports/spec.md", "reports/queue-output.md"}) {
		t.Fatalf("unexpected visible paths: %#v", processed.VisiblePaths)
	}

	specBytes, err := os.ReadFile(filepath.Join(parentWorkdir, "reports", "spec.md"))
	if err != nil {
		t.Fatalf("read synced spec: %v", err)
	}
	if string(specBytes) != "# synced spec" {
		t.Fatalf("unexpected synced spec contents: %q", specBytes)
	}
	outputBytes, err := os.ReadFile(filepath.Join(parentWorkdir, "reports", "queue-output.md"))
	if err != nil {
		t.Fatalf("read synced output: %v", err)
	}
	if string(outputBytes) != "# queued output" {
		t.Fatalf("unexpected synced output contents: %q", outputBytes)
	}

	loaded, err := runner.store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if !slices.Equal(loaded.VisiblePaths, []string{"reports/spec.md", "reports/queue-output.md"}) {
		t.Fatalf("unexpected persisted visible paths: %#v", loaded.VisiblePaths)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil {
		t.Fatalf("load background notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one background notification, got %#v", notifications)
	}
	if !slices.Equal(notifications[0].VisiblePaths, []string{"reports/spec.md", "reports/queue-output.md"}) {
		t.Fatalf("unexpected notification visible paths: %#v", notifications[0].VisiblePaths)
	}
}

func TestRunnerDelegateCopiesVisibleOutputsIntoRequestedWorkspace(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		callCount++
		current := callCount
		mu.Unlock()
		if current == 1 {
			_, _ = w.Write([]byte(`{
				"id":"resp_1",
				"status":"completed",
				"output":[
					{"type":"function_call","call_id":"call_progress","name":"write_file","arguments":"{\"path\":\"reports/progress.md\",\"content\":\"# delegated progress\"}"},
					{"type":"function_call","call_id":"call_output","name":"write_file","arguments":"{\"path\":\"reports/delegate-output.md\",\"content\":\"# delegated output\"}"}
				],
				"usage":{"input_tokens":10,"output_tokens":10}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"resp_2",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"delegated done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	t.Cleanup(server.Close)

	cfg := config.Default()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    server.URL,
		Model:      "gpt-5.4",
		TimeoutSec: 30,
		WireAPI:    "responses",
	}
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.Isolation.RootDir = t.TempDir()

	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "Write reports/progress.md and reports/delegate-output.md, then finish.",
		AgentName:       "reviewer",
		Workdir:         parentWorkdir,
		IsolationMode:   "auto",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("expected completed child, got %#v", result)
	}
	if result.Workdir == "" || result.Workdir == parentWorkdir {
		t.Fatalf("expected isolated child workdir, got %#v", result)
	}
	if !slices.Equal(result.VisiblePaths, []string{"reports/progress.md", "reports/delegate-output.md"}) {
		t.Fatalf("unexpected visible paths: %#v", result.VisiblePaths)
	}
	progressBytes, err := os.ReadFile(filepath.Join(parentWorkdir, "reports", "progress.md"))
	if err != nil {
		t.Fatalf("read synced progress: %v", err)
	}
	if string(progressBytes) != "# delegated progress" {
		t.Fatalf("unexpected synced progress contents: %q", progressBytes)
	}
	outputBytes, err := os.ReadFile(filepath.Join(parentWorkdir, "reports", "delegate-output.md"))
	if err != nil {
		t.Fatalf("read synced delegate output: %v", err)
	}
	if string(outputBytes) != "# delegated output" {
		t.Fatalf("unexpected synced delegate output contents: %q", outputBytes)
	}
}

func TestRunnerDelegateRejectsDepthLimit(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentID := createParentSessionWithDepth(t, runner.store, t.TempDir(), cfg.Runtime.MultiAgent.MaxDepth)

	_, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
	})
	if err == nil {
		t.Fatal("expected depth limit error")
	}
	if got := err.Error(); got != "max agent depth exceeded: 4" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProcessNextJobMarksFailedJobWithoutReturningError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	job := session.QueueJob{
		SchemaVersion:    1,
		ID:               session.NewQueueJobID(),
		Status:           session.QueueStatusQueued,
		Prompt:           "finish the queued task",
		Mode:             session.ModeExec,
		Provider:         "missing-provider",
		RequestedWorkdir: t.TempDir(),
		Background:       true,
	}
	if err := runner.store.EnqueueJob(job); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil {
		t.Fatalf("process next job: %v", err)
	}
	if !ok {
		t.Fatal("expected a queued job to be processed")
	}
	if processed.Status != session.QueueStatusFailed {
		t.Fatalf("expected failed job status, got %#v", processed)
	}
	if processed.LastError == "" {
		t.Fatalf("expected persisted last_error, got %#v", processed)
	}
	loaded, err := runner.store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if loaded.Status != session.QueueStatusFailed || loaded.LastError == "" {
		t.Fatalf("unexpected persisted failed job: %#v", loaded)
	}
}

func createParentSession(t *testing.T, store *session.Store, workdir string) string {
	return createParentSessionWithDepth(t, store, workdir, 0)
}

func createParentSessionWithDepth(t *testing.T, store *session.Store, workdir string, depth int) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := session.SessionMetadata{
		SchemaVersion:    1,
		ID:               "parent_" + session.NewQueueJobID(),
		CreatedAt:        now,
		Workdir:          workdir,
		RequestedWorkdir: workdir,
		Mode:             session.ModeRun,
		Provider:         "openai-compatible",
		Model:            "gpt-5.4",
		CompletionPolicy: session.CompletionPolicyInteractive,
		RootSessionID:    "",
		Depth:            depth,
	}
	meta.RootSessionID = meta.ID
	state := session.State{
		Status:    session.StatusCompleted,
		Phase:     "turn_decide",
		UpdatedAt: now,
	}
	if err := store.Create(meta, state); err != nil {
		t.Fatalf("create parent session: %v", err)
	}
	return meta.ID
}

func testRuntimeConfig(t *testing.T) *config.Config {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"delegated done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	t.Cleanup(server.Close)

	cfg := config.Default()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    server.URL,
		Model:      "gpt-5.4",
		TimeoutSec: 30,
		WireAPI:    "responses",
	}
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.Isolation.RootDir = t.TempDir()
	return cfg
}
