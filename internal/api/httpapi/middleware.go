package httpapi

import (
	"net/http"

	"zynthera/internal/shared/httpx"
)

// securityHeaders sets headers for an API that only ever returns JSON.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// limitBody rejects request bodies larger than max bytes.
func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				httpx.WriteError(w, http.StatusRequestEntityTooLarge, "validation_failed", "Request body is too large.", nil)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// originCheck is the CSRF defence for the cookie-authenticated API. A request
// with an unsafe method must come from the web app: its Origin header must
// equal webOrigin, or, when the browser sent no Origin, Sec-Fetch-Site must
// be same-origin.
func originCheck(webOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			ok := origin == webOrigin
			if origin == "" {
				ok = r.Header.Get("Sec-Fetch-Site") == "same-origin"
			}
			if !ok {
				httpx.WriteError(w, http.StatusForbidden, "forbidden", "This request did not come from the Zynthera web app.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
