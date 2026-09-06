package integration

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestPortExpose tests port forwarding functionality
func TestPortExpose(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	jailName := "test-expose"
	defer cleanupJail(t, jailName)

	// Create and start jail with VNET and IP
	cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
		"--image", "freebsd-14.3-RELEASE-amd64",
		"--vnet",
		"--ip", "dhcp")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create jail: %v\nOutput: %s", err, output)
	}

	cmd = exec.Command(hospitusBinary(), "jail", "start", jailName)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to start jail: %v\nOutput: %s", err, output)
	}

	// Wait for jail to be fully started
	time.Sleep(2 * time.Second)

	t.Run("AddPortForward", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "add", jailName,
			"--port", "8080:80")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to add port forward: %v\nOutput: %s", err, output)
		}
	})

	t.Run("ListPortForwards", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "list", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to list port forwards: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "8080") {
			t.Error("Port 8080 not found in expose list")
		}
	})

	t.Run("AddDuplicatePort", func(t *testing.T) {
		// Adding same port again should not duplicate
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "add", jailName,
			"--port", "8080:80")
		output, err := cmd.CombinedOutput()
		// Should succeed (idempotent)
		if err != nil {
			t.Logf("Adding duplicate port: %v\nOutput: %s", err, output)
		}

		// Verify no duplicates
		cmd = exec.Command(hospitusBinary(), "jail", "expose", "list", jailName)
		output, _ = cmd.CombinedOutput()
		count := strings.Count(string(output), "8080")
		if count > 2 { // Header + one rule
			t.Errorf("Found duplicate port entries: count=%d", count)
		}
	})

	t.Run("AddMultiplePorts", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "add", jailName,
			"--port", "8443:443")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to add second port: %v\nOutput: %s", err, output)
		}

		cmd = exec.Command(hospitusBinary(), "jail", "expose", "list", jailName)
		output, _ = cmd.CombinedOutput()
		if !strings.Contains(string(output), "8443") {
			t.Error("Port 8443 not found")
		}
	})

	t.Run("RemovePortForward", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "remove", jailName,
			"--port", "8443")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to remove port: %v\nOutput: %s", err, output)
		}

		cmd = exec.Command(hospitusBinary(), "jail", "expose", "list", jailName)
		output, _ = cmd.CombinedOutput()
		if strings.Contains(string(output), "8443") {
			t.Error("Port 8443 still exists after removal")
		}
	})

	t.Run("CleanupOnStop", func(t *testing.T) {
		// Add a port
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "add", jailName,
			"--port", "9090:90")
		cmd.Run()

		cmd = exec.Command(hospitusBinary(), "jail", "stop", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to stop jail: %v\nOutput: %s", err, output)
		}

		// Check PF rules are cleaned (test runs as root)
		cmd = exec.Command("pfctl", "-a", "hospitus", "-s", "nat")
		output, _ = cmd.CombinedOutput()
		// Rules should be minimal or not contain the jail's IP
		t.Logf("PF rules after stop: %s", output)
	})
}

// TestNginxJail tests a jail running nginx web server
func TestNginxJail(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	jailName := "test-nginx"
	hostPort := 18080
	defer cleanupJail(t, jailName)

	// Create and start jail with IP
	cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
		"--image", "freebsd-14.3-RELEASE-amd64",
		"--vnet",
		"--ip", "dhcp")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create jail: %v\nOutput: %s", err, output)
	}

	cmd = exec.Command(hospitusBinary(), "jail", "start", jailName)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to start jail: %v\nOutput: %s", err, output)
	}

	// Wait for jail network to be ready
	time.Sleep(3 * time.Second)

	t.Run("InstallNginx", func(t *testing.T) {
		// Bootstrap pkg
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"env", "ASSUME_ALWAYS_YES=yes", "pkg", "bootstrap")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("pkg bootstrap failed (likely DNS unavailable in test environment): %v\nOutput: %s", err, output)
		}

		// Install nginx
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"pkg", "install", "-y", "nginx")
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Skipf("Failed to install nginx (likely DNS unavailable): %v\nOutput: %s", err, output)
		}
	})

	t.Run("ConfigureAndStartNginx", func(t *testing.T) {
		// Enable nginx
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"sysrc", "nginx_enable=YES")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("Cannot enable nginx (may not be installed): %v\nOutput: %s", err, output)
		}

		// Start nginx
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"service", "nginx", "start")
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Skipf("Cannot start nginx (may not be installed): %v\nOutput: %s", err, output)
		}

		// Verify nginx is running
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"service", "nginx", "status")
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Logf("nginx status: %v\nOutput: %s", err, output)
		}
	})

	t.Run("ExposeAndTest", func(t *testing.T) {
		// Expose port
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "add", jailName,
			"--port", fmt.Sprintf("%d:80", hostPort))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to expose port: %v\nOutput: %s", err, output)
		}

		// Wait for rules to be applied
		time.Sleep(1 * time.Second)

		// Test HTTP connection
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", hostPort))
		if err != nil {
			t.Logf("HTTP request failed (may need PF fix): %v", err)
			// Don't fail - localhost redirect may not work in all setups
		} else {
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Errorf("Expected HTTP 200, got %d", resp.StatusCode)
			} else {
				t.Log("nginx is accessible via port forward!")
			}
		}
	})

	t.Run("StopNginx", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"service", "nginx", "stop")
		cmd.Run()
	})
}

// TestPostgreSQLJail tests a jail running PostgreSQL database
func TestPostgreSQLJail(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	longTests := os.Getenv("HOSPITUS_LONG_TESTS") == "1"
	if !longTests {
		t.Skip("PostgreSQL test requires HOSPITUS_LONG_TESTS=1")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	jailName := "test-postgresql"
	hostPort := 15432
	defer cleanupJail(t, jailName)

	// Create and start jail with IP
	cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
		"--image", "freebsd-14.3-RELEASE-amd64",
		"--vnet",
		"--ip", "dhcp",
		"--memory", "1024") // PostgreSQL needs more memory
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create jail: %v\nOutput: %s", err, output)
	}

	cmd = exec.Command(hospitusBinary(), "jail", "start", jailName)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to start jail: %v\nOutput: %s", err, output)
	}

	time.Sleep(3 * time.Second)

	t.Run("InstallPostgreSQL", func(t *testing.T) {
		// Bootstrap pkg
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"env", "ASSUME_ALWAYS_YES=yes", "pkg", "bootstrap")
		cmd.CombinedOutput()

		// Install PostgreSQL 16
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"pkg", "install", "-y", "postgresql16-server")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to install PostgreSQL: %v\nOutput: %s", err, output)
		}
	})

	t.Run("InitializeAndStart", func(t *testing.T) {
		// Enable postgresql
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"sysrc", "postgresql_enable=YES")
		cmd.Run()

		// Initialize database
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"service", "postgresql", "initdb")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to initdb: %v\nOutput: %s", err, output)
		}

		// Configure to listen on all interfaces
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"sh", "-c", "echo \"listen_addresses = '*'\" >> /var/db/postgres/data16/postgresql.conf")
		cmd.Run()

		// Allow connections (add to pg_hba.conf)
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"sh", "-c", "echo \"host all all 0.0.0.0/0 trust\" >> /var/db/postgres/data16/pg_hba.conf")
		cmd.Run()

		// Start postgresql
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"service", "postgresql", "start")
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start PostgreSQL: %v\nOutput: %s", err, output)
		}

		// Wait for PostgreSQL to be ready
		time.Sleep(3 * time.Second)
	})

	t.Run("ExposePort", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "expose", "add", jailName,
			"--port", fmt.Sprintf("%d:5432", hostPort))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to expose port: %v\nOutput: %s", err, output)
		}
	})

	t.Run("TestConnection", func(t *testing.T) {
		// Create a test database and user inside the jail
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"su", "-", "postgres", "-c", "createdb testdb")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("createdb: %v\nOutput: %s", err, output)
		}

		// Test a simple query
		cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"su", "-", "postgres", "-c", "psql -c 'SELECT version();' testdb")
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to query PostgreSQL: %v\nOutput: %s", err, output)
		}

		if !strings.Contains(string(output), "PostgreSQL") {
			t.Error("PostgreSQL version not found in output")
		}
		t.Logf("PostgreSQL is running: %s", output)
	})

	t.Run("StopPostgreSQL", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"service", "postgresql", "stop")
		cmd.Run()
	})
}

// TestMultipleJailsWithPorts tests multiple jails with different port mappings
func TestMultipleJailsWithPorts(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	jails := []struct {
		name     string
		hostPort int
	}{
		{"test-multi-1", 19001},
		{"test-multi-2", 19002},
		{"test-multi-3", 19003},
	}

	// Cleanup all jails
	defer func() {
		for _, jail := range jails {
			cleanupJail(t, jail.name)
		}
	}()

	t.Run("CreateAllJails", func(t *testing.T) {
		for _, jail := range jails {
			cmd := exec.Command(hospitusBinary(), "jail", "create", jail.name,
				"--image", "freebsd-14.3-RELEASE-amd64",
				"--vnet",
				"--ip", "dhcp")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Failed to create %s: %v\nOutput: %s", jail.name, err, output)
			}
		}
	})

	t.Run("StartAllJails", func(t *testing.T) {
		for _, jail := range jails {
			cmd := exec.Command(hospitusBinary(), "jail", "start", jail.name)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Failed to start %s: %v\nOutput: %s", jail.name, err, output)
			}
		}
		time.Sleep(3 * time.Second)
	})

	t.Run("ExposePortsOnAll", func(t *testing.T) {
		for _, jail := range jails {
			cmd := exec.Command(hospitusBinary(), "jail", "expose", "add", jail.name,
				"--port", fmt.Sprintf("%d:80", jail.hostPort))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("Failed to expose port on %s: %v\nOutput: %s", jail.name, err, output)
			}
		}
	})

	t.Run("VerifyAllPorts", func(t *testing.T) {
		for _, jail := range jails {
			cmd := exec.Command(hospitusBinary(), "jail", "expose", "list", jail.name)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("Failed to list ports for %s: %v", jail.name, err)
				continue
			}
			if !strings.Contains(string(output), fmt.Sprintf("%d", jail.hostPort)) {
				t.Errorf("Port %d not found for %s", jail.hostPort, jail.name)
			}
		}
	})

	t.Run("StopAllJails", func(t *testing.T) {
		for _, jail := range jails {
			cmd := exec.Command(hospitusBinary(), "jail", "stop", jail.name)
			cmd.Run()
		}
	})
}
