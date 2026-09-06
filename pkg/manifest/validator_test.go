package manifest

import (
	"testing"
)

// TestValidatorHooks covers the hooks that provision an instance after it is
// created.
//
// apply runs a hook only when its type is "exec". Anything else is skipped
// with a line on stdout, and the run still finishes with "Post-create hooks
// completed successfully" — so a manifest that omitted the type validated,
// applied, and provisioned nothing, while reporting success at every step.
// Validation is where that should be caught.
func TestValidatorHooks(t *testing.T) {
	base := func(hooks []HookSpec) *ParsedManifest {
		return &ParsedManifest{
			Workload: &WorkloadManifest{
				Workload:  WorkloadMeta{APIVersion: "hospitus.io/v1", Name: "test-app"},
				Provider:  ProviderSpec{Type: "jail"},
				Image:     ImageSpec{Source: "freebsd:14.3-RELEASE", Arch: "amd64"},
				Resources: ResourceSpec{CPU: 1, Memory: "512Mi"},
				Lifecycle: LifecycleSpec{Hooks: &HooksConfig{PostCreate: hooks}},
			},
		}
	}

	tests := []struct {
		name    string
		hooks   []HookSpec
		wantErr bool
	}{
		{
			name:    "exec hook with commands",
			hooks:   []HookSpec{{Type: "exec", Commands: []string{"echo hello"}}},
			wantErr: false,
		},
		{
			name:    "missing type",
			hooks:   []HookSpec{{Commands: []string{"echo hello"}}},
			wantErr: true,
		},
		{
			// "shell" was described in the struct comment for a long time and
			// never implemented; naming it must fail rather than vanish.
			name:    "type that was never implemented",
			hooks:   []HookSpec{{Type: "shell", Commands: []string{"echo hello"}}},
			wantErr: true,
		},
		{
			name:    "no commands to run",
			hooks:   []HookSpec{{Type: "exec"}},
			wantErr: true,
		},
		{
			name:    "unknown on_failure",
			hooks:   []HookSpec{{Type: "exec", Commands: []string{"echo hello"}, OnFailure: "retry"}},
			wantErr: true,
		},
		{
			name:    "on_failure continue",
			hooks:   []HookSpec{{Type: "exec", Commands: []string{"echo hello"}, OnFailure: "continue"}},
			wantErr: false,
		},
	}

	validator := NewValidator()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validator.Validate(base(tt.hooks))
			if got := len(errs) > 0; got != tt.wantErr {
				t.Errorf("Validate() errors = %v, want error: %v", errs, tt.wantErr)
			}
		})
	}
}

func TestValidatorWorkload(t *testing.T) {
	validator := NewValidator()

	validManifest := &ParsedManifest{
		Workload: &WorkloadManifest{
			Workload: WorkloadMeta{
				APIVersion: "hospitus.io/v1",
				Name:       "test-app",
			},
			Provider: ProviderSpec{
				Type: "jail",
			},
			Image: ImageSpec{
				Source: "freebsd:14.3-RELEASE",
				Arch:   "amd64",
			},
			Resources: ResourceSpec{
				CPU:    2,
				Memory: "1Gi",
			},
		},
	}

	errs := validator.Validate(validManifest)
	if errs.HasErrors() {
		t.Errorf("Expected no errors for valid manifest, got: %v", errs)
	}
}

func TestValidatorRequiredFields(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name       string
		manifest   *ParsedManifest
		expectErrs int
	}{
		{
			name: "missing name",
			manifest: &ParsedManifest{
				Workload: &WorkloadManifest{
					Provider: ProviderSpec{Type: "jail"},
					Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
				},
			},
			expectErrs: 1,
		},
		{
			name: "missing provider",
			manifest: &ParsedManifest{
				Workload: &WorkloadManifest{
					Workload: WorkloadMeta{Name: "test"},
					Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
				},
			},
			expectErrs: 1,
		},
		{
			name: "missing image",
			manifest: &ParsedManifest{
				Workload: &WorkloadManifest{
					Workload: WorkloadMeta{Name: "test"},
					Provider: ProviderSpec{Type: "jail"},
				},
			},
			expectErrs: 1,
		},
		{
			name: "unknown provider",
			manifest: &ParsedManifest{
				Workload: &WorkloadManifest{
					Workload: WorkloadMeta{Name: "test"},
					Provider: ProviderSpec{Type: "unknown"},
					Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
				},
			},
			expectErrs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validator.Validate(tt.manifest)
			if len(errs) != tt.expectErrs {
				t.Errorf("Expected %d errors, got %d: %v", tt.expectErrs, len(errs), errs)
			}
		})
	}
}

func TestValidatorImageProviderCompat(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name      string
		provider  string
		image     string
		expectErr bool
	}{
		{"jail+freebsd", "jail", "freebsd:14.3-RELEASE", false},
		{"jail+oci", "jail", "oci:nginx:latest", true},
		{"podman+oci", "podman", "oci:nginx:latest", false},
		{"podman+freebsd", "podman", "freebsd:14.3-RELEASE", true},
		{"bhyve+freebsd", "bhyve", "freebsd:14.3-RELEASE", false},
		{"bhyve+cloud", "bhyve", "cloud:ubuntu-24.04", false},
		{"bhyve+iso", "bhyve", "iso:FreeBSD-14.3-dvd1", false},
		{"qemu+cloud", "qemu", "cloud:debian-12", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := &ParsedManifest{
				Workload: &WorkloadManifest{
					Workload: WorkloadMeta{Name: "test"},
					Provider: ProviderSpec{Type: tt.provider},
					Image:    ImageSpec{Source: tt.image},
				},
			}

			errs := validator.Validate(manifest)
			hasErr := errs.HasErrors()
			if tt.expectErr && !hasErr {
				t.Error("Expected validation error")
			}
			if !tt.expectErr && hasErr {
				t.Errorf("Unexpected validation error: %v", errs)
			}
		})
	}
}

func TestValidatorNetworkConfig(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name      string
		network   NetworkSpec
		expectErr bool
	}{
		{
			name:      "valid bridge",
			network:   NetworkSpec{Name: "primary", Type: "bridge"},
			expectErr: false,
		},
		{
			name:      "missing name",
			network:   NetworkSpec{Type: "bridge"},
			expectErr: true,
		},
		{
			name:      "invalid type",
			network:   NetworkSpec{Name: "test", Type: "invalid"},
			expectErr: true,
		},
		{
			name: "valid static IP",
			network: NetworkSpec{
				Name: "test",
				Type: "bridge",
				IP: &IPConfig{
					Mode:    "static",
					Address: "10.0.0.100/24",
				},
			},
			expectErr: false,
		},
		{
			name: "static IP without address",
			network: NetworkSpec{
				Name: "test",
				Type: "bridge",
				IP: &IPConfig{
					Mode: "static",
				},
			},
			expectErr: true,
		},
		{
			name: "invalid CIDR",
			network: NetworkSpec{
				Name: "test",
				Type: "bridge",
				IP: &IPConfig{
					Mode:    "static",
					Address: "invalid",
				},
			},
			expectErr: true,
		},
		{
			name: "valid port",
			network: NetworkSpec{
				Name: "test",
				Type: "bridge",
				Ports: []PortConfig{
					{Host: 80, Container: 8080, Protocol: "tcp"},
				},
			},
			expectErr: false,
		},
		{
			name: "invalid port",
			network: NetworkSpec{
				Name: "test",
				Type: "bridge",
				Ports: []PortConfig{
					{Host: 0, Container: 8080},
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := &ParsedManifest{
				Workload: &WorkloadManifest{
					Workload: WorkloadMeta{Name: "test"},
					Provider: ProviderSpec{Type: "jail"},
					Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
					Networks: []NetworkSpec{tt.network},
				},
			}

			errs := validator.Validate(manifest)
			hasErr := errs.HasErrors()
			if tt.expectErr && !hasErr {
				t.Error("Expected validation error")
			}
			if !tt.expectErr && hasErr {
				t.Errorf("Unexpected validation error: %v", errs)
			}
		})
	}
}

func TestValidatorResourceLimits(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name      string
		resources ResourceSpec
		expectErr bool
	}{
		{
			name:      "valid resources",
			resources: ResourceSpec{CPU: 4, Memory: "4Gi"},
			expectErr: false,
		},
		{
			name:      "negative CPU",
			resources: ResourceSpec{CPU: -1, Memory: "1Gi"},
			expectErr: true,
		},
		{
			name:      "too many CPUs",
			resources: ResourceSpec{CPU: 500, Memory: "1Gi"},
			expectErr: true,
		},
		{
			name:      "invalid memory format",
			resources: ResourceSpec{CPU: 1, Memory: "invalid"},
			expectErr: true,
		},
		{
			name:      "memory too low",
			resources: ResourceSpec{CPU: 1, Memory: "32Mi"},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := &ParsedManifest{
				Workload: &WorkloadManifest{
					Workload:  WorkloadMeta{Name: "test"},
					Provider:  ProviderSpec{Type: "jail"},
					Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
					Resources: tt.resources,
				},
			}

			errs := validator.Validate(manifest)
			hasErr := errs.HasErrors()
			if tt.expectErr && !hasErr {
				t.Error("Expected validation error")
			}
			if !tt.expectErr && hasErr {
				t.Errorf("Unexpected validation error: %v", errs)
			}
		})
	}
}

func TestValidatorStackDependencies(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name      string
		instances []InstanceConfig
		expectErr bool
	}{
		{
			name: "valid dependencies",
			instances: []InstanceConfig{
				{Name: "db", Provider: "jail", Image: ImageSpec{Source: "freebsd:14.3-RELEASE"}},
				{
					Name:     "app",
					Provider: "jail",
					Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{
						Services: []string{"db"},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "unknown dependency",
			instances: []InstanceConfig{
				{Name: "db", Provider: "jail", Image: ImageSpec{Source: "freebsd:14.3-RELEASE"}},
				{
					Name:     "app",
					Provider: "jail",
					Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{
						Services: []string{"nonexistent"},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "self dependency",
			instances: []InstanceConfig{
				{
					Name:     "app",
					Provider: "jail",
					Image:    ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{
						Services: []string{"app"},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "duplicate instance names",
			instances: []InstanceConfig{
				{Name: "db", Provider: "jail", Image: ImageSpec{Source: "freebsd:14.3-RELEASE"}},
				{Name: "db", Provider: "jail", Image: ImageSpec{Source: "freebsd:14.3-RELEASE"}},
			},
			expectErr: true,
		},
		{
			name: "two-node dependency cycle",
			instances: []InstanceConfig{
				{
					Name:      "a",
					Provider:  "jail",
					Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{Services: []string{"b"}},
				},
				{
					Name:      "b",
					Provider:  "jail",
					Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{Services: []string{"a"}},
				},
			},
			expectErr: true,
		},
		{
			name: "three-node dependency cycle",
			instances: []InstanceConfig{
				{
					Name:      "a",
					Provider:  "jail",
					Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{Services: []string{"b"}},
				},
				{
					Name:      "b",
					Provider:  "jail",
					Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{Services: []string{"c"}},
				},
				{
					Name:      "c",
					Provider:  "jail",
					Image:     ImageSpec{Source: "freebsd:14.3-RELEASE"},
					DependsOn: &DependsOnConfig{Services: []string{"a"}},
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := &ParsedManifest{
				Stack: &StackManifest{
					Stack:     StackMeta{Name: "test-stack"},
					Instances: tt.instances,
				},
			}

			errs := validator.Validate(manifest)
			hasErr := errs.HasErrors()
			if tt.expectErr && !hasErr {
				t.Error("Expected validation error")
			}
			if !tt.expectErr && hasErr {
				t.Errorf("Unexpected validation error: %v", errs)
			}
		})
	}
}

func TestIsValidName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"web-app", true},
		{"myapp", true},
		{"a", true},
		{"app123", true},
		{"my-web-app", true},
		{"", false},
		{"-app", false},
		{"app-", false},
		{"App", true}, // Will be lowercased
		{"app_name", false},
		{"app.name", false},
		{"app name", false},
		{string(make([]byte, 64)), false}, // Too long
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidName(tt.name)
			if result != tt.valid {
				t.Errorf("isValidName(%q) = %v, want %v", tt.name, result, tt.valid)
			}
		})
	}
}
