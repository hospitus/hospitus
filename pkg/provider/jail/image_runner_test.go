package jail

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestGetOSRelease(t *testing.T) {
	p := &JailProvider{}

	rel, err := p.GetOSRelease("freebsd", "14.3-RELEASE", "amd64")
	if err != nil {
		t.Fatalf("GetOSRelease err = %v", err)
	}
	if rel.Type != "freebsd" || rel.Arch != "amd64" {
		t.Errorf("unexpected release: %+v", rel)
	}
	if !strings.Contains(rel.BaseURL, "releases/amd64/14.3-RELEASE/base.txz") {
		t.Errorf("BaseURL = %q", rel.BaseURL)
	}
}

func TestGetOSReleaseCurrentSnapshot(t *testing.T) {
	p := &JailProvider{}
	rel, err := p.GetOSRelease("freebsd", "15.0-CURRENT", "arm64")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(rel.BaseURL, "snapshots/arm64/aarch64/15.0-CURRENT/base.txz") {
		t.Errorf("expected snapshots URL, got %q", rel.BaseURL)
	}
}

func TestGetOSReleaseNonFreeBSD(t *testing.T) {
	p := &JailProvider{}
	rel, err := p.GetOSRelease("linux", "ubuntu-24.04", "amd64")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if rel.Type != "linux" || rel.BaseURL != "" {
		t.Errorf("unexpected linux release: %+v", rel)
	}
}

func TestDownloadBaseSystem(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}
	rel := &OSRelease{Type: "freebsd", Version: "14.3-RELEASE", Arch: "amd64", BaseURL: "https://example/base.txz"}

	if err := p.DownloadBaseSystem(context.Background(), rel, t.TempDir()); err != nil {
		t.Fatalf("DownloadBaseSystem err = %v", err)
	}
	// fetch then tar.
	if len(fake.Calls) < 2 {
		t.Fatalf("expected fetch and tar calls, got %d", len(fake.Calls))
	}
	if fake.Calls[0].Name != "fetch" {
		t.Errorf("first call = %q, want fetch", fake.Calls[0].Name)
	}
	if fake.Calls[1].Name != "tar" {
		t.Errorf("second call = %q, want tar", fake.Calls[1].Name)
	}
}

func TestDownloadBaseSystemNoURL(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}}
	rel := &OSRelease{Type: "freebsd"}
	if err := p.DownloadBaseSystem(context.Background(), rel, t.TempDir()); err == nil {
		t.Fatal("expected error for empty BaseURL")
	}
}

func TestDownloadBaseSystemFetchFails(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, _ []string) ([]byte, error) {
		if name == "fetch" {
			return []byte("404"), errors.New("exit 1")
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	rel := &OSRelease{BaseURL: "https://example/base.txz"}
	if err := p.DownloadBaseSystem(context.Background(), rel, t.TempDir()); err == nil {
		t.Fatal("expected error when fetch fails")
	}
}

func TestExtractBaseSystemEmptyImage(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}}
	if err := p.extractBaseSystem(context.Background(), "", t.TempDir(), ""); err != nil {
		t.Fatalf("empty image should be no-op, got %v", err)
	}
}

func TestExtractBaseSystemLocalFile(t *testing.T) {
	dir := t.TempDir()
	// Create a fake local archive file so os.Stat succeeds.
	archive := dir + "/base.txz"
	if err := os.WriteFile(archive, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}
	if err := p.extractBaseSystem(context.Background(), archive, dir, ""); err != nil {
		t.Fatalf("extractBaseSystem err = %v", err)
	}
	if want := "tar -xf " + archive + " -C " + dir; !hasCmd(fake, want) {
		t.Errorf("missing %q; got %q", want, lastCmd(fake))
	}
}

func TestExtractBaseSystemRejectsPathTraversal(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}, config: provider.ProviderConfig{DataDir: t.TempDir()}}
	if err := p.extractBaseSystem(context.Background(), "../etc/passwd", t.TempDir(), ""); err == nil {
		t.Fatal("expected error for path traversal image name")
	}
}
