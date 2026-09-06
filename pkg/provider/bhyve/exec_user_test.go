package bhyve

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestTheSSHUserCannotBecomeAnSSHOption covers a host-level command execution.
//
// The user is interpolated into "user@ip" as one argv element. ssh parses a
// leading dash as an option wherever it appears, so a user of
// "-oProxyCommand=<cmd>" runs <cmd> on the host, as hospitusd — verified
// against the real ssh binary, which created the file the ProxyCommand named.
//
// ExecCommand was already covered, because ValidateExecOptions checks the user
// and runs first. ExecInteractive called it only when a command was given, so
// a plain shell session — the common case for this method — carried the value
// through unchecked. Both are asserted so the guarantee does not quietly move.
func TestTheSSHUserCannotBecomeAnSSHOption(t *testing.T) {
	p := &BhyveProvider{}
	handle := provider.InstanceHandle{ID: "win11", Provider: "bhyve"}

	for _, user := range []string{
		"-oProxyCommand=/usr/bin/touch /tmp/pwned",
		"-obatchmode=no",
		"root -oProxyCommand=id",
	} {
		t.Run(user, func(t *testing.T) {
			_, err := p.ExecCommand(context.Background(), handle,
				provider.ExecOptions{Command: "/bin/true", User: user})
			if err == nil || !strings.Contains(err.Error(), "invalid user") {
				t.Errorf("ExecCommand: got %v, want a refusal naming the user", err)
			}

			// No command: the case that reached ssh unchecked.
			err = p.ExecInteractive(context.Background(), handle,
				provider.ExecOptions{User: user})
			if err == nil || !strings.Contains(err.Error(), "invalid user") {
				t.Errorf("ExecInteractive: got %v, want a refusal naming the user", err)
			}
		})
	}
}
