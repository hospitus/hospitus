// Package execx provides a small, injectable abstraction over os/exec.
//
// Provider code runs many external commands (jail(8), bhyve, qemu, podman, zfs,
// ifconfig, pfctl, ...). Calling os/exec directly makes the surrounding logic
// impossible to unit-test off a real FreeBSD host. Depending on the Runner
// interface instead lets production use OS{} while tests inject a Fake that
// returns canned output without executing anything.
//
// Only the three common call shapes are abstracted (CombinedOutput / Output /
// Run). Calls that wire pipes, custom stdin/stdout, env or working directory
// keep using os/exec directly — they are streaming/IO paths that a canned-output
// fake cannot meaningfully model. That split is also why trailing whitespace can
// be trimmed here: every binary-producing command (zfs send and friends) wires
// its own pipe and never passes through a Runner.
package execx

import (
	"bytes"
	"context"
	"os/exec"
)

// Runner runs external commands. Production code uses OS{}; tests inject a Fake.
type Runner interface {
	// CombinedOutput runs name+args and returns combined stdout+stderr.
	CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error)
	// Output runs name+args and returns stdout only.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Run runs name+args and returns only an error.
	Run(ctx context.Context, name string, args ...string) error
}

// OS is the production Runner backed by os/exec.
type OS struct{}

// CombinedOutput implements Runner.
func (OS) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return TrimOutput(out), err
}

// Output implements Runner.
func (OS) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return TrimOutput(out), err
}

// TrimOutput removes the trailing whitespace a command line tool ends its output
// with.
//
// Callers either parse the output — and already skip the empty last line, or
// trim it themselves — or interpolate it into an error message, where the
// trailing newline is pure noise: "(output: …improper\n)" renders as
// "(output: …improper; )" once the message is flattened onto one line for the
// API and the log.
//
// Runner implementations apply it so production and tests agree on what a
// command returned.
func TrimOutput(out []byte) []byte {
	return bytes.TrimRight(out, " \t\r\n")
}

// Run implements Runner.
func (OS) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// Default returns the production Runner.
func Default() Runner { return OS{} }
