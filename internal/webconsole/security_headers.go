package webconsole

import "net/http"

const webContentSecurityPolicy = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data: blob:; script-src 'self'; style-src 'self'; connect-src 'self' ws: wss:; font-src 'self'; media-src 'self'; worker-src 'self' blob:"

// SecurityHeaders wraps the complete Web console surface with browser-side
// containment. In particular, img-src prevents untrusted model output from
// causing automatic requests to external or private-network HTTP endpoints,
// even if a future renderer regression emits a remote image element again.
func SecurityHeaders(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		headers.Set("Content-Security-Policy", webContentSecurityPolicy)
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
