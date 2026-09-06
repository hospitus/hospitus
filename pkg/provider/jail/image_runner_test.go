package jail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// The archive is extracted into a jail root as root, so its digest is
	// checked first. The fake writes both files fetch would have produced.
	const payload = "base system"
	sum := sha256.Sum256([]byte(payload))
	digest := hex.EncodeToString(sum[:])

	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "fetch" && len(args) >= 3 {
			dest, url := args[1], args[2]
			body := payload
			if strings.HasSuffix(url, "MANIFEST") {
				body = "base.txz\t" + digest + "\t29929\tbase\t\"Base system\"\ton\n"
			}
			if err := os.WriteFile(dest, []byte(body), 0o600); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	rel := &OSRelease{Type: "freebsd", Version: "14.3-RELEASE", Arch: "amd64", BaseURL: "https://example/base.txz"}

	if err := p.DownloadBaseSystem(context.Background(), rel, t.TempDir()); err != nil {
		t.Fatalf("DownloadBaseSystem err = %v", err)
	}
	// fetch the archive, fetch the MANIFEST that carries its digest, then tar.
	calls := fake.Snapshot()
	if len(calls) < 3 {
		t.Fatalf("expected fetch, fetch MANIFEST and tar, got %d calls", len(calls))
	}
	if calls[0].Name != "fetch" {
		t.Errorf("first call = %q, want fetch", calls[0].Name)
	}
	if calls[1].Name != "fetch" || !strings.HasSuffix(calls[1].Args[2], "MANIFEST") {
		t.Errorf("second call = %v, want the MANIFEST fetch", calls[1])
	}
	if calls[2].Name != "tar" {
		t.Errorf("third call = %q, want tar", calls[2].Name)
	}
}

// TestDownloadBaseSystemRefusesADigestMismatch covers what the verification is
// for: a mirror that answers with something other than the release.
func TestDownloadBaseSystemRefusesADigestMismatch(t *testing.T) {
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "fetch" && len(args) >= 3 {
			body := "tampered"
			if strings.HasSuffix(args[2], "MANIFEST") {
				body = "base.txz\t" + strings.Repeat("0", 64) + "\t1\tbase\t\"x\"\ton\n"
			}
			return nil, os.WriteFile(args[1], []byte(body), 0o600)
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	rel := &OSRelease{Type: "freebsd", Version: "14.3-RELEASE", Arch: "amd64", BaseURL: "https://example/base.txz"}

	if err := p.DownloadBaseSystem(context.Background(), rel, t.TempDir()); err == nil {
		t.Fatal("an archive that does not match its digest was extracted")
	}
	for _, c := range fake.Snapshot() {
		if c.Name == "tar" {
			t.Error("tar ran on an unverified archive")
		}
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
