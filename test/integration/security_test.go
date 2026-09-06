//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/internal/api"
	"github.com/hospitus/hospitus/internal/auth"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// TestSecurityIntegration verifies all security features work together
func TestSecurityIntegration(t *testing.T) {
	// Create in-memory datastore
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("Failed to create datastore: %v", err)
	}
	defer ds.Close()

	// Create registry
	registry := provider.NewRegistry()

	apiKey, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("Failed to generate API key: %v", err)
	}

	// Create server with authentication enabled
	config := &api.ServerConfig{
		EnableAuth:        true,
		APIKeys:           []string{apiKey},
		EnableRateLimit:   true,
		RequestsPerSecond: 5,
		BurstSize:         10,
		AllowedOrigins:    []string{"https://example.com"},
	}

	server, err := api.NewServer(":8080", ds, registry, config)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	t.Run("Authentication", func(t *testing.T) {
		t.Run("NoAPIKey", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("Expected 401, got %d", w.Code)
			}

			var response map[string]string
			json.NewDecoder(w.Body).Decode(&response)
			if response["error"] != "Missing API key" {
				t.Errorf("Expected 'Missing API key' error, got: %s", response["error"])
			}
		})

		t.Run("InvalidAPIKey", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
			req.Header.Set("X-API-Key", "invalid_key")
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("Expected 401, got %d", w.Code)
			}
		})

		t.Run("ValidAPIKeyHeader", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
			req.Header.Set("X-API-Key", apiKey)
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Expected 200, got %d", w.Code)
			}
		})

		t.Run("ValidAPIKeyQuery", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/providers?api_key="+apiKey, nil)
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Expected 200, got %d", w.Code)
			}
		})

		t.Run("HealthEndpointPublic", func(t *testing.T) {
			// Health endpoint should work without auth
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Health endpoint should be public, got %d", w.Code)
			}
		})
	})

	t.Run("RateLimiting", func(t *testing.T) {
		// Make rapid requests to trigger rate limit
		successCount := 0
		rateLimitCount := 0

		for i := 0; i < 20; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
			req.Header.Set("X-API-Key", apiKey)
			req.RemoteAddr = "192.168.1.100:12345" // Consistent IP
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code == http.StatusOK {
				successCount++
			} else if w.Code == http.StatusTooManyRequests {
				rateLimitCount++
			}
		}

		// Should have some rate limited requests
		if rateLimitCount == 0 {
			t.Error("Expected some rate limited requests")
		}

		t.Logf("Success: %d, Rate limited: %d", successCount, rateLimitCount)
	})

	t.Run("CORS", func(t *testing.T) {
		t.Run("AllowedOrigin", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/api/v1/providers", nil)
			req.Header.Set("Origin", "https://example.com")
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Expected 200 for allowed origin, got %d", w.Code)
			}

			corsHeader := w.Header().Get("Access-Control-Allow-Origin")
			if corsHeader != "https://example.com" {
				t.Errorf("Expected CORS header 'https://example.com', got: %s", corsHeader)
			}
		})

		t.Run("DisallowedOrigin", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/api/v1/providers", nil)
			req.Header.Set("Origin", "https://evil.com")
			w := httptest.NewRecorder()

			handler := server.Handler()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("Expected 403 for disallowed origin, got %d", w.Code)
			}

			corsHeader := w.Header().Get("Access-Control-Allow-Origin")
			if corsHeader != "" {
				t.Errorf("Should not set CORS header for disallowed origin")
			}
		})
	})

	t.Run("InputValidation", func(t *testing.T) {
		t.Run("SQLInjectionBlocked", func(t *testing.T) {
			maliciousNames := []string{
				"test'; DROP TABLE instances; --",
				"test' OR '1'='1",
				"test'; DELETE FROM instances WHERE '1'='1",
			}

			for _, name := range maliciousNames {
				err := validation.ValidateInstanceName(name)
				if err == nil {
					t.Errorf("SQL injection should be blocked: %s", name)
				}
			}
		})

		t.Run("CommandInjectionBlocked", func(t *testing.T) {
			maliciousNames := []string{
				"test; rm -rf /",
				"test | cat /etc/passwd",
				"test`whoami`",
				"test$(whoami)",
			}

			for _, name := range maliciousNames {
				err := validation.ValidateInstanceName(name)
				if err == nil {
					t.Errorf("Command injection should be blocked: %s", name)
				}
			}
		})

		t.Run("PathTraversalBlocked", func(t *testing.T) {
			maliciousNames := []string{
				"../etc/passwd",
				"../../root/.ssh/id_rsa",
				"test/../../../etc/shadow",
			}

			for _, name := range maliciousNames {
				err := validation.ValidateInstanceName(name)
				if err == nil {
					t.Errorf("Path traversal should be blocked: %s", name)
				}
			}
		})

		t.Run("ValidNamesAccepted", func(t *testing.T) {
			validNames := []string{
				"test-vm",
				"vm-123",
				"my_instance_1",
				"production-web-server",
			}

			for _, name := range validNames {
				err := validation.ValidateInstanceName(name)
				if err != nil {
					t.Errorf("Valid name should be accepted: %s, error: %v", name, err)
				}
			}
		})
	})
}

// TestSecurityDefenseInDepth verifies multiple layers of security
func TestSecurityDefenseInDepth(t *testing.T) {
	t.Run("MultipleSecurityLayers", func(t *testing.T) {
		// Even if one layer fails, others should protect

		// Layer 1: Input validation
		err := validation.ValidateInstanceName("test'; DROP TABLE instances; --")
		if err == nil {
			t.Error("Input validation layer failed")
		}

		// Layer 2: SQL parameterization would prevent it anyway
		// (tested in datastore tests)

		// Layer 3: Authentication prevents unauthorized access
		// (tested above)
	})

	t.Run("ResourceLimits", func(t *testing.T) {
		// Prevent resource exhaustion attacks
		tests := []struct {
			name      string
			cpus      int
			memoryMB  int64
			shouldErr bool
		}{
			{"Normal", 4, 4096, false},
			{"TooManyCPUs", 2000, 1024, true},
			{"TooMuchMemory", 1, 5 * 1024 * 1024, true},
			{"Zero CPUs", 0, 1024, true},
			{"Negative Memory", 1, -1024, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := validation.ValidateResourceLimits(tt.cpus, tt.memoryMB)
				if tt.shouldErr && err == nil {
					t.Error("Expected error for invalid resource limits")
				}
				if !tt.shouldErr && err != nil {
					t.Errorf("Unexpected error for valid limits: %v", err)
				}
			})
		}
	})
}

// TestSecurityBestPractices verifies security best practices are followed
func TestSecurityBestPractices(t *testing.T) {
	t.Run("APIKeyFormat", func(t *testing.T) {
		// API keys should be strong and have proper format
		for i := 0; i < 10; i++ {
			key, err := auth.GenerateAPIKey()
			if err != nil {
				t.Fatalf("Failed to generate API key: %v", err)
			}

			// Should have prefix
			if len(key) < 10 || !strings.HasPrefix(key, auth.APIKeyPrefix) {
				t.Errorf("API key format invalid: %s", key)
			}

			// Should be long enough (32 bytes base64 encoded + prefix)
			if len(key) < 40 {
				t.Errorf("API key too short: %s (length %d)", key, len(key))
			}
		}
	})

	t.Run("PasswordHashing", func(t *testing.T) {
		// Verify bcrypt is used (not plaintext or weak hashing)
		key := "test_key_123"
		hash, err := auth.HashAPIKey(key)
		if err != nil {
			t.Fatalf("Failed to hash key: %v", err)
		}

		// Bcrypt hashes start with $2a$ or $2b$
		if len(hash) < 50 || (hash[:4] != "$2a$" && hash[:4] != "$2b$") {
			t.Errorf("Hash doesn't look like bcrypt: %s", hash)
		}

		// Hash should not equal original
		if hash == key {
			t.Error("Hash should not equal plaintext")
		}
	})

	t.Run("ConstantTimeComparison", func(t *testing.T) {
		// SimpleAuthProvider uses constant-time comparison
		provider := auth.NewSimpleAuthProvider([]string{"secret123"})

		// These should all take approximately the same time
		// (we can't test timing directly, but we verify functionality)
		testCases := []struct {
			key   string
			valid bool
		}{
			{"secret123", true},
			{"secret124", false},  // One char different
			{"secret12", false},   // Shorter
			{"secret1234", false}, // Longer
			{"", false},           // Empty
		}

		for _, tc := range testCases {
			result := provider.ValidateKey(tc.key)
			if result != tc.valid {
				t.Errorf("Key %q: expected %v, got %v", tc.key, tc.valid, result)
			}
		}
	})

	t.Run("SecureDefaults", func(t *testing.T) {
		// Verify secure defaults are used
		ds, _ := datastore.NewDatastore(":memory:", nil)
		defer ds.Close()
		registry := provider.NewRegistry()

		// nil config should use secure defaults
		server, err := api.NewServer(":8080", ds, registry, nil)
		if err != nil {
			t.Fatalf("Server should initialize with nil config: %v", err)
		}
		if server == nil {
			t.Error("Server should not be nil")
		}
	})
}

// BenchmarkSecurity tests performance of security features
func BenchmarkSecurity(b *testing.B) {
	b.Run("ValidateInstanceName", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			validation.ValidateInstanceName("test-vm-123")
		}
	})

	b.Run("GenerateAPIKey", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			auth.GenerateAPIKey()
		}
	})

	b.Run("HashAPIKey", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			auth.HashAPIKey("test_key")
		}
	})

	b.Run("ValidateAPIKey", func(b *testing.B) {
		am := auth.NewAuthManager()
		key, _ := auth.GenerateAPIKey()
		am.AddAPIKey("test", "Test", key, []string{"*"})

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			am.ValidateAPIKey(key)
		}
	})
}
