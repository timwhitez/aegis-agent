package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/provider"
	"aegis-agent/internal/session"
)

func TestHTTPAdapterCanonicalUsageReachesGoalBudget(t *testing.T) {
	tests := []struct {
		name, response, wrapup string
		total                  int64
		adapter                func(string, *http.Client) provider.Adapter
	}{
		{"anthropic", `{"id":"msg-usage","stop_reason":"end_turn","content":[{"type":"text","text":"working"}],"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":99}}`, `{"id":"msg-wrapup","stop_reason":"tool_use","content":[{"type":"tool_use","id":"wrapup","name":"record_goal_progress","input":{"kind":"budget_wrapup","summary":"Budget reached; work remains."}}]}`, 101, func(url string, c *http.Client) provider.Adapter {
			return provider.NewAnthropic(url, "key", "2023-06-01", c)
		}},
		{"google", `{"responseId":"resp-usage","candidates":[{"content":{"parts":[{"text":"working"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":100,"totalTokenCount":115}}`, `{"responseId":"resp-wrapup","candidates":[{"content":{"parts":[{"functionCall":{"id":"wrapup","name":"record_goal_progress","args":{"kind":"budget_wrapup","summary":"Budget reached; work remains."}}}]},"finishReason":"STOP"}]}`, 115, func(url string, c *http.Client) provider.Adapter { return provider.NewGoogle(url, "key", c) }},
		{"openai", `{"id":"resp-usage","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"working"}]}],"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":10}}}`, `{"id":"resp-wrapup","status":"completed","output":[{"type":"function_call","call_id":"wrapup","name":"record_goal_progress","arguments":"{\"kind\":\"budget_wrapup\",\"summary\":\"Budget reached; work remains.\"}"}]}`, 120, func(url string, c *http.Client) provider.Adapter { return provider.NewOpenAI(url, "key", c) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			engine, meta, state, registry, hooks, catalog := newTestEngine(t, session.ModeExec)
			meta.Provider = tc.name
			if err := engine.store.SaveMetadata(meta.ID, meta); err != nil {
				t.Fatal(err)
			}
			cap := int64(100)
			if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{Enabled: true, Objective: "Continue until budget", TokenBudget: &cap, StopOnBudget: true, Source: session.GoalSourceCLI}); err != nil {
				t.Fatal(err)
			}
			if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "do the work")); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					fmt.Fprint(w, tc.response)
				} else {
					if calls > 2 {
						t.Error("ordinary implementation continued after budget")
					}
					fmt.Fprint(w, tc.wrapup)
				}
			}))
			defer server.Close()
			adapter := tc.adapter(server.URL, server.Client())
			result, err := engine.Run(context.Background(), meta, state, "", adapter, catalog, registry, hooks)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != session.StatusAwaitingInput || calls != 2 {
				t.Fatalf("result=%#v calls=%d", result, calls)
			}
			goal, err := engine.store.LoadGoal(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if goal.TokensUsed != tc.total || goal.Status != session.GoalStatusBudgetLimited || goal.BudgetWrapUpRequestedAt == "" || !session.HasBudgetWrapUpRecord(goal) {
				t.Fatalf("goal=%#v", goal)
			}
			events, err := engine.store.LoadEvents(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := map[string]bool{}
			for _, evt := range events {
				if evt.Type == "goal.accounting.updated" && evt.Data["tokens_used_delta"] == float64(tc.total) {
					found["delta"] = true
				}
				if evt.Type == "provider.request.completed" || evt.Type == "turn.stopped" {
					if usage, ok := evt.Data["usage"].(map[string]any); ok && usage["total_tokens"] == float64(tc.total) {
						found[evt.Type] = true
					}
				}
				if evt.Type == "goal.budget_limited" || evt.Type == "goal.budget_wrapup_required" {
					found[evt.Type] = true
				}
			}
			for _, key := range []string{"delta", "provider.request.completed", "turn.stopped", "goal.budget_limited", "goal.budget_wrapup_required"} {
				if !found[key] {
					t.Errorf("missing %s", key)
				}
			}
			report, err := engine.store.ContextReport(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if report.Aggregate.TotalProviderUsage.TotalTokens != tc.total || report.Aggregate.UnknownUsageRequestCount != 1 {
				t.Fatalf("report=%#v", report.Aggregate)
			}
			summary, err := os.ReadFile(filepath.Join(engine.store.SessionDir(meta.ID), "session.md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(summary), fmt.Sprintf("token usage: `%d` / `100`", tc.total)) {
				t.Fatalf("summary mismatch: %s", summary)
			}
			// Restarting the engine reads the durable budget state without replaying usage.
			recovered, err := engine.store.LoadState(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			engine2 := NewEngine(engine.cfg, session.NewStore(engine.cfg.Session.Dir), engine.bus, &runControl{})
			finish := provider.NewFake(func(context.Context, provider.TurnRequest) (provider.TurnResult, error) {
				return provider.TurnResult{Text: "park", StopReason: "done_candidate"}, nil
			})
			_, err = engine2.Run(context.Background(), meta, recovered, "", finish, catalog, registry, hooks)
			if err != nil {
				t.Fatal(err)
			}
			goal, err = engine2.store.LoadGoal(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if goal.TokensUsed != tc.total {
				t.Fatalf("recovery counted old usage twice: %d", goal.TokensUsed)
			}
		})
	}
}

func TestGoalAccountingCanonicalPresence(t *testing.T) {
	zero, negative := int64(0), int64(-1)
	for _, tc := range []struct {
		name  string
		usage provider.Usage
		known bool
	}{
		{"missing", provider.Usage{}, false},
		{"measured zero", provider.Usage{Reported: true, TotalTokens: &zero, TotalTokensSource: "openai_sum"}, true},
		{"invalid negative", provider.Usage{Reported: true, TotalTokens: &negative, TotalTokensSource: "invalid"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, meta, _, _, _, _ := newTestEngine(t, session.ModeExec)
			if _, err := engine.store.CreateGoal(meta.ID, session.GoalDraft{Enabled: true, Objective: "presence", Source: session.GoalSourceCLI}); err != nil {
				t.Fatal(err)
			}
			goal, _, err := engine.updateGoalAccounting(meta.ID, 1, tc.usage, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if goal.TokensUsed != 0 {
				t.Fatalf("invalid count changed usage: %d", goal.TokensUsed)
			}
			events, err := engine.store.LoadEvents(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, evt := range events {
				if evt.Type == "goal.accounting.updated" && evt.Data["token_usage_known"] != tc.known {
					t.Fatalf("wrong presence: %#v", evt.Data)
				}
			}
			history, err := engine.store.LoadGoalHistory(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range history {
				if entry.Type == "goal.accounting.updated" && entry.Data["token_usage_known"] != tc.known {
					t.Fatalf("history lost presence: %#v", entry)
				}
			}
		})
	}
}
