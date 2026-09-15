package manifest

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/image"
)

// TestManifestImageResolvesInTheCatalog covers the path apply takes when an
// instance cannot be created because its image is missing.
//
// A manifest writes the image as "freebsd:14.3-RELEASE-riscv64", leaving the
// reference "14.3-RELEASE-riscv64", while the catalog is keyed on
// "freebsd-14.3-RELEASE-riscv64". FindProfile matches the key alone and misses
// it; ResolveProfile accepts both spellings, which is what `hospitus image fetch`
// uses. Apply has to agree with fetch about the name it prints.
func TestManifestImageResolvesInTheCatalog(t *testing.T) {
	catalog := image.NewCatalog(t.TempDir())

	sources := []string{
		"freebsd:14.3-RELEASE-amd64",
		"freebsd:14.3-RELEASE-arm64",
		"freebsd:14.3-RELEASE-riscv64",
	}

	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			ref := imageReference(source)
			if ref == source {
				t.Fatalf("the provider prefix was not stripped from %q", source)
			}

			if catalog.ResolveProfile(ref) == nil {
				t.Errorf("%q left reference %q, which the catalog does not resolve", source, ref)
			}
		})
	}
}

// TestImageReferenceLeavesAnUnprefixedSourceAlone guards the other spelling: a
// manifest may name the image without a provider in front of it.
func TestImageReferenceLeavesAnUnprefixedSourceAlone(t *testing.T) {
	const source = "freebsd-14.3-RELEASE-amd64"
	if got := imageReference(source); got != source {
		t.Errorf("imageReference(%q) = %q, want it unchanged", source, got)
	}
}
