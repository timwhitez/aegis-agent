package webconsole

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersContainRemoteImagesAndReferrers(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("wrapped handler status=%d want %d", recorder.Code, http.StatusNoContent)
	}
	policy := recorder.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"img-src 'self' data: blob:",
		"frame-ancestors 'none'",
		"object-src 'none'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
	} {
		if !strings.Contains(policy, directive) {
			t.Fatalf("Content-Security-Policy missing %q: %q", directive, policy)
		}
	}
	if strings.Contains(policy, "img-src *") || strings.Contains(policy, "img-src http:") || strings.Contains(policy, "img-src https:") {
		t.Fatalf("Content-Security-Policy permits remote image loads: %q", policy)
	}
	if strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("Content-Security-Policy enables inline scripts: %q", policy)
	}
	if got := recorder.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy=%q want no-referrer", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q want nosniff", got)
	}
	if got := recorder.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("Cross-Origin-Resource-Policy=%q want same-origin", got)
	}
}

func TestSecurityHeadersHandleNilHandler(t *testing.T) {
	recorder := httptest.NewRecorder()
	SecurityHeaders(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("nil handler status=%d want %d", recorder.Code, http.StatusNotFound)
	}
	if recorder.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("nil handler response omitted security headers")
	}
}
