package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type redirectTestTransport func(*http.Request) (*http.Response, error)

func (f redirectTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type redirectTestBody struct{ reads, closes int }

func (b *redirectTestBody) Read(_ []byte) (int, error) {
	b.reads++
	return 0, errors.New("must not read redirect body")
}
func (b *redirectTestBody) Close() error { b.closes++; return nil }

func TestProviderRedirectsNeverReachTargetOrRetry(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for name, target := range map[string]string{
			"cross-host":      "https://sink.invalid/leak?key=DUMMY_SECRET",
			"cross-port":      "https://provider.invalid:444/leak",
			"downgrade":       "http://provider.invalid/leak",
			"same-origin":     "https://provider.invalid/next",
			"relative":        "/next",
			"scheme-relative": "//sink.invalid/leak",
			"malformed":       "https://[DUMMY_SECRET",
		} {
			t.Run(fmt.Sprintf("%d/%s", status, name), func(t *testing.T) {
				requests, retries := 0, 0
				responseBody := &redirectTestBody{}
				responseHeader := http.Header{"Location": []string{target}}
				transport := redirectTestTransport(func(req *http.Request) (*http.Response, error) {
					requests++
					if requests != 1 || req.URL.String() != "https://provider.invalid/start" {
						t.Errorf("redirect destination received request: %s", req.URL)
						return nil, errors.New("unexpected target request")
					}
					for _, key := range []string{"x-api-key", "x-goog-api-key", "Authorization"} {
						if req.Header.Get(key) != "DUMMY_SECRET" {
							t.Errorf("original request lost %s", key)
						}
					}
					payload, err := io.ReadAll(req.Body)
					if err != nil || string(payload) != `{"prompt":"private fixture"}` {
						t.Errorf("payload=%s err=%v", payload, err)
					}
					return &http.Response{StatusCode: status, Header: responseHeader, Body: responseBody, Request: req}, nil
				})
				originalPolicy := func(_ *http.Request, _ []*http.Request) error {
					t.Error("caller policy unexpectedly invoked")
					return nil
				}
				base := &http.Client{Transport: transport, CheckRedirect: originalPolicy, Timeout: time.Second}
				c := JSONClient{Client: base, BaseURL: "https://provider.invalid", Provider: "fixture", Retry: RetryConfig{MaxAttempts: 3, BaseDelay: time.Nanosecond, RetryTransport: true, Retry5xx: true, Retry429: true}}
				err := c.DoJSON(context.Background(), http.MethodPost, "/start", map[string]string{
					"x-api-key": "DUMMY_SECRET", "x-goog-api-key": "DUMMY_SECRET", "Authorization": "DUMMY_SECRET",
				}, map[string]any{"prompt": "private fixture"}, nil, func(kind string, _ map[string]any) {
					if kind == "provider.retry" {
						retries++
					}
				})
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.Class != "invalid_request" || httpErr.StatusCode != status {
					t.Fatalf("expected non-retryable redirect error, got %v", err)
				}
				if requests != 1 || retries != 0 {
					t.Fatalf("requests=%d retries=%d", requests, retries)
				}
				if strings.Contains(err.Error(), "DUMMY_SECRET") || strings.Contains(err.Error(), "private fixture") {
					t.Fatalf("sensitive error: %v", err)
				}
				if responseBody.reads != 0 || responseBody.closes != 1 {
					t.Fatalf("response lifecycle: %+v", responseBody)
				}
				if responseHeader.Get("Location") != target {
					t.Fatal("shared transport response mutated")
				}
				if reflect.ValueOf(base.CheckRedirect).Pointer() != reflect.ValueOf(originalPolicy).Pointer() || base.Timeout != time.Second {
					t.Fatal("caller client mutated")
				}
			})
		}
	}
}

func TestProviderRedirectBlocksRealHTTPReplay(t *testing.T) {
	var sinkCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sinkCalls.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer sink.Close()
	var originalCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originalCalls.Add(1)
		w.Header().Set("Location", sink.URL+"/leak")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	c := JSONClient{BaseURL: origin.URL, Retry: RetryConfig{MaxAttempts: 3, RetryTransport: true}}
	err := c.DoJSON(context.Background(), http.MethodPost, "/start", map[string]string{"x-api-key": "DUMMY_KEY"}, map[string]string{"prompt": "private fixture"}, nil, nil)
	if err == nil || sinkCalls.Load() != 0 || originalCalls.Load() != 1 {
		t.Fatalf("err=%v source=%d target=%d", err, originalCalls.Load(), sinkCalls.Load())
	}
}

func TestProviderRedirectPolicyPreservesSuccessfulConcurrentCalls(t *testing.T) {
	var calls atomic.Int32
	base := &http.Client{Transport: redirectTestTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header), Request: req}, nil
	}), Timeout: time.Second}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out struct {
				OK bool `json:"ok"`
			}
			err := (JSONClient{Client: base, BaseURL: "https://provider.invalid"}).DoJSON(context.Background(), http.MethodPost, "/start", nil, nil, &out, nil)
			if err != nil || !out.OK {
				t.Errorf("response=%+v err=%v", out, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 32 || base.CheckRedirect != nil {
		t.Fatalf("calls=%d or original client changed", calls.Load())
	}
}
