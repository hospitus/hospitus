package jail

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// useMock points the CLI's global client at a mock for the duration of a test.
func useMock(t *testing.T, m cmdutil.APIClientInterface) {
	t.Helper()
	old := cmdutil.APIClient
	cmdutil.APIClient = m
	t.Cleanup(func() { cmdutil.APIClient = old })
}

type serviceMock struct {
	baseMockClient
	result *client.ServiceActionResult
}

func (m *serviceMock) GetInstance(_ context.Context, id string) (*datastore.Instance, error) {
	return &datastore.Instance{ID: id, Name: id, Provider: "jail"}, nil
}

func (m *serviceMock) ServiceAction(context.Context, string, client.ServiceActionRequest) (*client.ServiceActionResult, error) {
	return m.result, nil
}

// TestServiceActionFailureIsAnError: "nginx: start failed" printed on stdout
// with exit status 0 told every script calling this that the service had
// started.
func TestServiceActionFailureIsAnError(t *testing.T) {
	useMock(t, &serviceMock{result: &client.ServiceActionResult{
		Success: false,
		Message: "nginx: unknown directive",
	}})

	cmd := newServiceCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"start", "web", "nginx"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a failed service action exited 0")
	}
	if !strings.Contains(err.Error(), "unknown directive") {
		t.Errorf("the daemon's reason should reach the caller, got: %v", err)
	}
}

func TestServiceActionSuccessStaysQuiet(t *testing.T) {
	useMock(t, &serviceMock{result: &client.ServiceActionResult{Success: true}})

	cmd := newServiceCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"start", "web", "nginx"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "successful") {
		t.Errorf("success should still be reported, got: %q", buf.String())
	}
}

type nilInstanceMock struct {
	baseMockClient
}

func (m *nilInstanceMock) ListInstances(context.Context, client.ListInstancesFilter) ([]*datastore.Instance, error) {
	// A null element in the daemon's JSON array decodes to exactly this.
	return []*datastore.Instance{
		{ID: "web", Name: "web", Provider: "jail", State: provider.StateRunning},
		nil,
	}, nil
}

// TestListRejectsANilInstance: dereferencing the nil panicked the whole
// command. Skipping it silently would print a list quietly missing an entry,
// so it is reported instead.
func TestListRejectsANilInstance(t *testing.T) {
	useMock(t, &nilInstanceMock{})

	cmd := newListCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a null element in the daemon's list went unreported")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("the error should name the response as the problem, got: %v", err)
	}
}

// The same null, on the path that reads inst.Name to build a work list.
func TestSnapshotListRejectsANilInstance(t *testing.T) {
	useMock(t, &nilInstanceMock{})

	cmd := newSnapshotCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list", "--all"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("a null element in the daemon's list went unreported")
	}
}

type noJailsMock struct {
	baseMockClient
}

func (m *noJailsMock) ListInstances(context.Context, client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return nil, nil
}

// TestSnapshotListValidatesThePatternWithNoJails: filepath.Match ran inside the
// loop, so with nothing to iterate a broken pattern was never looked at and the
// command reported "No jails matching" and exited 0.
func TestSnapshotListValidatesThePatternWithNoJails(t *testing.T) {
	useMock(t, &noJailsMock{})

	cmd := newSnapshotCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list", "--pattern", "["})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a malformed --pattern was accepted because no jail existed to match it against")
	}
	if !strings.Contains(err.Error(), "invalid pattern") {
		t.Errorf("the error should name the pattern, got: %v", err)
	}
}

type snapshotMock struct {
	baseMockClient
	err error
}

func (m *snapshotMock) ListInstances(context.Context, client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return []*datastore.Instance{
		{ID: "a", Name: "a", Provider: "jail"},
		{ID: "b", Name: "b", Provider: "jail"},
	}, nil
}

func (m *snapshotMock) ListSnapshots(context.Context, string) ([]client.SnapshotInfo, error) {
	return nil, m.err
}

// TestSnapshotListReportsWhenEveryJailFailed: with every listing failing, this
// printed "No snapshots found" and exited 0 — the same answer as a host that
// genuinely has none.
func TestSnapshotListReportsWhenEveryJailFailed(t *testing.T) {
	useMock(t, &snapshotMock{err: errors.New("connection refused")})

	cmd := newSnapshotCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list", "--all"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("every listing failed and the command still exited 0")
	}
}
