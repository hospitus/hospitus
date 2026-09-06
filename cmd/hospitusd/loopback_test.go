package main

import "testing"

// TestLoopbackListenAddr pins the boundary of --allow-no-auth and
// --allow-insecure-tls. An empty host binds every interface and must not pass.
func TestLoopbackListenAddr(t *testing.T) {
	loopback := []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.2:1"}
	for _, addr := range loopback {
		if !loopbackListenAddr(addr) {
			t.Errorf("%q should count as loopback", addr)
		}
	}
	exposed := []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.168.1.16:8080", "hospitus.example.org:8443", "garbage"}
	for _, addr := range exposed {
		if loopbackListenAddr(addr) {
			t.Errorf("%q must not count as loopback", addr)
		}
	}
}
