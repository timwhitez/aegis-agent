package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestRunnerDelegateReportsParentCoordinationError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)
	coordinationPath := filepath.Join(runner.store.SessionDir(parentID), "parent-coordination.json")
	if err := os.Mkdir(coordinationPath, 0o700); err != nil {
		t.Fatalf("block parent coordination path: %v", err)
	}

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		IsolationMode:   "off",
	})
	if err == nil {
		t.Fatalf("expected parent coordination error, got result %#v", result)
	}
	if !strings.Contains(err.Error(), "parent-coordination.json") {
		t.Fatalf("expected parent coordination path error, got %v", err)
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

func TestResolveRequestedWorkdirPrefersParentRequestedWorkdir(t *testing.T) {
	root := t.TempDir()
	parentRequested := filepath.Join(root, "parent-requested")
	parentEffective := filepath.Join(root, "isolated-effective")
	child := filepath.Join(parentRequested, "child")
	for _, path := range []string{parentRequested, parentEffective, child} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
	}
	parent := &session.SessionMetadata{
		RequestedWorkdir: parentRequested,
		Workdir:          parentEffective,
	}

	got, err := resolveRequestedWorkdir("child", parent)
	if err != nil {
		t.Fatalf("resolve workdir: %v", err)
	}
	if got != child {
		t.Fatalf("expected child under requested parent workdir, got %q want %q", got, child)
	}
}

func TestResolveRequestedWorkdirUsesParentForMissingRelativePath(t *testing.T) {
	root := t.TempDir()
	parentWorkdir := filepath.Join(root, "parent")
	if err := os.MkdirAll(parentWorkdir, 0o755); err != nil {
		t.Fatalf("create parent workdir: %v", err)
	}
	t.Chdir(root)
	parent := &session.SessionMetadata{
		RequestedWorkdir: parentWorkdir,
		Workdir:          parentWorkdir,
	}

	got, err := resolveRequestedWorkdir("missing-child", parent)
	if err != nil {
		t.Fatalf("resolve workdir: %v", err)
	}
	want := filepath.Join(parentWorkdir, "missing-child")
	if got != want {
		t.Fatalf("expected missing relative path under parent workdir, got %q want %q", got, want)
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

func TestRunnerDelegateAppliesRoleProviderOverrideWhenProviderModelOmitted(t *testing.T) {
	roleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"role override done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	t.Cleanup(roleServer.Close)

	cfg := testRuntimeConfig(t)
	cfg.Providers["validator"] = cfg.Providers["openai-compatible"]
	validator := cfg.Providers["validator"]
	validator.APIProvider = "openai-compatible"
	validator.Model = "validator-default"
	validator.BaseURL = "http://validator-profile.invalid/v1"
	cfg.Providers["validator"] = validator
	cfg.RoleProviders.Evaluator = config.RoleProviderOverride{
		Provider:    "validator",
		APIProvider: "openai-compatible",
		BaseURL:     roleServer.URL,
		Model:       "validator-role-model",
	}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())

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
	if meta.Provider != "validator" || meta.Model != "validator-role-model" {
		t.Fatalf("expected evaluator provider/model override, got provider=%q model=%q", meta.Provider, meta.Model)
	}
	if meta.ProviderOptions.APIProvider != "openai-compatible" || meta.ProviderOptions.BaseURL != roleServer.URL {
		t.Fatalf("expected evaluator adapter override in provider options, got %#v", meta.ProviderOptions)
	}
}

func TestRunnerDelegateExplicitProviderModelWinsOverRoleProviderOverride(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.Providers["validator"] = cfg.Providers["openai-compatible"]
	validator := cfg.Providers["validator"]
	validator.APIProvider = "openai-compatible"
	cfg.Providers["validator"] = validator
	cfg.RoleProviders.Evaluator = config.RoleProviderOverride{
		Provider: "validator",
		Model:    "validator-role-model",
	}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the delegated task",
		AgentName:       "reviewer",
		AgentRole:       "evaluator",
		Provider:        "openai-compatible",
		Model:           "explicit-model",
		IsolationMode:   "none",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if meta.Provider != "openai-compatible" || meta.Model != "explicit-model" {
		t.Fatalf("expected explicit provider/model to win, got provider=%q model=%q", meta.Provider, meta.Model)
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

func TestRunnerProcessNextJobReportsQueueLifecycleEventAppendError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_events","name":"shell","arguments":"{\"command\":\"rm events.jsonl && mkdir events.jsonl\"}"},
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
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		Workdir:         filepath.Join(runner.store.SessionDir(parentID), "..", parentID),
		IsolationMode:   "off",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected parent queue lifecycle event append error, got job=%#v ok=%v err=%v", processed, ok, err)
	}
	if !ok || processed.ID != job.ID {
		t.Fatalf("expected the claimed job to be returned, got job=%#v ok=%v", processed, ok)
	}
	if processed.Status != session.QueueStatusCompleted || processed.LastError != "" {
		t.Fatalf("expected event append error not to turn completed child into failed job, got %#v", processed)
	}
}

func TestRunnerQueueSubmitReportsParentCoordinationError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)
	coordinationPath := filepath.Join(runner.store.SessionDir(parentID), "parent-coordination.json")
	if err := os.Mkdir(coordinationPath, 0o700); err != nil {
		t.Fatalf("block parent coordination path: %v", err)
	}

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		IsolationMode:   "off",
	})
	if err == nil {
		t.Fatalf("expected parent coordination error, got job %#v", job)
	}
	if !strings.Contains(err.Error(), "parent-coordination.json") {
		t.Fatalf("expected parent coordination path error, got %v", err)
	}
	jobs, listErr := runner.store.ListJobs(10)
	if listErr != nil {
		t.Fatalf("list jobs after failed queue submit: %v", listErr)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected failed parent-linked queue submit to roll back queued job, got %#v", jobs)
	}
}

func TestRunnerProcessNextJobReportsParentCoordinationError(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	parentID := createParentSession(t, runner.store, parentWorkdir)
	if _, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentName:       "batch",
		IsolationMode:   "off",
	}); err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	coordinationPath := filepath.Join(runner.store.SessionDir(parentID), "parent-coordination.json")
	if err := os.Remove(coordinationPath); err != nil {
		t.Fatalf("remove parent coordination: %v", err)
	}
	if err := os.Mkdir(coordinationPath, 0o700); err != nil {
		t.Fatalf("block parent coordination path: %v", err)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err == nil {
		t.Fatalf("expected parent coordination error, got ok=%t job %#v", ok, processed)
	}
	if !strings.Contains(err.Error(), "parent-coordination.json") {
		t.Fatalf("expected parent coordination path in error, got %v", err)
	}
}

func TestQueueWorkerRefreshesHeartbeat(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.Runtime.Queue.PollIntervalMS = 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_heartbeat",
			"status":"completed",
			"output":[
				{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"heartbeat done\"}"}
			],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	defer server.Close()
	provider := cfg.Providers["openai-compatible"]
	provider.BaseURL = server.URL
	cfg.Providers["openai-compatible"] = provider

	runner := NewRunner(cfg)
	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		Prompt:        "finish the queued task slowly",
		IsolationMode: "off",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}

	done := make(chan struct{})
	var processed session.QueueJob
	var ok bool
	var processErr error
	go func() {
		processed, ok, processErr = runner.ProcessNextJob(context.Background())
		close(done)
	}()

	firstHeartbeat := ""
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		loaded, err := runner.store.LoadJob(job.ID)
		if err == nil && loaded.Status == session.QueueStatusRunning && loaded.HeartbeatAt != "" {
			firstHeartbeat = loaded.HeartbeatAt
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if firstHeartbeat == "" {
		t.Fatal("expected initial running heartbeat")
	}
	heartbeatUpdated := false
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		loaded, err := runner.store.LoadJob(job.ID)
		if err == nil && loaded.HeartbeatAt != "" && loaded.HeartbeatAt != firstHeartbeat {
			heartbeatUpdated = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !heartbeatUpdated {
		t.Fatal("expected queue heartbeat to refresh while job runs")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queue worker did not finish")
	}
	if processErr != nil || !ok || processed.Status != session.QueueStatusCompleted {
		t.Fatalf("unexpected processed job ok=%t err=%v job=%#v", ok, processErr, processed)
	}
	if processed.HeartbeatAt == "" || processed.ClaimedBy == "" || processed.ProcessStartID == "" || processed.WorkerPID == 0 {
		t.Fatalf("expected lease fields on completed job, got %#v", processed)
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

func TestRunnerQueueSubmitPersistsRoleProviderOverrideOptions(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.Providers["builder"] = cfg.Providers["openai-compatible"]
	builder := cfg.Providers["builder"]
	builder.APIProvider = "openai-compatible"
	builder.Model = "builder-default"
	cfg.Providers["builder"] = builder
	cfg.RoleProviders.Generator = config.RoleProviderOverride{
		Provider: "builder",
		BaseURL:  "http://role-builder.invalid/v1",
		Model:    "builder-role-model",
	}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "finish the queued task",
		AgentRole:       "generator",
	})
	if err != nil {
		t.Fatalf("queue submit: %v", err)
	}
	if job.Provider != "builder" || job.Model != "builder-role-model" {
		t.Fatalf("expected generator role provider/model override, got %#v", job)
	}
	if job.ProviderOptions.BaseURL != "http://role-builder.invalid/v1" {
		t.Fatalf("expected role base URL to persist on queue job, got %#v", job.ProviderOptions)
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
			if len(notifications) > 0 {
				return
			}
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

func TestRunnerProcessNextJobReportsCorruptChildHandoffMessages(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	escapedSessionRoot := strings.ReplaceAll(cfg.Session.Dir, "'", "'\"'\"'")
	cfg.Hooks.SessionComplete = []config.HookDefinition{
		{
			Name:    "corrupt-child-messages",
			Command: []string{"/bin/sh", "-c", "printf '{' > '" + escapedSessionRoot + "'/\"$SESSION_ID\"/messages.jsonl"},
		},
	}
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "Finish the queued task.",
		AgentName:       "batch",
		IsolationMode:   "off",
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
	if processed.ID != job.ID {
		t.Fatalf("processed unexpected job: %#v", processed)
	}
	if processed.Status != session.QueueStatusFailed {
		t.Fatalf("expected corrupt child messages to fail queue handoff, got %#v", processed)
	}
	if !strings.Contains(processed.LastError, "messages.jsonl") {
		t.Fatalf("expected child messages error, got %#v", processed)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil {
		t.Fatalf("load background notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one failure notification, got %#v", notifications)
	}
	if notifications[0].Status != session.QueueStatusFailed || !strings.Contains(notifications[0].LastError, "messages.jsonl") {
		t.Fatalf("expected persisted corrupt handoff notification, got %#v", notifications[0])
	}
}

func TestRunnerProcessNextJobReportsCorruptChildHandoffMetadata(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	escapedSessionRoot := strings.ReplaceAll(cfg.Session.Dir, "'", "'\"'\"'")
	cfg.Hooks.SessionComplete = []config.HookDefinition{
		{
			Name:    "corrupt-child-metadata",
			Command: []string{"/bin/sh", "-c", "printf '{' > '" + escapedSessionRoot + "'/\"$SESSION_ID\"/session.json"},
		},
	}
	parentID := createParentSession(t, runner.store, parentWorkdir)

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "Finish the queued task.",
		AgentName:       "batch",
		IsolationMode:   "off",
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
	if processed.ID != job.ID {
		t.Fatalf("processed unexpected job: %#v", processed)
	}
	if processed.Status != session.QueueStatusFailed {
		t.Fatalf("expected corrupt child metadata to fail queue handoff, got %#v", processed)
	}
	if !strings.Contains(processed.LastError, "session.json") {
		t.Fatalf("expected child metadata error, got %#v", processed)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil {
		t.Fatalf("load background notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one failure notification, got %#v", notifications)
	}
	if notifications[0].Status != session.QueueStatusFailed || !strings.Contains(notifications[0].LastError, "session.json") {
		t.Fatalf("expected persisted corrupt handoff notification, got %#v", notifications[0])
	}
}

func TestSyncVisibleSessionOutputsRejectsDeniedSymlinkAlias(t *testing.T) {
	requestedWorkdir := t.TempDir()
	effectiveWorkdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(effectiveWorkdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir effective reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(effectiveWorkdir, "reports", "queue-output.md"), []byte("# child output"), 0o600); err != nil {
		t.Fatalf("write child output: %v", err)
	}
	envPath := filepath.Join(requestedWorkdir, ".env")
	if err := os.WriteFile(envPath, []byte("KEEP=1\n"), 0o600); err != nil {
		t.Fatalf("write requested env: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(requestedWorkdir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir requested reports: %v", err)
	}
	if err := os.Symlink(envPath, filepath.Join(requestedWorkdir, "reports", "queue-output.md")); err != nil {
		t.Fatalf("symlink visible output alias: %v", err)
	}

	synced := syncVisibleSessionOutputs(requestedWorkdir, effectiveWorkdir, []string{"reports/queue-output.md"})
	if len(synced) != 0 {
		t.Fatalf("expected denied symlink alias not to sync, got %#v", synced)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read requested env: %v", err)
	}
	if string(data) != "KEEP=1\n" {
		t.Fatalf("expected .env to remain unchanged, got %q", data)
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

func TestRunnerDelegateReportsCorruptChildHandoffMessages(t *testing.T) {
	assertRunnerDelegateReportsCorruptChildHandoffFact(t, "messages.jsonl")
}

func assertRunnerDelegateReportsCorruptChildHandoffFact(t *testing.T, factName string) {
	t.Helper()
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentWorkdir := t.TempDir()
	escapedSessionRoot := strings.ReplaceAll(cfg.Session.Dir, "'", "'\"'\"'")
	cfg.Hooks.SessionComplete = []config.HookDefinition{
		{
			Name:    "corrupt-child-" + strings.ReplaceAll(factName, ".", "-"),
			Command: []string{"/bin/sh", "-c", "printf '{' > '" + escapedSessionRoot + "'/\"$SESSION_ID\"/" + factName},
		},
	}
	parentID := createParentSession(t, runner.store, parentWorkdir)

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "Finish the delegated task.",
		AgentName:       "reviewer",
		IsolationMode:   "off",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if result.SessionID == "" {
		t.Fatalf("expected child session id, got %#v", result)
	}
	if result.Status != session.StatusFailed {
		t.Fatalf("expected corrupt child handoff to fail delegate result, got %#v", result)
	}
	if !strings.Contains(result.LastError, factName) {
		t.Fatalf("expected corrupt %s error, got %#v", factName, result)
	}
	coordination, err := runner.store.LoadParentCoordination(parentID)
	if err != nil {
		t.Fatalf("load parent coordination: %v", err)
	}
	if !slices.Contains(coordination.FailedChildSessions, result.SessionID) {
		t.Fatalf("expected failed child coordination for corrupt handoff, got %#v", coordination)
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
