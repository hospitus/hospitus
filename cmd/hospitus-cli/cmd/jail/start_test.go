package jail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
)

// mockStartClient is a mock API client for testing start operations
type mockStartClient struct {
	baseMockClient // embed base mock for default method stubs
	startError     error
}

func (m *mockStartClient) StartInstance(ctx context.Context, id string, _ io.Writer) error {
	return m.startError
}

// A real instance, provider included: returning nil made the resolver report
// no provider, which a jail command must refuse rather than act on.
func (m *mockStartClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return &datastore.Instance{ID: id, Name: id, Provider: "jail"}, nil
}

func (m *mockStartClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockStartClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockStartClient) VerifyBackup(_ context.Context, _ string) error     { return nil }

func (m *mockStartClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockStartClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockStartClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockStartClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockStartClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }

func TestStartCommand(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		mockClient     *mockStartClient
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "start jail successfully",
			args: []string{"testjail"},
			mockClient: &mockStartClient{
				startError: nil,
			},
			expectedOutput: []string{
				"Jail started: testjail",
			},
			expectError: false,
		},
		{
			name: "start fails with API error",
			args: []string{"failjail"},
			mockClient: &mockStartClient{
				startError: errors.New("connection refused"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "start non-existent jail",
			args: []string{"nonexistent"},
			mockClient: &mockStartClient{
				startError: errors.New("instance not found"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			name: "start already running jail",
			args: []string{"runningjail"},
			mockClient: &mockStartClient{
				startError: errors.New("instance is already running"),
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
			cmd := newStartCommand()
			cmd.SetArgs(tt.args)

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

func TestStartCommand_NoArgs(t *testing.T) {
	cmd := newStartCommand()
	cmd.SetArgs([]string{})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err == nil {
		t.Errorf("expected error when no jail name provided, but got none")
	}
}
