package integration

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

// TestMain skips the whole package in short mode.
//
// These tests drive a real hospitusd against real jails, VMs and downloaded images:
// they need root, network access and several minutes. `make test` and the
// FreeBSD CI run `go test -short ./...`, where that work does not belong, while
// `make test-integration` runs without -short and executes them.
func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		fmt.Println("integration tests skipped in short mode (run `make test-integration`)")
		os.Exit(0)
	}

	os.Exit(m.Run())
}
