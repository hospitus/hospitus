// Package security provides security monitoring and metrics for HOSPITUS.
//
// This package tracks security-relevant events and provides metrics for:
//   - Authentication attempts (successful/failed)
//   - Rate limiting violations
//   - API key usage patterns
//   - Suspicious activity detection
package security

import (
	"sync"
	"sync/atomic"
	"time"
)

// Metrics counts what the security layer sees: authentication attempts, rate
// limit violations, API key use and suspicious requests.
//
// The counters are atomic and the per-IP maps are guarded, so handlers update
// them from any goroutine without coordinating.
type Metrics struct {
	// Authentication metrics
	authAttemptsTotal    uint64            // Total authentication attempts
	authSuccessTotal     uint64            // Successful authentications
	authFailuresTotal    uint64            // Failed authentications
	authFailuresByIP     map[string]uint64 // Failed auth attempts by IP
	authFailuresByIPLock sync.RWMutex

	// Rate limiting metrics
	rateLimitViolationsTotal    uint64            // Total rate limit violations
	rateLimitViolationsByIP     map[string]uint64 // Rate limit violations by IP
	rateLimitViolationsByIPLock sync.RWMutex

	// API key usage metrics
	apiKeyUsageTotal     uint64            // Total API key uses
	apiKeyUsageByKey     map[string]uint64 // Usage count by key ID
	apiKeyUsageByKeyLock sync.RWMutex

	// Suspicious activity metrics
	suspiciousActivityTotal uint64            // Total suspicious activities
	suspiciousActivityByIP  map[string]uint64 // Suspicious activity by IP
	suspiciousActivityLock  sync.RWMutex

	// Security events
	securityEventsTotal uint64 // Total security events
	lastSecurityEvent   time.Time
	lastSecurityEventMu sync.RWMutex

	// Request metrics
	requestsTotal        uint64 // Total requests
	blockedRequestsTotal uint64 // Blocked requests
}

// NewMetrics creates a new security metrics tracker.
func NewMetrics() *Metrics {
	return &Metrics{
		authFailuresByIP:        make(map[string]uint64),
		rateLimitViolationsByIP: make(map[string]uint64),
		apiKeyUsageByKey:        make(map[string]uint64),
		suspiciousActivityByIP:  make(map[string]uint64),
	}
}

// RecordAuthAttempt records an authentication attempt.
func (m *Metrics) RecordAuthAttempt(success bool, ip string) {
	atomic.AddUint64(&m.authAttemptsTotal, 1)

	if success {
		atomic.AddUint64(&m.authSuccessTotal, 1)
	} else {
		atomic.AddUint64(&m.authFailuresTotal, 1)

		// Track failures by IP and read the updated count under the same lock,
		// so the suspicious-pattern check reflects this increment exactly.
		m.authFailuresByIPLock.Lock()
		m.authFailuresByIP[ip]++
		failures := m.authFailuresByIP[ip]
		m.authFailuresByIPLock.Unlock()

		if failures >= 5 {
			m.RecordSuspiciousActivity(ip)
		}
	}
}

// RecordRateLimitViolation records a rate limit violation.
func (m *Metrics) RecordRateLimitViolation(ip string) {
	atomic.AddUint64(&m.rateLimitViolationsTotal, 1)

	m.rateLimitViolationsByIPLock.Lock()
	m.rateLimitViolationsByIP[ip]++
	m.rateLimitViolationsByIPLock.Unlock()

	m.RecordSecurityEvent()
}

// RecordAPIKeyUsage records API key usage.
func (m *Metrics) RecordAPIKeyUsage(keyID string) {
	atomic.AddUint64(&m.apiKeyUsageTotal, 1)

	m.apiKeyUsageByKeyLock.Lock()
	m.apiKeyUsageByKey[keyID]++
	m.apiKeyUsageByKeyLock.Unlock()
}

// RecordSuspiciousActivity records suspicious activity.
func (m *Metrics) RecordSuspiciousActivity(ip string) {
	atomic.AddUint64(&m.suspiciousActivityTotal, 1)

	m.suspiciousActivityLock.Lock()
	m.suspiciousActivityByIP[ip]++
	m.suspiciousActivityLock.Unlock()

	m.RecordSecurityEvent()
}

// RecordSecurityEvent records a generic security event.
func (m *Metrics) RecordSecurityEvent() {
	atomic.AddUint64(&m.securityEventsTotal, 1)

	m.lastSecurityEventMu.Lock()
	m.lastSecurityEvent = time.Now()
	m.lastSecurityEventMu.Unlock()
}

// RecordRequest records a request.
func (m *Metrics) RecordRequest(blocked bool) {
	atomic.AddUint64(&m.requestsTotal, 1)
	if blocked {
		atomic.AddUint64(&m.blockedRequestsTotal, 1)
	}
}

// GetAuthMetrics returns authentication metrics.
func (m *Metrics) GetAuthMetrics() AuthMetrics {
	return AuthMetrics{
		AttemptsTotal: atomic.LoadUint64(&m.authAttemptsTotal),
		SuccessTotal:  atomic.LoadUint64(&m.authSuccessTotal),
		FailuresTotal: atomic.LoadUint64(&m.authFailuresTotal),
		FailuresByIP:  m.getAuthFailuresByIP(),
		SuccessRate:   m.calculateAuthSuccessRate(),
	}
}

// GetRateLimitMetrics returns rate limiting metrics.
func (m *Metrics) GetRateLimitMetrics() RateLimitMetrics {
	return RateLimitMetrics{
		ViolationsTotal: atomic.LoadUint64(&m.rateLimitViolationsTotal),
		ViolationsByIP:  m.getRateLimitViolationsByIP(),
	}
}

// GetAPIKeyMetrics returns API key usage metrics.
func (m *Metrics) GetAPIKeyMetrics() APIKeyMetrics {
	return APIKeyMetrics{
		UsageTotal: atomic.LoadUint64(&m.apiKeyUsageTotal),
		UsageByKey: m.getAPIKeyUsageByKey(),
	}
}

// GetSuspiciousActivityMetrics returns suspicious activity metrics.
func (m *Metrics) GetSuspiciousActivityMetrics() SuspiciousActivityMetrics {
	return SuspiciousActivityMetrics{
		ActivityTotal: atomic.LoadUint64(&m.suspiciousActivityTotal),
		ActivityByIP:  m.getSuspiciousActivityByIP(),
	}
}

// GetSecurityEventMetrics returns security event metrics.
func (m *Metrics) GetSecurityEventMetrics() SecurityEventMetrics {
	m.lastSecurityEventMu.RLock()
	lastEvent := m.lastSecurityEvent
	m.lastSecurityEventMu.RUnlock()

	// When no security event has ever been recorded, lastEvent is the zero time
	// and time.Since would report an absurd multi-billion-second value. Report 0
	// instead; callers distinguish "no events" via EventsTotal == 0.
	secondsSince := 0.0
	if !lastEvent.IsZero() {
		secondsSince = time.Since(lastEvent).Seconds()
	}

	return SecurityEventMetrics{
		EventsTotal:       atomic.LoadUint64(&m.securityEventsTotal),
		LastEventTime:     lastEvent,
		SecondsSinceEvent: secondsSince,
	}
}

// GetRequestMetrics returns request metrics.
func (m *Metrics) GetRequestMetrics() RequestMetrics {
	total := atomic.LoadUint64(&m.requestsTotal)
	blocked := atomic.LoadUint64(&m.blockedRequestsTotal)

	var blockRate float64
	if total > 0 {
		blockRate = float64(blocked) / float64(total) * 100
	}

	return RequestMetrics{
		RequestsTotal:        total,
		BlockedRequestsTotal: blocked,
		BlockRate:            blockRate,
	}
}

// Helper methods

func (m *Metrics) getAuthFailuresByIP() map[string]uint64 {
	m.authFailuresByIPLock.RLock()
	defer m.authFailuresByIPLock.RUnlock()

	result := make(map[string]uint64, len(m.authFailuresByIP))
	for ip, count := range m.authFailuresByIP {
		result[ip] = count
	}
	return result
}

func (m *Metrics) getRateLimitViolationsByIP() map[string]uint64 {
	m.rateLimitViolationsByIPLock.RLock()
	defer m.rateLimitViolationsByIPLock.RUnlock()

	result := make(map[string]uint64, len(m.rateLimitViolationsByIP))
	for ip, count := range m.rateLimitViolationsByIP {
		result[ip] = count
	}
	return result
}

func (m *Metrics) getAPIKeyUsageByKey() map[string]uint64 {
	m.apiKeyUsageByKeyLock.RLock()
	defer m.apiKeyUsageByKeyLock.RUnlock()

	result := make(map[string]uint64, len(m.apiKeyUsageByKey))
	for key, count := range m.apiKeyUsageByKey {
		result[key] = count
	}
	return result
}

func (m *Metrics) getSuspiciousActivityByIP() map[string]uint64 {
	m.suspiciousActivityLock.RLock()
	defer m.suspiciousActivityLock.RUnlock()

	result := make(map[string]uint64, len(m.suspiciousActivityByIP))
	for ip, count := range m.suspiciousActivityByIP {
		result[ip] = count
	}
	return result
}

func (m *Metrics) calculateAuthSuccessRate() float64 {
	attempts := atomic.LoadUint64(&m.authAttemptsTotal)
	if attempts == 0 {
		return 0.0
	}

	success := atomic.LoadUint64(&m.authSuccessTotal)
	return float64(success) / float64(attempts) * 100
}

// Reset resets all metrics (useful for testing).
func (m *Metrics) Reset() {
	atomic.StoreUint64(&m.authAttemptsTotal, 0)
	atomic.StoreUint64(&m.authSuccessTotal, 0)
	atomic.StoreUint64(&m.authFailuresTotal, 0)
	atomic.StoreUint64(&m.rateLimitViolationsTotal, 0)
	atomic.StoreUint64(&m.apiKeyUsageTotal, 0)
	atomic.StoreUint64(&m.suspiciousActivityTotal, 0)
	atomic.StoreUint64(&m.securityEventsTotal, 0)
	atomic.StoreUint64(&m.requestsTotal, 0)
	atomic.StoreUint64(&m.blockedRequestsTotal, 0)

	m.authFailuresByIPLock.Lock()
	m.authFailuresByIP = make(map[string]uint64)
	m.authFailuresByIPLock.Unlock()

	m.rateLimitViolationsByIPLock.Lock()
	m.rateLimitViolationsByIP = make(map[string]uint64)
	m.rateLimitViolationsByIPLock.Unlock()

	m.apiKeyUsageByKeyLock.Lock()
	m.apiKeyUsageByKey = make(map[string]uint64)
	m.apiKeyUsageByKeyLock.Unlock()

	m.suspiciousActivityLock.Lock()
	m.suspiciousActivityByIP = make(map[string]uint64)
	m.suspiciousActivityLock.Unlock()

	// Without this GetSecurityEventMetrics reports zero events alongside the
	// old timestamp and a growing SecondsSinceEvent.
	m.lastSecurityEventMu.Lock()
	m.lastSecurityEvent = time.Time{}
	m.lastSecurityEventMu.Unlock()
}

// Metric types

// AuthMetrics contains authentication metrics.
type AuthMetrics struct {
	AttemptsTotal uint64
	SuccessTotal  uint64
	FailuresTotal uint64
	FailuresByIP  map[string]uint64
	SuccessRate   float64
}

// RateLimitMetrics contains rate limiting metrics.
type RateLimitMetrics struct {
	ViolationsTotal uint64
	ViolationsByIP  map[string]uint64
}

// APIKeyMetrics contains API key usage metrics.
type APIKeyMetrics struct {
	UsageTotal uint64
	UsageByKey map[string]uint64
}

// SuspiciousActivityMetrics contains suspicious activity metrics.
type SuspiciousActivityMetrics struct {
	ActivityTotal uint64
	ActivityByIP  map[string]uint64
}

// SecurityEventMetrics contains security event metrics.
type SecurityEventMetrics struct {
	EventsTotal       uint64
	LastEventTime     time.Time
	SecondsSinceEvent float64
}

// RequestMetrics contains request metrics.
type RequestMetrics struct {
	RequestsTotal        uint64
	BlockedRequestsTotal uint64
	BlockRate            float64
}

// Global metrics instance (can be replaced with dependency injection)
var globalMetrics = NewMetrics()

// GetGlobalMetrics returns the global metrics instance.
func GetGlobalMetrics() *Metrics {
	return globalMetrics
}
