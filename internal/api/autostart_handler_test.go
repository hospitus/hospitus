package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

func TestSetAutoStartAnswersWithWhatItStored(t *testing.T) {
	handle := provider.InstanceHandle{ID: "ubuntu-server", Provider: "qemu"}
	prov := &mockAutoStartProvider{}
	s := &Server{logger: logging.WithComponent("test")}

	body := `{"enabled":true,"priority":20,"delay_ms":5000}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/autostart/qemu/ubuntu-server", strings.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleSetAutoStart(rec, req, prov, handle)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got client.AutoStartInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the CLI cannot read the answer: %v (%s)", err, rec.Body.String())
	}
	if got.ID != "ubuntu-server" {
		t.Errorf("id = %q, want ubuntu-server", got.ID)
	}
	if got.AutoStart.Priority != 20 || got.AutoStart.DelayMS != 5000 {
		t.Errorf("config = %+v, want priority 20 and delay 5000", got.AutoStart)
	}
}

// TestSetAutoStartOnAProviderWithoutItAnswers501 covers the other half of the
// dispatcher: a provider that does not implement AutoStartProvider.
//
// A capability reached by type assertion is unavailable when the assertion
// fails, and the contract is 501 — never a panic on a nil interface.
func TestSetAutoStartOnAProviderWithoutItAnswers501(t *testing.T) {
	s, ds := setupTestServer(t)
	defer ds.Close()

	body := `{"enabled":true,"priority":20}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/autostart/mock/anything", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501; body=%s", rec.Code, rec.Body.String())
	}
}
