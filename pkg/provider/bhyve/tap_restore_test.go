package bhyve

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// ifconfigFake answers "ifconfig <iface>" — the existence probe — from the set
// of interfaces the host is pretending to have, and lets every other ifconfig
// call succeed.
func ifconfigFake(existing ...string) *execx.Fake {
	present := make(map[string]bool, len(existing))
	for _, iface := range existing {
		present[iface] = true
	}
	return &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		if len(args) == 1 && !present[args[0]] {
			return []byte("ifconfig: interface " + args[0] + " does not exist"), errors.New("exit status 1")
		}
		return nil, nil
	}}
}

func ranCommand(fake *execx.Fake, want string) bool {
	for _, call := range fake.Calls {
		if call.Name+" "+strings.Join(call.Args, " ") == want {
			return true
		}
	}
	return false
}

func TestRestoreTapDevices(t *testing.T) {
	tests := []struct {
		name     string
		config   *vmConfig
		existing []string
		want     []string
		absent   []string
	}{
		{
			name:     "recreates a tap the host lost across a reboot",
			config:   &vmConfig{Name: "windows", TapDevs: []string{"tap_windows_0"}},
			existing: nil,
			want: []string{
				"ifconfig tap create name tap_windows_0",
				"ifconfig tap_windows_0 up",
			},
		},
		{
			name:     "leaves an existing tap alone",
			config:   &vmConfig{Name: "windows", TapDevs: []string{"tap_windows_0"}},
			existing: []string{"tap_windows_0"},
			want:     []string{"ifconfig tap_windows_0 up"},
			absent:   []string{"ifconfig tap create name tap_windows_0"},
		},
		{
			name: "puts a bridged tap back on its bridge",
			config: &vmConfig{
				Name:    "web",
				TapDevs: []string{"tap_web_0"},
				Bridges: []string{"hospitus0"},
			},
			existing: nil,
			want: []string{
				"ifconfig tap create name tap_web_0",
				"ifconfig bridge create name hospitus0",
				"ifconfig hospitus0 addm tap_web_0",
			},
		},
		{
			name: "does not bridge a NAT tap",
			config: &vmConfig{
				Name:       "windows",
				TapDevs:    []string{"tap_windows_0"},
				Bridges:    []string{""},
				NATEnabled: true,
			},
			existing: nil,
			want:     []string{"ifconfig tap create name tap_windows_0"},
			absent:   []string{"ifconfig hospitus-nat addm tap_windows_0"},
		},
		{
			name:     "skips an empty tap slot",
			config:   &vmConfig{Name: "web", TapDevs: []string{""}},
			existing: nil,
			absent:   []string{"ifconfig tap create name "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := ifconfigFake(tt.existing...)
			p := &BhyveProvider{runner: fake}

			if err := p.restoreTapDevices(context.Background(), tt.config); err != nil {
				t.Fatalf("restoreTapDevices: %v", err)
			}

			for _, want := range tt.want {
				if !ranCommand(fake, want) {
					t.Errorf("missing command %q; ran %v", want, fake.Calls)
				}
			}
			for _, absent := range tt.absent {
				if ranCommand(fake, absent) {
					t.Errorf("unexpected command %q", absent)
				}
			}
		})
	}
}

func TestRestoreTapDevicesReportsCreateFailure(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("ifconfig: SIOCIFCREATE2: File exists"), errors.New("exit status 1")
	}}
	p := &BhyveProvider{runner: fake}

	err := p.restoreTapDevices(context.Background(), &vmConfig{Name: "windows", TapDevs: []string{"tap_windows_0"}})
	if err == nil {
		t.Fatal("expected an error when the tap cannot be created")
	}
	if !strings.Contains(err.Error(), "tap_windows_0") {
		t.Errorf("error should name the tap, got %v", err)
	}
}

// The bridge a tap belongs to only survives a reboot because the config file
// carries it, so it has to make the round trip through save/load.
func TestVMConfigRoundTripsBridges(t *testing.T) {
	cfg := &vmConfig{
		Name:    "web",
		TapDevs: []string{"tap_web_0", "tap_web_1"},
		Bridges: []string{"hospitus0", ""},
	}
	p := configVMProvider(t, cfg)

	loaded, err := p.loadVMConfig(filepath.Join(p.dataDir, "web"))
	if err != nil {
		t.Fatalf("loadVMConfig: %v", err)
	}
	if len(loaded.Bridges) != 2 || loaded.Bridges[0] != "hospitus0" || loaded.Bridges[1] != "" {
		t.Errorf("Bridges = %v, want [hospitus0 \"\"]", loaded.Bridges)
	}
}
