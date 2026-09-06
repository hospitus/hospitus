package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		expected string
	}{
		{
			name:     "URL with http prefix",
			baseURL:  "http://localhost:8080",
			expected: "http://localhost:8080",
		},
		{
			name:     "URL without prefix",
			baseURL:  "localhost:8080",
			expected: "http://localhost:8080",
		},
		{
			name:     "URL with trailing slash",
			baseURL:  "http://localhost:8080/",
			expected: "http://localhost:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.baseURL)
			if client.baseURL != tt.expected {
				t.Errorf("Expected baseURL %s, got %s", tt.expected, client.baseURL)
			}
		})
	}
}

func TestHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("Expected path /health, got %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("Expected GET method, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "healthy",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	health, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("Health check failed: %v", err)
	}

	if health["status"] != "healthy" {
		t.Errorf("Expected status 'healthy', got %v", health["status"])
	}
}

func TestListProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/providers" {
			t.Errorf("Expected path /api/v1/providers, got %s", r.URL.Path)
		}

		providers := []provider.ProviderInfo{
			{
				Name:        "test-provider",
				Type:        provider.ProviderTypeVM,
				Version:     "1.0.0",
				Description: "Test provider",
				Available:   true,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(providers)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	providers, err := client.ListProviders(ctx)
	if err != nil {
		t.Fatalf("Failed to list providers: %v", err)
	}

	if len(providers) != 1 {
		t.Errorf("Expected 1 provider, got %d", len(providers))
	}

	if providers[0].Name != "test-provider" {
		t.Errorf("Expected provider 'test-provider', got %s", providers[0].Name)
	}
}

func TestGetProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/providers/test-provider" {
			t.Errorf("Expected path /api/v1/providers/test-provider, got %s", r.URL.Path)
		}

		detail := ProviderDetail{
			Metadata: provider.ProviderMetadata{
				Name:        "test-provider",
				Type:        provider.ProviderTypeVM,
				Version:     "1.0.0",
				Description: "Test provider",
			},
			Capabilities: provider.ProviderCapabilities{
				SupportsSnapshots: true,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(detail)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	detail, err := client.GetProvider(ctx, "test-provider")
	if err != nil {
		t.Fatalf("Failed to get provider: %v", err)
	}

	if detail.Metadata.Name != "test-provider" {
		t.Errorf("Expected provider 'test-provider', got %s", detail.Metadata.Name)
	}

	if !detail.Capabilities.SupportsSnapshots {
		t.Error("Expected provider to support snapshots")
	}
}

func TestCreateInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances" {
			t.Errorf("Expected path /api/v1/instances, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST method, got %s", r.Method)
		}

		// Check query parameter
		providerParam := r.URL.Query().Get("provider")
		if providerParam != "test-provider" {
			t.Errorf("Expected provider 'test-provider', got %s", providerParam)
		}

		// Decode request body
		var spec provider.InstanceSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}

		if spec.Name != "test-vm" {
			t.Errorf("Expected name 'test-vm', got %s", spec.Name)
		}

		instance := datastore.Instance{
			ID:       "test-vm-id",
			Name:     spec.Name,
			Provider: "test-provider",
			State:    provider.StateStopped,
			Spec:     spec,
			Handle: provider.InstanceHandle{
				ID:       "test-vm-id",
				Provider: "test-provider",
			},
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		}

		// Client expects streaming response with SUCCESS: prefix
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		instanceJSON, _ := json.Marshal(instance)
		fmt.Fprintf(w, "SUCCESS: %s\n", string(instanceJSON))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	req := CreateInstanceRequest{
		Provider: "test-provider",
		Spec: provider.InstanceSpec{
			Name:     "test-vm",
			CPUs:     2,
			MemoryMB: 2048,
		},
	}

	instance, err := client.CreateInstance(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create instance: %v", err)
	}

	if instance.ID != "test-vm-id" {
		t.Errorf("Expected ID 'test-vm-id', got %s", instance.ID)
	}

	if instance.Name != "test-vm" {
		t.Errorf("Expected name 'test-vm', got %s", instance.Name)
	}
}

func TestListInstances(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances" {
			t.Errorf("Expected path /api/v1/instances, got %s", r.URL.Path)
		}

		// Check query parameters
		providerFilter := r.URL.Query().Get("provider")
		if providerFilter != "test-provider" {
			t.Errorf("Expected provider filter 'test-provider', got %s", providerFilter)
		}

		instances := []*datastore.Instance{
			{
				ID:       "vm-1",
				Name:     "vm-1",
				Provider: "test-provider",
				State:    provider.StateRunning,
				Spec: provider.InstanceSpec{
					Name: "vm-1",
				},
				Handle: provider.InstanceHandle{
					ID:       "vm-1",
					Provider: "test-provider",
				},
				Labels:      make(map[string]string),
				Annotations: make(map[string]string),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(instances)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	filter := ListInstancesFilter{
		Provider: "test-provider",
	}

	instances, err := client.ListInstances(ctx, filter)
	if err != nil {
		t.Fatalf("Failed to list instances: %v", err)
	}

	if len(instances) != 1 {
		t.Errorf("Expected 1 instance, got %d", len(instances))
	}
}

// TestListInstancesSendsLabelFiltersTheServerParses pins the label filter to
// the wire format handleListInstances reads: repeated label=key=value
// parameters. The client used to send label.<key>=<value>, which the server
// ignored, so client-side label filtering silently never filtered.
func TestListInstancesSendsLabelFiltersTheServerParses(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()["label"]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]*datastore.Instance{})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, err := client.ListInstances(context.Background(), ListInstancesFilter{
		Labels: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}

	if len(got) != 1 || got[0] != "env=prod" {
		t.Errorf("label params = %v, want [\"env=prod\"]", got)
	}
}

// TestExposePortLeavesProviderToTheServer covers the dropped client-side
// default: an empty Provider must reach the daemon empty so the server applies
// its own default, rather than every mapping being recorded as a jail's.
func TestExposePortLeavesProviderToTheServer(t *testing.T) {
	var sent ExposePortRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PortMapping{})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	if _, err := client.ExposePort(context.Background(), "myvm",
		ExposePortRequest{HostPort: 8080, TargetPort: 80}); err != nil {
		t.Fatalf("ExposePort: %v", err)
	}

	if sent.Provider != "" {
		t.Errorf("Provider = %q, want it left empty for the server to default", sent.Provider)
	}
	if sent.Instance != "myvm" {
		t.Errorf("Instance = %q, want myvm", sent.Instance)
	}
}

func TestGetInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances/test-vm" {
			t.Errorf("Expected path /api/v1/instances/test-vm, got %s", r.URL.Path)
		}

		instance := datastore.Instance{
			ID:       "test-vm",
			Name:     "test-vm",
			Provider: "test-provider",
			State:    provider.StateRunning,
			Spec: provider.InstanceSpec{
				Name: "test-vm",
			},
			Handle: provider.InstanceHandle{
				ID:       "test-vm",
				Provider: "test-provider",
			},
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(instance)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	instance, err := client.GetInstance(ctx, "test-vm")
	if err != nil {
		t.Fatalf("Failed to get instance: %v", err)
	}

	if instance.ID != "test-vm" {
		t.Errorf("Expected ID 'test-vm', got %s", instance.ID)
	}
}

func TestDeleteInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances/test-vm" {
			t.Errorf("Expected path /api/v1/instances/test-vm, got %s", r.URL.Path)
		}
		if r.Method != http.MethodDelete {
			t.Errorf("Expected DELETE method, got %s", r.Method)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	err := client.DeleteInstance(ctx, "test-vm", false)
	if err != nil {
		t.Fatalf("Failed to delete instance: %v", err)
	}
}

// TestStartInstanceReportsErrorDetail covers the non-streaming failure path,
// which decodes the error envelope itself rather than going through doRequest.
// It has to read the detail as well as the error: "Instance not found" on its
// own does not say which instance.
func TestStartInstanceReportsErrorDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Instance not found","detail":"web"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	err := NewClient(server.URL).StartInstance(context.Background(), "web", &out)
	if err == nil {
		t.Fatal("starting an unknown instance returned no error")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("error does not say which instance: %v", err)
	}
}

func TestStartInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances/test-vm/start" {
			t.Errorf("Expected path /api/v1/instances/test-vm/start, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST method, got %s", r.Method)
		}

		// Drive the streaming branch: text/plain with provider messages,
		// a warning line, and a terminating SUCCESS marker.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("RCTL warning: racct not enabled\nSUCCESS: started\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	var out bytes.Buffer
	err := client.StartInstance(ctx, "test-vm", &out)
	if err != nil {
		t.Fatalf("Failed to start instance: %v", err)
	}

	// The provider message must be streamed to the writer; the SUCCESS marker
	// must be consumed (not echoed).
	got := out.String()
	if !strings.Contains(got, "RCTL warning: racct not enabled") {
		t.Errorf("expected provider message in streamed output, got %q", got)
	}
	if strings.Contains(got, "SUCCESS:") {
		t.Errorf("SUCCESS marker should not be echoed to output, got %q", got)
	}
}

func TestStopInstance(t *testing.T) {
	tests := []struct {
		name      string
		force     bool
		expectURL string
	}{
		{
			name:      "Normal stop",
			force:     false,
			expectURL: "/api/v1/instances/test-vm/stop",
		},
		{
			name:      "Force stop",
			force:     true,
			expectURL: "/api/v1/instances/test-vm/stop?force=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fullPath := r.URL.Path
				if r.URL.RawQuery != "" {
					fullPath += "?" + r.URL.RawQuery
				}

				if fullPath != tt.expectURL {
					t.Errorf("Expected URL %s, got %s", tt.expectURL, fullPath)
				}

				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewClient(server.URL)
			ctx := context.Background()

			err := client.StopInstance(ctx, "test-vm", tt.force)
			if err != nil {
				t.Fatalf("Failed to stop instance: %v", err)
			}
		})
	}
}

func TestRestartInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances/test-vm/restart" {
			t.Errorf("Expected path /api/v1/instances/test-vm/restart, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST method, got %s", r.Method)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	err := client.RestartInstance(ctx, "test-vm")
	if err != nil {
		t.Fatalf("Failed to restart instance: %v", err)
	}
}

func TestGetEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances/test-vm/events" {
			t.Errorf("Expected path /api/v1/instances/test-vm/events, got %s", r.URL.Path)
		}

		limit := r.URL.Query().Get("limit")
		if limit != "10" {
			t.Errorf("Expected limit '10', got %s", limit)
		}

		events := []datastore.Event{
			{
				InstanceID: "test-vm",
				Type:       datastore.EventTypeCreated,
				State:      provider.StateStopped,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	ctx := context.Background()

	events, err := client.GetEvents(ctx, "test-vm", 10)
	if err != nil {
		t.Fatalf("Failed to get events: %v", err)
	}

	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}
}

func TestErrorHandling(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		responseBody   string
		expectError    bool
		errorSubstring string
	}{
		{
			name:           "404 Not Found",
			statusCode:     http.StatusNotFound,
			responseBody:   `{"error": "instance not found"}`,
			expectError:    true,
			errorSubstring: "instance not found",
		},
		{
			name:           "500 Internal Server Error",
			statusCode:     http.StatusInternalServerError,
			responseBody:   `{"error": "internal error"}`,
			expectError:    true,
			errorSubstring: "internal error",
		},
		{
			name:           "400 Bad Request",
			statusCode:     http.StatusBadRequest,
			responseBody:   `{"error": "invalid request"}`,
			expectError:    true,
			errorSubstring: "invalid request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			client := NewClient(server.URL)
			ctx := context.Background()

			_, err := client.GetInstance(ctx, "test-vm")

			if tt.expectError {
				if err == nil {
					t.Fatal("Expected error but got none")
				}
				if tt.errorSubstring != "" {
					if !strings.Contains(err.Error(), tt.errorSubstring) {
						t.Errorf("Expected error to contain '%s', got '%s'", tt.errorSubstring, err.Error())
					}
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	// Create a server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server would respond, but context will be canceled
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL)

	// Create a context that's already canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetInstance(ctx, "test-vm")
	if err == nil {
		t.Error("Expected error due to canceled context")
	}
}

// --- Backup client tests ---

func TestListBackups_All(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/backups" || r.Method != http.MethodGet {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode([]BackupInfo{})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ListBackups(context.Background(), "")
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if res == nil {
		t.Error("expected non-nil slice")
	}
}

func TestListBackups_ByInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances/myjail/backups" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode([]BackupInfo{{ID: "bk1", InstanceID: "myjail"}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ListBackups(context.Background(), "myjail")
	if err != nil {
		t.Fatalf("ListBackups by instance: %v", err)
	}
	if len(res) != 1 || res[0].ID != "bk1" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestGetBackup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(BackupInfo{ID: "bk42", Status: "completed"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	b, err := c.GetBackup(context.Background(), "bk42")
	if err != nil || b == nil || b.ID != "bk42" {
		t.Fatalf("GetBackup: err=%v b=%+v", err, b)
	}
}

func TestCreateBackup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(BackupInfo{ID: "bk99", Type: "snapshot"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	b, err := c.CreateBackup(context.Background(), "myjail", "snapshot")
	if err != nil || b == nil || b.ID != "bk99" {
		t.Fatalf("CreateBackup: err=%v b=%+v", err, b)
	}
}

func TestDeleteBackup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DeleteBackup(context.Background(), "bk42"); err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}
}

func TestRestoreBackup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.RestoreBackup(context.Background(), "bk1", ""); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
}

func TestVerifyBackup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.VerifyBackup(context.Background(), "bk1"); err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
}

// --- AutoStart client tests ---

func TestListAutoStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/autostart" || r.Method != http.MethodGet {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(autoStartListResponse{
			Instances: []AutoStartInfo{{ID: "myjail"}},
			Count:     1,
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.ListAutoStart(context.Background())
	if err != nil || len(res) != 1 || res[0].ID != "myjail" {
		t.Fatalf("ListAutoStart: err=%v res=%+v", err, res)
	}
}

func TestGetAutoStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(AutoStartInfo{ID: "myjail", Provider: "jail"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	info, err := c.GetAutoStart(context.Background(), "jail", "myjail")
	if err != nil || info == nil || info.ID != "myjail" {
		t.Fatalf("GetAutoStart: err=%v info=%+v", err, info)
	}
}

func TestSetAutoStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(AutoStartInfo{ID: "myjail"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	info, err := c.SetAutoStart(context.Background(), "jail", "myjail", provider.AutoStartConfig{Enabled: true})
	if err != nil || info == nil {
		t.Fatalf("SetAutoStart: err=%v info=%+v", err, info)
	}
}

func TestDisableAutoStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DisableAutoStart(context.Background(), "jail", "myjail"); err != nil {
		t.Fatalf("DisableAutoStart: %v", err)
	}
}

// --- Images client tests ---

func TestListImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/images" || r.Method != http.MethodGet {
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode([]map[string]interface{}{{"name": "14.3-RELEASE-amd64"}})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	imgs, err := c.ListImages(context.Background())
	if err != nil || len(imgs) != 1 {
		t.Fatalf("ListImages: err=%v imgs=%+v", err, imgs)
	}
}

func TestDeleteImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.DeleteImage(context.Background(), "14.3-RELEASE-amd64"); err != nil {
		t.Fatalf("DeleteImage: %v", err)
	}
}

func TestRefreshCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.RefreshCatalog(context.Background()); err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}
}

// --- Instance extended methods ---

func TestUpdateInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(datastore.Instance{ID: "myjail"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	inst, err := c.UpdateInstance(context.Background(), "myjail", UpdateInstanceRequest{})
	if err != nil || inst == nil {
		t.Fatalf("UpdateInstance: err=%v", err)
	}
}

func TestRenameInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"id": "myjail", "name": "newjail"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.RenameInstance(context.Background(), "myjail", "newjail")
	if err != nil || res == nil {
		t.Fatalf("RenameInstance: err=%v", err)
	}
}

func TestPauseInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.PauseInstance(context.Background(), "myjail"); err != nil {
		t.Fatalf("PauseInstance: %v", err)
	}
}

func TestResumeInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.ResumeInstance(context.Background(), "myjail"); err != nil {
		t.Fatalf("ResumeInstance: %v", err)
	}
}

func TestUpgradeInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"job_id": "j123"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	res, err := c.UpgradeInstance(context.Background(), "myjail", "14.3-RELEASE")
	if err != nil || res == nil {
		t.Fatalf("UpgradeInstance: err=%v", err)
	}
}

func TestAPIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantParts  []string
	}{
		{
			name:       "message and detail",
			statusCode: 500,
			body:       `{"error":"Failed to delete instance","detail":"container is running"}`,
			wantParts:  []string{"Failed to delete instance", "container is running"},
		},
		{
			name:       "message only",
			statusCode: 409,
			body:       `{"error":"Instance demo already exists"}`,
			wantParts:  []string{"conflict", "Instance demo already exists"},
		},
		{
			name:       "not JSON falls back to the body",
			statusCode: 502,
			body:       "upstream exploded",
			wantParts:  []string{"upstream exploded"},
		},
		{
			name:       "empty body falls back to the status text",
			statusCode: 404,
			body:       "",
			wantParts:  []string{"not found", http.StatusText(404)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := APIError(tt.statusCode, []byte(tt.body))
			if err == nil {
				t.Fatal("APIError returned nil")
			}
			for _, want := range tt.wantParts {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			// The raw JSON must never reach the reader.
			if strings.Contains(err.Error(), `{"error"`) {
				t.Errorf("raw JSON body leaked into the message: %v", err)
			}
		})
	}
}

// TestPlaintextAgainstTLSSaysWhichURLIsWrong covers the answer Go's HTTP server
// gives a plaintext request on a TLS listener. Passed through, it reads as a
// complaint about the request:
//
//	invalid request: Client sent an HTTP request to an HTTPS server.
//
// The request was fine; the URL was http:// against a daemon serving https://.
// It catches people who run one command as themselves and the next under doas,
// because the context is per user and root's falls back to http://.
func TestPlaintextAgainstTLSSaysWhichURLIsWrong(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Client sent an HTTP request to an HTTPS server."))
	}))
	defer server.Close()

	c := NewClient(server.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, want := range []string{"TLS", "http://", "context show", "doas"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "invalid request") {
		t.Errorf("the pass-through wording survived: %v", err)
	}
}

// TestCrossHostRedirectDropsAPIKey is the leak the redirect guard exists for.
// Go strips Authorization across hosts but not X-API-Key, and the guard reached
// only the streaming client: every ordinary request built a fresh http.Client
// without it.
func TestCrossHostRedirectDropsAPIKey(t *testing.T) {
	var received string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer origin.Close()

	c := NewClientWithOptions(origin.URL, ClientOptions{APIKey: "SECRET"})
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if received != "" {
		t.Fatalf("the API key followed a redirect to another host: %q", received)
	}
}

// TestRateLimitedRequestIsRetried covers what made the CLI unusable in a loop:
// the daemon answers 429 above ten requests a second per client, and anything
// driving the API in sequence crossed that line.
func TestRateLimitedRequestIsRetried(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"Rate limit exceeded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL)
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("a rate-limited request was not retried through: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (two rejections then success)", attempts)
	}
}

// TestRateLimitGivesUpAndExplains keeps the retry bounded: a saturated daemon
// must produce an error, not an infinite wait.
func TestRateLimitGivesUpAndExplains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Rate limit exceeded"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("a permanently rate-limited request reported success")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error does not name the cause: %v", err)
	}
}
