package jail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestImportExtractsWithPortableTarFlags pins the flags the import path gives
// tar.
//
// FreeBSD ships bsdtar, which rejects GNU's --no-absolute-names outright:
//
//	tar: Option --no-absolute-names is not supported
//
// tar exits before reading the archive, so "hospitus jail import" failed on the
// primary platform every time, whatever the archive held. Nothing caught it
// because no test looked at the command line, and the flag reads as a safety
// measure — which it is not: assertSafeTarEntries rejects absolute and
// traversing entries before tar runs at all.
func TestImportExtractsWithPortableTarFlags(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "jail.tar.gz")
	if err := os.WriteFile(archive, []byte("not a real archive"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		// assertSafeTarEntries lists the archive first; answer with one safe
		// entry so the extraction below is reached.
		if cmd == "tar" && len(args) > 0 && args[0] == "-tf" {
			return []byte("myjail/\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{stateDir: t.TempDir(), dataDir: t.TempDir(), runner: fake}

	// The import fails afterwards — the archive is a text file, so nothing is
	// extracted and no jail directory turns up. The command line is what this
	// is about, and it has been recorded by then.
	_, _ = p.ImportInstance(context.Background(), archive, provider.ImportOptions{})

	// Match on -xf appearing at all rather than leading, so an extraction
	// carrying a rejected flag is reported as such instead of going missing.
	var extraction []string
	for _, c := range fake.Calls {
		if c.Name != "tar" {
			continue
		}
		for _, arg := range c.Args {
			if arg == "-xf" {
				extraction = c.Args
				break
			}
		}
		if extraction != nil {
			break
		}
	}
	if extraction == nil {
		t.Fatalf("import ran no tar extraction; calls were: %v", fake.Calls)
	}

	for _, arg := range extraction {
		if strings.HasPrefix(arg, "--") {
			t.Errorf("extraction passes %q; bsdtar accepts no GNU long options here", arg)
		}
	}
}
