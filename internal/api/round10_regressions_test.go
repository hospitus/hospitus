package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoggingResponseWriterRecordsImplicitStatus covers what the request log
// and the audit trail are told.
//
// The promoted Write commits 200 without touching statusCode, so a handler that
// wrote before calling WriteHeader had its later — and dropped — status
// recorded instead of the 200 the client actually received.
func TestLoggingResponseWriterRecordsImplicitStatus(t *testing.T) {
	t.Run("a bare write records 200", func(t *testing.T) {
		lw := &loggingResponseWriter{ResponseWriter: httptest.NewRecorder()}
		if _, err := lw.Write([]byte("body")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if lw.statusCode != http.StatusOK {
			t.Errorf("statusCode = %d, want 200", lw.statusCode)
		}
		// A later status must not overwrite what the client already got.
		lw.WriteHeader(http.StatusUnauthorized)
		if lw.statusCode != http.StatusOK {
			t.Errorf("statusCode = %d after a late WriteHeader, want the committed 200", lw.statusCode)
		}
	})

	t.Run("a bare flush records 200", func(t *testing.T) {
		lw := &loggingResponseWriter{ResponseWriter: httptest.NewRecorder()}
		lw.Flush()
		if lw.statusCode != http.StatusOK {
			t.Errorf("statusCode = %d, want 200", lw.statusCode)
		}
	})

	t.Run("an explicit status is still recorded", func(t *testing.T) {
		lw := &loggingResponseWriter{ResponseWriter: httptest.NewRecorder()}
		lw.WriteHeader(http.StatusTeapot)
		if _, err := lw.Write([]byte("x")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if lw.statusCode != http.StatusTeapot {
			t.Errorf("statusCode = %d, want 418", lw.statusCode)
		}
	})
}

// TestFreeBSDRouteShape covers the exact paths the freebsd routes serve.
//
// The guard tested only a minimum length, so a deeper path dispatched on
// parts[5] and the rest was ignored: /freebsd/rctl/extra ran rctl, and
// /freebsd/vnet/disable reached handleVnet — which routes on the method alone,
// so a POST to it enabled VNET.
func TestFreeBSDRouteShape(t *testing.T) {
	s, ds := setupTestServer(t)

	inst := makeSimpleInstance("fb-shape", "mock")
	if err := ds.CreateInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		method string
		path   string
		want   int
	}{
		// mockProvider implements neither capability, so a route that resolves
		// answers 501. That is what tells a served path from a refused one.
		{http.MethodGet, "/api/v1/instances/fb-shape/freebsd/rctl", http.StatusNotImplemented},
		{http.MethodGet, "/api/v1/instances/fb-shape/freebsd/vnet", http.StatusNotImplemented},
		{http.MethodPost, "/api/v1/instances/fb-shape/freebsd/vnet", http.StatusNotImplemented},

		{http.MethodGet, "/api/v1/instances/fb-shape/freebsd/rctl/extra", http.StatusNotFound},
		{http.MethodPost, "/api/v1/instances/fb-shape/freebsd/vnet/disable", http.StatusNotFound},
		{http.MethodPost, "/api/v1/instances/fb-shape/freebsd/vnet/enable", http.StatusNotFound},
		{http.MethodGet, "/api/v1/instances/fb-shape/freebsd", http.StatusNotFound},
		{http.MethodGet, "/api/v1/instances/fb-shape/freebsd/bogus", http.StatusNotFound},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)

		if w.Code != tt.want {
			t.Errorf("%s %s = %d, want %d; body=%s", tt.method, tt.path, w.Code, tt.want, w.Body.String())
		}
	}
}
