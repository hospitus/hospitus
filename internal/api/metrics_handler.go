package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/provider"
)

// promLabelEscaper escapes a Prometheus label value per the text exposition
// format: backslash, double-quote and newline. Go's %q is not a correct
// substitute because it applies additional Go-specific escapes.
var promLabelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// handleMetrics returns security and operational metrics.
//
// SECURITY NOTE: This endpoint should be protected in production
// as it may expose sensitive information about API usage patterns.
//
// Endpoint: GET /metrics
// Response: JSON containing all security metrics
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	metrics := s.getMetrics()
	s.writeJSON(w, http.StatusOK, metrics)
}

// handlePrometheusMetrics returns metrics in Prometheus format.
//
// Endpoint: GET /metrics/prometheus
// Response: Plain text in Prometheus exposition format
//
// Includes:
//   - Security metrics (auth, rate limiting, suspicious activity)
//   - Jail metrics (CPU, memory, disk, network per jail)
func (s *Server) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	secMetrics := security.GetGlobalMetrics()

	// Authentication metrics
	authMetrics := secMetrics.GetAuthMetrics()
	rateLimitMetrics := secMetrics.GetRateLimitMetrics()
	apiKeyMetrics := secMetrics.GetAPIKeyMetrics()
	suspMetrics := secMetrics.GetSuspiciousActivityMetrics()
	secEventMetrics := secMetrics.GetSecurityEventMetrics()
	reqMetrics := secMetrics.GetRequestMetrics()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	// Write security Prometheus metrics
	fmt.Fprintf(w, "# HELP hospitus_auth_attempts_total Total authentication attempts\n")
	fmt.Fprintf(w, "# TYPE hospitus_auth_attempts_total counter\n")
	fmt.Fprintf(w, "hospitus_auth_attempts_total %d\n", authMetrics.AttemptsTotal)

	fmt.Fprintf(w, "# HELP hospitus_auth_success_total Successful authentications\n")
	fmt.Fprintf(w, "# TYPE hospitus_auth_success_total counter\n")
	fmt.Fprintf(w, "hospitus_auth_success_total %d\n", authMetrics.SuccessTotal)

	fmt.Fprintf(w, "# HELP hospitus_auth_failures_total Failed authentications\n")
	fmt.Fprintf(w, "# TYPE hospitus_auth_failures_total counter\n")
	fmt.Fprintf(w, "hospitus_auth_failures_total %d\n", authMetrics.FailuresTotal)

	fmt.Fprintf(w, "# HELP hospitus_auth_success_rate Authentication success rate\n")
	fmt.Fprintf(w, "# TYPE hospitus_auth_success_rate gauge\n")
	fmt.Fprintf(w, "hospitus_auth_success_rate %.2f\n", authMetrics.SuccessRate)

	fmt.Fprintf(w, "# HELP hospitus_rate_limit_violations_total Rate limit violations\n")
	fmt.Fprintf(w, "# TYPE hospitus_rate_limit_violations_total counter\n")
	fmt.Fprintf(w, "hospitus_rate_limit_violations_total %d\n", rateLimitMetrics.ViolationsTotal)

	fmt.Fprintf(w, "# HELP hospitus_api_key_usage_total API key usage count\n")
	fmt.Fprintf(w, "# TYPE hospitus_api_key_usage_total counter\n")
	fmt.Fprintf(w, "hospitus_api_key_usage_total %d\n", apiKeyMetrics.UsageTotal)

	fmt.Fprintf(w, "# HELP hospitus_suspicious_activity_total Suspicious activity detections\n")
	fmt.Fprintf(w, "# TYPE hospitus_suspicious_activity_total counter\n")
	fmt.Fprintf(w, "hospitus_suspicious_activity_total %d\n", suspMetrics.ActivityTotal)

	fmt.Fprintf(w, "# HELP hospitus_security_events_total Security events\n")
	fmt.Fprintf(w, "# TYPE hospitus_security_events_total counter\n")
	fmt.Fprintf(w, "hospitus_security_events_total %d\n", secEventMetrics.EventsTotal)

	fmt.Fprintf(w, "# HELP hospitus_security_seconds_since_last_event Seconds since last security event\n")
	fmt.Fprintf(w, "# TYPE hospitus_security_seconds_since_last_event gauge\n")
	fmt.Fprintf(w, "hospitus_security_seconds_since_last_event %.2f\n", secEventMetrics.SecondsSinceEvent)

	fmt.Fprintf(w, "# HELP hospitus_requests_total Total HTTP requests\n")
	fmt.Fprintf(w, "# TYPE hospitus_requests_total counter\n")
	fmt.Fprintf(w, "hospitus_requests_total %d\n", reqMetrics.RequestsTotal)

	fmt.Fprintf(w, "# HELP hospitus_requests_blocked_total Blocked HTTP requests\n")
	fmt.Fprintf(w, "# TYPE hospitus_requests_blocked_total counter\n")
	fmt.Fprintf(w, "hospitus_requests_blocked_total %d\n", reqMetrics.BlockedRequestsTotal)

	fmt.Fprintf(w, "# HELP hospitus_requests_block_rate Request block rate percentage\n")
	fmt.Fprintf(w, "# TYPE hospitus_requests_block_rate gauge\n")
	fmt.Fprintf(w, "hospitus_requests_block_rate %.2f\n", reqMetrics.BlockRate)

	// Add jail metrics if jail provider is available
	s.writeJailPrometheusMetrics(w, r)
}

// getMetrics returns all metrics as a structured JSON object.
func (s *Server) getMetrics() map[string]interface{} {
	secMetrics := security.GetGlobalMetrics()

	return map[string]interface{}{
		"authentication":      secMetrics.GetAuthMetrics(),
		"rate_limiting":       secMetrics.GetRateLimitMetrics(),
		"api_keys":            secMetrics.GetAPIKeyMetrics(),
		"suspicious_activity": secMetrics.GetSuspiciousActivityMetrics(),
		"security_events":     secMetrics.GetSecurityEventMetrics(),
		"requests":            secMetrics.GetRequestMetrics(),
	}
}

// handleSecurityStatus returns security status and health.
//
// Endpoint: GET /security/status
// Response: JSON with security health information
func (s *Server) handleSecurityStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	secMetrics := security.GetGlobalMetrics()
	authMetrics := secMetrics.GetAuthMetrics()
	reqMetrics := secMetrics.GetRequestMetrics()
	secEventMetrics := secMetrics.GetSecurityEventMetrics()

	// Calculate security health score (0-100)
	score := s.calculateSecurityScore(authMetrics, reqMetrics, secEventMetrics)

	status := map[string]interface{}{
		"score":  score,
		"status": s.getSecurityStatusLevel(score),
		"checks": map[string]interface{}{
			"authentication_enabled": s.config.EnableAuth,
			"tls_enabled":            s.config.TLSCert != "" && s.config.TLSKey != "",
			"rate_limiting_enabled":  s.config.EnableRateLimit,
			"auth_success_rate":      authMetrics.SuccessRate,
			"request_block_rate":     reqMetrics.BlockRate,
			"seconds_since_incident": secEventMetrics.SecondsSinceEvent,
		},
		"recommendations": s.getSecurityRecommendations(),
	}

	s.writeJSON(w, http.StatusOK, status)
}

// calculateSecurityScore calculates an overall security health score.
func (s *Server) calculateSecurityScore(
	authMetrics security.AuthMetrics,
	reqMetrics security.RequestMetrics,
	secEventMetrics security.SecurityEventMetrics,
) int {
	score := 100

	// Deduct points for disabled security features
	if !s.config.EnableAuth {
		score -= 30
	}
	if s.config.TLSCert == "" || s.config.TLSKey == "" {
		score -= 20
	}
	if !s.config.EnableRateLimit {
		score -= 10
	}

	// Deduct points for poor metrics
	if authMetrics.SuccessRate < 90 && authMetrics.AttemptsTotal > 10 {
		score -= 10
	}
	if reqMetrics.BlockRate > 10 && reqMetrics.RequestsTotal > 100 {
		score -= 10
	}

	// Deduct points for recent security events (only when an event has actually
	// occurred; SecondsSinceEvent is 0 when EventsTotal is 0).
	if secEventMetrics.EventsTotal > 0 && secEventMetrics.SecondsSinceEvent < 300 { // Last 5 minutes
		score -= 5
	}

	if score < 0 {
		score = 0
	}

	return score
}

// getSecurityStatusLevel returns a security status level based on score.
func (s *Server) getSecurityStatusLevel(score int) string {
	switch {
	case score >= 90:
		return "EXCELLENT"
	case score >= 75:
		return "GOOD"
	case score >= 60:
		return "FAIR"
	case score >= 40:
		return "POOR"
	default:
		return "CRITICAL"
	}
}

// getSecurityRecommendations returns security recommendations based on config.
func (s *Server) getSecurityRecommendations() []string {
	recommendations := []string{}

	if !s.config.EnableAuth {
		recommendations = append(recommendations, "Enable authentication for production use")
	}

	if s.config.TLSCert == "" || s.config.TLSKey == "" {
		recommendations = append(recommendations, "Enable TLS/HTTPS for encrypted communications")
	}

	if !s.config.EnableRateLimit {
		recommendations = append(recommendations, "Enable rate limiting to prevent DoS attacks")
	}

	if len(s.config.AllowedOrigins) == 1 && s.config.AllowedOrigins[0] == "*" {
		recommendations = append(recommendations, "Restrict CORS to specific trusted origins")
	}

	if len(recommendations) == 0 {
		recommendations = append(recommendations, "Security configuration looks good!")
	}

	return recommendations
}

// writeJailPrometheusMetrics writes jail metrics in Prometheus format
func (s *Server) writeJailPrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	// Get jail provider from registry
	jailProv, err := s.registry.Get("jail")
	if err != nil {
		// Jail provider not available, skip jail metrics
		return
	}

	ctx := r.Context()

	// List instances and collect metrics
	instances, err := jailProv.ListInstances(ctx, provider.InstanceFilter{})
	if err != nil {
		fmt.Fprintf(w, "# Error listing instances: %v\n", err)
		return
	}

	if len(instances) == 0 {
		return
	}

	// Write instance count metric
	fmt.Fprintf(w, "\n# HELP hospitus_jails_total Total number of jails\n")
	fmt.Fprintf(w, "# TYPE hospitus_jails_total gauge\n")
	fmt.Fprintf(w, "hospitus_jails_total %d\n", len(instances))

	// Get state for each instance and count running
	running := 0
	type instanceInfo struct {
		ID     string
		Status string
	}
	var infos []instanceInfo

	for _, inst := range instances {
		state, err := jailProv.GetInstanceState(ctx, inst)
		status := "unknown"
		if err == nil {
			status = string(state)
			if state == provider.StateRunning {
				running++
			}
		}
		infos = append(infos, instanceInfo{ID: inst.ID, Status: status})
	}

	fmt.Fprintf(w, "\n# HELP hospitus_jails_running Number of running jails\n")
	fmt.Fprintf(w, "# TYPE hospitus_jails_running gauge\n")
	fmt.Fprintf(w, "hospitus_jails_running %d\n", running)

	// Write per-jail info metric
	fmt.Fprintf(w, "\n# HELP hospitus_jail_info Information about jails\n")
	fmt.Fprintf(w, "# TYPE hospitus_jail_info gauge\n")
	for _, info := range infos {
		isRunning := 0
		if info.Status == string(provider.StateRunning) {
			isRunning = 1
		}
		fmt.Fprintf(w, "hospitus_jail_info{name=\"%s\",status=\"%s\"} %d\n",
			promLabelEscaper.Replace(info.ID), promLabelEscaper.Replace(info.Status), isRunning)
	}
}
