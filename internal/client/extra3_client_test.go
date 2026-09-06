package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGetTimeoutFromEnv covers all branches of getTimeoutFromEnv.
func TestGetTimeoutFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     time.Duration
	}{
		{
			name:     "empty uses default",
			envValue: "",
			want:     DefaultTimeout,
		},
		{
			name:     "duration string 15s",
			envValue: "15s",
			want:     15 * time.Second,
		},
		{
			name:     "duration string 2m",
			envValue: "2m",
			want:     2 * time.Minute,
		},
		{
			name:     "integer seconds 60",
			envValue: "60",
			want:     60 * time.Second,
		},
		{
			name:     "invalid uses default",
			envValue: "notaduration",
			want:     DefaultTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOSPITUS_TIMEOUT", tt.envValue)
			got := getTimeoutFromEnv()
			if got != tt.want {
				t.Errorf("getTimeoutFromEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNewClientWithOptions_TLSSkipVerify exercises the TLSSkipVerify path.
func TestNewClientWithOptions_TLSSkipVerify(t *testing.T) {
	c := NewClientWithOptions("http://localhost:8080", ClientOptions{
		TLSSkipVerify: true,
		APIKey:        "test-key",
	})
	if c == nil {
		t.Fatal("expected non-nil client")
	}

	if c.apiKey != "test-key" {
		t.Errorf("expected APIKey %q to be stored, got %q", "test-key", c.apiKey)
	}

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.httpClient.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig to be set when TLSSkipVerify is true")
	}
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be true")
	}
}

// TestCheckpointClient covers CreateCheckpoint, RestoreCheckpoint, DeleteCheckpoint.
func TestCheckpointClient(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/instances/vm1/checkpoint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/instances/vm1/checkpoint/snap1/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/instances/vm1/checkpoint/snap1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	if err := c.CreateCheckpoint(ctx, "vm1", "snap1"); err != nil {
		t.Errorf("CreateCheckpoint: %v", err)
	}
	if err := c.RestoreCheckpoint(ctx, "vm1", "snap1"); err != nil {
		t.Errorf("RestoreCheckpoint: %v", err)
	}
	if err := c.DeleteCheckpoint(ctx, "vm1", "snap1"); err != nil {
		t.Errorf("DeleteCheckpoint: %v", err)
	}
}

// TestFetchImage covers the FetchImage streaming path.
func TestFetchImage(t *testing.T) {
	t.Run("json already_exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(FetchImageResult{Status: "already_exists", Path: "/images/14.3"})
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		if err := c.FetchImage(context.Background(), "14.3-RELEASE", io.Discard); err != nil {
			t.Errorf("FetchImage already_exists: %v", err)
		}
	})

	t.Run("json success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(FetchImageResult{Status: "success", Message: "done"})
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		if err := c.FetchImage(context.Background(), "14.3-RELEASE", io.Discard); err != nil {
			t.Errorf("FetchImage json success: %v", err)
		}
	})

	t.Run("text streaming complete", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("PROGRESS: 50%\nCOMPLETE: done\nSUCCESS: ok\n"))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		if err := c.FetchImage(context.Background(), "14.3-RELEASE", io.Discard); err != nil {
			t.Errorf("FetchImage text streaming: %v", err)
		}
	})

	t.Run("text streaming error line", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("PROGRESS: 10%\nERROR: download failed\n"))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		if err := c.FetchImage(context.Background(), "14.3-RELEASE", io.Discard); err == nil {
			t.Error("expected error from FetchImage on ERROR line, got nil")
		}
	})
}

// TestFetchImage_HTTPError covers the FetchImage HTTP error path.
func TestFetchImage_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.FetchImage(context.Background(), "14.3-RELEASE", io.Discard); err == nil {
		t.Error("expected error from FetchImage on 500 response, got nil")
	}
}
