package jail

import (
	"testing"
)

func TestServiceActionConstants(t *testing.T) {
	tests := []struct {
		action   ServiceAction
		expected string
	}{
		{ServiceActionStart, "start"},
		{ServiceActionStop, "stop"},
		{ServiceActionRestart, "restart"},
		{ServiceActionReload, "reload"},
		{ServiceActionStatus, "status"},
	}

	for _, tt := range tests {
		if string(tt.action) != tt.expected {
			t.Errorf("ServiceAction %s = %s, expected %s", tt.action, string(tt.action), tt.expected)
		}
	}
}

func TestServiceInfoStruct(t *testing.T) {
	info := ServiceInfo{
		Name:        "nginx",
		Enabled:     true,
		Running:     true,
		Description: "nginx web server",
		RCScript:    "/usr/local/etc/rc.d/nginx",
	}

	if info.Name != "nginx" {
		t.Error("ServiceInfo Name mismatch")
	}
	if !info.Enabled {
		t.Error("ServiceInfo Enabled should be true")
	}
	if !info.Running {
		t.Error("ServiceInfo Running should be true")
	}
	if info.RCScript != "/usr/local/etc/rc.d/nginx" {
		t.Error("ServiceInfo RCScript mismatch")
	}
}

func TestServiceInfoDisabled(t *testing.T) {
	info := ServiceInfo{
		Name:    "sendmail",
		Enabled: false,
		Running: false,
	}

	if info.Enabled {
		t.Error("ServiceInfo Enabled should be false")
	}
	if info.Running {
		t.Error("ServiceInfo Running should be false")
	}
}

func TestServiceInfoEnabledNotRunning(t *testing.T) {
	// Service enabled but not started yet
	info := ServiceInfo{
		Name:     "postgresql",
		Enabled:  true,
		Running:  false,
		RCScript: "/usr/local/etc/rc.d/postgresql",
	}

	if !info.Enabled {
		t.Error("ServiceInfo Enabled should be true")
	}
	if info.Running {
		t.Error("ServiceInfo Running should be false")
	}
}

func TestServiceInfoRunningNotEnabled(t *testing.T) {
	// Manually started service but not enabled at boot
	info := ServiceInfo{
		Name:     "sshd",
		Enabled:  false,
		Running:  true,
		RCScript: "/etc/rc.d/sshd",
	}

	if info.Enabled {
		t.Error("ServiceInfo Enabled should be false")
	}
	if !info.Running {
		t.Error("ServiceInfo Running should be true")
	}
}

func TestServiceInfoMultiple(t *testing.T) {
	services := []ServiceInfo{
		{Name: "nginx", Enabled: true, Running: true, RCScript: "/usr/local/etc/rc.d/nginx"},
		{Name: "postgresql", Enabled: true, Running: true, RCScript: "/usr/local/etc/rc.d/postgresql"},
		{Name: "redis", Enabled: true, Running: false, RCScript: "/usr/local/etc/rc.d/redis"},
		{Name: "sendmail", Enabled: false, Running: false, RCScript: "/etc/rc.d/sendmail"},
	}

	if len(services) != 4 {
		t.Errorf("Expected 4 services, got %d", len(services))
	}

	enabledCount := 0
	for _, svc := range services {
		if svc.Enabled {
			enabledCount++
		}
	}
	if enabledCount != 3 {
		t.Errorf("Expected 3 enabled services, got %d", enabledCount)
	}

	runningCount := 0
	for _, svc := range services {
		if svc.Running {
			runningCount++
		}
	}
	if runningCount != 2 {
		t.Errorf("Expected 2 running services, got %d", runningCount)
	}
}

func TestServiceInfoBaseVsLocal(t *testing.T) {
	// Base system service
	base := ServiceInfo{
		Name:     "sshd",
		RCScript: "/etc/rc.d/sshd",
	}

	// Installed package service
	local := ServiceInfo{
		Name:     "nginx",
		RCScript: "/usr/local/etc/rc.d/nginx",
	}

	if base.RCScript == local.RCScript {
		t.Error("Base and local rc scripts should be different")
	}

	// Check path prefixes
	if base.RCScript[:9] != "/etc/rc.d" {
		t.Error("Base service should be in /etc/rc.d")
	}
	if local.RCScript[:19] != "/usr/local/etc/rc.d" {
		t.Error("Local service should be in /usr/local/etc/rc.d")
	}
}
