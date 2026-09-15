package image

import (
	"slices"
	"testing"
)

func TestImageProviders(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  []string
	}{
		{"jail base set", "freebsd-14.3-RELEASE-amd64", []string{"jail"}},
		// The name a caller types, without the prefix the catalog keys on. An
		// exact comparison against the key never matched it, so every image fell
		// back to the full list and a base set for jails was advertised for
		// bhyve, QEMU and Podman as well.
		{"the same set, named as the caller types it", "14.3-RELEASE-amd64", []string{"jail"}},
		{"arm64 cloud image is QEMU only", "alpine-3.20-arm64", []string{"qemu"}},
		{"amd64 cloud image serves both VM providers", "debian-12-amd64", []string{"bhyve", "qemu"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := imageProviders(tt.image)
			if !slices.Equal(got, tt.want) {
				t.Errorf("imageProviders(%q) = %v, want %v", tt.image, got, tt.want)
			}
		})
	}
}

// An image the catalog does not know about must still produce a usable hint.
func TestImageProvidersUnknownImage(t *testing.T) {
	got := imageProviders("not-a-catalog-image")
	if len(got) == 0 {
		t.Fatal("imageProviders returned no provider for an unknown image")
	}
	if !slices.Contains(got, "qemu") {
		t.Errorf("fallback should list every provider, got %v", got)
	}
}
