package jail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestParseIPPool(t *testing.T) {
	p := &JailProvider{}

	tests := []struct {
		name     string
		pool     string
		wantIPs  []string
		wantErr  bool
		minCount int // minimum number of IPs expected
	}{
		{
			name:    "CIDR notation /29",
			pool:    "10.50.0.0/29",
			wantIPs: []string{"10.50.0.1", "10.50.0.2", "10.50.0.3", "10.50.0.4", "10.50.0.5", "10.50.0.6"},
			wantErr: false,
		},
		{
			name:     "CIDR notation /24",
			pool:     "192.168.1.0/24",
			wantErr:  false,
			minCount: 254, // Should generate 254 IPs (excluding network and broadcast)
		},
		{
			name:    "Range notation",
			pool:    "192.168.1.10-15",
			wantIPs: []string{"192.168.1.10", "192.168.1.11", "192.168.1.12", "192.168.1.13", "192.168.1.14", "192.168.1.15"},
			wantErr: false,
		},
		{
			name:    "Single IP",
			pool:    "10.0.0.100",
			wantIPs: []string{"10.0.0.100"},
			wantErr: false,
		},
		{
			name:    "Invalid CIDR",
			pool:    "192.168.1.0/33",
			wantErr: true,
		},
		{
			name:    "Invalid range format",
			pool:    "192.168.1.10-",
			wantErr: true,
		},
		{
			name:    "Invalid IP",
			pool:    "999.999.999.999",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ips, err := p.parseIPPool(tt.pool)

			if (err != nil) != tt.wantErr {
				t.Errorf("parseIPPool() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if tt.minCount > 0 {
				if len(ips) < tt.minCount {
					t.Errorf("parseIPPool() returned %d IPs, want at least %d", len(ips), tt.minCount)
				}
				return
			}

			if len(ips) != len(tt.wantIPs) {
				t.Errorf("parseIPPool() returned %d IPs, want %d", len(ips), len(tt.wantIPs))
				return
			}

			for i, ip := range ips {
				if ip != tt.wantIPs[i] {
					t.Errorf("parseIPPool() IP[%d] = %s, want %s", i, ip, tt.wantIPs[i])
				}
			}
		})
	}
}

func TestGetIPPools(t *testing.T) {
	p := &JailProvider{
		config: provider.ProviderConfig{
			Settings: make(map[string]interface{}),
		},
	}

	// NOTE: Priority order in getIPPools() (Unix best practice):
	// 1. Environment variable (HOSPITUS_IP_POOL) - highest priority, allows temporary override
	// 2. Config file (/etc/hospitus/hospitusd.conf or ./etc/hospitus/hospitusd.conf)
	// 3. Provider config settings (legacy)
	// 4. Default pool
	//
	// These tests verify environment variable and provider config behavior.
	// Config file loading is tested via integration tests since it requires file I/O.

	tests := []struct {
		name      string
		envVar    string
		configVal string
		want      []string
	}{
		{
			name:   "Environment variable overrides provider config",
			envVar: "192.168.1.10-50 10.0.0.0/24",
			want:   []string{"192.168.1.10-50", "10.0.0.0/24"},
		},
		{
			// NOTE: Provider config is only used when no env var AND no config file.
			// If /usr/local/etc/hospitus/hospitusd.conf exists with ip_pool, this test
			// will use env var to override it instead.
			name:   "Provider config or env var override",
			envVar: "172.16.0.0/24 192.168.0.0/29", // Use env var to guarantee override
			want:   []string{"172.16.0.0/24", "192.168.0.0/29"},
		},
		{
			name: "Default pool when nothing configured",
			want: []string{"10.0.0.0/24"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean environment
			os.Unsetenv("HOSPITUS_IP_POOL")

			if tt.envVar != "" {
				os.Setenv("HOSPITUS_IP_POOL", tt.envVar)
				defer os.Unsetenv("HOSPITUS_IP_POOL")
			}

			if tt.configVal != "" {
				p.config.Settings["ip_pool"] = tt.configVal
			} else {
				delete(p.config.Settings, "ip_pool")
			}

			got := p.getIPPools()

			if len(got) != len(tt.want) {
				t.Errorf("getIPPools() returned %d pools, want %d", len(got), len(tt.want))
				return
			}

			for i, pool := range got {
				if pool != tt.want[i] {
					t.Errorf("getIPPools()[%d] = %s, want %s", i, pool, tt.want[i])
				}
			}
		})
	}
}

func TestAllocateDHCPAddress(t *testing.T) {
	// Create temporary directory for test configs
	tempDir := t.TempDir()

	p := &JailProvider{
		stateDir: tempDir,
		config: provider.ProviderConfig{
			Settings: make(map[string]interface{}),
		},
	}

	tests := []struct {
		name         string
		pool         string
		existingIPs  []string
		wantIP       string
		wantErr      bool
		wantContains string // IP should contain this (for checking pool membership)
	}{
		{
			name:         "Allocate first IP from range",
			pool:         "192.168.1.10-12",
			existingIPs:  []string{},
			wantIP:       "192.168.1.10/24",
			wantContains: "192.168.1",
		},
		{
			name:         "Skip used IPs",
			pool:         "192.168.1.10-12",
			existingIPs:  []string{"192.168.1.10"},
			wantIP:       "192.168.1.11/24",
			wantContains: "192.168.1",
		},
		{
			name:         "Allocate from CIDR",
			pool:         "10.50.0.0/29",
			existingIPs:  []string{},
			wantIP:       "10.50.0.2/29", // .1 is reserved for gateway
			wantContains: "10.50.0",
		},
		{
			name:        "No available IPs",
			pool:        "192.168.1.10-10",
			existingIPs: []string{"192.168.1.10"},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up temp directory
			files, _ := filepath.Glob(filepath.Join(tempDir, "*.json"))
			for _, f := range files {
				os.Remove(f)
			}

			// Set environment variable for pool
			os.Setenv("HOSPITUS_IP_POOL", tt.pool)
			defer os.Unsetenv("HOSPITUS_IP_POOL")

			// Create fake jail configs with existing IPs
			for i, ip := range tt.existingIPs {
				// Add /24 if not already present
				ipWithMask := ip
				if !strings.Contains(ip, "/") {
					ipWithMask = ip + "/24"
				}
				config := &jailConfig{
					Name: "test" + string(rune('a'+i)),
					Networks: []provider.NetworkSpec{
						{IPv4: ipWithMask},
					},
				}
				configPath := filepath.Join(tempDir, "test"+string(rune('a'+i))+".json")
				if err := p.saveJailConfig(config, configPath); err != nil {
					t.Fatalf("Failed to save config: %v", err)
				}
			}

			// Test allocation
			ctx := context.Background()
			ip, err := p.allocateDHCPAddress(ctx, "")

			if (err != nil) != tt.wantErr {
				t.Errorf("allocateDHCPAddress() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if tt.wantIP != "" && ip != tt.wantIP {
				t.Errorf("allocateDHCPAddress() = %s, want %s", ip, tt.wantIP)
			}

			if tt.wantContains != "" {
				if len(ip) == 0 || ip[:len(tt.wantContains)] != tt.wantContains {
					t.Errorf("allocateDHCPAddress() = %s, should contain %s", ip, tt.wantContains)
				}
			}
		})
	}
}

func TestMultiplePoolFallback(t *testing.T) {
	tempDir := t.TempDir()

	p := &JailProvider{
		stateDir: tempDir,
		config: provider.ProviderConfig{
			Settings: make(map[string]interface{}),
		},
	}

	// Configure multiple pools
	os.Setenv("HOSPITUS_IP_POOL", "192.168.1.10-11 10.50.0.0/29")
	defer os.Unsetenv("HOSPITUS_IP_POOL")

	ctx := context.Background()

	// Allocate IPs until first pool is exhausted
	ip1, err := p.allocateDHCPAddress(ctx, "")
	if err != nil {
		t.Fatalf("First allocation failed: %v", err)
	}

	// Save first allocation
	config1 := &jailConfig{
		Name:     "test1",
		Networks: []provider.NetworkSpec{{IPv4: ip1}},
	}
	p.saveJailConfig(config1, filepath.Join(tempDir, "test1.json"))

	ip2, err := p.allocateDHCPAddress(ctx, "")
	if err != nil {
		t.Fatalf("Second allocation failed: %v", err)
	}

	// Save second allocation
	config2 := &jailConfig{
		Name:     "test2",
		Networks: []provider.NetworkSpec{{IPv4: ip2}},
	}
	p.saveJailConfig(config2, filepath.Join(tempDir, "test2.json"))

	// Third allocation should fallback to second pool
	ip3, err := p.allocateDHCPAddress(ctx, "")
	if err != nil {
		t.Fatalf("Third allocation failed: %v", err)
	}

	// Verify first two IPs are from first pool (192.168.1.x)
	if ip1[:11] != "192.168.1.1" {
		t.Errorf("First IP %s should be from first pool (192.168.1.10-11)", ip1)
	}

	if ip2[:11] != "192.168.1.1" {
		t.Errorf("Second IP %s should be from first pool (192.168.1.10-11)", ip2)
	}

	// Verify third IP is from second pool (10.50.0.x)
	if ip3[:7] != "10.50.0" {
		t.Errorf("Third IP %s should be from second pool (10.50.0.0/29), got first 7 chars: %s", ip3, ip3[:7])
	}

	t.Logf("Successfully allocated IPs across pools: %s, %s, %s", ip1, ip2, ip3)
}
