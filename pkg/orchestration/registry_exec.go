package orchestration

import (
	"context"
	"fmt"

	"github.com/hospitus/hospitus/pkg/provider"
)

// registryExecProvider runs a command inside an instance through the provider
// that owns it.
//
// A health checker holds one ExecProvider, while a stack's services can run
// under different providers. The instance handle names its own, so the dispatch
// is per instance rather than per checker.
type registryExecProvider struct {
	registry *provider.Registry
}

// NewRegistryExecProvider returns an ExecProvider that dispatches to whichever
// provider owns the instance being asked about.
func NewRegistryExecProvider(registry *provider.Registry) provider.ExecProvider {
	return registryExecProvider{registry: registry}
}

func (r registryExecProvider) execFor(handle provider.InstanceHandle) (provider.ExecProvider, error) {
	if r.registry == nil {
		return nil, fmt.Errorf("no provider registry to reach instance %s", handle.ID)
	}
	prov, err := r.registry.Get(handle.Provider)
	if err != nil {
		return nil, fmt.Errorf("provider %s is not available: %w", handle.Provider, err)
	}
	exec, ok := prov.(provider.ExecProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s cannot run a command inside an instance", handle.Provider)
	}
	return exec, nil
}

func (r registryExecProvider) ExecCommand(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) (*provider.ExecResult, error) {
	exec, err := r.execFor(handle)
	if err != nil {
		return nil, err
	}
	return exec.ExecCommand(ctx, handle, opts)
}

func (r registryExecProvider) ExecInteractive(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) error {
	exec, err := r.execFor(handle)
	if err != nil {
		return err
	}
	return exec.ExecInteractive(ctx, handle, opts)
}
