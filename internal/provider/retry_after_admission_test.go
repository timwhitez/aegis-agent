package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRetryAfterAdmissionStopsLongWait(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, header := range []string{"31", "60", "3600", "9223372036854775807", strings.Repeat("9", 100), time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
			t.Run(fmt.Sprintf("%d/%s", status, header), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Retry-After", header)
					http.Error(w, "busy", status)
				}))
				defer server.Close()
				client := JSONClient{Client: server.Client(), BaseURL: server.URL, Provider: "fixture", Retry: RetryConfig{MaxAttempts: 3, BaseDelay: time.Nanosecond, Retry429: true, Retry5xx: true}}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				retries := 0
				err := client.DoJSON(ctx, http.MethodPost, "/", nil, nil, nil, func(kind string, _ map[string]any) {
					if kind == "provider.retry" {
						retries++
					}
				})
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.Provider != "fixture" || httpErr.StatusCode != status || httpErr.RetryAfter <= maxRetryAfterDelay {
					t.Fatalf("lost upstream error/wait: %v (%#v)", err, httpErr)
				}
				wantClass := "upstream_unavailable"
				if status == http.StatusTooManyRequests {
					wantClass = "rate_limit"
				}
				if httpErr.Class != wantClass || !strings.Contains(err.Error(), "automatic retry not scheduled") || !strings.Contains(err.Error(), "30s") || ctx.Err() != nil || requests.Load() != 1 || retries != 0 {
					t.Fatalf("admission err=%v ctx=%v requests=%d retries=%d", err, ctx.Err(), requests.Load(), retries)
				}
				if httpErr.RetryAfter == time.Duration(1<<63-1) && !strings.Contains(err.Error(), "saturated") {
					t.Fatalf("saturated wait must be labelled: %v", err)
				}
			})
		}
	}
}

func TestRetryAfterAdmissionIncludesJitterAndLocalBackoffInDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		original := &HTTPError{Provider: "fixture", Class: "rate_limit", StatusCode: http.StatusTooManyRequests, RetryAfter: time.Second}
		// The server floor fits; the final plan with a larger local delay does not.
		_, err := admitRetryAfter(ctx, original, 2*time.Second)
		if !errors.Is(err, original) || !strings.Contains(err.Error(), "planned wait=") || ctx.Err() != nil {
			t.Fatalf("must preserve live context and upstream error: %v", err)
		}
		ctxJitter, cancelJitter := context.WithTimeout(context.Background(), time.Second)
		defer cancelJitter()
		_, err = admitRetryAfter(ctxJitter, original, time.Second)
		if !errors.Is(err, original) || !strings.Contains(err.Error(), "caller deadline") || ctxJitter.Err() != nil {
			t.Fatalf("jitter must be included in deadline admission: %v", err)
		}
		cancel()
		if _, err := admitRetryAfter(ctx, original, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("actual cancellation wins over admission: %v", err)
		}
	})
}

func TestRetryAfterAdmissionFallbackAndDisabledRetry(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, header := range []string{"", " ", "0", "-1", "+2", "1.5", "invalid", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), "60"} {
			for _, attempts := range []int{1, 3} {
				t.Run(fmt.Sprintf("%d/%s/%d", status, header, attempts), func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						w.Header().Set("Retry-After", header)
						http.Error(w, "busy", status)
					}))
					defer server.Close()
					retry := RetryConfig{MaxAttempts: attempts, BaseDelay: time.Nanosecond, Retry429: header != "60" || attempts == 1, Retry5xx: header != "60" || attempts == 1}
					client := JSONClient{Client: server.Client(), BaseURL: server.URL, Retry: retry}
					err := client.DoJSON(context.Background(), http.MethodPost, "/", nil, nil, nil, nil)
					var httpErr *HTTPError
					wantRequests := attempts
					if header == "60" {
						wantRequests = 1
					}
					wantClass := "upstream_unavailable"
					if status == http.StatusTooManyRequests {
						wantClass = "rate_limit"
					}
					if !errors.As(err, &httpErr) || httpErr.Class != wantClass || httpErr.StatusCode != status || strings.Contains(err.Error(), "automatic retry not scheduled") || int(requests.Load()) != wantRequests {
						t.Fatalf("fallback: err=%v requests=%d want=%d", err, requests.Load(), wantRequests)
					}
				})
			}
		}
	}
}

func TestRetryAfterAdmissionSharedByAdapters(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "google"} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%s/%d", name, status), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Retry-After", "60")
					http.Error(w, "busy", status)
				}))
				defer server.Close()
				retry := RetryConfig{MaxAttempts: 3, Retry429: true, Retry5xx: true}
				var adapter Adapter
				switch name {
				case "openai":
					adapter = NewOpenAIWithRetry(server.URL, "fixture", server.Client(), retry)
				case "anthropic":
					adapter = NewAnthropicWithRetry(server.URL, "fixture", "2023-06-01", server.Client(), retry)
				case "google":
					adapter = NewGoogleWithRetry(server.URL, "fixture", server.Client(), retry)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err := adapter.RunTurn(ctx, TurnRequest{SessionID: "fixture", Model: "fixture"}, nil)
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.Provider != name || httpErr.StatusCode != status || httpErr.RetryAfter != time.Minute || !strings.Contains(err.Error(), "automatic retry not scheduled") || ctx.Err() != nil || requests.Load() != 1 {
					t.Fatalf("adapter admission: err=%v requests=%d", err, requests.Load())
				}
			})
		}
	}
}

type retryAdmissionTransport func(*http.Request) (*http.Response, error)

func (f retryAdmissionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Virtual time exercises the actual timer/request loop without long real waits.
func TestRetryAfterAdmissionWaitAndCancellation(t *testing.T) {
	for _, floor := range []time.Duration{time.Second, maxRetryAfterDelay} {
		for _, mode := range []string{"retry", "deadline", "cancel", "already_cancelled"} {
			t.Run(fmt.Sprintf("%s/%s", floor, mode), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					requests, retries := 0, 0
					start := time.Now()
					client := JSONClient{BaseURL: "https://fixture.invalid", Provider: "fixture", Retry: RetryConfig{MaxAttempts: 2, BaseDelay: time.Second, Retry429: true}}
					client.Client = &http.Client{Transport: retryAdmissionTransport(func(r *http.Request) (*http.Response, error) {
						if err := r.Context().Err(); err != nil {
							return nil, err
						}
						requests++
						if requests == 2 {
							if elapsed := time.Since(start); elapsed < floor || elapsed > floor+maxRetryAfterJitter {
								t.Fatalf("retry outside floor/jitter bounds: %v", elapsed)
							}
							return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
						}
						return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {strconv.Itoa(int(floor / time.Second))}}, Body: io.NopCloser(strings.NewReader("busy"))}, nil
					})}
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					if mode == "deadline" {
						ctx, cancel = context.WithTimeout(ctx, floor/2)
						defer cancel()
					}
					if mode == "already_cancelled" {
						cancel()
					}
					err := client.DoJSON(ctx, http.MethodPost, "/", nil, nil, nil, func(kind string, _ map[string]any) {
						if kind == "provider.retry" {
							retries++
							if mode == "cancel" {
								go func() { time.Sleep(floor / 2); cancel() }()
							}
						}
					})
					switch mode {
					case "retry":
						if err != nil || requests != 2 || retries != 1 {
							t.Fatalf("retry: err=%v requests=%d retries=%d", err, requests, retries)
						}
					case "deadline":
						var httpErr *HTTPError
						if !errors.As(err, &httpErr) || httpErr.Class != "rate_limit" || !strings.Contains(err.Error(), "caller deadline") || ctx.Err() != nil || requests != 1 || retries != 0 {
							t.Fatalf("deadline admission: err=%v ctx=%v requests=%d retries=%d", err, ctx.Err(), requests, retries)
						}
					case "cancel":
						if !errors.Is(err, context.Canceled) || requests != 1 || retries != 1 || time.Since(start) != floor/2 {
							t.Fatalf("cancel: err=%v requests=%d retries=%d elapsed=%v", err, requests, retries, time.Since(start))
						}
					case "already_cancelled":
						if !errors.Is(err, context.Canceled) || requests != 0 || retries != 0 {
							t.Fatalf("pre-cancel: err=%v requests=%d retries=%d", err, requests, retries)
						}
					}
				})
			})
		}
	}
}
