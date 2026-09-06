package api

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/hospitus/hospitus/internal/security"
)

// Middleware Chain (request-execution order, outermost first):
//  1. Recovery (catch panics)
//  2. CORS (cross-origin requests)
//  3. Security Headers (HSTS, CSP, etc.)
//  4. Rate Limiting (prevent DoS)
//  5. Request Body Size Limiting (cap payload size)
//  6. Logging (request/response logging)
//  7. Authentication (API key validation)

// authRateLimiterBurst is the burst size for the per-IP authentication rate
// limiter. Kept tight to resist credential stuffing; shared by the limiter
// constructor and the cleanup sweep so they never drift apart.
const authRateLimiterBurst = 3

// contextKey is a custom type for context keys to avoid collisions.
//
// It carries a name: as a bare struct{} every value of the type compared equal
// to every other, so a second key would have silently read and overwritten the
// first one's value.
type contextKey struct{ name string }

var (
	contextKeyPermissions = contextKey{"permissions"}
	contextKeyAPIKeyID    = contextKey{"api-key-id"}
)

// apiKeyIDFromContext returns the opaque identifier of the key that made the
// request, or "" when no key did.
func apiKeyIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKeyAPIKeyID).(string)
	return id
}

// permissionsFromContext returns the permissions of the key that made the
// request, and whether authentication ran at all.
//
// The two are different: a daemon started with --allow-no-auth has no key and no
// permissions, and must not be treated as a key that holds none.
func permissionsFromContext(ctx context.Context) ([]string, bool) {
	perms, ok := ctx.Value(contextKeyPermissions).([]string)
	return perms, ok
}

// HasPermission checks if the current request has a specific permission.
func HasPermission(r *http.Request, permission string) bool {
	perms, ok := r.Context().Value(contextKeyPermissions).([]string)
	if !ok {
		return false
	}
	return hasPerm(perms, permission)
}

// hasPerm reports whether perms grants required ("*" is a wildcard).
func hasPerm(perms []string, required string) bool {
	for _, p := range perms {
		if p == "*" || p == required {
			return true
		}
	}
	return false
}

// requiredPermission returns the permission a request needs based on its method
// and path. Keys created with the "*" wildcard (the default) satisfy every
// requirement; explicitly scoped keys are constrained.
//
// Classification is by exact path segments, never substrings: a resource name
// that merely starts with "exec" or "console" (e.g. an instance named
// "exec-web") must not change the permission its routes require.
func requiredPermission(method, path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	isAPI := len(segs) >= 3 && segs[0] == "api" && segs[1] == "v1"
	switch {
	case isAPI && segs[2] == "auth":
		// API key management is administrative regardless of method.
		return "admin"
	case isAPI && segs[2] == "instances" && len(segs) >= 5 &&
		(segs[4] == "exec" || segs[4] == "console"):
		// Command execution and interactive consoles on an instance:
		// /api/v1/instances/{id}/exec, /api/v1/instances/{id}/console[/ws].
		return "exec"
	case isAPI && segs[2] == "console":
		// Console session management: /api/v1/console/sessions.
		return "exec"
	case method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions:
		return "read"
	default:
		return "write"
	}
}

// extractClientIP extracts the real client IP from a request.
func (s *Server) extractClientIP(r *http.Request) string {
	// Get the direct connection IP
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	// If no trusted proxies configured, always use direct IP
	if len(s.config.TrustedProxies) == 0 {
		return remoteIP
	}

	// Check if request comes from a trusted proxy
	if s.isTrustedProxy(remoteIP) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// Right to left, not left to right.
			//
			// X-Forwarded-For reads "client, proxy1, proxy2": each hop appends
			// the address it saw, so only the rightmost entries were written by
			// infrastructure we trust. The leftmost is whatever the client sent
			// — a caller who writes "X-Forwarded-For: 1.2.3.4" was recorded as
			// 1.2.3.4, which gave it a fresh rate-limit bucket per request and
			// put a chosen address in the audit log.
			//
			// Walking backwards past our own proxies, the first address that is
			// not one of them is the closest hop we have any reason to believe.
			hops := strings.Split(forwarded, ",")
			for i := len(hops) - 1; i >= 0; i-- {
				hop := strings.TrimSpace(hops[i])
				if net.ParseIP(hop) == nil {
					// A malformed entry ends the chain: everything to its left
					// was written by something that does not follow the format,
					// so none of it can be relied on.
					break
				}
				if !s.isTrustedProxy(hop) {
					return hop
				}
			}
		}
		if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
			if net.ParseIP(realIP) != nil {
				return realIP
			}
		}
	}

	return remoteIP
}

// isTrustedProxy reports whether an address is one of the configured proxies.
func (s *Server) isTrustedProxy(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, trustedCIDR := range s.config.TrustedProxies {
		var ipNet *net.IPNet
		if strings.Contains(trustedCIDR, "/") {
			_, ipNet, _ = net.ParseCIDR(trustedCIDR)
		} else {
			trusted := net.ParseIP(trustedCIDR)
			if trusted == nil {
				continue
			}
			bits := 128
			if trusted.To4() != nil {
				bits = 32
			}
			ipNet = &net.IPNet{IP: trusted, Mask: net.CIDRMask(bits, bits)}
		}
		if ipNet != nil && ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// auditIdentity names the caller in an audit entry without recording the key.
//
// The audit log carried an empty identity everywhere, so several operators
// behind one proxy were indistinguishable in it — which is most of what an
// audit trail is for.
//
// The identifier comes from the authentication that already ran, not from the
// header: it is the key's own ID, stable across restarts and the same string
// the key is listed under, and it tells a reader nothing they could test
// against the key itself. A request that carried no key — a daemon started
// with --allow-no-auth — has no identity, and says so.
func (s *Server) auditIdentity(r *http.Request) string {
	return apiKeyIDFromContext(r.Context())
}

// Handler returns the fully-wrapped HTTP handler (routes plus the middleware
// chain) the production server serves. It is exported so tests — notably the
// security integration suite — can exercise the server through the exact same
// middleware stack.
func (s *Server) Handler() http.Handler {
	return s.withMiddleware(s.mux)
}

// withMiddleware wraps a handler with all middleware in the correct order.
//
// Middleware order matters (wrapping is inside-out; the list below is the
// request-execution order, outermost first):
//   - Recovery is outermost to catch any panics
//   - Security headers and CORS wrap everything else so their headers are
//     present even on the 401/429 responses that auth and rate limiting emit
//   - Rate limiting happens before the request body is read
//   - Request body size limiting prevents DoS via huge payloads
//   - Logging captures the final status
//   - Authentication is innermost, just before the routed handler
func (s *Server) withMiddleware(handler http.Handler) http.Handler {
	// Innermost first.
	if s.config.EnableAuth {
		handler = s.authMiddleware(handler)
	}

	handler = s.loggingMiddleware(handler)
	handler = s.bodySizeLimitMiddleware(handler)

	// Add rate limiting if enabled
	if s.config.EnableRateLimit {
		handler = s.rateLimitMiddleware(handler)
	}

	// Security headers and CORS are applied outside auth and rate limiting so
	// their headers are set before those middlewares can short-circuit a
	// request with 401/429.
	handler = s.securityHeadersMiddleware(handler)
	handler = s.corsMiddleware(handler)

	handler = s.recoveryMiddleware(handler)
	return handler
}

// bodySizeLimitMiddleware limits the request body to prevent DoS via large payloads.
// Streaming WebSocket upgrades are excluded (they don't have a traditional body).
func (s *Server) bodySizeLimitMiddleware(next http.Handler) http.Handler {
	const maxBodyBytes = 32 << 20 // 32 MB
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip body limiting for a real WebSocket handshake only. Testing the
		// header alone let any request opt out by claiming an upgrade it never
		// performs.
		if isWebSocketHandshake(r) {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// isWebSocketHandshake reports whether the request is the GET that opens a
// console stream, rather than one that merely names the header.
func isWebSocketHandshake(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	// Connection is a comma-separated list of tokens.
	hasUpgrade := false
	for _, token := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			hasUpgrade = true
			break
		}
	}
	return hasUpgrade
}

// recoveryMiddleware recovers from panics and returns 500 error.
//
// Why recover from panics?
//   - Prevents entire server from crashing on handler panic
//   - Logs stack trace for debugging
//   - Returns proper HTTP 500 to client
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Recovery is the outermost middleware, so w here is the server's own
		// writer: it cannot read loggingResponseWriter's status. It tracks
		// commitment itself.
		cw := &committedWriter{ResponseWriter: w}
		defer func() {
			if err := recover(); err != nil {
				s.logger.Error("panic recovered in HTTP middleware",
					"panic", err,
					"method", r.Method,
					"path", r.URL.Path,
					"response_committed", cw.committed)
				if cw.committed {
					// The status and some of the body are already on the wire.
					// A 500 JSON document appended here does not replace them:
					// it corrupts a response the client is reading as valid.
					// Dropping the connection is the only honest signal left,
					// and net/http does that for a handler that panics — so
					// re-panic rather than write.
					panic(err)
				}
				s.writeError(cw, http.StatusInternalServerError, "Internal server error")
			}
		}()
		next.ServeHTTP(cw, r)
	})
}

// committedWriter records whether anything has reached the client yet.
type committedWriter struct {
	http.ResponseWriter
	committed bool
}

func (cw *committedWriter) WriteHeader(code int) {
	cw.committed = true
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *committedWriter) Write(b []byte) (int, error) {
	cw.committed = true
	return cw.ResponseWriter.Write(b)
}

func (cw *committedWriter) Flush() {
	cw.committed = true
	if flusher, ok := cw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap keeps http.ResponseController working through this wrapper, as
// loggingResponseWriter does: without it the streaming handlers lose their
// deadline control.
func (cw *committedWriter) Unwrap() http.ResponseWriter {
	return cw.ResponseWriter
}

// Hijack implements http.Hijacker.
//
// Recovery is the outermost middleware, so this wrapper sits underneath
// loggingResponseWriter: without this method that one's Hijack delegates to a
// writer which is not an http.Hijacker, and every WebSocket console upgrade
// fails. A hijacked connection is committed by definition — the handler owns
// the socket and no 500 may be appended to it.
func (cw *committedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := cw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	conn, rw, err := hj.Hijack()
	if err == nil {
		cw.committed = true
	}
	return conn, rw, err
}

// loggingMiddleware logs all HTTP requests with security context.
//
// Each request produces one structured slog record with method, path, status,
// duration, client_ip and auth_status fields.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		clientIP := s.extractClientIP(r)

		// Check authentication status for logging
		authStatus := "PUBLIC"
		if r.URL.Path != "/health" {
			if s.config.EnableAuth {
				// KEY_PRESENT, not AUTHENTICATED: this runs before the key is
				// verified, so every 401 for a wrong key was logged as an
				// authenticated request.
				apiKey := r.Header.Get("X-API-Key")
				if apiKey != "" {
					authStatus = "KEY_PRESENT"
				} else {
					authStatus = "UNAUTHENTICATED"
				}
			} else {
				authStatus = "NO_AUTH_REQUIRED"
			}
		}

		// Wrap ResponseWriter to capture status code
		// Initialize to 0 so WriteHeader can properly capture the actual status code
		lw := &loggingResponseWriter{ResponseWriter: w, statusCode: 0}

		next.ServeHTTP(lw, r)

		duration := time.Since(start)

		// Default to 200 if status code was never explicitly set
		statusCode := lw.statusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		// Enhanced security logging
		s.logger.Info("HTTP request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", statusCode,
			"duration", duration,
			"client_ip", clientIP,
			"auth_status", authStatus,
		)

		// Log security events with audit logging
		if statusCode == http.StatusUnauthorized {
			s.logger.Warn("security authentication failure", "client_ip", clientIP, "method", r.Method, "path", r.URL.Path)
			security.GetGlobalAuditLogger().LogAuthFailure(clientIP, "unauthorized access attempt")
		}
		if !s.config.EnableAuth && r.URL.Path != "/health" {
			// Log no-auth access at WARNING level in server log (not audit-log spam).
			// Operators should enable auth; this is a configuration concern, not per-request CRITICAL.
			s.logger.Warn("no-auth access detected (configure API keys for production)",
				"client_ip", clientIP, "method", r.Method, "path", r.URL.Path, "status", statusCode)
		}
		if statusCode == http.StatusTooManyRequests {
			s.logger.Warn("security rate limit exceeded", "client_ip", clientIP, "method", r.Method, "path", r.URL.Path)
			security.GetGlobalAuditLogger().LogRateLimitExceeded(clientIP, r.URL.Path)
		}
		if statusCode == http.StatusForbidden {
			s.logger.Warn("security forbidden access attempt", "client_ip", clientIP, "method", r.Method, "path", r.URL.Path)
			security.GetGlobalAuditLogger().LogSuspiciousActivity(clientIP, "forbidden access attempt", map[string]interface{}{
				"method": r.Method,
				"path":   r.URL.Path,
				"status": statusCode,
			})
		}
		// Only 401/403/429 are audit-logged: 400/404/405 are normal client
		// errors, not security events.
	})
}

// loggingResponseWriter wraps http.ResponseWriter to capture status code.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	if lw.statusCode == 0 { // Only set once to avoid "superfluous response.WriteHeader" error
		lw.statusCode = code
		lw.ResponseWriter.WriteHeader(code)
	}
}

// Unwrap gives http.ResponseController the writer underneath.
//
// The controller walks the Unwrap chain to find deadline support. Without it
// SetWriteDeadline answered ErrNotSupported for every request through this
// middleware — which is all of them — so the streaming handlers could not
// clear the write deadline, and the configured WriteTimeout still cut off exec
// output, console sessions and image fetches.
func (lw *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return lw.ResponseWriter
}

// Write records the 200 that an unannounced write commits.
//
// The promoted ResponseWriter.Write sends 200 without touching statusCode, so a
// later WriteHeader(401) — which net/http drops as superfluous — was still the
// status the request log and the audit trail recorded. They named a status the
// client never received.
func (lw *loggingResponseWriter) Write(b []byte) (int, error) {
	if lw.statusCode == 0 {
		lw.WriteHeader(http.StatusOK)
	}
	return lw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher for streaming responses
func (lw *loggingResponseWriter) Flush() {
	// A flush commits the response just as a write does.
	if lw.statusCode == 0 {
		lw.WriteHeader(http.StatusOK)
	}
	if flusher, ok := lw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker so connection upgrades (the WebSocket
// console) work through the logging wrapper. Without it the type assertion to
// http.Hijacker in the WebSocket handler fails and the console cannot connect.
func (lw *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := lw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	return hj.Hijack()
}

// corsMiddleware adds CORS headers for cross-origin requests.
//
// Configurable CORS policy
//   - Uses whitelist of allowed origins; "*" is honored only when configured
//     explicitly
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Vary: Origin is required when the response depends on the Origin header
		// (prevents cache poisoning across different origins)
		if len(s.config.AllowedOrigins) > 0 {
			w.Header().Add("Vary", "Origin")
		}

		// Check if origin is allowed
		allowed, header := s.originAllowed(origin)
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", header)
		}

		// Only set CORS headers if origin is allowed
		if allowed {
			// PATCH included: PATCH /api/v1/instances/{id} exists, and a browser
			// refused it at the preflight before ever sending the request.
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		}

		// A preflight, not merely an OPTIONS. Every OPTIONS was short-circuited
		// here, so with no configured origin — or from a client that sends no
		// Origin at all — the answer was 403 and the routed OPTIONS handling,
		// Allow headers included, was unreachable.
		if r.Method == http.MethodOptions &&
			origin != "" && r.Header.Get("Access-Control-Request-Method") != "" {
			if allowed {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// originAllowed reports whether the configured policy admits this origin, and
// what Access-Control-Allow-Origin should say when it does.
//
// Shared with the WebSocket upgrade, which had its own rule — an exact match
// against the request Host — and so refused a browser UI on an origin the rest
// of the API accepts.
func (s *Server) originAllowed(origin string) (allowed bool, header string) {
	for _, allowedOrigin := range s.config.AllowedOrigins {
		if allowedOrigin == "*" {
			return true, "*"
		}
		if allowedOrigin == origin {
			return true, origin
		}
	}
	return false, ""
}

// securityHeadersMiddleware adds security headers to all responses
//
// Security Headers:
//   - Strict-Transport-Security: Force HTTPS (HSTS)
//   - X-Content-Type-Options: Prevent MIME sniffing
//   - X-Frame-Options: Prevent clickjacking
//   - Content-Security-Policy: Restrict resource loading
//   - Referrer-Policy: Control referrer information
//   - Cache-Control: Prevent caching of API responses
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HSTS: Force HTTPS for 1 year (only if TLS is configured)
		if s.config.TLSCert != "" && s.config.TLSKey != "" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}

		// Prevent MIME sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")

		// Content Security Policy - restrict to API only
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

		// Referrer policy - don't leak URLs
		w.Header().Set("Referrer-Policy", "no-referrer")

		// Remove server header to avoid disclosing technology stack
		w.Header().Del("Server")

		// Cache control for API responses
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
		w.Header().Set("Pragma", "no-cache")

		next.ServeHTTP(w, r)
	})
}

// authMiddleware validates API keys
//
// API authentication
// - Checks X-API-Key header
// - Uses constant-time comparison to prevent timing attacks
// - Allows health check endpoint without authentication
// - Records authentication metrics for monitoring
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /health is always public (load balancers, health probes)
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// /metrics and /metrics/prometheus are public only when explicitly enabled
		if s.config.MetricsPublic &&
			(r.URL.Path == "/metrics" || r.URL.Path == "/metrics/prometheus") {
			next.ServeHTTP(w, r)
			return
		}

		clientIP := s.extractClientIP(r)

		// Get API key from header only
		apiKey := r.Header.Get("X-API-Key")

		// The auth rate limiter throttles failed authentication attempts
		// (brute force). It must NOT be consumed by every request, otherwise a
		// legitimate client with a valid key is throttled by its own successful
		// traffic. Overall request throughput is governed by rateLimitMiddleware.
		rejectAuthRateLimited := func() bool {
			if s.getOrCreateAuthRateLimiter(clientIP).Allow() {
				return false
			}
			security.GetGlobalMetrics().RecordRateLimitViolation(clientIP)
			security.GetGlobalAuditLogger().LogRateLimitExceeded(clientIP, r.URL.Path)
			w.Header().Set("Retry-After", "60")
			s.writeError(w, http.StatusTooManyRequests, "Authentication rate limit exceeded")
			return true
		}

		if apiKey == "" {
			if rejectAuthRateLimited() {
				return
			}
			// Record failed authentication attempt
			security.GetGlobalMetrics().RecordAuthAttempt(false, clientIP)
			security.GetGlobalAuditLogger().LogAuthFailure(clientIP, "missing API key")
			s.writeError(w, http.StatusUnauthorized, "Missing API key")
			return
		}

		// A key is verified with bcrypt against every configured key, which is
		// deliberately expensive. Check the limiter first, without spending a
		// token — that happens on failure below — so a flood of wrong keys
		// cannot buy that work unauthenticated.
		// The peek and the spend are both Allow() calls on the same limiter,
		// so a token refilled between them cannot make this answer 429 without
		// having written one.
		//
		// Under a lock, so that requests arriving together cannot all pass the
		// peek and verify in parallel. See authVerifyLocks.
		//
		// In a closure with a deferred unlock, not unlocked on each path: a
		// panic in GetPermissions is caught by the recovery middleware, so the
		// daemon keeps serving — with that shard held for the life of the
		// process, and every client hashing to it blocked on it forever.
		// Only the limiter and the verification are under the lock. Writing
		// the 429 from inside it held one of the 64 shards for as long as the
		// response body took to leave — up to the write deadline against a
		// client that does not read — and every other address hashing to that
		// shard waited behind it.
		verify := func() (perms []string, id string, exists bool, limited bool) {
			verifyLock := authVerifyLock(clientIP)
			verifyLock.Lock()
			defer verifyLock.Unlock()

			if s.authRateLimitExhausted(clientIP) && !s.getOrCreateAuthRateLimiter(clientIP).Allow() {
				return nil, "", false, true
			}

			// One call for the permissions and the identifier, because the
			// bcrypt comparison behind them is the expensive part.
			perms, id, exists = s.authProvider.Authenticate(apiKey)
			if !exists {
				// The token is spent here, under the lock, so the peek above
				// means what it says for the next request.
				limited = !s.getOrCreateAuthRateLimiter(clientIP).Allow()
			}
			return perms, id, exists, limited
		}

		permissions, apiKeyID, keyExists, limited := verify()
		if limited {
			security.GetGlobalMetrics().RecordRateLimitViolation(clientIP)
			security.GetGlobalAuditLogger().LogRateLimitExceeded(clientIP, r.URL.Path)
			w.Header().Set("Retry-After", "60")
			s.writeError(w, http.StatusTooManyRequests, "Authentication rate limit exceeded")
			return
		}
		if !keyExists {
			// Record failed authentication attempt
			security.GetGlobalMetrics().RecordAuthAttempt(false, clientIP)
			security.GetGlobalAuditLogger().LogAuthFailure(clientIP, "invalid API key")
			s.writeError(w, http.StatusUnauthorized, "Invalid API key")
			return
		}

		// Recorded under the key's own identifier, never a digest of the key:
		// an unsalted digest in the audit log and the usage metrics is
		// something an attacker who reads them can test guesses against.
		security.GetGlobalMetrics().RecordAuthAttempt(true, clientIP)
		security.GetGlobalMetrics().RecordAPIKeyUsage(apiKeyID)
		security.GetGlobalAuditLogger().LogAuthSuccess(clientIP, apiKeyID)

		// Enforce authorization: the key must hold the permission the route
		// requires. Without this, any valid key — including one deliberately
		// scoped to "read" — could exec, destroy, import/export or manage keys.
		required := requiredPermission(r.Method, r.URL.Path)
		if !hasPerm(permissions, required) {
			security.GetGlobalAuditLogger().LogAuthFailure(clientIP, "insufficient permission: "+required)
			s.writeError(w, http.StatusForbidden, "Insufficient permissions")
			return
		}

		// Store permissions and the key's identifier in context
		ctx := context.WithValue(r.Context(), contextKeyPermissions, permissions)
		ctx = context.WithValue(ctx, contextKeyAPIKeyID, apiKeyID)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// cleanupRateLimiters periodically removes stale rate limiters
// to prevent memory growth from tracking too many IPs.
func (s *Server) cleanupRateLimiters(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.rateLimiterMu.Lock()
			// Remove limiters that haven't been used recently
			// A limiter with full tokens hasn't been used in a while
			for ip, limiter := range s.rateLimiters {
				// If the limiter has recovered to full burst capacity,
				// it hasn't been used recently and can be removed
				if limiter.Tokens() >= float64(s.config.BurstSize) {
					delete(s.rateLimiters, ip)
				}
			}
			// Also clean up auth rate limiters. Auth limiters use
			// authRateLimiterBurst (not s.config.BurstSize, which is for general
			// traffic); using the wrong constant would leak limiters forever.
			for ip, limiter := range s.authRateLimiters {
				if limiter.Tokens() >= float64(authRateLimiterBurst) {
					delete(s.authRateLimiters, ip)
				}
			}
			s.rateLimiterMu.Unlock()
		}
	}
}

// getOrCreateAuthRateLimiter gets or creates a rate limiter for authentication attempts for the given IP.
func (s *Server) getOrCreateAuthRateLimiter(ip string) *rate.Limiter {
	s.rateLimiterMu.Lock()
	defer s.rateLimiterMu.Unlock()

	limiter, exists := s.authRateLimiters[ip]
	if !exists {
		// Create a rate limiter that allows 5 auth attempts per minute with a
		// burst of authRateLimiterBurst.
		limiter = rate.NewLimiter(
			rate.Limit(5.0/60.0), // 5 attempts per minute
			authRateLimiterBurst, // Burst size: tight to prevent credential stuffing
		)
		s.authRateLimiters[ip] = limiter
	}
	return limiter
}

// authRateLimitExhausted reports whether this client has attempts left, without
// consuming one: a request that is about to succeed must not spend a token.
func (s *Server) authRateLimitExhausted(ip string) bool {
	return s.getOrCreateAuthRateLimiter(ip).Tokens() < 1
}

// authVerifyLocks serializes credential verification per client address.
//
// The limiter was only read before bcrypt and spent after it, so any number of
// requests arriving together read the same remaining token and every one of
// them went on to verify: the limiter bounded sequential attempts and left the
// expensive path unbounded. One caller could buy arbitrary bcrypt work — about
// 100 ms of CPU each — without holding a valid key. Holding a lock across
// peek, verify and spend makes the peek mean what it says.
//
// A fixed array rather than a map keyed by address: the keys arrive from the
// network, and a map would grow with them. Distinct addresses can collide on a
// mutex and wait on each other, which is the price; it also caps concurrent
// verification for the daemon as a whole.
var authVerifyLocks [64]sync.Mutex

func authVerifyLock(ip string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ip))
	return &authVerifyLocks[h.Sum32()%uint32(len(authVerifyLocks))]
}

// rateLimitMiddleware implements per-IP rate limiting
//
// - Limits requests per IP address
// - Configurable rate and burst size
// - Uses token bucket algorithm
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.extractClientIP(r)

		// Get or create rate limiter for this IP
		s.rateLimiterMu.RLock()
		limiter, exists := s.rateLimiters[ip]
		s.rateLimiterMu.RUnlock()

		if !exists {
			s.rateLimiterMu.Lock()
			// Double-check after acquiring write lock
			if limiter, exists = s.rateLimiters[ip]; !exists {
				limiter = rate.NewLimiter(
					rate.Limit(s.config.RequestsPerSecond),
					s.config.BurstSize,
				)
				s.rateLimiters[ip] = limiter
			}
			s.rateLimiterMu.Unlock()
		}

		// Check if request is allowed
		if !limiter.Allow() {
			security.GetGlobalMetrics().RecordRateLimitViolation(ip)
			security.GetGlobalMetrics().RecordRequest(true) // Blocked request
			security.GetGlobalAuditLogger().LogRateLimitExceeded(ip, r.URL.Path)

			w.Header().Set("Retry-After", "1")
			s.writeError(w, http.StatusTooManyRequests, "Rate limit exceeded")
			return
		}

		// Record allowed request
		security.GetGlobalMetrics().RecordRequest(false)

		next.ServeHTTP(w, r)
	})
}
