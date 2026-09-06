package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestExecCommandStreamSendsAPIKey verifies the streaming exec request carries
// the X-API-Key header, which it previously omitted (audit HIGH exec.go:67).
func TestExecCommandStreamSendsAPIKey(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientWithOptions(srv.URL, ClientOptions{APIKey: "secret-key-123"})
	if _, err := c.ExecCommandStream(context.Background(), "web", ExecRequest{Command: "true"}, nil, nil); err != nil {
		t.Fatalf("ExecCommandStream: %v", err)
	}
	if gotKey != "secret-key-123" {
		t.Errorf("streaming request X-API-Key = %q, want secret-key-123", gotKey)
	}
}
