package client

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestAPIErrorWrapsNotFound: callers that treat a missing instance as an
// acceptable outcome have to tell it from a timeout or a 500, and matching on
// the message text is not that.
func TestAPIErrorWrapsNotFound(t *testing.T) {
	err := APIError(http.StatusNotFound, []byte(`{"error":"Instance not found","detail":"web"}`))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a 404 must be recognizable with errors.Is, got %v", err)
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("the detail should survive, got: %v", err)
	}

	for _, code := range []int{http.StatusInternalServerError, http.StatusForbidden, http.StatusConflict} {
		if errors.Is(APIError(code, nil), ErrNotFound) {
			t.Errorf("%d was reported as not-found", code)
		}
	}
}

// TestReadErrorBodyIsBounded: an error body was read whole into memory, so a
// daemon (or a proxy in front of it) answering a failure with a large body
// made the CLI hold all of it.
func TestReadErrorBodyIsBounded(t *testing.T) {
	huge := strings.NewReader(strings.Repeat("x", maxErrorBody*4))
	if got := len(readErrorBody(huge)); got != maxErrorBody {
		t.Errorf("read %d bytes of the error body, want it capped at %d", got, maxErrorBody)
	}
}
