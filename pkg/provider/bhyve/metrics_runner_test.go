package bhyve

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// netstat -ibI output: Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll
const netstatSample = "Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll\n" +
	"tap0 1500 <Link#3> 00:11:22:33:44:55 1000 0 0 500000 800 0 400000 0\n"

func TestGetInterfaceMetrics(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, args []string) ([]byte, error) {
		return []byte(netstatSample), nil
	}}
	p := &BhyveProvider{runner: fake}

	m, err := p.getInterfaceMetrics(context.Background(), "tap0")
	if err != nil {
		t.Fatalf("getInterfaceMetrics: %v", err)
	}
	if m.Name != "tap0" {
		t.Errorf("Name = %q, want tap0", m.Name)
	}
	if m.RxBytes != 500000 || m.TxBytes != 400000 {
		t.Errorf("bytes rx=%d tx=%d, want 500000/400000", m.RxBytes, m.TxBytes)
	}
	if m.RxPackets != 1000 || m.TxPackets != 800 {
		t.Errorf("packets rx=%d tx=%d, want 1000/800", m.RxPackets, m.TxPackets)
	}
	// The command must target the requested interface.
	if got := fake.Calls[0].Args; got[0] != "-ibI" || got[1] != "tap0" {
		t.Errorf("netstat args = %v, want -ibI tap0", got)
	}
}

func TestGetInterfaceMetricsError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}}
	p := &BhyveProvider{runner: fake}
	if _, err := p.getInterfaceMetrics(context.Background(), "tap9"); err == nil {
		t.Error("expected error when netstat fails")
	}
}

func TestGetNetworkIORxParse(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(netstatSample), nil
	}}
	p := &BhyveProvider{runner: fake}
	rx, _, err := p.getNetworkIO(context.Background(), []string{"tap0"})
	if err != nil {
		t.Fatalf("getNetworkIO: %v", err)
	}
	if rx != 500000 {
		t.Errorf("rx = %d, want 500000 (Ibytes)", rx)
	}
}

func TestGetNetworkIOTxParse(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte(netstatSample), nil
	}}
	p := &BhyveProvider{runner: fake}
	_, tx, err := p.getNetworkIO(context.Background(), []string{"tap0"})
	if err != nil {
		t.Fatalf("getNetworkIO: %v", err)
	}
	// TX must be Obytes (field 10 = 400000), not Oerrs (field 9 = 0).
	if tx != 400000 {
		t.Errorf("tx = %d, want 400000 (Obytes)", tx)
	}
}

func TestGetNetworkIONoTapsIsZero(t *testing.T) {
	p := &BhyveProvider{runner: &execx.Fake{}}
	rx, tx, err := p.getNetworkIO(context.Background(), nil)
	if err != nil || rx != 0 || tx != 0 {
		t.Errorf("empty taps → (%d,%d,%v), want (0,0,nil)", rx, tx, err)
	}
}
