package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aegis-agent/internal/provider"
	"aegis-agent/internal/session"
)

func TestEngineRetryAfterAdmissionDoesNotAutoResume(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, seconds := range []int{30, 60} {
			t.Run(strconv.Itoa(status)+"/"+strconv.Itoa(seconds), func(t *testing.T) {
				engine, meta, state, registry, hookManager, catalog := newTestEngine(t, session.ModeExec)
				engine.cfg.Runtime.ProviderAutoResume.Enabled = true
				engine.cfg.Runtime.ProviderAutoResume.MaxAttempts = 2
				if err := engine.store.AppendMessage(meta.ID, session.NewMessage("user", "hello")); err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Retry-After", strconv.Itoa(seconds))
					http.Error(w, "busy", status)
				}))
				defer server.Close()
				adapter := provider.NewOpenAIWithRetry(server.URL, "fixture", server.Client(), provider.RetryConfig{MaxAttempts: 3, Retry429: true, Retry5xx: true})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, err := engine.Run(ctx, meta, state, "", adapter, catalog, registry, hookManager)
				var httpErr *provider.HTTPError
				wantClass := "upstream_unavailable"
				if status == http.StatusTooManyRequests {
					wantClass = "rate_limit"
				}
				if !errors.As(err, &httpErr) || httpErr.Class != wantClass || httpErr.StatusCode != status || httpErr.RetryAfter != time.Duration(seconds)*time.Second || !strings.Contains(err.Error(), "automatic retry not scheduled") || result.Status != session.StatusFailed || ctx.Err() != nil || requests.Load() != 1 {
					t.Fatalf("run result=%#v err=%v requests=%d", result, err, requests.Load())
				}
				events, err := loadEvents(engine.store, meta.ID)
				if err != nil {
					t.Fatal(err)
				}
				if hasEventType(events, "provider.auto_resume") || hasEventType(events, "provider.retry") || countEventType(events, "provider.call") != 1 || countEventType(events, "provider.request.failed") != 1 {
					t.Fatalf("unexpected request lifecycle: %#v", events)
				}
				attempts, err := engine.store.LoadProviderAttempts(meta.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(attempts) != 1 || attempts[0].Attempt != int(requests.Load()) || attempts[0].Outcome != "failure" || attempts[0].ErrorClass != wantClass || attempts[0].StatusCode != status {
					t.Fatalf("attempt ledger differs from network: %#v", attempts)
				}
			})
		}
	}
}
