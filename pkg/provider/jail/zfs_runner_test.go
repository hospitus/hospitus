package jail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestCreateZFSDataset(t *testing.T) {
	tests := []struct {
		name    string
		runErr  error
		wantErr bool
	}{
		{name: "success", runErr: nil, wantErr: false},
		{name: "zfs failure", runErr: errors.New("boom"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
				return nil, tt.runErr
			}}
			p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}

			err := p.createZFSDataset(context.Background(), "zroot/hospitus/jails/web")
			if (err != nil) != tt.wantErr {
				t.Fatalf("createZFSDataset err = %v, wantErr %v", err, tt.wantErr)
			}
			if want := "zfs create -p zroot/hospitus/jails/web"; !hasCmd(fake, want) {
				t.Errorf("missing command %q; got %q", want, lastCmd(fake))
			}
		})
	}
}

func TestEnsureZFSDatasetExists(t *testing.T) {
	// zfs list succeeds -> dataset exists -> no create issued.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}

	if err := p.ensureZFSDataset(context.Background(), "zroot/hospitus/jails"); err != nil {
		t.Fatalf("ensureZFSDataset err = %v", err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("expected 1 call (list only), got %d", fake.CallCount())
	}
	if want := "zfs list zroot/hospitus/jails"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestEnsureZFSDatasetCreatesWhenMissing(t *testing.T) {
	// zfs list fails -> triggers create.
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list" {
			return nil, errors.New("does not exist")
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}

	if err := p.ensureZFSDataset(context.Background(), "zroot/hospitus/jails"); err != nil {
		t.Fatalf("ensureZFSDataset err = %v", err)
	}
	if want := "zfs create -p zroot/hospitus/jails"; !hasCmd(fake, want) {
		t.Errorf("expected create to be issued after failed list; got calls %+v", fake.Calls)
	}
}

func TestDestroyZFSDataset(t *testing.T) {
	tests := []struct {
		name     string
		force    bool
		output   string
		outErr   error
		wantErr  bool
		wantArgs string
	}{
		{name: "success no force", force: false, wantArgs: "zfs destroy -r zroot/hospitus/jails/web"},
		{name: "success force", force: true, wantArgs: "zfs destroy -r -f zroot/hospitus/jails/web"},
		{name: "already gone is ok", output: "dataset does not exist", outErr: errors.New("exit 1"), wantErr: false, wantArgs: "zfs destroy -r zroot/hospitus/jails/web"},
		{name: "real error", output: "pool busy", outErr: errors.New("exit 1"), wantErr: true, wantArgs: "zfs destroy -r zroot/hospitus/jails/web"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
				return []byte(tt.output), tt.outErr
			}}
			p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}

			err := p.destroyZFSDataset(context.Background(), "zroot/hospitus/jails/web", tt.force)
			if (err != nil) != tt.wantErr {
				t.Fatalf("destroyZFSDataset err = %v, wantErr %v", err, tt.wantErr)
			}
			if !hasCmd(fake, tt.wantArgs) {
				t.Errorf("missing command %q; got %q", tt.wantArgs, lastCmd(fake))
			}
		})
	}
}

// TestDestroyZFSDatasetRetriesBusyDataset covers deleting a jail that has been
// started at least once: its root stays mounted-busy after the jail is gone, so
// a plain destroy failed and the instance could not be deleted at all.
func TestDestroyZFSDatasetRetriesBusyDataset(t *testing.T) {
	const busy = "cannot unmount '/zroot/hospitus/jails/web': pool or dataset is busy"

	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		for _, a := range args {
			if a == "-f" {
				return nil, nil
			}
		}
		return []byte(busy), errors.New("exit status 1")
	}}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}

	if err := p.destroyZFSDataset(context.Background(), "zroot/hospitus/jails/web", false); err != nil {
		t.Fatalf("destroyZFSDataset err = %v, want the busy mount to be forced", err)
	}
	if want := "zfs destroy -r -f zroot/hospitus/jails/web"; !hasCmd(fake, want) {
		t.Errorf("missing retry %q; got calls %+v", want, fake.Calls)
	}
}

// TestDestroyZFSDatasetReportsWhy covers the message the caller ends up with:
// zfs explains itself on stderr, and dropping that leaves only "exit status 1".
func TestDestroyZFSDatasetReportsWhy(t *testing.T) {
	const reason = "cannot destroy 'zroot/hospitus/jails/web': filesystem has children"

	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(reason), errors.New("exit status 1")
	}}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}

	err := p.destroyZFSDataset(context.Background(), "zroot/hospitus/jails/web", true)
	if err == nil {
		t.Fatal("destroyZFSDataset returned no error")
	}
	if !strings.Contains(err.Error(), "filesystem has children") {
		t.Errorf("error does not say why: %v", err)
	}
}

func TestGetZFSMountpoint(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("/zroot/hospitus/jails/web\n"), nil
	}}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}

	mp, err := p.getZFSMountpoint(context.Background(), "zroot/hospitus/jails/web")
	if err != nil {
		t.Fatalf("getZFSMountpoint err = %v", err)
	}
	if mp != "/zroot/hospitus/jails/web" {
		t.Errorf("mountpoint = %q, want /zroot/hospitus/jails/web", mp)
	}
	if want := "zfs get -H -o value mountpoint zroot/hospitus/jails/web"; !hasCmd(fake, want) {
		t.Errorf("missing %q; got %q", want, lastCmd(fake))
	}
}

func TestGetZFSMountpointError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return nil, errors.New("no such dataset")
	}}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	if _, err := p.getZFSMountpoint(context.Background(), "bad"); err == nil {
		t.Fatal("expected error from getZFSMountpoint")
	}
}

func TestApplyZFSQuotas(t *testing.T) {
	t.Run("zero disk is no-op", func(t *testing.T) {
		fake := &execx.Fake{}
		p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
		if err := p.applyZFSQuotas(context.Background(), "ds", 0); err != nil {
			t.Fatalf("err = %v", err)
		}
		if fake.CallCount() != 0 {
			t.Errorf("expected no commands for zero disk, got %d", fake.CallCount())
		}
	})

	t.Run("sets quota and refquota", func(t *testing.T) {
		fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
		p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
		if err := p.applyZFSQuotas(context.Background(), "zroot/hospitus/jails/web", 10); err != nil {
			t.Fatalf("err = %v", err)
		}
		if want := "zfs set quota=10G zroot/hospitus/jails/web"; !hasCmd(fake, want) {
			t.Errorf("missing %q", want)
		}
		if want := "zfs set refquota=10G zroot/hospitus/jails/web"; !hasCmd(fake, want) {
			t.Errorf("missing %q", want)
		}
	})
}

func TestZfsDestroyCleanupUsesRunner(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
	p.zfsDestroyCleanup(context.Background(), "zroot/hospitus/jails/web@snap")
	if want := "zfs destroy zroot/hospitus/jails/web@snap"; !hasCmd(fake, want) {
		t.Errorf("missing %q; got %q", want, lastCmd(fake))
	}
}

// TestDestroyZFSDatasetRefusesForeignDatasets is the guard behind the API: a
// dataset name reaches destroyZFSDataset from handle metadata, which a PATCH
// could once rewrite to any dataset on the host. Nothing outside the jail and
// volumes parents is destroyed, and no zfs command is even run.
func TestDestroyZFSDatasetRefusesForeignDatasets(t *testing.T) {
	refused := []string{
		"zroot/ROOT/default",
		"zroot/hospitus",
		"zroot/hospitus/jails",
		"zroot/hospitus/jails/",
		"zroot/hospitus/jails/web@snap",
		"zroot/hospitus/jails/../ROOT",
		"zroot/hospitus/jailsweb",
		"zroot/hospitus/bases/14.3",
		"",
	}
	for _, ds := range refused {
		fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
		p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
		if err := p.destroyZFSDataset(context.Background(), ds, true); err == nil {
			t.Errorf("%q: expected a refusal", ds)
		}
		if fake.CallCount() != 0 {
			t.Errorf("%q: a zfs command ran anyway: %q", ds, lastCmd(fake))
		}
	}

	allowed := []string{"zroot/hospitus/jails/web", "zroot/hospitus/volumes/data"}
	for _, ds := range allowed {
		fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
		p := &JailProvider{runner: fake, zfsParent: "zroot/hospitus/jails"}
		if err := p.destroyZFSDataset(context.Background(), ds, false); err != nil {
			t.Errorf("%q: unexpected refusal: %v", ds, err)
		}
		if want := "zfs destroy -r " + ds; !hasCmd(fake, want) {
			t.Errorf("%q: missing %q; got %q", ds, want, lastCmd(fake))
		}
	}
}
