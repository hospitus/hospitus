package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/orchestration"
)

// Tests for stack handlers. setupTestServer goes through NewServer, which
// builds a real stackManager — an empty one — so these cover the behavior of
// the handlers against a manager that holds no stacks, not a nil-manager
// guard. The comment used to claim the latter.

func TestHandleStacks_Dispatch(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{
			name:   "list stacks (nil manager returns empty list)",
			method: http.MethodGet,
			path:   "/api/v1/stacks",
			want:   http.StatusOK,
		},
		{
			name:   "deploy stack (invalid manifest returns 400)",
			method: http.MethodPost,
			path:   "/api/v1/stacks",
			body:   `{"manifest":""}`,
			want:   http.StatusBadRequest,
		},
		{
			name:   "wrong method on /stacks returns 405",
			method: http.MethodPatch,
			path:   "/api/v1/stacks",
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "get stack (not found returns 404)",
			method: http.MethodGet,
			path:   "/api/v1/stacks/mystack",
			want:   http.StatusNotFound,
		},
		{
			name:   "delete stack (not found returns 404)",
			method: http.MethodDelete,
			path:   "/api/v1/stacks/mystack",
			want:   http.StatusNotFound,
		},
		{
			name:   "wrong method on stack name returns 405",
			method: http.MethodPut,
			path:   "/api/v1/stacks/mystack",
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "start stack (not found returns 404)",
			method: http.MethodPost,
			path:   "/api/v1/stacks/mystack/start",
			want:   http.StatusNotFound,
		},
		{
			name:   "start stack wrong method returns 405",
			method: http.MethodGet,
			path:   "/api/v1/stacks/mystack/start",
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "stop stack (not found returns 404)",
			method: http.MethodPost,
			path:   "/api/v1/stacks/mystack/stop",
			want:   http.StatusNotFound,
		},
		{
			name:   "stop stack wrong method returns 405",
			method: http.MethodGet,
			path:   "/api/v1/stacks/mystack/stop",
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "services (unknown stack returns 200 with empty list)",
			method: http.MethodGet,
			path:   "/api/v1/stacks/mystack/services",
			want:   http.StatusOK,
		},
		{
			name:   "services wrong method returns 405",
			method: http.MethodPost,
			path:   "/api/v1/stacks/mystack/services",
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "unknown action returns 404",
			method: http.MethodGet,
			path:   "/api/v1/stacks/mystack/unknown",
			want:   http.StatusNotFound,
		},
		{
			name:   "extra path segments return 404",
			method: http.MethodGet,
			path:   "/api/v1/stacks/a/b/c/d",
			want:   http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = &bytes.Buffer{}
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			srv.mux.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestHandleStacks_DeployInvalidBody(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	// Invalid JSON body → 400 bad request
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rr.Code)
	}
}

func TestStackToResponse_NilStack(t *testing.T) {
	resp := stackToResponse(nil)
	if resp.Name != "" {
		t.Errorf("expected empty response for nil stack, got name=%q", resp.Name)
	}
}

func TestStackToResponse_WithInstances(t *testing.T) {
	now := time.Now()
	stack := &orchestration.Stack{
		Name:      "mystack",
		Status:    orchestration.StackStatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
		Instances: []*orchestration.StackInstance{
			{
				Name:       "web",
				InstanceID: "mystack-web",
				Provider:   "jail",
				Status:     "running",
				Health:     orchestration.HealthStatusHealthy,
				DependsOn:  []string{"db"},
			},
			{
				Name:       "db",
				InstanceID: "mystack-db",
				Provider:   "jail",
				Status:     "running",
				Health:     orchestration.HealthStatusHealthy,
				DependsOn:  nil,
			},
		},
	}

	resp := stackToResponse(stack)
	if resp.Name != "mystack" {
		t.Errorf("Name = %q, want %q", resp.Name, "mystack")
	}
	if resp.Status != "running" {
		t.Errorf("Status = %q, want %q", resp.Status, "running")
	}
	if len(resp.Instances) != 2 {
		t.Fatalf("len(Instances) = %d, want 2", len(resp.Instances))
	}
	if resp.Instances[0].Name != "web" {
		t.Errorf("Instances[0].Name = %q, want %q", resp.Instances[0].Name, "web")
	}
	if resp.Instances[0].InstanceID != "mystack-web" {
		t.Errorf("Instances[0].InstanceID = %q, want %q", resp.Instances[0].InstanceID, "mystack-web")
	}
	if len(resp.Instances[0].DependsOn) != 1 || resp.Instances[0].DependsOn[0] != "db" {
		t.Errorf("Instances[0].DependsOn = %v, want [db]", resp.Instances[0].DependsOn)
	}
	if resp.Instances[1].Name != "db" {
		t.Errorf("Instances[1].Name = %q, want %q", resp.Instances[1].Name, "db")
	}
}
