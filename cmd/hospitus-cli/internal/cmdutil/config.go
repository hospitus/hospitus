package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the hospitus CLI configuration file (~/.config/hospitus/config.yaml).
//
// Multiple contexts allow the CLI to target different hospitusd instances:
//
//	hospitus context add prod --url https://hospitus.example.com:8443 --api-key <key>
//	hospitus context use prod
//	hospitus jail list   # now talks to the prod server
type Config struct {
	// CurrentContext is the name of the active context.
	CurrentContext string `yaml:"current_context"`

	// Contexts is the list of named server profiles.
	Contexts []Context `yaml:"contexts"`
}

// Context holds connection parameters for one hospitusd instance.
type Context struct {
	// Name uniquely identifies this context (e.g. "local", "prod", "staging").
	Name string `yaml:"name"`

	// URL is the base URL of the hospitusd daemon (e.g. "https://192.168.1.10:8443").
	// If the scheme is omitted it defaults to http, or to https when either
	// TLS option below is set.
	URL string `yaml:"url"`

	// APIKey is the secret sent as X-API-Key on every request.
	APIKey string `yaml:"api_key,omitempty"`

	// TLSSkipVerify disables TLS certificate verification.
	// Only safe on private/test networks.
	TLSSkipVerify bool `yaml:"tls_skip_verify,omitempty"`

	// TLSCACert is the path to a PEM CA certificate bundle for the server.
	TLSCACert string `yaml:"tls_ca_cert,omitempty"`
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "hospitus", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.config/hospitus/config.yaml"
	}
	return filepath.Join(home, ".config", "hospitus", "config.yaml")
}

// LoadConfig reads the config file, returning an empty default config if the
// file does not exist yet.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config %q: %w", path, err)
	}
	return &cfg, nil
}

// SaveConfig writes the config to disk, creating parent directories as needed.
func SaveConfig(path string, cfg *Config) error {
	if path == "" {
		path = DefaultConfigPath()
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write atomically via a temp file + rename so a crash never leaves a
	// truncated config, and so the final file always has 0600 permissions
	// (rename replaces any pre-existing, possibly world-readable, file).
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to set config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to write config %q: %w", path, err)
	}
	return nil
}

// ActiveContext returns the context matching cfg.CurrentContext, or nil if none
// is set / found.
func (cfg *Config) ActiveContext() *Context {
	if cfg.CurrentContext == "" {
		return nil
	}
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == cfg.CurrentContext {
			return &cfg.Contexts[i]
		}
	}
	return nil
}

// GetContext returns the context with the given name, or nil.
func (cfg *Config) GetContext(name string) *Context {
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == name {
			return &cfg.Contexts[i]
		}
	}
	return nil
}

// AddOrUpdateContext inserts or replaces the context with the same name.
func (cfg *Config) AddOrUpdateContext(ctx Context) {
	for i := range cfg.Contexts {
		if cfg.Contexts[i].Name == ctx.Name {
			cfg.Contexts[i] = ctx
			return
		}
	}
	cfg.Contexts = append(cfg.Contexts, ctx)
}

// RemoveContext deletes the context with the given name.
// Returns false if no such context exists.
func (cfg *Config) RemoveContext(name string) bool {
	for i, c := range cfg.Contexts {
		if c.Name == name {
			cfg.Contexts = append(cfg.Contexts[:i], cfg.Contexts[i+1:]...)
			return true
		}
	}
	return false
}
