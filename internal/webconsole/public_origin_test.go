package webconsole

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// Issue #156: behind an outer proxy that terminates TLS the browser sends
// Origin: https://<public host> while Go sees a plain-HTTP request whose Host
// may also be rewritten. web.public_origins declares those origins; nothing
// else (and no X-Forwarded-* header) widens the same-origin guard.

func publicOriginService(t *testing.T, origins ...string) *Service {
	t.Helper()
	cfg := testConfig(t, "")
	cfg.Web.AllowedHosts = []string{"agent.example"}
	cfg.Web.PublicOrigins = origins
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func postWorkers(svc *Service, host, origin string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := newLocalWebRequest(http.MethodPost, "/api/workers", bytes.NewBufferString(`{"desired_count":0}`))
	request.Host = host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("X-Forwarded-Proto", "https")
	SecurityHeaders(svc).ServeHTTP(recorder, request)
	return recorder
}

func TestPublicOriginsAllowHTTPSProxyMutations(t *testing.T) {
	svc := publicOriginService(t, "https://agent.example")
	for _, tc := range []struct {
		name, host, origin string
		want               int
	}{
		{"preserved Host", "agent.example", "https://agent.example", http.StatusAccepted},
		{"preserved Host with explicit default port", "agent.example", "https://agent.example:443", http.StatusAccepted},
		{"Host rewritten to the upstream address", "127.0.0.1:3940", "https://agent.example", http.StatusAccepted},
		{"origin case is irrelevant", "agent.example", "HTTPS://Agent.Example", http.StatusAccepted},
		{"plain same-origin still works", "agent.example", "http://agent.example", http.StatusAccepted},
		{"undeclared https port", "agent.example", "https://agent.example:8443", http.StatusForbidden},
		{"undeclared host", "agent.example", "https://evil.example", http.StatusForbidden},
		{"lookalike suffix", "agent.example", "https://agent.example.evil", http.StatusForbidden},
		{"opaque origin", "agent.example", "null", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := postWorkers(svc, tc.host, tc.origin)
			if recorder.Code != tc.want {
				t.Fatalf("Host=%s Origin=%s: status %d, want %d (body %s)", tc.host, tc.origin, recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}

func TestWithoutPublicOriginsHTTPSOriginOverPlainHTTPIsRejected(t *testing.T) {
	svc := publicOriginService(t)
	recorder := postWorkers(svc, "agent.example", "https://agent.example")
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "cross-origin") {
		t.Fatalf("X-Forwarded-Proto alone must not widen the guard: status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicOriginsDoNotBypassTheHostGuard(t *testing.T) {
	svc := publicOriginService(t, "https://agent.example")
	recorder := postWorkers(svc, "rebind.invalid", "https://agent.example")
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "untrusted Host") {
		t.Fatalf("an untrusted Host must still be refused before the origin check: status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicOriginsAllowTheWebSocketHandshake(t *testing.T) {
	dial := func(svc *Service, origin string) (int, error) {
		server := httptest.NewServer(svc)
		defer server.Close()
		header := http.Header{}
		header.Set("Origin", origin)
		conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", header)
		if conn != nil {
			conn.Close()
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return status, err
	}
	if status, err := dial(publicOriginService(t, "https://agent.example"), "https://agent.example"); err != nil || status != http.StatusSwitchingProtocols {
		t.Fatalf("declared public origin must upgrade: status %d err %v", status, err)
	}
	if status, err := dial(publicOriginService(t), "https://agent.example"); err == nil || status != http.StatusForbidden {
		t.Fatalf("undeclared https origin must be refused: status %d err %v", status, err)
	}
}

func TestPublicOriginsValidation(t *testing.T) {
	for name, entry := range map[string]string{
		"empty":          " ",
		"no scheme":      "agent.example",
		"other scheme":   "ftp://agent.example",
		"no host":        "https://",
		"user info":      "https://user@agent.example",
		"path":           "https://agent.example/console",
		"query":          "https://agent.example?x=1",
		"empty query":    "https://agent.example?",
		"fragment":       "https://agent.example#top",
		"wildcard":       "https://*.example",
		"zero port":      "https://agent.example:0",
		"port too large": "https://agent.example:70000",
		"bad port":       "https://agent.example:https",
		"space":          "https://agent .example",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, "")
			cfg.Web.PublicOrigins = []string{entry}
			if _, err := New(cfg, Options{WorkerCount: 0}); err == nil || !strings.Contains(err.Error(), "web.public_origins[0]") {
				t.Fatalf("expected web.public_origins validation failure for %q, got %v", entry, err)
			}
		})
	}
	origins, err := parseWebPublicOrigins([]string{"https://Agent.Example:443/", "http://[::1]:8080", "http://proxy.local:80"})
	if err != nil {
		t.Fatalf("valid origins rejected: %v", err)
	}
	for _, want := range []string{"https://agent.example", "http://[::1]:8080", "http://proxy.local"} {
		if _, ok := origins[want]; !ok {
			t.Fatalf("normalized origins %v missing %s", origins, want)
		}
	}
}
