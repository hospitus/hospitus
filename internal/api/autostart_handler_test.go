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
