// Package cloudinit provides cloud-init configuration generation for VMs.
// This package is used by bhyve and QEMU providers to configure VMs at boot.
package cloudinit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// Config represents cloud-init configuration
type Config struct {
	// Instance metadata
	InstanceID    string
	LocalHostname string

	// Network configuration
	Networks []NetworkConfig

	// User configuration
	Users []UserConfig

	// Package installation
	Packages []string

	// Commands to run at boot
	RunCMD []string

	// SSH authorized keys (for default user)
	SSHAuthorizedKeys []string

	// Custom user-data (raw string, will be appended)
	CustomUserData string
}

// NetworkConfig represents a network interface configuration
type NetworkConfig struct {
	Name       string // Interface name (e.g., "eth0", "ens3")
	Type       string // "dhcp" or "static"
	Address    string // IP address with CIDR (e.g., "10.0.0.100/24")
	Gateway    string // Default gateway
	DNS        []string
	MACAddress string
}

// UserConfig represents a user to create
type UserConfig struct {
	Name   string
	Groups []string
	Shell  string
	Sudo   string // e.g., "ALL=(ALL) NOPASSWD:ALL"
	// Doas is a FreeBSD doas rule string; %u is replaced with the username.
	// e.g., "permit nopass %u as root"
	Doas              string
	SSHAuthorizedKeys []string
	LockPasswd        bool
	// PlainTextPasswd sets a plaintext password (nuageinit: plain_text_passwd).
	// For Linux cloud-init use passwd (hashed) instead.
	PlainTextPasswd string
}

// Generator creates cloud-init ISO images
type Generator struct {
	// mkisofs or genisoimage path
	mkisofsBin string
}

// NewGenerator creates a new cloud-init generator
func NewGenerator() (*Generator, error) {
	// Try mkisofs first, then genisoimage
	mkisofsBin, err := exec.LookPath("mkisofs")
	if err != nil {
		mkisofsBin, err = exec.LookPath("genisoimage")
		if err != nil {
			return nil, fmt.Errorf("neither mkisofs nor genisoimage found in PATH")
		}
	}

	return &Generator{
		mkisofsBin: mkisofsBin,
	}, nil
}

// GenerateISO creates a cloud-init ISO at the specified path
func (g *Generator) GenerateISO(ctx context.Context, config *Config, outputPath string) error {
	// Create temporary directory for cloud-init files
	tmpDir, err := os.MkdirTemp("", "cloudinit-")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	metaData, err := g.generateMetaData(config)
	if err != nil {
		return fmt.Errorf("failed to generate meta-data: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "meta-data"), []byte(metaData), 0o600); err != nil {
		return fmt.Errorf("failed to write meta-data: %w", err)
	}

	userData, err := g.generateUserData(config)
	if err != nil {
		return fmt.Errorf("failed to generate user-data: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "user-data"), []byte(userData), 0o600); err != nil {
		return fmt.Errorf("failed to write user-data: %w", err)
	}

	// Generate network-config (if networks specified)
	if len(config.Networks) > 0 {
		networkConfig, err := g.generateNetworkConfig(config)
		if err != nil {
			return fmt.Errorf("failed to generate network-config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "network-config"), []byte(networkConfig), 0o600); err != nil {
			return fmt.Errorf("failed to write network-config: %w", err)
		}
	}

	// Create ISO using mkisofs/genisoimage
	args := []string{
		"-output", outputPath,
		"-volid", "cidata",
		"-joliet",
		"-rock",
		tmpDir,
	}

	cmd := exec.CommandContext(ctx, g.mkisofsBin, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create ISO: %w (output: %s)", err, string(output))
	}

	return nil
}

// generateMetaData generates the meta-data file content
func (g *Generator) generateMetaData(config *Config) (string, error) {
	var buf bytes.Buffer
	if err := metaDataTmpl.Execute(&buf, config); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// generateUserData generates the user-data file content
func (g *Generator) generateUserData(config *Config) (string, error) {
	var buf bytes.Buffer
	if err := userDataTmpl.Execute(&buf, config); err != nil {
		return "", err
	}

	// SECURITY: Append CustomUserData literally after template rendering to
	// prevent template injection via user-supplied Go template syntax ({{ }}).
	result := buf.String()
	if config.CustomUserData != "" {
		result += "\n" + config.CustomUserData
	}

	return result, nil
}

// generateNetworkConfig generates the network-config file content (v2 format)
func (g *Generator) generateNetworkConfig(config *Config) (string, error) {
	var buf bytes.Buffer
	if err := networkConfigTmpl.Execute(&buf, config); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// Cloud-init templates are compiled once at package load instead of on every
// call. CustomUserData is intentionally not part of userDataTmpl; it is
// appended literally to prevent Go-template injection.
var (
	metaDataTmpl = template.Must(template.New("meta-data").Parse(`instance-id: {{.InstanceID}}
local-hostname: {{.LocalHostname}}
`))

	userDataTmpl = template.Must(template.New("user-data").Funcs(template.FuncMap{
		"join": strings.Join,
	}).Parse(`#cloud-config
{{- if .Users}}
users:
{{- range .Users}}
  - name: {{.Name}}
{{- if .Groups}}
    groups: {{join .Groups ", "}}
{{- end}}
{{- if .Shell}}
    shell: {{.Shell}}
{{- end}}
{{- if .Sudo}}
    sudo: {{.Sudo}}
{{- end}}
{{- if .Doas}}
    doas: {{.Doas}}
{{- end}}
{{- if .PlainTextPasswd}}
    plain_text_passwd: {{.PlainTextPasswd}}
{{- end}}
{{- if .LockPasswd}}
    lock_passwd: true
{{- end}}
{{- if .SSHAuthorizedKeys}}
    ssh_authorized_keys:
{{- range .SSHAuthorizedKeys}}
      - {{.}}
{{- end}}
{{- end}}
{{- end}}
{{- end}}
{{- if .SSHAuthorizedKeys}}
ssh_authorized_keys:
{{- range .SSHAuthorizedKeys}}
  - {{.}}
{{- end}}
{{- end}}
{{- if .Packages}}
packages:
{{- range .Packages}}
  - {{.}}
{{- end}}
{{- end}}
{{- if .RunCMD}}
runcmd:
{{- range .RunCMD}}
  - {{.}}
{{- end}}
{{- end}}
`))

	networkConfigTmpl = template.Must(template.New("network-config").Parse(`version: 2
ethernets:
{{- range .Networks}}
  {{.Name}}:
{{- if eq .Type "dhcp"}}
    dhcp4: true
{{- else}}
    dhcp4: false
    addresses:
      - {{.Address}}
{{- if .Gateway}}
    routes:
      - to: default
        via: {{.Gateway}}
{{- end}}
{{- if .DNS}}
    nameservers:
      addresses:
{{- range .DNS}}
        - {{.}}
{{- end}}
{{- end}}
{{- end}}
{{- if .MACAddress}}
    match:
      macaddress: {{.MACAddress}}
{{- end}}
{{- end}}
`))
)

// ConfigFromSpec creates a cloud-init Config from common VM parameters
func ConfigFromSpec(instanceID, hostname string, networks []NetworkConfig, sshKeys []string) *Config {
	config := &Config{
		InstanceID:    instanceID,
		LocalHostname: hostname,
		Networks:      networks,
	}

	// Add default user with SSH keys if provided
	if len(sshKeys) > 0 {
		config.Users = []UserConfig{
			{
				Name:              "hospitus",
				Groups:            []string{"wheel", "sudo"},
				Shell:             "/bin/sh",
				Sudo:              "ALL=(ALL) NOPASSWD:ALL",
				SSHAuthorizedKeys: sshKeys,
				LockPasswd:        true,
			},
		}
		config.SSHAuthorizedKeys = sshKeys
	}

	return config
}

// NetworkFromProviderSpec converts provider.NetworkSpec style config to NetworkConfig
func NetworkFromProviderSpec(name, ipv4, gateway string, dns []string, mac string) NetworkConfig {
	nc := NetworkConfig{
		Name:       name,
		MACAddress: mac,
	}

	if ipv4 == "" || ipv4 == "dhcp" {
		nc.Type = "dhcp"
	} else {
		nc.Type = "static"
		nc.Address = ipv4
		nc.Gateway = gateway
		nc.DNS = dns
	}

	return nc
}
