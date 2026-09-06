package jail

import (
	"strings"
	"testing"
	"time"
)

func TestJailMetricsStruct(t *testing.T) {
	metrics := JailMetrics{
		Name:          "testjail",
		State:         "running",
		CPUPercent:    25.5,
		CPUTimeUsed:   3600,
		MemoryUsed:    536870912,  // 512MB
		MemoryLimit:   1073741824, // 1GB
		ProcessCount:  10,
		ThreadCount:   20,
		DiskUsed:      10737418240, // 10GB
		DiskAvailable: 5368709120,  // 5GB
		DiskQuota:     21474836480, // 20GB
		Uptime:        86400,       // 1 day
		CollectedAt:   time.Now(),
	}

	if metrics.Name != "testjail" {
		t.Error("JailMetrics Name mismatch")
	}
	if metrics.State != "running" {
		t.Error("JailMetrics State mismatch")
	}
	if metrics.CPUPercent != 25.5 {
		t.Error("JailMetrics CPUPercent mismatch")
	}
	if metrics.MemoryUsed != 536870912 {
		t.Error("JailMetrics MemoryUsed mismatch")
	}
	if metrics.ProcessCount != 10 {
		t.Error("JailMetrics ProcessCount mismatch")
	}
}

func TestFormatPrometheusMetrics(t *testing.T) {
	metrics := []JailMetrics{
		{
			Name:          "web",
			State:         "running",
			CPUPercent:    10.5,
			CPUTimeUsed:   1800,
			MemoryUsed:    268435456, // 256MB
			MemoryLimit:   536870912, // 512MB
			ProcessCount:  5,
			ThreadCount:   15,
			DiskUsed:      5368709120,  // 5GB
			DiskAvailable: 10737418240, // 10GB
			Uptime:        3600,
		},
		{
			Name:         "db",
			State:        "running",
			CPUPercent:   45.0,
			MemoryUsed:   1073741824, // 1GB
			MemoryLimit:  2147483648, // 2GB
			ProcessCount: 3,
			DiskUsed:     21474836480, // 20GB
			Uptime:       7200,
		},
		{
			Name:  "stopped-jail",
			State: "stopped",
		},
	}

	output := FormatPrometheusMetrics(metrics)

	// Check required sections exist
	if !strings.Contains(output, "# HELP hospitus_jail_info") {
		t.Error("Missing hospitus_jail_info HELP")
	}
	if !strings.Contains(output, "# TYPE hospitus_jail_info gauge") {
		t.Error("Missing hospitus_jail_info TYPE")
	}
	if !strings.Contains(output, "hospitus_jail_info{name=\"web\"") {
		t.Error("Missing web jail info")
	}
	if !strings.Contains(output, "hospitus_jail_info{name=\"db\"") {
		t.Error("Missing db jail info")
	}

	// Check CPU metrics
	if !strings.Contains(output, "# HELP hospitus_jail_cpu_percent") {
		t.Error("Missing CPU percent HELP")
	}
	if !strings.Contains(output, "hospitus_jail_cpu_percent{name=\"web\"} 10.50") {
		t.Error("Missing or incorrect web CPU percent")
	}

	// Check memory metrics
	if !strings.Contains(output, "# HELP hospitus_jail_memory_bytes") {
		t.Error("Missing memory bytes HELP")
	}
	if !strings.Contains(output, "hospitus_jail_memory_bytes{name=\"web\"} 268435456") {
		t.Error("Missing or incorrect web memory bytes")
	}

	// Check memory limit metrics
	if !strings.Contains(output, "hospitus_jail_memory_limit_bytes{name=\"web\"} 536870912") {
		t.Error("Missing or incorrect web memory limit")
	}

	// Check disk metrics
	if !strings.Contains(output, "# HELP hospitus_jail_disk_used_bytes") {
		t.Error("Missing disk used HELP")
	}
	if !strings.Contains(output, "hospitus_jail_disk_used_bytes{name=\"web\"} 5368709120") {
		t.Error("Missing or incorrect web disk used")
	}

	// Check uptime metrics
	if !strings.Contains(output, "# HELP hospitus_jail_uptime_seconds") {
		t.Error("Missing uptime HELP")
	}
	if !strings.Contains(output, "hospitus_jail_uptime_seconds{name=\"web\"} 3600") {
		t.Error("Missing or incorrect web uptime")
	}

	// Check process count
	if !strings.Contains(output, "hospitus_jail_processes{name=\"web\"} 5") {
		t.Error("Missing or incorrect web process count")
	}
}

func TestFormatPrometheusMetricsEmpty(t *testing.T) {
	metrics := []JailMetrics{}
	output := FormatPrometheusMetrics(metrics)

	// Should still have headers but no data
	if !strings.Contains(output, "# HELP hospitus_jail_info") {
		t.Error("Missing hospitus_jail_info HELP even for empty metrics")
	}
}

func TestFormatPrometheusMetricsNetwork(t *testing.T) {
	metrics := []JailMetrics{
		{
			Name:             "web",
			State:            "running",
			NetworkRxBytes:   1073741824, // 1GB
			NetworkTxBytes:   536870912,  // 512MB
			NetworkRxPackets: 1000000,
			NetworkTxPackets: 500000,
		},
	}

	output := FormatPrometheusMetrics(metrics)

	if !strings.Contains(output, "hospitus_jail_network_receive_bytes_total{name=\"web\"} 1073741824") {
		t.Error("Missing or incorrect network rx bytes")
	}
	if !strings.Contains(output, "hospitus_jail_network_transmit_bytes_total{name=\"web\"} 536870912") {
		t.Error("Missing or incorrect network tx bytes")
	}
	if !strings.Contains(output, "hospitus_jail_network_receive_packets_total{name=\"web\"} 1000000") {
		t.Error("Missing or incorrect network rx packets")
	}
	if !strings.Contains(output, "hospitus_jail_network_transmit_packets_total{name=\"web\"} 500000") {
		t.Error("Missing or incorrect network tx packets")
	}
}

func TestFormatPrometheusMetricsIO(t *testing.T) {
	metrics := []JailMetrics{
		{
			Name:      "db",
			State:     "running",
			ReadBPS:   104857600, // 100MB/s
			WriteBPS:  52428800,  // 50MB/s
			ReadIOPS:  10000,
			WriteIOPS: 5000,
			OpenFiles: 256,
		},
	}

	output := FormatPrometheusMetrics(metrics)

	if !strings.Contains(output, "hospitus_jail_io_read_bytes_per_second{name=\"db\"} 104857600") {
		t.Error("Missing or incorrect read BPS")
	}
	if !strings.Contains(output, "hospitus_jail_io_write_bytes_per_second{name=\"db\"} 52428800") {
		t.Error("Missing or incorrect write BPS")
	}
	if !strings.Contains(output, "hospitus_jail_io_read_ops_per_second{name=\"db\"} 10000") {
		t.Error("Missing or incorrect read IOPS")
	}
	if !strings.Contains(output, "hospitus_jail_io_write_ops_per_second{name=\"db\"} 5000") {
		t.Error("Missing or incorrect write IOPS")
	}
	if !strings.Contains(output, "hospitus_jail_open_files{name=\"db\"} 256") {
		t.Error("Missing or incorrect open files")
	}
}
