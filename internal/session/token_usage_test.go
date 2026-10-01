package session

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"aegis-agent/internal/events"
)

func TestTokenSumAndGoalAccountingSaturate(t *testing.T) {
	for _, tc := range []struct {
		values []int64
		want   int64
		valid  bool
	}{{[]int64{1, 99, 1}, 101, true}, {[]int64{math.MaxInt64, 1}, math.MaxInt64, true}, {[]int64{math.MaxInt64, 1, -1}, 0, false}} {
		total, valid := TokenSum(tc.values...)
		if total != tc.want || valid != tc.valid {
			t.Fatalf("sum %v=(%d,%v)", tc.values, total, valid)
		}
	}
	store := NewStore(t.TempDir())
	meta := SessionMetadata{SchemaVersion: 1, ID: "usage-saturate", CreatedAt: contextReportTestTime, Workdir: t.TempDir(), Mode: ModeExec, Provider: "fake", Model: "fixture", CompletionPolicy: CompletionPolicyAutonomous}
	createContextReportTestSession(t, store, meta)
	cap := int64(math.MaxInt64)
	if _, err := store.CreateGoal(meta.ID, GoalDraft{Enabled: true, Objective: "usage", TokenBudget: &cap, StopOnBudget: true, Source: GoalSourceCLI}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpdateGoalAccounting(meta.ID, GoalUsageDelta{TokensUsedDelta: math.MaxInt64 - 1}); err != nil {
		t.Fatal(err)
	}
	goal, limited, err := store.UpdateGoalAccounting(meta.ID, GoalUsageDelta{TokensUsedDelta: 100})
	if err != nil {
		t.Fatal(err)
	}
	if goal.TokensUsed != math.MaxInt64 || !limited || goal.Status != GoalStatusBudgetLimited {
		t.Fatalf("goal=%#v limited=%v", goal, limited)
	}
}

func TestContextCanonicalUsageLegacyAndDurability(t *testing.T) {
	for _, tc := range []struct {
		api   string
		want  int64
		known bool
	}{{"anthropic-compatible", 101, true}, {"openai-compatible", 2, true}, {"google", 0, false}, {"custom-private-profile", 0, false}} {
		t.Run(tc.api, func(t *testing.T) {
			store := NewStore(t.TempDir())
			meta := SessionMetadata{SchemaVersion: 1, ID: "legacy-usage", CreatedAt: contextReportTestTime, Workdir: t.TempDir(), Mode: ModeExec, Provider: "custom-private-profile", Model: "fixture", CompletionPolicy: CompletionPolicyAutonomous, ProviderOptions: ProviderOptions{APIProvider: tc.api}}
			createContextReportTestSession(t, store, meta)
			if _, err := store.CreateGoal(meta.ID, GoalDraft{Enabled: true, Objective: "old subtotal", Source: GoalSourceCLI}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.UpdateGoalAccounting(meta.ID, GoalUsageDelta{TokensUsedDelta: 2}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "goal.json"))
			if err != nil {
				t.Fatal(err)
			}
			appendContextReportEvents(t, store, meta.ID, []events.Event{contextReportEvent("legacy", meta.ID, "turn.stopped", contextReportTestTime, map[string]any{"request_id": "legacy:1:main:0", "request_kind": "main", "turn": 1, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "cache_read_input_tokens": 99}})})
			report, err := store.ContextReport(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			usage := report.Sessions[0].Requests[0].Usage
			if (usage.TotalTokens != nil) != tc.known || report.Aggregate.TotalProviderUsage.TotalTokens != tc.want {
				t.Fatalf("usage=%#v totals=%#v", usage, report.Aggregate.TotalProviderUsage)
			}
			if tc.known && usage.TotalTokensSource != "legacy_inferred" {
				t.Fatalf("source=%s", usage.TotalTokensSource)
			}
			if !tc.known && (usage.TotalTokensSource != "legacy_incomplete" || report.Aggregate.TotalProviderUsage.IncompleteRequestCount != 1) {
				t.Fatalf("incomplete historical usage=%#v", usage)
			}
			after, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "goal.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("report silently recalculated persisted Goal")
			}
			// Counts above float64's exact range must remain exact after JSONL reload.
			const precise = int64(9007199254740993)
			appendContextReportEvents(t, store, meta.ID, []events.Event{contextReportEvent("precise", meta.ID, "provider.request.completed", contextReportTestTime, map[string]any{"request_id": "legacy:precise:main:0", "request_kind": "main", "usage": map[string]any{"reported": true, "total_tokens": precise, "total_tokens_source": "anthropic_sum"}})})
			exact, err := NewStore(store.root).ContextReport(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if exact.Aggregate.TotalProviderUsage.TotalTokens != precise+tc.want {
				t.Fatalf("lost integer precision: %d", exact.Aggregate.TotalProviderUsage.TotalTokens)
			}
			appendContextReportEvents(t, store, meta.ID, []events.Event{contextReportEvent("canonical", meta.ID, "provider.request.completed", contextReportTestTime, map[string]any{"request_id": "legacy:2:main:0", "request_kind": "main", "turn": 2, "usage": map[string]any{"reported": true, "total_tokens": int64(math.MaxInt64), "total_tokens_source": "anthropic_sum"}})})
			report, err = NewStore(store.root).ContextReport(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if report.Aggregate.TotalProviderUsage.TotalTokens != math.MaxInt64 {
				t.Fatalf("roundtrip/saturation lost total: %#v", report.Aggregate.TotalProviderUsage)
			}
		})
	}
}

func TestContextLegacyUsageRejectsNegativeRawCounters(t *testing.T) {
	for _, api := range []string{"openai-compatible", "anthropic-compatible"} {
		for _, field := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			t.Run(api+"/"+field, func(t *testing.T) {
				data := map[string]any{"reported": true, "input_tokens": 1, "output_tokens": 1}
				data[field] = -1
				usage := contextProviderUsage(data, api)
				if usage.TotalTokens != nil || usage.TotalTokensSource != "invalid" {
					t.Fatalf("negative raw counter counted as canonical: %#v", usage)
				}
				var total ContextUsageTotals
				addContextUsage(&total, usage)
				if total.TotalTokens != 0 || total.IncompleteRequestCount != 1 {
					t.Fatalf("invalid usage aggregated as complete: %#v", total)
				}
			})
		}
	}
}
