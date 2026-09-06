package jail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
)

// mockCreateClient is a mock API client for testing create operations
type mockCreateClient struct {
	baseMockClient  // embed base mock for default method stubs
	createdInstance *datastore.Instance
	createError     error
	startError      error
	startCalled     bool
	lastCreateReq   client.CreateInstanceRequest // records the request sent
}

func (m *mockCreateClient) CreateInstance(ctx context.Context, req client.CreateInstanceRequest) (*datastore.Instance, error) {
	m.lastCreateReq = req
	if m.createError != nil {
		return nil, m.createError
	}
	if m.createdInstance != nil {
		return m.createdInstance, nil
	}
	// Default response
	return &datastore.Instance{
		ID:        req.Spec.Name,
		Name:      req.Spec.Name,
		Provider:  req.Provider,
		State:     provider.StateStopped,
		Spec:      req.Spec,
		CreatedAt: time.Now(),
	}, nil
}

func (m *mockCreateClient) StartInstance(ctx context.Context, id string, _ io.Writer) error {
	m.startCalled = true
	return m.startError
}

func (m *mockCreateClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return m.createdInstance, nil
}

func (m *mockCreateClient) UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return m.createdInstance, nil
}

func (m *mockCreateClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockCreateClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockCreateClient) VerifyBackup(_ context.Context, _ string) error     { return nil }

func (m *mockCreateClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockCreateClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockCreateClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockCreateClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockCreateClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }

func TestCreateCommand_Basic(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		flags          map[string]string
		mockClient     *mockCreateClient
		expectedOutput []string
		expectError    bool
	}{
		{
			name: "create basic jail",
			args: []string{"testjail"},
			mockClient: &mockCreateClient{
				createdInstance: &datastore.Instance{
					ID:       "testjail",
					Name:     "testjail",
					Provider: "jail",
					State:    provider.StateStopped,
					Spec: provider.InstanceSpec{
						Name:     "testjail",
						CPUs:     1,
						MemoryMB: 512,
					},
					CreatedAt: time.Now(),
				},
			},
			expectedOutput: []string{
				"Creating jail testjail",
				"Jail created: testjail",
				"(ID: testjail)",
			},
			expectError: false,
		},
		{
			name: "create jail with custom resources",
			args: []string{"resourcejail"},
			flags: map[string]string{
				"cpus":   "4",
				"memory": "2048",
				"image":  "14.1-RELEASE-amd64",
			},
			mockClient: &mockCreateClient{
				createdInstance: &datastore.Instance{
					ID:        "resourcejail",
					Name:      "resourcejail",
					Provider:  "jail",
					State:     provider.StateStopped,
					CreatedAt: time.Now(),
				},
			},
			expectedOutput: []string{
				"Creating jail resourcejail",
				"Jail created: resourcejail",
			},
			expectError: false,
		},
		{
			name: "create jail with vnet",
			args: []string{"vnetjail"},
			flags: map[string]string{
				"vnet":   "true",
				"bridge": "bridge0",
				"ip":     "10.0.0.5/24",
			},
			mockClient: &mockCreateClient{},
			expectedOutput: []string{
				"Creating jail vnetjail",
				"Jail created: vnetjail",
			},
			expectError: false,
		},
		{
			name: "create jail and start it",
			args: []string{"autojail"},
			flags: map[string]string{
				"start": "true",
			},
			mockClient: &mockCreateClient{
				createdInstance: &datastore.Instance{
					ID:        "autojail",
					Name:      "autojail",
					Provider:  "jail",
					State:     provider.StateRunning,
					CreatedAt: time.Now(),
				},
			},
			expectedOutput: []string{
				"Creating jail autojail",
				"Starting jail autojail",
				"Jail started: autojail",
			},
			expectError: false,
		},
		{
			name: "create fails with API error",
			args: []string{"failjail"},
			mockClient: &mockCreateClient{
				createError: errors.New("API connection failed"),
			},
			expectedOutput: []string{},
			expectError:    true,
		},
		{
			// --auto-start is CBSD's astart: it records that the host should
			// bring this jail up at boot. It must not start it now — the two
			// were one flag once, and the name meant the wrong thing.
			name: "auto-start configures boot, it does not start now",
			args: []string{"bootjail"},
			flags: map[string]string{
				"auto-start": "true",
			},
			mockClient: &mockCreateClient{
				createdInstance: &datastore.Instance{
					ID:        "bootjail",
					Name:      "bootjail",
					Provider:  "jail",
					State:     provider.StateStopped,
					CreatedAt: time.Now(),
				},
			},
			expectedOutput: []string{
				"Jail created: bootjail",
				"auto-start:",
			},
			expectError: false,
		},
		{
			name: "create succeeds but start fails",
			args: []string{"starterror"},
			flags: map[string]string{
				"start": "true",
			},
			mockClient: &mockCreateClient{
				createdInstance: &datastore.Instance{
					ID:        "starterror",
					Name:      "starterror",
					Provider:  "jail",
					State:     provider.StateStopped,
					CreatedAt: time.Now(),
				},
				startError: errors.New("failed to start jail"),
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

			cmd := newCreateCommand()
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

			// Check output
			output := buf.String()
			for _, expected := range tt.expectedOutput {
				if !strings.Contains(output, expected) {
					t.Errorf("expected output to contain %q, but got:\n%s", expected, output)
				}
			}

			// --start means start it now; --auto-start means start it at host
			// boot and must not reach StartInstance here.
			if tt.flags["start"] == "true" && !tt.expectError && !tt.mockClient.startCalled {
				t.Error("--start did not start the jail")
			}
			if tt.flags["auto-start"] == "true" && tt.mockClient.startCalled {
				t.Error("--auto-start started the jail now; it only configures host boot")
			}
		})
	}
}

func TestCreateCommand_NetworkConfiguration(t *testing.T) {
	mockClient := &mockCreateClient{}

	oldClient := cmdutil.APIClient
	cmdutil.APIClient = mockClient
	defer func() { cmdutil.APIClient = oldClient }()

	cmd := newCreateCommand()
	cmd.SetArgs([]string{"netjail"})
	// Checked: a renamed or missing flag otherwise surfaced much later, as a
	// puzzling assertion failure on the spec.
	for _, f := range []struct{ name, value string }{
		{"vnet", "true"},
		{"bridge", "bridge0"},
		{"ip", "192.168.1.10/24"},
	} {
		if err := cmd.Flags().Set(f.name, f.value); err != nil {
			t.Fatalf("set --%s: %v", f.name, err)
		}
	}

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Creating jail netjail") {
		t.Errorf("expected creation message, got: %s", output)
	}

	// Verify the network configuration was actually sent in the request.
	spec := mockClient.lastCreateReq.Spec
	if got := spec.ProviderConfig["vnet"]; got != true {
		t.Errorf("expected vnet=true in provider config, got %v", got)
	}
	if len(spec.Networks) != 1 {
		t.Fatalf("expected exactly one network spec, got %d", len(spec.Networks))
	}
	net := spec.Networks[0]
	if net.Bridge != "bridge0" {
		t.Errorf("expected bridge %q, got %q", "bridge0", net.Bridge)
	}
	if net.IPv4 != "192.168.1.10/24" {
		t.Errorf("expected IPv4 %q, got %q", "192.168.1.10/24", net.IPv4)
	}
}
