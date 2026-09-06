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
	"github.com/hospitus/hospitus/pkg/job"
)

// mockStopClient is a mock API client for testing stop operations
type mockStopClient struct {
	// Only the calls these tests exercise are overridden. The rest fall
	// through to baseMockClient, which returns "not implemented": a stub
	// answering nil turns an unexpected call into a silent success, which is
	// the opposite of what a test double is for.
	baseMockClient
	stopError error
	gotForce  bool // records the force flag passed to StopInstance
}

func (m *mockStopClient) StopInstance(ctx context.Context, id string, force bool) error {
	m.gotForce = force
	return m.stopError
}

// A real instance, provider included: returning nil made the resolver report
// no provider, which a jail command must refuse rather than act on.
func (m *mockStopClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return &datastore.Instance{ID: id, Name: id, Provider: "jail"}, nil
}

func (m *mockStopClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockStopClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockStopClient) VerifyBackup(_ context.Context, _ string) error     { return nil }

func (m *mockStopClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockStopClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockStopClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockStopClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockStopClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }

func TestStopCommand(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		flags          map[string]string
		mockClient     *mockStopClient
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "stop jail successfully",
			args: []string{"testjail"},
			mockClient: &mockStopClient{
				stopError: nil,
			},
			expectedOutput: []string{
				"Jail stopped: testjail",
			},
			expectError: false,
		},
		{
			name: "stop jail with force flag",
			args: []string{"testjail"},
			flags: map[string]string{
				"force": "true",
			},
			mockClient: &mockStopClient{
				stopError: nil,
			},
			expectedOutput: []string{
				"Jail stopped: testjail",
			},
			expectError: false,
		},
		{
			name: "stop fails with API error",
			args: []string{"failjail"},
			mockClient: &mockStopClient{
				stopError: errors.New("connection refused"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "stop non-existent jail",
			args: []string{"nonexistent"},
			mockClient: &mockStopClient{
				stopError: errors.New("instance not found"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "stop already stopped jail",
			args: []string{"stoppedjail"},
			mockClient: &mockStopClient{
				stopError: errors.New("instance is already stopped"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Override the global API client
			oldClient := cmdutil.APIClient
			cmdutil.APIClient = tt.mockClient
			defer func() { cmdutil.APIClient = oldClient }()

			// Create command
			cmd := newStopCommand()
			cmd.SetArgs(tt.args)

			// Set flags
			for key, value := range tt.flags {
				if err := cmd.Flags().Set(key, value); err != nil {
					t.Fatalf("failed to set flag %s=%s: %v", key, value, err)
				}
			}

			// Capture output
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)
			cmd.SetErr(buf)

			// Execute command
			err := cmd.Execute()

			// Check error expectation
			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			// Both directions: asserting only the true case let a command that
			// always forces pass, which is the more dangerous of the two.
			if !tt.expectError {
				wantForce := tt.flags["force"] == "true"
				if tt.mockClient.gotForce != wantForce {
					t.Errorf("StopInstance called with force=%v, want %v", tt.mockClient.gotForce, wantForce)
				}
			}

			// Check output
			output := buf.String()
			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("expected output to contain %q, but got:\n%s", expected, output)
				}
			}
		})
	}
}

func TestStopCommand_NoArgs(t *testing.T) {
	cmd := newStopCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Errorf("expected error when no jail name provided, but got none")
	}
}
