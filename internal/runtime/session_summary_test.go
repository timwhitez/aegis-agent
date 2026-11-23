package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis-agent/internal/session"
)

func TestSessionSummaryCountsCacheUsageOncePerResponse(t *testing.T) {
	attempt := func(turn int, outcome, responseID string, read, creation int) session.ProviderAttempt {
		return session.ProviderAttempt{
			Turn:                     turn,
			Attempt:                  1,
			Outcome:                  outcome,
			Provider:                 "anthropic",
			Model:                    "test-model",
			ProviderResponseID:       responseID,
			CacheReadInputTokens:     read,
			CacheCreationInputTokens: creation,
			ResponseCommitted:        outcome == "success" || outcome == "max_tokens_resume",
			CreatedAt:                time.Now().UTC().Format(time.RFC3339Nano),
		}
	}
	// withAttempt sets the per-turn attempt number as the engine records it.
	withAttempt := func(item session.ProviderAttempt, attemptNo int) session.ProviderAttempt {
		item.Attempt = attemptNo
		return item
	}
	for _, tc := range []struct {
		name     string
		attempts []session.ProviderAttempt
		want     providerAttemptCacheSummary
	}{
		{
			name:     "plain success",
			attempts: []session.ProviderAttempt{attempt(1, "success", "resp_1", 9, 5)},
			want:     providerAttemptCacheSummary{ReadTokens: 9, CreationTokens: 5, HitAttempts: 1},
		},
		{
			name: "truncated response resumed then completed",
			attempts: []session.ProviderAttempt{
				attempt(1, "success", "resp_partial", 23, 5),
				attempt(1, "max_tokens_resume", "resp_partial", 23, 5),
				attempt(2, "success", "resp_done", 0, 0),
			},
			want: providerAttemptCacheSummary{ReadTokens: 23, CreationTokens: 5, HitAttempts: 1},
		},
		{
			name: "two independent cached responses",
			attempts: []session.ProviderAttempt{
				attempt(1, "success", "resp_a", 13053, 0),
				attempt(2, "success", "resp_b", 13053, 0),
			},
			want: providerAttemptCacheSummary{ReadTokens: 26106, CreationTokens: 0, HitAttempts: 2},
		},
		{
			name: "two consecutive truncations with cache hits",
			attempts: []session.ProviderAttempt{
				attempt(1, "success", "resp_a", 10, 2),
				attempt(1, "max_tokens_resume", "resp_a", 10, 2),
				attempt(2, "success", "resp_b", 7, 0),
				attempt(2, "max_tokens_resume", "resp_b", 7, 0),
				attempt(3, "success", "resp_c", 0, 0),
			},
			want: providerAttemptCacheSummary{ReadTokens: 17, CreationTokens: 2, HitAttempts: 2},
		},
		{
			name: "records without response IDs",
			attempts: []session.ProviderAttempt{
				attempt(1, "success", "", 23, 5),
				attempt(1, "max_tokens_resume", "", 23, 5),
				attempt(2, "success", "", 23, 5),
			},
			want: providerAttemptCacheSummary{ReadTokens: 46, CreationTokens: 10, HitAttempts: 2},
		},
		{
			name: "retry before the resumed response",
			attempts: []session.ProviderAttempt{
				{Turn: 1, Attempt: 1, Outcome: "retry", Provider: "anthropic", Model: "test-model", Retryable: true, Error: "timeout", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
				withAttempt(attempt(1, "success", "resp_retry", 31, 4), 2),
				withAttempt(attempt(1, "max_tokens_resume", "resp_retry", 31, 4), 1),
			},
			want: providerAttemptCacheSummary{ReadTokens: 31, CreationTokens: 4, HitAttempts: 1},
		},
		{
			name: "resume record whose success is outside the ledger",
			attempts: []session.ProviderAttempt{
				attempt(4, "max_tokens_resume", "resp_tail", 8, 1),
				attempt(5, "success", "resp_next", 0, 0),
			},
			want: providerAttemptCacheSummary{ReadTokens: 8, CreationTokens: 1, HitAttempts: 1},
		},
		{
			name: "resume record that does not repeat the success",
			attempts: []session.ProviderAttempt{
				attempt(1, "success", "resp_x", 5, 0),
				attempt(1, "max_tokens_resume", "resp_y", 6, 0),
			},
			want: providerAttemptCacheSummary{ReadTokens: 11, CreationTokens: 0, HitAttempts: 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeProviderAttemptCache(tc.attempts); got != tc.want {
				t.Fatalf("summarizeProviderAttemptCache() = %+v, want %+v", got, tc.want)
			}
			store, meta := newRuntimeTestSession(t)
			for _, item := range tc.attempts {
				if err := store.AppendProviderAttempt(meta.ID, item); err != nil {
					t.Fatalf("append provider attempt: %v", err)
				}
			}
			if err := writeSessionSummary(store, meta.ID); err != nil {
				t.Fatalf("write summary: %v", err)
			}
			summary, err := os.ReadFile(filepath.Join(store.SessionDir(meta.ID), "session.md"))
			if err != nil {
				t.Fatalf("read summary: %v", err)
			}
			want := fmt.Sprintf("cache usage: read=`%d` creation=`%d` hit_attempts=`%d`", tc.want.ReadTokens, tc.want.CreationTokens, tc.want.HitAttempts)
			if !strings.Contains(string(summary), want) {
				t.Fatalf("expected %q in session.md, got:\n%s", want, summary)
			}
			attempts, err := store.LoadProviderAttempts(meta.ID)
			if err != nil {
				t.Fatalf("load attempts: %v", err)
			}
			if len(attempts) != len(tc.attempts) {
				t.Fatalf("summary must not rewrite the audit ledger: got %d records, want %d", len(attempts), len(tc.attempts))
			}
		})
	}
}
