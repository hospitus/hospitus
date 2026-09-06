package jail

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestValidateRCTLAmount covers the amounts SetResourceLimits will hand to
// rctl(8). It calls the production check; this file used to re-implement it,
// and the copy never applied rctlAmountPattern at all.
func TestValidateRCTLAmount(t *testing.T) {
	tests := []struct {
		name    string
		amount  string
		wantErr bool
	}{
		{
			name:    "valid amount 100M",
			amount:  "100M",
			wantErr: false,
		},
		{
			name:    "valid amount 2G",
			amount:  "2G",
			wantErr: false,
		},
		{
			// pcpu is "in percents of a single CPU core" — a bare number. rctl
			// refuses the percent sign, so accepting it here only deferred the
			// error to the provider.
			name:    "percent sign is not rctl's grammar",
			amount:  "50%",
			wantErr: true,
		},
		{
			name:    "pcpu as rctl spells it",
			amount:  "50",
			wantErr: false,
		},
		{
			// expand_number(3) takes a whole number, never a fraction.
			name:    "fractional amount",
			amount:  "1.5G",
			wantErr: true,
		},
		{
			// 16 * 2^60 does not fit a uint64; expand_number(3) answers ERANGE
			// and rctl refuses it, so it is a 400 and not a provider failure.
			name:    "amount that overflows its unit suffix",
			amount:  "16E",
			wantErr: true,
		},
		{
			name:    "the largest amount that still fits",
			amount:  "15E",
			wantErr: false,
		},
		{
			name:    "valid amount numeric only",
			amount:  "3600",
			wantErr: false,
		},
		{
			name:    "amount with newline",
			amount:  "100M\n",
			wantErr: true,
		},
		{
			name:    "amount with semicolon",
			amount:  "100M;echo",
			wantErr: true,
		},
		{
			name:    "amount with pipe",
			amount:  "100M|cat",
			wantErr: true,
		},
		{
			name:    "amount with ampersand",
			amount:  "100M&",
			wantErr: true,
		},
		{
			name:    "amount with null byte",
			amount:  "100M\x00",
			wantErr: true,
		},
		{
			name:    "amount too long",
			amount:  "123456789012345678901",
			wantErr: true,
		},
		{
			name:    "amount exactly 20 chars ok",
			amount:  "12345678901234567890",
			wantErr: false,
		},
		{
			// Non-UTF8: invalid byte sequence
			name:    "non-utf8 amount",
			amount:  "100M\xfe\xfe",
			wantErr: true,
		},
		{
			name:    "amount with backtick",
			amount:  "100M`ls`",
			wantErr: true,
		},
		{
			name:    "amount with dollar sign",
			amount:  "$(whoami)",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRCTLAmount(tt.amount)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRCTLAmount(%q) error = %v, wantErr %v", tt.amount, err, tt.wantErr)
			}
		})
	}
}

// TestIsValidRctlAction covers the actions an rctl rule may name.
//
// The signal case tested only the "sig" prefix, so "sigbogus" passed here and
// was refused by rctl(8) — turning a caller's typo into a provider failure.
func TestIsValidRctlAction(t *testing.T) {
	for _, tt := range []struct {
		action string
		want   bool
	}{
		{"deny", true},
		{"log", true},
		{"devctl", true},
		{"throttle", true},
		{"sigterm", true},
		{"sigkill", true},
		// The seven the first hand-written list left out.
		{"sigemt", true},
		{"sigchld", true},
		{"sigio", true},
		{"sigprof", true},
		{"sigwinch", true},
		{"siginfo", true},
		{"sigthr", true},
		// Named in neither rctl action table.
		{"siglibrt", false},
		{"sigbogus", false},
		{"sig", false},
		{"signal", false},
		{"", false},
		{"DENY", false},
	} {
		if got := isValidRctlAction(tt.action); got != tt.want {
			t.Errorf("isValidRctlAction(%q) = %v, want %v", tt.action, got, tt.want)
		}
	}
}

// TestRctlCombinations covers the resource/action pairs rctl(8) will take.
//
// The two allowlists are independent, so a pair each half accepts can still be
// one rctl refuses — and SetResourceLimits had already removed the jail's
// existing rules by the time rctl said so.
func TestRctlCombinations(t *testing.T) {
	for _, tt := range []struct {
		resource string
		action   string
		wantErr  bool
	}{
		// rctl(8): deny is not supported for these six.
		{"cputime", "deny", true},
		{"wallclock", "deny", true},
		{"readbps", "deny", true},
		{"writebps", "deny", true},
		{"readiops", "deny", true},
		{"writeiops", "deny", true},

		// ... but every other action is.
		{"cputime", "log", false},
		{"cputime", "sigterm", false},
		{"wallclock", "devctl", false},

		// throttle is only supported for the four I/O resources.
		{"readbps", "throttle", false},
		{"writebps", "throttle", false},
		{"readiops", "throttle", false},
		{"writeiops", "throttle", false},
		{"memoryuse", "throttle", true},
		{"pcpu", "throttle", true},

		// The combinations applyRCTLLimits itself generates.
		{"pcpu", "deny", false},
		{"memoryuse", "deny", false},
		{"maxproc", "deny", false},
	} {
		err := rctlCombinationError(tt.resource, tt.action)
		if (err != nil) != tt.wantErr {
			t.Errorf("rctlCombinationError(%q, %q) = %v, wantErr %v", tt.resource, tt.action, err, tt.wantErr)
		}
	}
}

// TestRctlPublicEntryPointsDoNotDeadlock covers the lock the rctl paths share.
//
// GetResourceLimits and RemoveResourceLimits now take rctlLock so a read or a
// DELETE cannot land between a replacement's removal and its additions. The
// replacement paths hold that same lock already, and Go mutexes are not
// reentrant — so they must call the unlocked helpers. If any of them calls a
// public method instead, this test hangs rather than fails, which is why it
// runs under a deadline.
func TestRctlPublicEntryPointsDoNotDeadlock(t *testing.T) {
	// Both entry points refuse before they reach the lock unless their
	// preconditions hold, and a test that trips those refusals exercises none
	// of the locking it claims to cover:
	//   - applyRCTLLimits wants kern.racct.enable to read "1"
	//   - SetResourceLimits calls GetInstanceState first, which stats
	//     <stateDir>/<id>.json and answers "jail not found" without it
	handle := provider.InstanceHandle{ID: "lock-probe"}

	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, handle.ID+".json"), []byte(`{"name":"lock-probe"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "sysctl" {
			return []byte("1\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake, stateDir: stateDir}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := context.Background()

		// Each of these takes the lock, and each must release it.
		_, _ = p.GetResourceLimits(ctx, handle)
		_ = p.RemoveResourceLimits(ctx, handle)

		// The replacement paths take the same lock and reach the helpers
		// underneath it.
		_ = p.SetResourceLimits(ctx, handle, []ResourceLimit{
			{Resource: "memoryuse", Action: "deny", Amount: "1G"},
		})
		_ = p.applyRCTLLimits(ctx, handle.ID, provider.ResourceSpec{MemoryMB: 512})

		// And the lock is free again afterwards.
		_, _ = p.GetResourceLimits(ctx, handle)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("an rctl entry point is holding its own lock: a public method was called from a locked path")
	}

	// Not merely "it did not hang": both replacement paths must have reached
	// rctl, or the run proved nothing about the lock they take.
	var added int
	for _, call := range fake.Calls {
		if call.Name == "rctl" && len(call.Args) > 0 && call.Args[0] == "-a" {
			added++
		}
	}
	if added < 2 {
		t.Fatalf("only %d rctl additions were made: SetResourceLimits and applyRCTLLimits did not both reach the lock", added)
	}
}
