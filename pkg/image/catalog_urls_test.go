package image

import (
	"net/http"
	"os"
	"testing"
	"time"
)

// TestCatalogURLsResolve checks that every artifact the catalog offers still
// exists.
//
// Mirrors rotate: a third of the entries once pointed at files the FreeBSD,
// Debian, Ubuntu and OpenBSD mirrors had already removed, and an image fetch
// answered 404 for each of them. This is what catches the next round. It stays
// opt-in so neither CI nor `make test` depends on the network.
func TestCatalogURLsResolve(t *testing.T) {
	if os.Getenv("HOSPITUS_CHECK_CATALOG_URLS") != "1" {
		t.Skip("set HOSPITUS_CHECK_CATALOG_URLS=1 to check the catalog against the mirrors")
	}

	client := &http.Client{Timeout: 20 * time.Second}
	for _, profile := range NewCatalog(t.TempDir()).Available() {
		if profile.URL == "" {
			continue
		}
		t.Run(profile.Name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequest(http.MethodHead, profile.URL, nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", profile.URL, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s answered %d", profile.URL, resp.StatusCode)
			}
		})
	}
}
