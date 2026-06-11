package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authExempt lists the operational endpoints that bypass token authentication:
// liveness/readiness probes (load balancers and orchestrators cannot easily
// attach credentials) and the self-metrics endpoint (scraped by Prometheus;
// it exposes process health, not graph data). Every data endpoint requires a
// token.
var authExempt = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
	"/metrics": true,
}

// WithAuth wraps next with static Bearer-token authentication. A request must
// carry "Authorization: Bearer <token>" where <token> matches one of tokens;
// comparison is constant-time per candidate. Requests to the exempt
// operational endpoints pass through untouched.
//
// WithAuth panics when tokens is empty: an accidentally empty token set must
// never silently produce an open server. Callers that intend to run without
// authentication must not wrap the handler at all (and should have to say so
// explicitly, e.g. via an -insecure-no-auth flag).
func WithAuth(next http.Handler, tokens []string) http.Handler {
	if len(tokens) == 0 {
		panic("api.WithAuth: empty token set (refusing to construct an open authenticated server)")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authExempt[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		presented, ok := bearerToken(r)
		if !ok || !tokenMatches(presented, tokens) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="promhash-api"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header. The scheme comparison is case-insensitive per RFC 7235.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return h[len(prefix):], true
}

// tokenMatches reports whether presented equals any configured token, using a
// constant-time comparison for each candidate so timing does not reveal how
// much of a token matched.
func tokenMatches(presented string, tokens []string) bool {
	p := []byte(presented)
	matched := false
	for _, t := range tokens {
		if subtle.ConstantTimeCompare(p, []byte(t)) == 1 {
			matched = true
		}
	}
	return matched
}
