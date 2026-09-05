package webconsole

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"

	"golang.org/x/crypto/bcrypt"
)

// newLocalWebRequest mirrors httptest.NewRequest but replaces the default
// `example.com` Host (used for relative targets) with a loopback Host that
// passes the console host guard, matching how a browser on the loopback
// origin addresses the server. Absolute-URL targets keep their URL Host so
// guard-specific tests can choose untrusted hosts explicitly.
func newLocalWebRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	if req.Host == "example.com" {
		req.Host = "127.0.0.1:3940"
	}
	return req
}

func TestWebHostGuardRejectsUntrustedHostAcrossEntryPoints(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	meta := testSessionMetadata(t, "host_guard_secret_session")
	meta.RootSessionID = meta.ID
	if err := svc.store.Create(meta, testSessionState(session.StatusCompleted)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	entries := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/v2-assets/app.js"},
		{http.MethodGet, "/api/meta"},
		{http.MethodGet, "/api/sessions"},
		{http.MethodGet, "/api/config"},
		{http.MethodGet, "/api/file/read?path=README.md"},
		{http.MethodHead, "/api/sessions"},
		{http.MethodOptions, "/api/sessions"},
		{http.MethodGet, "/ws"},
	}
	for _, entry := range entries {
		t.Run(entry.method+" "+entry.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := newLocalWebRequest(entry.method, entry.path, nil)
			req.Host = "rebind.invalid:3940"
			req.Header.Set("Origin", "http://rebind.invalid:3940")
			svc.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("untrusted Host %s status=%d body=%s", entry.method, recorder.Code, recorder.Body.String())
			}
			if body := recorder.Body.String(); strings.Contains(body, "host_guard_secret_session") || strings.Contains(body, "session_root") {
				t.Fatalf("untrusted Host response leaked data: %s", body)
			}
		})
	}

	recorder := httptest.NewRecorder()
	req := newLocalWebRequest(http.MethodGet, "/ws", nil)
	req.Host = "rebind.invalid:3940"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	svc.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("untrusted Host websocket handshake status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWebHostGuardAllowsLocalAndLiteralHosts(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	for _, host := range []string{
		"localhost:3940",
		"localhost",
		"127.0.0.1:3940",
		"127.0.0.1",
		"[::1]:3940",
		"::1",
		"192.168.10.20:3940",
		"sub.localhost:3940",
	} {
		t.Run(host, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := newLocalWebRequest(http.MethodGet, "/api/meta", nil)
			req.Host = host
			svc.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("legitimate Host %s status=%d body=%s", host, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWebHostGuardHonorsConfiguredAllowlist(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Web.AllowedHosts = []string{"Aegis.Internal.Example", "proxy.corp:8443"}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()
	allowed := []string{"aegis.internal.example:3940", "aegis.internal.example", "proxy.corp:8443"}
	for _, host := range allowed {
		t.Run("allowed "+host, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := newLocalWebRequest(http.MethodGet, "/api/meta", nil)
			req.Host = host
			svc.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("configured Host %s status=%d body=%s", host, recorder.Code, recorder.Body.String())
			}
		})
	}
	rejected := []string{"proxy.corp:9999", "evil.aegis.internal.example", "aegis.internal.example.evil.io", "rebind.invalid:3940"}
	for _, host := range rejected {
		t.Run("rejected "+host, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := newLocalWebRequest(http.MethodGet, "/api/meta", nil)
			req.Host = host
			svc.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("Host %s must stay rejected, got %d body=%s", host, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWebHostGuardRejectsInvalidAllowlistEntries(t *testing.T) {
	for name, entry := range map[string]string{
		"empty":             "  ",
		"scheme":            "https://aegis.example",
		"path":              "aegis.example/console",
		"userinfo":          "operator@aegis.example",
		"wildcard":          "*.example",
		"bad port":          "aegis.example:0",
		"non-numeric port":  "aegis.example:http",
		"unbracketed ipv6":  "::1:8443",
		"space":             "aegis .example",
		"backslash":         `aegis.example\evil`,
		"query":             "aegis.example?x=1",
		"fragment":          "aegis.example#top",
		"port out of range": "aegis.example:70000",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, "")
			cfg.Web.AllowedHosts = []string{entry}
			if _, err := New(cfg, Options{WorkerCount: 0}); err == nil || !strings.Contains(err.Error(), "web.allowed_hosts") {
				t.Fatalf("expected web.allowed_hosts validation failure for %q, got %v", entry, err)
			}
		})
	}
}

func TestWebHostGuardPrecedesBasicAuth(t *testing.T) {
	cfg := testConfig(t, "")
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate password hash: %v", err)
	}
	cfg.Web.BasicAuth = config.WebBasicAuthConfig{Username: "operator", PasswordHash: string(passwordHash)}
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	recorder := httptest.NewRecorder()
	untrusted := newLocalWebRequest(http.MethodGet, "/api/meta", nil)
	untrusted.Host = "rebind.invalid:3940"
	svc.ServeHTTP(recorder, untrusted)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("untrusted Host with auth enabled status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("untrusted Host must not receive an auth challenge, got %q", got)
	}

	authRecorder := httptest.NewRecorder()
	trusted := newLocalWebRequest(http.MethodGet, "/api/meta", nil)
	svc.ServeHTTP(authRecorder, trusted)
	if authRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("trusted Host without credentials status=%d want 401", authRecorder.Code)
	}
}

func TestWebHostGuardKeepsMutationGuards(t *testing.T) {
	cfg := testConfig(t, "")
	svc, err := New(cfg, Options{WorkerCount: 0})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer svc.Close()

	crossOrigin := httptest.NewRecorder()
	crossOriginReq := newLocalWebRequest(http.MethodPost, "/api/config", strings.NewReader(`{"provider":"openai"}`))
	crossOriginReq.Header.Set("Origin", "http://evil.invalid")
	crossOriginReq.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(crossOrigin, crossOriginReq)
	if crossOrigin.Code != http.StatusForbidden || !strings.Contains(crossOrigin.Body.String(), "cross-origin") {
		t.Fatalf("cross-origin mutation on a trusted Host must stay rejected, got %d body=%s", crossOrigin.Code, crossOrigin.Body.String())
	}

	untrustedHost := httptest.NewRecorder()
	untrustedReq := newLocalWebRequest(http.MethodPost, "/api/config", strings.NewReader(`{"provider":"openai"}`))
	untrustedReq.Host = "rebind.invalid:3940"
	untrustedReq.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(untrustedHost, untrustedReq)
	if untrustedHost.Code != http.StatusForbidden || !strings.Contains(untrustedHost.Body.String(), "untrusted Host") {
		t.Fatalf("untrusted Host mutation must be rejected by the host guard, got %d body=%s", untrustedHost.Code, untrustedHost.Body.String())
	}

	allowed := httptest.NewRecorder()
	allowedReq := newLocalWebRequest(http.MethodPost, "/api/workers", strings.NewReader(`{"desired_count":0}`))
	allowedReq.Header.Set(webMutationHeader, "1")
	allowedReq.Header.Set("Content-Type", "application/json")
	svc.ServeHTTP(allowed, allowedReq)
	if allowed.Code != http.StatusAccepted {
		t.Fatalf("same-origin mutation on a trusted Host must keep working, got %d body=%s", allowed.Code, allowed.Body.String())
	}
}
