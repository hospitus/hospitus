package jail

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hospitus/hospitus/pkg/provider"
)

var _ provider.ReconfigureProvider = (*JailProvider)(nil)

// Reconfigure writes a changed spec into the jail's own configuration.
//
// StartInstance reads that file, not the datastore, so a change recorded only
// there is reported as applied and comes back on the next start as it was: the
// jail keeps its old RCTL limits and its old parameters.
func (p *JailProvider) Reconfigure(ctx context.Context, handle provider.InstanceHandle, spec provider.InstanceSpec) error {
	_, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", handle.ID))
	config, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail configuration: %w", err)
	}

	if spec.CPUs > 0 {
		config.Resources.CPUs = spec.CPUs
		config.Spec.CPUs = spec.CPUs
	}
	if spec.MemoryMB > 0 {
		config.Resources.MemoryMB = spec.MemoryMB
		config.Spec.MemoryMB = spec.MemoryMB
	}

	if len(spec.ProviderConfig) > 0 {
		merged, err := ApplyJailParameterMap(config.JailParameters, spec.ProviderConfig)
		if err != nil {
			return fmt.Errorf("invalid jail parameter: %w", err)
		}
		config.JailParameters = merged
		if config.Parameters == nil {
			config.Parameters = map[string]interface{}{}
		}
		// The spec's own provider config is what the instance was created with.
		// Leaving it as it was makes the record disagree with the jail: an
		// operator reading it back is told a permission is on that the jail no
		// longer has.
		if config.Spec.ProviderConfig == nil {
			config.Spec.ProviderConfig = map[string]interface{}{}
		}
		for key, value := range jailParametersFrom(spec.ProviderConfig) {
			config.Parameters[key] = value
			config.Spec.ProviderConfig[key] = value
		}
	}

	if err := p.saveJailConfig(config, configPath); err != nil {
		return fmt.Errorf("failed to write jail configuration: %w", err)
	}
	return nil
}
