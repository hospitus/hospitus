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

func (m *mockCreateClient) ListInstances(ctx context.Context, filter client.ListInstancesFilter) ([]*datastore.Instance, error) {
	return nil, nil
}

func (m *mockCreateClient) GetInstance(ctx context.Context, id string) (*datastore.Instance, error) {
	return m.createdInstance, nil
}

func (m *mockCreateClient) UpdateInstance(ctx context.Context, id string, req client.UpdateInstanceRequest) (*datastore.Instance, error) {
	return m.createdInstance, nil
}

func (m *mockCreateClient) StopInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockCreateClient) RestartInstance(ctx context.Context, id string) error {
	return nil
}

func (m *mockCreateClient) DeleteInstance(ctx context.Context, id string, force bool) error {
	return nil
}

func (m *mockCreateClient) FetchImage(ctx context.Context, version string, _ io.Writer) error {
	return nil
}

func (m *mockCreateClient) ListImages(ctx context.Context) ([]map[string]interface{}, error) {
	return nil, nil
}

func (m *mockCreateClient) DeleteImage(ctx context.Context, imageName string) error {
	return nil
}

func (m *mockCreateClient) ExportInstance(ctx context.Context, instanceID string, opts client.ExportOptions) (*client.ExportResult, error) {
	return nil, nil
}

func (m *mockCreateClient) ImportInstance(ctx context.Context, opts client.ImportOptions) (*client.ImportResult, error) {
	return nil, nil
}

func (m *mockCreateClient) ExposePort(ctx context.Context, instanceName string, req client.ExposePortRequest) (*client.ExposePortResult, error) {
	return nil, nil
}

func (m *mockCreateClient) UnexposePort(ctx context.Context, instanceName string, hostPort int, protocol string) error {
	return nil
}

func (m *mockCreateClient) ListExposedPorts(ctx context.Context, instanceName string) ([]client.PortMapping, error) {
	return nil, nil
}

func (m *mockCreateClient) ExecCommand(ctx context.Context, instanceID string, req client.ExecRequest) (*client.ExecResult, error) {
	return nil, nil
}

func (m *mockCreateClient) ExecCommandStream(ctx context.Context, instanceID string, req client.ExecRequest, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}

func (m *mockCreateClient) ListSnapshots(ctx context.Context, instanceID string) ([]client.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockCreateClient) CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockCreateClient) DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockCreateClient) RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	return nil
}

func (m *mockCreateClient) CloneInstance(ctx context.Context, instanceID string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockCreateClient) CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts client.CloneOptions) (*client.CloneResult, error) {
	return nil, nil
}

func (m *mockCreateClient) GetInstanceMetrics(ctx context.Context, instanceID string) (*client.InstanceMetrics, error) {
	return nil, nil
}

func (m *mockCreateClient) GetInstanceHealth(ctx context.Context, instanceID string) (*client.InstanceHealth, error) {
	return nil, nil
}

func (m *mockCreateClient) EjectMedia(ctx context.Context, instanceID string, deviceID string) error {
	return nil
}

func (m *mockCreateClient) InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error {
	return nil
}

func (m *mockCreateClient) ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error) {
	return nil, nil
}

func (m *mockCreateClient) SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error {
	return nil
}

func (m *mockCreateClient) GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error) {
	return nil, nil
}

func (m *mockCreateClient) ListAutoStart(ctx context.Context) ([]client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockCreateClient) GetAutoStart(ctx context.Context, providerName, instanceID string) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockCreateClient) SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*client.AutoStartInfo, error) {
	return nil, nil
}

func (m *mockCreateClient) DisableAutoStart(ctx context.Context, providerName, instanceID string) error {
	return nil
}

func (m *mockCreateClient) ListBackups(_ context.Context, _ string) ([]client.BackupInfo, error) {
	return nil, nil
}

func (m *mockCreateClient) GetBackup(_ context.Context, _ string) (*client.BackupInfo, error) {
	return nil, nil
}

func (m *mockCreateClient) CreateBackup(_ context.Context, _, _ string) (*client.BackupInfo, error) {
	return nil, nil
}
func (m *mockCreateClient) DeleteBackup(_ context.Context, _ string) error     { return nil }
func (m *mockCreateClient) RestoreBackup(_ context.Context, _, _ string) error { return nil }
func (m *mockCreateClient) VerifyBackup(_ context.Context, _ string) error     { return nil }
func (m *mockCreateClient) SetBackupConfig(_ context.Context, _ string, _ client.BackupConfigRequest) error {
	return nil
}

func (m *mockCreateClient) GetBackupConfig(_ context.Context, _ string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *mockCreateClient) ListJobs(_ context.Context, _ string) (*client.JobListResponse, error) {
	return &client.JobListResponse{}, nil
}
func (m *mockCreateClient) GetJob(_ context.Context, _ string) (*job.Job, error) { return nil, nil }
func (m *mockCreateClient) CancelJob(_ context.Context, _ string) error          { return nil }
func (m *mockCreateClient) DeleteJob(_ context.Context, _ string) error          { return nil }
func (m *mockCreateClient) JobStats(_ context.Context) (map[string]int, error)   { return nil, nil }
func (m *mockCreateClient) ListPortForwardsVM(_ context.Context, _ string) ([]provider.PortForward, error) {
	return nil, nil
}

func (m *mockCreateClient) AddPortForwardVM(_ context.Context, _ string, _ provider.PortForward) error {
	return nil
}

func (m *mockCreateClient) RemovePortForwardVM(_ context.Context, _ string, _ string, _ int) error {
	return nil
}

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
	cmd.Flags().Set("vnet", "true")
	cmd.Flags().Set("bridge", "bridge0")
	cmd.Flags().Set("ip", "192.168.1.10/24")

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
