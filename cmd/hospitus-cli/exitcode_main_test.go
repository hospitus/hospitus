package main

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// TestRunCarriesAGuestExitStatus drives run() itself.
//
// It answered 1 for every error, so "hospitus jail exec web sh -c 'exit 42'"
// reported 1 — and printed "Error: command exited with status 42" followed by
// a usage hint, for a program that had merely failed and already written its
// own output.
func TestRunCarriesAGuestExitStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "a guest status is carried through", err: &cmdutil.ExitCodeError{Code: 42}, want: 42},
		{name: "a guest status of 1 is still 1", err: &cmdutil.ExitCodeError{Code: 1}, want: 1},
		{name: "the CLI's own failure stays 1", err: errors.New("could not reach the daemon"), want: 1},
		{name: "success is 0", err: nil, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe := &cobra.Command{
				Use:           "exitcode-probe",
				Hidden:        true,
				SilenceErrors: true,
				SilenceUsage:  true,
				// Its own, so the root's does not run: that one builds an API
				// client, which made this test's answer depend on whatever
				// config the machine happened to have.
				PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
				RunE:              func(*cobra.Command, []string) error { return tc.err },
			}
			rootCmd.AddCommand(probe)
			t.Cleanup(func() { rootCmd.RemoveCommand(probe) })

			rootCmd.SetArgs([]string{"exitcode-probe"})
			t.Cleanup(func() { rootCmd.SetArgs(nil) })

			if got := run(); got != tc.want {
				t.Errorf("run() = %d, want %d", got, tc.want)
			}
		})
	}
}
