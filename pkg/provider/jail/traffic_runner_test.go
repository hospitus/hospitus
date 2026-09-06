package jail

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestGetTrafficStats(t *testing.T) {
	netstatOut := "Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll\n" +
		"epair0b 1500 <Link> 00:00 100 0 0 5000 200 0 8000 0\n"
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		switch {
		case name == "jls":
			return []byte("42\n"), nil
		case name == "jexec" && len(args) >= 2 && args[1] == "ifconfig":
			return []byte("epair0b lo0\n"), nil
		case name == "jexec" && len(args) >= 2 && args[1] == "netstat":
			return []byte(netstatOut), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}

	stats, err := p.GetTrafficStats(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetTrafficStats err = %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d stats, want 1 (lo0 skipped): %+v", len(stats), stats)
	}
	if stats[0].InterfaceName != "epair0b" || stats[0].RxBytes != 5000 || stats[0].TxBytes != 8000 {
		t.Errorf("unexpected stats: %+v", stats[0])
	}
}

func TestGetTrafficStatsNotRunning(t *testing.T) {
	// jls returns 0 -> not running.
	fake := &execx.Fake{Func: func(name string, _ []string) ([]byte, error) {
		if name == "jls" {
			return []byte("0\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if _, err := p.GetTrafficStats(context.Background(), provider.InstanceHandle{ID: "web"}); err == nil {
		t.Fatal("expected error when jail not running")
	}
}

func TestResetTrafficStatsUnsupported(t *testing.T) {
	p := &JailProvider{}
	if err := p.ResetTrafficStats(context.Background(), provider.InstanceHandle{ID: "web"}); err == nil {
		t.Fatal("expected ErrUnsupportedOperation")
	}
}
