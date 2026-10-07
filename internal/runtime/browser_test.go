package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/provider"
	"aegis-agent/internal/session"
)

func browserEngineConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Tools.Browser.Enabled = true
	cfg.Tools.Browser.InstallRoot = t.TempDir()
	cfg.Runtime.CommandTimeoutSec = 5
	cfg.Runtime.Queue.AutoWorker = false
	cfg.Skills.Dirs = nil
	fake, err := os.ReadFile("../tools/testdata/browser_fake.py")
	if err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(cfg.Tools.Browser.InstallRoot, "bin"), 0700)
	for _, path := range []string{filepath.Join(cfg.Tools.Browser.InstallRoot, "bin", "python"), filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")} {
		if err := os.WriteFile(path, fake, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Tools.Browser.BrowserExecutable = filepath.Join(cfg.Tools.Browser.InstallRoot, "chrome")
	return cfg
}

func TestBrowserGuidanceDisclosureInProviderRequest(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{repo, t.TempDir()} {
		for _, enabled := range []bool{false, true} {
			t.Run(filepath.Base(workspace)+"/enabled="+strconv.FormatBool(enabled), func(t *testing.T) {
				cfg := config.Default()
				cfg.Tools.Browser.Enabled = enabled
				cfg.Tools.Browser.InstallRoot = filepath.Join(t.TempDir(), "not-installed")
				cfg.Skills.Dirs = []string{filepath.Join(workspace, "skills")}
				engine, meta, state, registry, hm, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
				meta.Workdir = workspace
				fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
					found := false
					for _, tool := range req.Tools {
						if tool.Name == "browser_exec" {
							found = true
							if !strings.Contains(tool.Description, "Browser helper guidance") || !strings.Contains(tool.Description, "goto_url(url)") {
								t.Fatal("embedded browser guidance absent in model-visible description")
							}
						}
						if !enabled && strings.HasPrefix(tool.Name, "browser_") {
							t.Fatal("disabled browser tool disclosed")
						}
					}
					if found != enabled {
						t.Fatalf("browser disclosure: got %v enabled %v", found, enabled)
					}
					if !enabled && (strings.Contains(req.SystemPrompt, "browser-use") || strings.Contains(req.SystemPrompt, "browser_exec") || strings.Contains(req.SystemPrompt, "Browser helper guidance")) {
						t.Fatal("disabled browser skill/guidance disclosed in system prompt")
					}
					return provider.TurnResult{StopReason: "tool_use", ToolCalls: []provider.ToolCall{{ID: "finish", Name: "finish", Arguments: json.RawMessage(`{"message":"disclosure checked"}`)}}}, nil
				})
				if _, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hm); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(engine.store.SessionDir(meta.ID), "browser")); !os.IsNotExist(err) {
					t.Fatal("disclosure initialized browser state", err)
				}
				if _, err := os.Stat(cfg.Tools.Browser.InstallRoot); !os.IsNotExist(err) {
					t.Fatal("disclosure initialized browser install", err)
				}
			})
		}
	}
}
func TestBrowserEngineRegistryHooksArtifactNextProvider(t *testing.T) {
	cfg := browserEngineConfig(t)
	cfg.Runtime.ToolOutput.LLMOutputMaxBytes = 1024
	cfg.Hooks.ToolBefore = []config.HookDefinition{{Name: "browser-before", Match: config.HookMatch{Tool: "browser_exec"}, Inject: &config.HookInject{Field: "arguments", Set: `{"code":"print('HEAD'+'x'*6000+'MIDDLE'+'y'*6000+'TAIL')"}`}}}
	cfg.Hooks.ToolAfter = []config.HookDefinition{{Name: "browser-after", Match: config.HookMatch{Tool: "browser_exec"}, Inject: &config.HookInject{Field: "llm_output", Prefix: "AFTER-HOOK\n"}}}
	engine, meta, state, registry, hm, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	engine.store.AppendMessage(meta.ID, session.NewMessage("user", "Use the optional browser tools."))
	calls := 0
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		calls++
		found := false
		for _, tool := range req.Tools {
			if tool.Name == "browser_exec" {
				found = true
			}
		}
		if !found {
			t.Fatal("real runtime assembly lacks browser tool")
		}
		return provider.TurnResult{StopReason: "tool_use", ToolCalls: []provider.ToolCall{{ID: "browser-one", Name: "browser_exec", Arguments: json.RawMessage(`{"code":"raise RuntimeError('before hook did not apply')"}`)}, {ID: "browser-shot", Name: "browser_screenshot", Arguments: json.RawMessage(`{}`)}}}, nil
	}, func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		calls++
		foundExec, foundShot := false, false
		for _, msg := range req.Messages {
			for _, r := range msg.ToolResults {
				switch r.Name {
				case "browser_exec":
					foundExec = true
					if r.IsError || !strings.HasPrefix(r.LLMOutput, "AFTER-HOOK") || len(r.LLMOutput) > 1024 {
						t.Fatal(r)
					}
					// Hook-modified preview may acquire a second bounded text artifact, while
					// the complete command source still exists in the canonical quota root.
				case "browser_screenshot":
					foundShot = true
					if r.IsError || r.Metadata["image_delivery"] != "ref-only" || r.Metadata["model_image_visible"] != false || !strings.Contains(r.LLMOutput, "ref-only") {
						t.Fatal(r)
					}
				}
			}
		}
		if !foundExec || !foundShot {
			t.Fatal("tool results absent from next provider request")
		}
		return provider.TurnResult{StopReason: "tool_use", ToolCalls: []provider.ToolCall{{ID: "finish", Name: "finish", Arguments: json.RawMessage(`{"message":"process and ref observed"}`)}}}, nil
	})
	result, err := engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hm)
	if err != nil || result.Status != session.StatusCompleted || calls != 2 {
		t.Fatal(result, err, calls)
	}
	events, err := engine.store.LoadEvents(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range events {
		counts[e.Type]++
	}
	for _, kind := range []string{"tool.before", "tool.after", "hook.finished", "browser.started", "browser.cleanup"} {
		if counts[kind] == 0 {
			t.Fatal(kind, counts)
		}
	}
	entries, err := os.ReadDir(filepath.Join(engine.store.SessionDir(meta.ID), "artifacts", "tool-outputs"))
	if err != nil {
		t.Fatal(err)
	}
	full := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".txt") {
			data, _ := os.ReadFile(filepath.Join(engine.store.SessionDir(meta.ID), "artifacts", "tool-outputs", entry.Name()))
			if strings.Contains(string(data), "HEAD") && strings.Contains(string(data), "MIDDLE") && strings.Contains(string(data), "TAIL") {
				full = true
			}
		}
	}
	if !full {
		t.Fatal("raw command artifact not recoverable")
	}
}
func TestBrowserPlanModeBlocksForgedDispatch(t *testing.T) {
	cfg := browserEngineConfig(t)
	engine, meta, state, registry, hm, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
	if _, err := engine.store.CreatePlanMode(meta.ID, session.PlanModeDraft{Enabled: true, Objective: "Review first"}); err != nil {
		t.Fatal(err)
	}
	fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		for _, tool := range req.Tools {
			if strings.HasPrefix(tool.Name, "browser_") {
				t.Fatal("pending approval disclosed browser")
			}
		}
		return provider.TurnResult{StopReason: "tool_use", ToolCalls: []provider.ToolCall{{ID: "forged", Name: "browser_exec", Arguments: json.RawMessage(`{"code":"print('must not run')"}`)}}}, nil
	}, func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
		for _, msg := range req.Messages {
			for _, r := range msg.ToolResults {
				if r.Name == "browser_exec" && (!r.IsError || r.Metadata["guard"] != "plan_mode_pending") {
					t.Fatal(r)
				}
			}
		}
		return provider.TurnResult{StopReason: "error"}, nil
	})
	engine.Run(context.Background(), meta, state, "", fake, catalog, registry, hm)
	if _, err := os.Stat(filepath.Join(engine.store.SessionDir(meta.ID), "browser")); !os.IsNotExist(err) {
		t.Fatal("Plan Mode started a browser")
	}
}

func TestBrowserPauseAndAbortSettlesOwnedProcesses(t *testing.T) {
	for _, kind := range []string{"pause", "abort", "child_stop"} {
		t.Run(kind, func(t *testing.T) {
			cfg := browserEngineConfig(t)
			engine, meta, state, registry, hm, catalog := newTestEngineWithConfig(t, cfg, session.ModeExec)
			marker := filepath.Join(t.TempDir(), "started")
			fake := provider.NewFake(func(_ context.Context, req provider.TurnRequest) (provider.TurnResult, error) {
				args, _ := json.Marshal(map[string]string{"code": "import time;print('EFFECT_SENT',flush=True);open(" + strconv.Quote(marker) + ",'w').close();time.sleep(30)"})
				return provider.TurnResult{StopReason: "tool_use", ToolCalls: []provider.ToolCall{{ID: "cancel-owned", Name: "browser_exec", Arguments: args}}}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					if _, err := os.Stat(marker); err == nil {
						if kind == "abort" {
							cancel()
						} else if kind == "child_stop" {
							engine.control.requestPauseWithReason(agentCancelRequestedReason)
						} else {
							engine.control.requestPause()
						}
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(10 * time.Millisecond):
					}
				}
			}()
			result, err := engine.Run(ctx, meta, state, "", fake, catalog, registry, hm)
			<-done
			if kind == "pause" && (err != nil || result.Status != session.StatusPaused) {
				t.Fatal(result, err)
			}
			if kind == "child_stop" && result.Status != session.StatusCancelled {
				t.Fatal(result, err)
			}
			messages, err := engine.store.LoadMessages(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, msg := range messages {
				for _, r := range msg.ToolResults {
					if r.ToolCallID == "cancel-owned" {
						found = true
						if !r.IsError || r.Metadata["failure_class"] != "interrupted" || r.Metadata["cleanup"] != "confirmed" || !strings.Contains(r.LLMOutput, "EFFECT_SENT") {
							t.Fatal(r)
						}
					}
				}
			}
			if !found {
				t.Fatal("dangling interrupted call")
			}
			events, err := engine.store.LoadEvents(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			cleanup := false
			for _, event := range events {
				if event.Type == "browser.cleanup" && event.Data["status"] == "confirmed" {
					cleanup = true
				}
			}
			if !cleanup {
				t.Fatal("owned cleanup not durably confirmed")
			}
		})
	}
}
