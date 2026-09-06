package security

import (
	"testing"
	"time"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}

	// Verify initial state
	authMetrics := m.GetAuthMetrics()
	if authMetrics.AttemptsTotal != 0 {
		t.Errorf("Expected 0 attempts, got %d", authMetrics.AttemptsTotal)
	}
}

func TestRecordAuthAttempt(t *testing.T) {
	m := NewMetrics()

	// Record successful auth
	m.RecordAuthAttempt(true, "192.168.1.1")

	authMetrics := m.GetAuthMetrics()
	if authMetrics.AttemptsTotal != 1 {
		t.Errorf("Expected 1 attempt, got %d", authMetrics.AttemptsTotal)
	}
	if authMetrics.SuccessTotal != 1 {
		t.Errorf("Expected 1 success, got %d", authMetrics.SuccessTotal)
	}
	if authMetrics.SuccessRate != 100.0 {
		t.Errorf("Expected 100%% success rate, got %.2f", authMetrics.SuccessRate)
	}

	// Record failed auth
	m.RecordAuthAttempt(false, "192.168.1.2")

	authMetrics = m.GetAuthMetrics()
	if authMetrics.AttemptsTotal != 2 {
		t.Errorf("Expected 2 attempts, got %d", authMetrics.AttemptsTotal)
	}
	if authMetrics.FailuresTotal != 1 {
		t.Errorf("Expected 1 failure, got %d", authMetrics.FailuresTotal)
	}
	if authMetrics.SuccessRate != 50.0 {
		t.Errorf("Expected 50%% success rate, got %.2f", authMetrics.SuccessRate)
	}
}

func TestRecordAuthFailuresByIP(t *testing.T) {
	m := NewMetrics()

	// Record multiple failures from same IP
	for i := 0; i < 3; i++ {
		m.RecordAuthAttempt(false, "192.168.1.100")
	}

	authMetrics := m.GetAuthMetrics()
	if authMetrics.FailuresByIP["192.168.1.100"] != 3 {
		t.Errorf("Expected 3 failures for IP, got %d", authMetrics.FailuresByIP["192.168.1.100"])
	}
}

func TestSuspiciousActivityDetection(t *testing.T) {
	m := NewMetrics()

	// Record 5 failed auth attempts (should trigger suspicious activity)
	for i := 0; i < 5; i++ {
		m.RecordAuthAttempt(false, "10.0.0.1")
	}

	suspMetrics := m.GetSuspiciousActivityMetrics()
	if suspMetrics.ActivityTotal == 0 {
		t.Error("Expected suspicious activity to be detected")
	}

	if suspMetrics.ActivityByIP["10.0.0.1"] == 0 {
		t.Error("Expected suspicious activity from specific IP")
	}
}

func TestRecordRateLimitViolation(t *testing.T) {
	m := NewMetrics()

	m.RecordRateLimitViolation("192.168.1.50")
	m.RecordRateLimitViolation("192.168.1.50")

	rateLimitMetrics := m.GetRateLimitMetrics()
	if rateLimitMetrics.ViolationsTotal != 2 {
		t.Errorf("Expected 2 violations, got %d", rateLimitMetrics.ViolationsTotal)
	}

	if rateLimitMetrics.ViolationsByIP["192.168.1.50"] != 2 {
		t.Errorf("Expected 2 violations for IP, got %d", rateLimitMetrics.ViolationsByIP["192.168.1.50"])
	}
}

func TestRecordAPIKeyUsage(t *testing.T) {
	m := NewMetrics()

	m.RecordAPIKeyUsage("key1")
	m.RecordAPIKeyUsage("key1")
	m.RecordAPIKeyUsage("key2")

	apiKeyMetrics := m.GetAPIKeyMetrics()
	if apiKeyMetrics.UsageTotal != 3 {
		t.Errorf("Expected 3 total uses, got %d", apiKeyMetrics.UsageTotal)
	}

	if apiKeyMetrics.UsageByKey["key1"] != 2 {
		t.Errorf("Expected 2 uses for key1, got %d", apiKeyMetrics.UsageByKey["key1"])
	}

	if apiKeyMetrics.UsageByKey["key2"] != 1 {
		t.Errorf("Expected 1 use for key2, got %d", apiKeyMetrics.UsageByKey["key2"])
	}
}

func TestRecordSecurityEvent(t *testing.T) {
	m := NewMetrics()

	before := time.Now()
	m.RecordSecurityEvent()
	after := time.Now()

	secMetrics := m.GetSecurityEventMetrics()
	if secMetrics.EventsTotal != 1 {
		t.Errorf("Expected 1 security event, got %d", secMetrics.EventsTotal)
	}

	if secMetrics.LastEventTime.Before(before) || secMetrics.LastEventTime.After(after) {
		t.Error("LastEventTime not within expected range")
	}

	if secMetrics.SecondsSinceEvent > 1.0 {
		t.Errorf("SecondsSinceEvent too large: %.2f", secMetrics.SecondsSinceEvent)
	}
}

func TestRecordRequest(t *testing.T) {
	m := NewMetrics()

	m.RecordRequest(false) // Allowed
	m.RecordRequest(false) // Allowed
	m.RecordRequest(true)  // Blocked
	m.RecordRequest(true)  // Blocked

	reqMetrics := m.GetRequestMetrics()
	if reqMetrics.RequestsTotal != 4 {
		t.Errorf("Expected 4 total requests, got %d", reqMetrics.RequestsTotal)
	}

	if reqMetrics.BlockedRequestsTotal != 2 {
		t.Errorf("Expected 2 blocked requests, got %d", reqMetrics.BlockedRequestsTotal)
	}

	if reqMetrics.BlockRate != 50.0 {
		t.Errorf("Expected 50%% block rate, got %.2f", reqMetrics.BlockRate)
	}
}

func TestReset(t *testing.T) {
	m := NewMetrics()

	// Add some data
	m.RecordAuthAttempt(true, "192.168.1.1")
	m.RecordRateLimitViolation("192.168.1.2")
	m.RecordAPIKeyUsage("key1")
	m.RecordSecurityEvent()

	// Verify data exists
	if m.GetAuthMetrics().AttemptsTotal == 0 {
		t.Fatal("Expected data before reset")
	}

	// Reset
	m.Reset()

	// Verify all metrics are zero
	authMetrics := m.GetAuthMetrics()
	if authMetrics.AttemptsTotal != 0 {
		t.Errorf("Expected 0 attempts after reset, got %d", authMetrics.AttemptsTotal)
	}

	rateLimitMetrics := m.GetRateLimitMetrics()
	if rateLimitMetrics.ViolationsTotal != 0 {
		t.Errorf("Expected 0 violations after reset, got %d", rateLimitMetrics.ViolationsTotal)
	}

	apiKeyMetrics := m.GetAPIKeyMetrics()
	if apiKeyMetrics.UsageTotal != 0 {
		t.Errorf("Expected 0 API key uses after reset, got %d", apiKeyMetrics.UsageTotal)
	}

	secMetrics := m.GetSecurityEventMetrics()
	if secMetrics.EventsTotal != 0 {
		t.Errorf("Expected 0 security events after reset, got %d", secMetrics.EventsTotal)
	}
}

func TestConcurrentAccess(t *testing.T) {
	m := NewMetrics()

	// Run concurrent operations
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				m.RecordAuthAttempt(j%2 == 0, "192.168.1.1")
				m.RecordRateLimitViolation("192.168.1.2")
				m.RecordAPIKeyUsage("key1")
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify counts
	authMetrics := m.GetAuthMetrics()
	if authMetrics.AttemptsTotal != 1000 {
		t.Errorf("Expected 1000 attempts, got %d", authMetrics.AttemptsTotal)
	}

	rateLimitMetrics := m.GetRateLimitMetrics()
	if rateLimitMetrics.ViolationsTotal != 1000 {
		t.Errorf("Expected 1000 violations, got %d", rateLimitMetrics.ViolationsTotal)
	}

	apiKeyMetrics := m.GetAPIKeyMetrics()
	if apiKeyMetrics.UsageTotal != 1000 {
		t.Errorf("Expected 1000 API key uses, got %d", apiKeyMetrics.UsageTotal)
	}
}

func TestGlobalMetrics(t *testing.T) {
	m := GetGlobalMetrics()
	if m == nil {
		t.Fatal("GetGlobalMetrics returned nil")
	}

	// Reset for clean state
	m.Reset()

	// Test using global instance
	m.RecordAuthAttempt(true, "192.168.1.1")

	authMetrics := m.GetAuthMetrics()
	if authMetrics.AttemptsTotal == 0 {
		t.Error("Global metrics not working")
	}
}

func BenchmarkRecordAuthAttempt(b *testing.B) {
	m := NewMetrics()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.RecordAuthAttempt(true, "192.168.1.1")
	}
}

func BenchmarkRecordRateLimitViolation(b *testing.B) {
	m := NewMetrics()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.RecordRateLimitViolation("192.168.1.1")
	}
}

func BenchmarkRecordAPIKeyUsage(b *testing.B) {
	m := NewMetrics()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.RecordAPIKeyUsage("key1")
	}
}

func BenchmarkConcurrentRecordAuthAttempt(b *testing.B) {
	m := NewMetrics()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.RecordAuthAttempt(true, "192.168.1.1")
		}
	})
}
