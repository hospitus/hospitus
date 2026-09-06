package client

import (
	"testing"
	"time"
)

func TestMetricsTimeoutOutlastsPodmanStats(t *testing.T) {
	// Measured on FreeBSD 15.1: "podman stats --no-stream --format json web"
	// returns in about 47 seconds, twice in a row. A metrics request has to
	// outlast that, which the 30-second DefaultTimeout did not.
	const observedPodmanStats = 47 * time.Second

	if metricsTimeout <= observedPodmanStats {
		t.Errorf("metricsTimeout = %v, too short for podman stats at %v", metricsTimeout, observedPodmanStats)
	}
	if metricsTimeout <= DefaultTimeout {
		t.Errorf("metricsTimeout = %v, no longer than DefaultTimeout %v", metricsTimeout, DefaultTimeout)
	}
}
