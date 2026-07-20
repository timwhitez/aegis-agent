package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/events"
	"go-cli-agent/internal/provider"
	"go-cli-agent/internal/session"
	"go-cli-agent/internal/skills"
	"go-cli-agent/internal/tools"
)

func TestExplorerRoleNormalizationGuidanceAndIsolationFallback(t *testing.T) {
	role, err := normalizeAgentRole(" Explorer ", "ignored-name")
	if err != nil || role != agentRoleExplorer {
		t.Fatalf("normalize explorer role: role=%q err=%v", role, err)
	}
	if _, err := normalizeAgentRole("researcher", ""); err == nil {
		t.Fatal("unknown agent role must remain rejected")
	}

	fallback := isolationFallbackForAgentRole(agentRoleExplorer, "auto")
	for input, want := range map[string]string{
		"":        "off",
		"default": "off",
		"off":     "off",
		"auto":    "auto",
		"copy":    "copy",
		"git":     "git",
	} {
		got, err := normalizeAndValidateIsolationMode(input, fallback)
		if err != nil || got != want {
			t.Errorf("explorer isolation %q=%q want=%q err=%v", input, got, want, err)
		}
	}
	if got := isolationFallbackForAgentRole(agentRoleEvaluator, "auto"); got != "auto" {
		t.Fatalf("non-explorer isolation fallback changed: %q", got)
	}

	prompt := buildSystemPrompt(
		"/tmp/work",
		session.ModeExec,
		"",
		nil,
		[]skills.CommandTool{{Name: "trusted_mutator", Description: "must stay hidden"}},
		session.State{},
		[]session.Message{session.NewMessage("user", "Map the relevant modules and return evidence.")},
		"repo-explorer",
		agentRoleExplorer,
	)
	for _, want := range []string{"acting as the explorer role", "read-only", "claim | file:line | confidence", "uncovered", "raw search output"} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(want)) {
			t.Fatalf("explorer prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{"must delegate", "always spawn", "read these files in order"} {
		if strings.Contains(strings.ToLower(prompt), forbidden) {
			t.Fatalf("explorer prompt introduced fixed orchestration %q:\n%s", forbidden, prompt)
		}
	}
	for _, hiddenGuidance := range []string{"Use the `shell` tool's", "use `await_input`", "use `agent_wait`", "## Skill Command Tools", "trusted_mutator"} {
		if strings.Contains(prompt, hiddenGuidance) {
			t.Fatalf("explorer prompt advertises hidden capability %q:\n%s", hiddenGuidance, prompt)
		}
	}
}

func TestRunnerDelegateExplorerPersistsEffectiveProfileOptionsAndProviderSchema(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err == nil {
			_ = json.Unmarshal(body, &captured)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_explorer",
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_finish","name":"finish","arguments":"{\"message\":\"Concise findings.\\n\\n| claim | file:line | confidence |\\n| --- | --- | --- |\\n| entrypoint found | internal/runtime/runner.go:326 | high |\\n\\nUncovered: provider-specific live behavior.\\nKey uncertainty: none.\"}"}],
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
	}))
	t.Cleanup(server.Close)

	cfg := testRuntimeConfig(t)
	providerCfg := cfg.Providers["openai-compatible"]
	providerCfg.BaseURL = server.URL
	providerCfg.ReasoningEffort = "low"
	providerCfg.MaxOutputTokens = 1024
	cfg.Providers["openai-compatible"] = providerCfg
	cfg.RoleProviders.Explorer = config.RoleProviderOverride{
		ReasoningEffort: "medium",
		MaxOutputTokens: 4096,
	}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	parentMeta, err := runner.store.LoadMetadata(parentID)
	if err != nil {
		t.Fatalf("load parent metadata: %v", err)
	}
	parentMeta.ProviderOptions = providerOptionsFromConfig(parentMeta.Provider, providerCfg)
	parentMeta.ProviderOptions.ReasoningEffort = "high"
	parentMeta.ProviderOptions.MaxOutputTokens = 8192
	if err := runner.store.SaveMetadata(parentID, parentMeta); err != nil {
		t.Fatalf("save parent provider options: %v", err)
	}

	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "Inspect the runtime entrypoints and return only an evidence handoff.",
		AgentName:       "runtime-explorer",
		AgentRole:       "explorer",
	})
	if err != nil {
		t.Fatalf("delegate explorer: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("explorer did not complete: %#v", result)
	}
	meta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load explorer metadata: %v", err)
	}
	if meta.AgentRole != "explorer" || meta.ToolProfile != session.ToolProfileExplorerReadOnly {
		t.Fatalf("explorer role/profile not durable: %#v", meta)
	}
	if meta.Isolation == nil || meta.Isolation.Mode != "off" {
		t.Fatalf("implicit explorer isolation must persist as off: %#v", meta.Isolation)
	}
	if meta.ProviderOptions.ReasoningEffort != "medium" || meta.ProviderOptions.MaxOutputTokens != 4096 {
		t.Fatalf("role generation options did not override inherited parent values: %#v", meta.ProviderOptions)
	}

	if got := int(captured["max_output_tokens"].(float64)); got != 4096 {
		t.Fatalf("provider request max_output_tokens=%d want=4096 body=%#v", got, captured)
	}
	reasoning, _ := captured["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Fatalf("provider request reasoning effort did not use explorer override: %#v", captured)
	}
	toolNames := openAIRequestToolNames(captured["tools"])
	sort.Strings(toolNames)
	wantTools := []string{"finish", "glob", "grep", "grep_files", "load_skill", "read_file"}
	if strings.Join(toolNames, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("explorer provider schema=%v want=%v", toolNames, wantTools)
	}

	events, err := runner.store.LoadEvents(result.SessionID)
	if err != nil {
		t.Fatalf("load explorer events: %v", err)
	}
	created := eventByType(events, "session.created")
	if created == nil || created.Data["tool_profile"] != session.ToolProfileExplorerReadOnly || created.Data["isolation_mode"] != "off" {
		t.Fatalf("session.created missing explorer effective facts: %#v", created)
	}
}

func TestRunnerQueueSubmitExplorerPersistsProfileOptionsAndOffIsolation(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.RoleProviders.Explorer = config.RoleProviderOverride{ReasoningEffort: "medium", MaxOutputTokens: 2048}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "Return a bounded repository evidence handoff.",
		AgentRole:       "explorer",
	})
	if err != nil {
		t.Fatalf("queue explorer: %v", err)
	}
	if job.AgentRole != "explorer" || job.ToolProfile != session.ToolProfileExplorerReadOnly || job.IsolationMode != "off" {
		t.Fatalf("queued explorer effective role/profile/isolation missing: %#v", job)
	}
	if job.ProviderOptions.ReasoningEffort != "medium" || job.ProviderOptions.MaxOutputTokens != 2048 {
		t.Fatalf("queued explorer role options missing: %#v", job.ProviderOptions)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil || !ok {
		t.Fatalf("process explorer job: ok=%v err=%v job=%#v", ok, err, processed)
	}
	childMeta, err := runner.store.LoadMetadata(processed.SessionID)
	if err != nil {
		t.Fatalf("load queued explorer metadata: %v", err)
	}
	if childMeta.ToolProfile != session.ToolProfileExplorerReadOnly || childMeta.AgentRole != "explorer" || childMeta.Isolation == nil || childMeta.Isolation.Mode != "off" {
		t.Fatalf("worker-created explorer facts drifted: %#v", childMeta)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil {
		t.Fatalf("load explorer background notification: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one explorer background notification, got %#v", notifications)
	}
	notification := notifications[0]
	if notification.AgentRole != agentRoleExplorer || notification.ToolProfile != session.ToolProfileExplorerReadOnly || notification.Provider != processed.Provider || notification.Model != processed.Model || notification.IsolationMode != "off" {
		t.Fatalf("background notification lost explorer identity/routing facts: %#v", notification)
	}
	if notification.ProviderOptions.ReasoningEffort != "medium" || notification.ProviderOptions.MaxOutputTokens != 2048 {
		t.Fatalf("background notification lost explorer provider options: %#v", notification.ProviderOptions)
	}
}

func TestQueuedExplorerUsesDurableProviderOptionSnapshotAfterSettingsChange(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.RoleProviders.Explorer = config.RoleProviderOverride{}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())

	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "Return a read-only evidence handoff using the queued snapshot.",
		AgentRole:       agentRoleExplorer,
	})
	if err != nil {
		t.Fatalf("queue explorer: %v", err)
	}
	if job.ProviderOptions.ReasoningEffort != "" || job.ProviderOptions.MaxOutputTokens != 0 {
		t.Fatalf("expected inherited empty generation snapshot before settings change: %#v", job.ProviderOptions)
	}

	// A queued job is an effective durable snapshot. A later Settings update
	// must affect only future children, including when the old value was the
	// zero/inherit representation.
	cfg.RoleProviders.Explorer = config.RoleProviderOverride{ReasoningEffort: "high", MaxOutputTokens: 8192}
	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil || !ok {
		t.Fatalf("process queued explorer: ok=%v err=%v job=%#v", ok, err, processed)
	}
	childMeta, err := runner.store.LoadMetadata(processed.SessionID)
	if err != nil {
		t.Fatalf("load queued explorer metadata: %v", err)
	}
	if childMeta.ProviderOptions.ReasoningEffort != "" || childMeta.ProviderOptions.MaxOutputTokens != 0 {
		t.Fatalf("settings update reinterpreted durable queued options: %#v", childMeta.ProviderOptions)
	}
}

func TestQueuedExplorerCancellationKeepsDurableProfileFacts(t *testing.T) {
	cfg := testRuntimeConfig(t)
	cfg.RoleProviders.Explorer = config.RoleProviderOverride{ReasoningEffort: "low", MaxOutputTokens: 1024}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "Inspect the queued scope read-only.",
		AgentRole:       agentRoleExplorer,
	})
	if err != nil {
		t.Fatalf("queue explorer: %v", err)
	}
	stopped, err := runner.StopAgent(context.Background(), tools.AgentStopRequest{
		ParentSessionID: parentID,
		QueueJobID:      job.ID,
	})
	if err != nil {
		t.Fatalf("cancel queued explorer: %v", err)
	}
	if stopped.Status != session.QueueStatusCancelled || !stopped.Accepted {
		t.Fatalf("unexpected queued explorer cancellation: %#v", stopped)
	}
	cancelled, err := runner.store.LoadJob(job.ID)
	if err != nil {
		t.Fatalf("load cancelled explorer job: %v", err)
	}
	if cancelled.AgentRole != agentRoleExplorer || cancelled.ToolProfile != session.ToolProfileExplorerReadOnly || cancelled.IsolationMode != "off" || cancelled.ProviderOptions.ReasoningEffort != "low" || cancelled.ProviderOptions.MaxOutputTokens != 1024 {
		t.Fatalf("cancelled explorer job lost effective facts: %#v", cancelled)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil || len(notifications) != 1 {
		t.Fatalf("load cancelled explorer notification: notifications=%#v err=%v", notifications, err)
	}
	if notifications[0].ToolProfile != session.ToolProfileExplorerReadOnly || notifications[0].IsolationMode != "off" || notifications[0].ProviderOptions.ReasoningEffort != "low" || notifications[0].ProviderOptions.MaxOutputTokens != 1024 {
		t.Fatalf("cancelled explorer notification lost effective facts: %#v", notifications[0])
	}
}

func TestQueuedExplorerFailureKeepsDurableProfileFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		http.Error(w, "deterministic explorer provider rejection", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	cfg := testRuntimeConfig(t)
	providerCfg := cfg.Providers["openai-compatible"]
	providerCfg.BaseURL = server.URL
	cfg.Providers["openai-compatible"] = providerCfg
	cfg.RoleProviders.Explorer = config.RoleProviderOverride{ReasoningEffort: "low", MaxOutputTokens: 1024}
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	job, err := runner.QueueSubmit(context.Background(), QueueSubmitRequest{
		ParentSessionID: parentID,
		Prompt:          "Inspect the failing provider path read-only.",
		AgentRole:       agentRoleExplorer,
	})
	if err != nil {
		t.Fatalf("queue explorer: %v", err)
	}

	processed, ok, err := runner.ProcessNextJob(context.Background())
	if err != nil || !ok {
		t.Fatalf("process failing explorer job: ok=%v err=%v job=%#v", ok, err, processed)
	}
	if processed.ID != job.ID || processed.Status != session.QueueStatusFailed || processed.SessionStatus != session.StatusFailed || strings.TrimSpace(processed.LastError) == "" {
		t.Fatalf("unexpected failed explorer result: %#v", processed)
	}
	if processed.AgentRole != agentRoleExplorer || processed.ToolProfile != session.ToolProfileExplorerReadOnly || processed.IsolationMode != "off" || processed.ProviderOptions.ReasoningEffort != "low" || processed.ProviderOptions.MaxOutputTokens != 1024 {
		t.Fatalf("failed explorer job lost effective facts: %#v", processed)
	}
	childMeta, err := runner.store.LoadMetadata(processed.SessionID)
	if err != nil {
		t.Fatalf("load failed explorer metadata: %v", err)
	}
	if childMeta.AgentRole != agentRoleExplorer || childMeta.ToolProfile != session.ToolProfileExplorerReadOnly || childMeta.Isolation == nil || childMeta.Isolation.Mode != "off" {
		t.Fatalf("failed explorer child lost effective facts: %#v", childMeta)
	}
	notifications, err := runner.store.LoadBackgroundNotifications(parentID)
	if err != nil || len(notifications) != 1 {
		t.Fatalf("load failed explorer notification: notifications=%#v err=%v", notifications, err)
	}
	if notifications[0].Status != session.QueueStatusFailed || notifications[0].AgentRole != agentRoleExplorer || notifications[0].ToolProfile != session.ToolProfileExplorerReadOnly || notifications[0].IsolationMode != "off" || notifications[0].ProviderOptions.ReasoningEffort != "low" || notifications[0].ProviderOptions.MaxOutputTokens != 1024 {
		t.Fatalf("failed explorer notification lost effective facts: %#v", notifications[0])
	}
}

func TestExplorerParentRecoveryKeepsReadOnlyProfile(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       string
		pauseReason  string
		wantBehavior string
	}{
		{name: "failed", status: session.StatusFailed, wantBehavior: "continued_failed_child"},
		{name: "paused", status: session.StatusPaused, pauseReason: "manual_stop", wantBehavior: "continued_parent_stopped_child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRuntimeConfig(t)
			runner := NewRunner(cfg)
			parentID := createParentSession(t, runner.store, t.TempDir())
			now := time.Now().UTC().Format(time.RFC3339Nano)
			workdir := t.TempDir()
			providerCfg := cfg.Providers["openai-compatible"]
			child := session.SessionMetadata{
				SchemaVersion:    1,
				ID:               "explorer_recovery_" + tc.name,
				CreatedAt:        now,
				Workdir:          workdir,
				RequestedWorkdir: workdir,
				Mode:             session.ModeExec,
				Provider:         "openai-compatible",
				Model:            providerCfg.Model,
				ProviderOptions:  providerOptionsFromConfig("openai-compatible", providerCfg),
				CompletionPolicy: session.CompletionPolicyAutonomous,
				ParentSessionID:  parentID,
				RootSessionID:    parentID,
				AgentRole:        agentRoleExplorer,
				ToolProfile:      session.ToolProfileExplorerReadOnly,
				Depth:            1,
				Isolation:        &session.IsolationInfo{Mode: "off", RequestedMode: "off", ParentWorkdir: workdir, Workdir: workdir},
			}
			state := session.State{Status: tc.status, Phase: "interrupt", PauseReason: tc.pauseReason, UpdatedAt: now}
			if tc.status == session.StatusFailed {
				state.Phase = "provider_call"
				state.LastError = "recoverable explorer provider failure"
			}
			if err := runner.store.Create(child, state); err != nil {
				t.Fatalf("create recoverable explorer: %v", err)
			}
			if err := addParentChildSession(runner.store, parentID, child.ID, parentWaitAll); err != nil {
				t.Fatalf("link recoverable explorer: %v", err)
			}

			result, err := runner.PromptAgent(context.Background(), tools.AgentPromptRequest{
				ParentSessionID: parentID,
				SessionID:       child.ID,
				Message:         "Return the concise evidence handoff now.",
			})
			if err != nil || !result.Accepted || result.Behavior != tc.wantBehavior {
				t.Fatalf("resume recoverable explorer: result=%#v err=%v", result, err)
			}
			finalState, err := runner.store.LoadState(child.ID)
			if err != nil || finalState.Status != session.StatusCompleted {
				t.Fatalf("recoverable explorer did not complete: state=%#v err=%v", finalState, err)
			}
			finalMeta, err := runner.store.LoadMetadata(child.ID)
			if err != nil {
				t.Fatalf("load recovered explorer metadata: %v", err)
			}
			if finalMeta.AgentRole != agentRoleExplorer || finalMeta.ToolProfile != session.ToolProfileExplorerReadOnly || finalMeta.Isolation == nil || finalMeta.Isolation.Mode != "off" {
				t.Fatalf("recovered explorer lost read-only profile: %#v", finalMeta)
			}
			status, err := runner.AgentStatus(context.Background(), tools.AgentStatusRequest{ParentSessionID: parentID, SessionID: child.ID})
			if err != nil || status.ToolProfile != session.ToolProfileExplorerReadOnly || status.AgentRole != agentRoleExplorer {
				t.Fatalf("agent status lost recovered explorer profile: status=%#v err=%v", status, err)
			}
		})
	}
}

func TestExplorerInheritsDurableParentZeroProviderOptions(t *testing.T) {
	cfg := testRuntimeConfig(t)
	runner := NewRunner(cfg)
	parentID := createParentSession(t, runner.store, t.TempDir())
	parentMeta, err := runner.store.LoadMetadata(parentID)
	if err != nil {
		t.Fatalf("load parent metadata: %v", err)
	}
	parentMeta.ProviderOptions = providerOptionsFromConfig(parentMeta.Provider, cfg.Providers[parentMeta.Provider])
	if parentMeta.ProviderOptions.ReasoningEffort != "" || parentMeta.ProviderOptions.MaxOutputTokens != 0 {
		t.Fatalf("unexpected parent fixture options: %#v", parentMeta.ProviderOptions)
	}
	if err := runner.store.SaveMetadata(parentID, parentMeta); err != nil {
		t.Fatalf("save parent provider snapshot: %v", err)
	}

	updatedProvider := cfg.Providers[parentMeta.Provider]
	updatedProvider.ReasoningEffort = "high"
	updatedProvider.MaxOutputTokens = 8192
	cfg.Providers[parentMeta.Provider] = updatedProvider
	result, err := runner.Delegate(context.Background(), DelegateRequest{
		ParentSessionID: parentID,
		Prompt:          "Return a bounded evidence handoff using the parent provider snapshot.",
		AgentRole:       agentRoleExplorer,
	})
	if err != nil {
		t.Fatalf("delegate explorer: %v", err)
	}
	childMeta, err := runner.store.LoadMetadata(result.SessionID)
	if err != nil {
		t.Fatalf("load child metadata: %v", err)
	}
	if childMeta.ProviderOptions.ReasoningEffort != "" || childMeta.ProviderOptions.MaxOutputTokens != 0 {
		t.Fatalf("current provider config replaced durable parent zero options: %#v", childMeta.ProviderOptions)
	}
}

func TestExplorerCapabilityDenialRunsBeforeToolLifecycleHooks(t *testing.T) {
	hookSentinel := filepath.Join(t.TempDir(), "forbidden-hook-ran")
	cfg := config.Default()
	cfg.Runtime.GuardrailsMode = "standard"
	cfg.Hooks.ToolBefore = []config.HookDefinition{{
		Name:    "forbidden-shell-hook",
		Match:   config.HookMatch{Tool: "shell"},
		Command: []string{"/bin/sh", "-c", "printf ran > \"$1\"", "hook", hookSentinel},
	}}
	engine, meta, state, _, hookManager, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	meta.AgentRole = agentRoleExplorer
	meta.ToolProfile = session.ToolProfileExplorerReadOnly
	if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
		t.Fatalf("save explorer metadata: %v", err)
	}
	registry, err := tools.NewRegistryForToolProfile(engine.cfg, catalog, engine.store, nil, session.ToolProfileExplorerReadOnly, meta.Workdir)
	if err != nil {
		t.Fatalf("new explorer registry: %v", err)
	}
	if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Inspect without executing forbidden tools.")); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	fake := provider.NewFake(
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_forged_shell", Name: "shell", Arguments: json.RawMessage(`{"command":"touch forbidden"}`)}},
				StopReason: "tool_use",
			}, nil
		},
		func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
			return provider.TurnResult{
				ToolCalls:  []provider.ToolCall{{ID: "call_finish", Name: "finish", Arguments: json.RawMessage(`{"message":"bounded handoff"}`)}},
				StopReason: "tool_use",
			}, nil
		},
	)
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hookManager)
	if err != nil {
		t.Fatalf("run explorer: %v", err)
	}
	if result.Status != session.StatusCompleted {
		messages, _ := engine.store.LoadMessages(meta.ID)
		events, _ := engine.store.LoadEvents(meta.ID)
		t.Fatalf("explorer did not finish after denial: result=%#v messages=%#v events=%#v", result, messages, events)
	}
	if _, err := os.Stat(hookSentinel); !os.IsNotExist(err) {
		t.Fatalf("forbidden tool reached tool.before hook: %v", err)
	}
	messages, err := engine.store.LoadMessages(meta.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var denial *session.ToolResult
	for i := range messages {
		for j := range messages[i].ToolResults {
			candidate := &messages[i].ToolResults[j]
			if candidate.ToolCallID == "call_forged_shell" {
				denial = candidate
			}
		}
	}
	if denial == nil || !denial.IsError || denial.Metadata[tools.MetadataFailureClass] != tools.FailureClassSchemaReject || denial.Metadata["error_code"] != tools.ErrorCodeToolNotAllowedForRole || denial.Metadata["tool_profile"] != session.ToolProfileExplorerReadOnly {
		t.Fatalf("forged explorer call lost stable denial: %#v", denial)
	}
	events, err := engine.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	for _, event := range events {
		if event.Type == "tool.before" && event.Data["tool_name"] == "shell" {
			t.Fatalf("forbidden explorer tool emitted tool.before: %#v", event)
		}
	}
}

func TestExplorerChildTrajectoryStaysOutOfParentProviderMessages(t *testing.T) {
	const childOnlySentinel = "CHILD-RAW-TRAJECTORY-SENTINEL-9f71c2"
	var mu sync.Mutex
	var parentBodies [][]byte
	var childBodies [][]byte
	parentTurn := 0
	childTurn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		metadata, _ := request["metadata"].(map[string]any)
		profile, _ := metadata["tool_profile"].(string)

		mu.Lock()
		isChild := profile == session.ToolProfileExplorerReadOnly
		if isChild {
			childBodies = append(childBodies, append([]byte(nil), body...))
			childTurn++
		} else {
			parentBodies = append(parentBodies, append([]byte(nil), body...))
			parentTurn++
		}
		currentParentTurn := parentTurn
		currentChildTurn := childTurn
		mu.Unlock()

		var name, callID string
		var arguments map[string]any
		switch {
		case isChild && currentChildTurn == 1:
			name, callID = "read_file", "call_child_read"
			arguments = map[string]any{"path": "sentinel.txt"}
		case isChild:
			name, callID = "finish", "call_child_finish"
			arguments = map[string]any{"message": "Concise child finding.\n\n| claim | file:line | confidence |\n| --- | --- | --- |\n| sentinel fixture exists | sentinel.txt:1 | high |\n\nUncovered scope: none.\nKey uncertainties: none."}
		case currentParentTurn == 1:
			name, callID = "agent_spawn", "call_parent_spawn"
			arguments = map[string]any{
				"prompt":     "Read sentinel.txt and return only a concise evidence handoff without quoting its contents.",
				"agent_name": "fixture-explorer",
				"agent_role": agentRoleExplorer,
			}
		default:
			name, callID = "finish", "call_parent_finish"
			arguments = map[string]any{"message": "Parent synthesis used the bounded explorer handoff."}
		}
		argumentBytes, _ := json.Marshal(arguments)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_" + callID,
			"status": "completed",
			"output": []map[string]any{{
				"type":      "function_call",
				"call_id":   callID,
				"name":      name,
				"arguments": string(argumentBytes),
			}},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "sentinel.txt"), []byte(childOnlySentinel+"\n"), 0o600); err != nil {
		t.Fatalf("write child-only sentinel: %v", err)
	}
	cfg := config.Default()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIProvider: "openai-compatible",
		APIKeyEnv:   "OPENAI_API_KEY",
		BaseURL:     server.URL,
		Model:       "gpt-fixture",
		TimeoutSec:  30,
	}
	cfg.Session.Dir = t.TempDir()
	cfg.Runtime.Isolation.RootDir = t.TempDir()
	runner := NewRunner(cfg)
	result, err := runner.Start(context.Background(), StartRequest{
		Prompt:   "Use an explorer when useful, then synthesize the bounded evidence.",
		Provider: "openai-compatible",
		Model:    "gpt-fixture",
		Workdir:  workdir,
		Mode:     session.ModeExec,
	})
	if err != nil {
		t.Fatalf("run parent explorer fixture: %v", err)
	}
	if result.Status != session.StatusCompleted {
		t.Fatalf("parent explorer fixture did not complete: %#v", result)
	}

	mu.Lock()
	parentSnapshot := append([][]byte(nil), parentBodies...)
	childSnapshot := append([][]byte(nil), childBodies...)
	mu.Unlock()
	if len(parentSnapshot) < 2 || len(childSnapshot) < 2 {
		t.Fatalf("unexpected parent/child provider call counts: parent=%d child=%d", len(parentSnapshot), len(childSnapshot))
	}
	if !strings.Contains(string(childSnapshot[len(childSnapshot)-1]), childOnlySentinel) {
		t.Fatalf("fixture did not place child tool trajectory in child context: %s", childSnapshot[len(childSnapshot)-1])
	}
	for index, body := range parentSnapshot {
		if strings.Contains(string(body), childOnlySentinel) {
			t.Fatalf("child raw trajectory leaked into parent provider request %d: %s", index+1, body)
		}
	}
	children, err := runner.store.ListChildren(result.SessionID, -1)
	if err != nil || len(children) != 1 {
		t.Fatalf("load durable explorer child: children=%#v err=%v", children, err)
	}
	if children[0].AgentRole != agentRoleExplorer || children[0].ToolProfile != session.ToolProfileExplorerReadOnly {
		t.Fatalf("durable child identity mismatch: %#v", children[0])
	}
}

func eventByType(items []events.Event, eventType string) *events.Event {
	for i := range items {
		if items[i].Type == eventType {
			return &items[i]
		}
	}
	return nil
}
