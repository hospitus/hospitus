package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Firewall handler tests. The test server has firewallMgr = nil, so all
// handlers that check firewallMgr == nil will return 503. Some checks
// come before the nil check (routing, method dispatch) so we also hit
// those early paths.

func TestHandleFirewall_Routing(t *testing.T) {
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
			name:   "expose POST (nil firewallMgr → 503)",
			method: http.MethodPost,
			path:   "/api/v1/firewall/expose",
			body:   `{"instance":"vm","host_port":8080,"target_port":80,"target_ip":"10.0.0.1"}`,
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "expose GET without instance (→ 400)",
			method: http.MethodGet,
			path:   "/api/v1/firewall/expose",
			want:   http.StatusBadRequest,
		},
		{
			name:   "expose GET with instance (nil firewallMgr → 503)",
			method: http.MethodGet,
			path:   "/api/v1/firewall/expose/myvm",
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "expose DELETE (nil firewallMgr → 503)",
			method: http.MethodDelete,
			path:   "/api/v1/firewall/expose",
			body:   `{"instance":"vm","host_port":8080}`,
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "expose wrong method (→ 405)",
			method: http.MethodPut,
			path:   "/api/v1/firewall/expose",
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "nat POST (nil firewallMgr → 503)",
			method: http.MethodPost,
			path:   "/api/v1/firewall/nat",
			body:   `{"instance":"vm","source_network":"10.0.0.0/24","out_interface":"em0"}`,
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "nat DELETE (nil firewallMgr → 503)",
			method: http.MethodDelete,
			path:   "/api/v1/firewall/nat",
			body:   `{"instance":"vm"}`,
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "nat wrong method (→ 405)",
			method: http.MethodGet,
			path:   "/api/v1/firewall/nat",
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "unknown firewall path (→ 404)",
			method: http.MethodGet,
			path:   "/api/v1/firewall/unknown",
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

func TestHandleUnexposePort_InvalidBody(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	// nil firewallMgr → 503 before body decode
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/firewall/expose",
		bytes.NewBufferString("not json"))
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rr.Code)
	}
}
