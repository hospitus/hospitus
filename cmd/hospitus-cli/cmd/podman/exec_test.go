package podman

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
)

// execMock answers only the two calls runExec makes; the embedded interface
// leaves the rest nil, which is what the cmdutil tests do.
type execMock struct {
	cmdutil.APIClientInterface
	result *client.ExecResult
}

func (m *execMock) GetInstance(_ context.Context, id string) (*datastore.Instance, error) {
	return &datastore.Instance{ID: id, Name: id, Provider: "podman"}, nil
}

func (m *execMock) ExecCommand(context.Context, string, client.ExecRequest) (*client.ExecResult, error) {
	return m.result, nil
}

// TestExecCarriesTheGuestExitStatus: this branch used to call os.Exit, which
// ended the process from inside RunE and skipped every deferred close — and
// left the branch untestable, which is why it went unnoticed.
func TestExecCarriesTheGuestExitStatus(t *testing.T) {
	old := cmdutil.APIClient
	cmdutil.APIClient = &execMock{result: &client.ExecResult{Stdout: "out\n", ExitCode: 42}}
	t.Cleanup(func() { cmdutil.APIClient = old })

	cmd := NewPodmanCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"exec", "web", "sh", "-c", "exit 42"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a non-zero guest status was reported as success")
	}

	code, ok := cmdutil.ExitCodeFrom(err)
	if !ok {
		t.Fatalf("the error does not carry an exit status: %v", err)
	}
	if code != 42 {
		t.Errorf("exit status = %d, want the guest's 42", code)
	}
	if !bytes.Contains(buf.Bytes(), []byte("out")) {
		t.Errorf("the guest's stdout should still reach the caller, got %q", buf.String())
	}
}

// A guest that succeeded must not produce an error at all.
func TestExecIsQuietOnSuccess(t *testing.T) {
	old := cmdutil.APIClient
	cmdutil.APIClient = &execMock{result: &client.ExecResult{Stdout: "ok\n", ExitCode: 0}}
	t.Cleanup(func() { cmdutil.APIClient = old })

	cmd := NewPodmanCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"exec", "web", "true"})

	if err := cmd.Execute(); err != nil {
		var exitErr *cmdutil.ExitCodeError
		if errors.As(err, &exitErr) {
			t.Fatalf("a successful command carried exit status %d", exitErr.Code)
		}
		t.Fatalf("unexpected error: %v", err)
	}
}
