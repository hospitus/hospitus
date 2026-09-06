package jail

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockListClient is a mock API client for testing
type mockListClient struct {
	baseMockClient // embed base mock for default method stubs
	instances      []*datastore.Instance
	err            error
}

func (m *mockListClient) ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.instances, nil
}

func (m *mockListClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockListClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockListClient) VerifyBackup(_ context.Context, _ string) error     { return nil }

func (m *mockListClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockListClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockListClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockListClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockListClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }

func TestListCommand(t *testing.T) {
	tests := []struct {
		name           string
		instances      []*datastore.Instance
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "list multiple jails",
			instances: []*datastore.Instance{
				{
					ID:       "jail1",
					Name:     "jail1",
					Provider: "jail",
					State:    provider.StateRunning,
					Spec: provider.InstanceSpec{
						Name:     "jail1",
						CPUs:     2,
						MemoryMB: 1024,
					},
					CreatedAt: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
				},
				{
					ID:       "jail2",
					Name:     "jail2",
					Provider: "jail",
					State:    provider.StateStopped,
					Spec: provider.InstanceSpec{
						Name:     "jail2",
						CPUs:     1,
						MemoryMB: 512,
					},
					CreatedAt: time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC),
				},
			},
			expectedOutput: []string{
				"ID",
				"NAME",
				"PROVIDER",
				"STATE",
				"jail1",
				"jail2",
				"running",
				"stopped",
			},
			expectError: false,
		},
		{
			name:           "no jails found",
			instances:      []*datastore.Instance{},
			expectedOutput: []string{"No instances found"},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock client
			mockClient := &mockListClient{
				instances: tt.instances,
			}

			// Override the global API client
			oldClient := cmdutil.APIClient
			cmdutil.APIClient = mockClient
			defer func() { cmdutil.APIClient = oldClient }()

			// Create command
			cmd := newListCommand()

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

func TestListCommand_JSON_Output(t *testing.T) {
	mockClient := &mockListClient{
		instances: []*datastore.Instance{
			{
				ID:       "jail1",
				Name:     "jail1",
				Provider: "jail",
				State:    provider.StateRunning,
				Spec: provider.InstanceSpec{
					Name:     "jail1",
					CPUs:     2,
					MemoryMB: 1024,
				},
				CreatedAt: time.Now(),
			},
		},
	}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mockClient
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newListCommand()
	cmd.Flags().Set("output", "json")

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	output := buf.String()
	// Verify JSON output contains expected fields
	expectedFields := []string{"\"id\"", "\"name\"", "\"provider\"", "\"state\""}
	for _, field := range expectedFields {
		if !strings.Contains(output, field) {
			t.Errorf("expected JSON output to contain %s, but got:\n%s", field, output)
		}
	}
}
