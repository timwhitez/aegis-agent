package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/session"
)

const (
	budgetCrashHelperEnv   = "GO_CLI_AGENT_BUDGET_CRASH_HELPER"
	budgetCrashRootEnv     = "GO_CLI_AGENT_BUDGET_CRASH_ROOT"
	budgetCrashWorkdirEnv  = "GO_CLI_AGENT_BUDGET_CRASH_WORKDIR"
	budgetCrashSessionEnv  = "GO_CLI_AGENT_BUDGET_CRASH_SESSION"
	budgetCrashScenarioEnv = "GO_CLI_AGENT_BUDGET_CRASH_SCENARIO"
	budgetCrashMarkerEnv   = "GO_CLI_AGENT_BUDGET_CRASH_MARKER"
)

func TestChildActiveRuntimeSurvivesProviderAndShellProcessKills(t *testing.T) {
	for _, scenario := range []string{"provider", "shell"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "test")
			sessionRoot := filepath.Join(t.TempDir(), "sessions")
			workdir := t.TempDir()
			marker := filepath.Join(t.TempDir(), "shell-started")
			store := session.NewStore(sessionRoot)
			now := time.Now().UTC()
			budget := session.NewEffectiveBudget(session.BudgetSourceRuntimeChild, 0, 10, 0, 0, now)
			budget.ActiveRuntimeCheckpointIntervalMS = 100
			meta := session.SessionMetadata{
				SchemaVersion:    1,
				ID:               session.NewSessionID(),
				CreatedAt:        now.Format(time.RFC3339Nano),
				Workdir:          workdir,
				RequestedWorkdir: workdir,
				Mode:             session.ModeExec,
				Provider:         "openai-compatible",
				Model:            "gpt-5.4",
				CompletionPolicy: session.CompletionPolicyAutonomous,
				ParentSessionID:  "parent_budget_crash_" + scenario,
				RootSessionID:    "parent_budget_crash_" + scenario,
				Depth:            1,
				EffectiveBudget:  budget,
			}
			state := session.State{Status: session.StatusRunning, Phase: "prepare", UpdatedAt: now.Format(time.RFC3339Nano)}
			if err := store.Create(meta, state); err != nil {
				t.Fatalf("create crash-test child: %v", err)
			}
			if err := store.AppendMessage(meta.ID, session.NewMessage("user", "run until the process is killed")); err != nil {
				t.Fatalf("append crash-test user message: %v", err)
			}

			var helperOutput bytes.Buffer
			cmd := exec.Command(os.Args[0], "-test.run=^TestChildBudgetCrashSubprocessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				budgetCrashHelperEnv+"=1",
				budgetCrashRootEnv+"="+sessionRoot,
				budgetCrashWorkdirEnv+"="+workdir,
				budgetCrashSessionEnv+"="+meta.ID,
				budgetCrashScenarioEnv+"="+scenario,
				budgetCrashMarkerEnv+"="+marker,
				"OPENAI_API_KEY=test",
			)
			cmd.Stdout = &helperOutput
			cmd.Stderr = &helperOutput
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatalf("start crash helper: %v", err)
			}
			killed := false
			shellPID := 0
			t.Cleanup(func() {
				if !killed && cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					_ = cmd.Wait()
				}
				if shellPID > 0 {
					_ = syscall.Kill(-shellPID, syscall.SIGKILL)
					_ = syscall.Kill(shellPID, syscall.SIGKILL)
				}
			})

			var beforeKill *session.EffectiveBudget
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				loaded, err := store.LoadMetadata(meta.ID)
				if err != nil {
					t.Fatalf("load helper budget: %v", err)
				}
				scenarioReady := scenario == "provider"
				if scenario == "shell" {
					pidBytes, readErr := os.ReadFile(marker)
					if readErr == nil {
						shellPID, _ = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
					}
					scenarioReady = shellPID > 0
				}
				if scenarioReady && loaded.EffectiveBudget != nil && loaded.EffectiveBudget.ActiveRuntimeLeaseOpen && loaded.EffectiveBudget.UsedActiveRuntimeMS >= 100 {
					beforeKill = session.CloneEffectiveBudget(loaded.EffectiveBudget)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if beforeKill == nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Wait()
				killed = true
				t.Fatalf("helper never persisted a live checkpoint for %s; output=%s", scenario, helperOutput.String())
			}
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				t.Fatalf("kill crash helper process group: %v", err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatalf("crash helper exited without SIGKILL; output=%s", helperOutput.String())
			}
			killed = true
			if shellPID > 0 {
				_ = syscall.Kill(-shellPID, syscall.SIGKILL)
				_ = syscall.Kill(shellPID, syscall.SIGKILL)
			}

			// The offline interval is deliberately much larger than the 100 ms
			// checkpoint. Recovery must charge one bounded interval, not this wall time.
			time.Sleep(1200 * time.Millisecond)
			crashed, err := store.LoadMetadata(meta.ID)
			if err != nil {
				t.Fatalf("load killed helper budget: %v", err)
			}
			if crashed.EffectiveBudget == nil || !crashed.EffectiveBudget.ActiveRuntimeLeaseOpen || crashed.EffectiveBudget.UsedActiveRuntimeMS < beforeKill.UsedActiveRuntimeMS {
				t.Fatalf("killed helper did not leave a durable open lease: before=%#v after=%#v", beforeKill, crashed.EffectiveBudget)
			}
			state, err = store.LoadState(meta.ID)
			if err != nil {
				t.Fatalf("load killed helper state: %v", err)
			}
			state.Status = session.StatusPaused
			state.Phase = "interrupt"
			state.PauseReason = "stale_owner_reconciled"
			state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := store.SaveState(meta.ID, state); err != nil {
				t.Fatalf("persist simulated stale-owner recovery: %v", err)
			}

			resumeServer := newBudgetCrashResumeServer(t)
			cfg := budgetCrashConfig(sessionRoot, workdir, resumeServer.URL)
			runner := NewRunner(cfg)
			result, err := runner.Continue(context.Background(), ContinueRequest{SessionID: meta.ID, Source: "crash_recovery_test"})
			if err != nil || result.Status != session.StatusAwaitingInput {
				t.Fatalf("resume killed child: result=%#v err=%v", result, err)
			}
			recovered, err := store.LoadMetadata(meta.ID)
			if err != nil {
				t.Fatalf("load recovered child budget: %v", err)
			}
			if recovered.EffectiveBudget == nil || recovered.EffectiveBudget.ActiveRuntimeLastRecoveryMS != 100 || recovered.EffectiveBudget.ActiveRuntimeLastRecoveryAt == "" || recovered.EffectiveBudget.ActiveRuntimeLeaseOpen || recovered.EffectiveBudget.ActiveRuntimeLeaseOwner != "" {
				t.Fatalf("restart did not apply and settle one bounded recovery charge: %#v", recovered.EffectiveBudget)
			}
			if recovered.EffectiveBudget.UsedActiveRuntimeMS-beforeKill.UsedActiveRuntimeMS >= 1200 {
				t.Fatalf("offline wall time leaked into active runtime: before=%#v after=%#v", beforeKill, recovered.EffectiveBudget)
			}
			eventsList, err := store.LoadEvents(meta.ID)
			if err != nil {
				t.Fatalf("load recovered child events: %v", err)
			}
			if countEventType(eventsList, "session.child_budget.active_runtime_recovered") != 1 {
				t.Fatalf("expected one recovery event after process kill: %#v", eventsList)
			}
			if scenario == "shell" {
				messages, err := store.LoadMessages(meta.ID)
				if err != nil {
					t.Fatalf("load recovered shell messages: %v", err)
				}
				if !hasRecoveredBudgetCrashShellResult(messages) {
					t.Fatalf("shell crash resume did not repair dangling tool call: %#v", messages)
				}
			}
		})
	}
}

func TestChildBudgetCrashSubprocessHelper(t *testing.T) {
	if os.Getenv(budgetCrashHelperEnv) != "1" {
		return
	}
	sessionRoot := os.Getenv(budgetCrashRootEnv)
	workdir := os.Getenv(budgetCrashWorkdirEnv)
	sessionID := os.Getenv(budgetCrashSessionEnv)
	scenario := os.Getenv(budgetCrashScenarioEnv)
	marker := os.Getenv(budgetCrashMarkerEnv)
	if sessionRoot == "" || workdir == "" || sessionID == "" || (scenario != "provider" && scenario != "shell") {
		t.Fatalf("invalid crash helper environment")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		if scenario == "provider" {
			<-r.Context().Done()
			return
		}
		command := fmt.Sprintf("echo $$ > %q; sleep 30", marker)
		arguments, _ := json.Marshal(map[string]any{"command": command})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_budget_crash_shell",
			"status": "completed",
			"output": []map[string]any{{
				"type":      "function_call",
				"call_id":   "call_budget_crash_shell",
				"name":      "shell",
				"arguments": string(arguments),
			}},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()
	cfg := budgetCrashConfig(sessionRoot, workdir, server.URL)
	runner := NewRunner(cfg)
	meta, err := runner.store.LoadMetadata(sessionID)
	if err != nil {
		t.Fatalf("load crash helper metadata: %v", err)
	}
	state, err := runner.store.LoadState(sessionID)
	if err != nil {
		t.Fatalf("load crash helper state: %v", err)
	}
	result, err := runner.runExisting(context.Background(), meta, state, "", nil)
	t.Fatalf("crash helper returned before parent kill: result=%#v err=%v", result, err)
}

func budgetCrashConfig(sessionRoot, workdir, baseURL string) *config.Config {
	cfg := config.Default()
	cfg.DefaultProvider = "openai-compatible"
	cfg.Providers["openai-compatible"] = config.Provider{
		APIKeyEnv:  "OPENAI_API_KEY",
		BaseURL:    baseURL,
		Model:      "gpt-5.4",
		TimeoutSec: 60,
		WireAPI:    "responses",
	}
	cfg.Session.Dir = sessionRoot
	cfg.Runtime.Isolation.RootDir = filepath.Join(workdir, ".isolation")
	cfg.Runtime.MaxTurnsHard = -1
	cfg.Runtime.MaxTurnsSoft = 100
	cfg.Runtime.ChildBudget = config.ChildBudgetConfig{
		MaxActiveRuntimeSec:       10,
		ActiveRuntimeCheckpointMS: 100,
	}
	cfg.Skills.Dirs = nil
	return cfg
}

func newBudgetCrashResumeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_, _ = io.ReadAll(r.Body)
		arguments, _ := json.Marshal(map[string]any{
			"kind":             "external_wait",
			"reason":           "recovered",
			"resume_condition": "continue",
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "resp_budget_crash_resume",
			"status": "completed",
			"output": []map[string]any{{
				"type":      "function_call",
				"call_id":   "call_budget_crash_await",
				"name":      "await_input",
				"arguments": string(arguments),
			}},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func hasRecoveredBudgetCrashShellResult(messages []session.Message) bool {
	for _, message := range messages {
		for _, result := range message.ToolResults {
			if result.ToolCallID == "call_budget_crash_shell" && result.Name == "shell" && result.IsError && result.Metadata != nil && result.Metadata["recovered_dangling_tool_call"] == true {
				return true
			}
		}
	}
	return false
}
