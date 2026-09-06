package bhyve

import (
	"crypto/rand"
	"fmt"
)

// ensureNICMACs gives every tap a recorded guest MAC, and reports whether it had
// to assign any.
//
// The address has to be hospitus's own and has to persist: bhyve generates one at
// start and rewrites its process title, so nothing can read it back, and an
// address that changed on every start would break every lease and every ARP
// entry that refers to it.
func ensureNICMACs(config *vmConfig) (bool, error) {
	if len(config.TapDevs) == 0 {
		return false, nil
	}

	assigned := false
	for len(config.NICMACs) < len(config.TapDevs) {
		config.NICMACs = append(config.NICMACs, "")
	}
	for i := range config.TapDevs {
		if config.NICMACs[i] != "" {
			continue
		}
		mac, err := randomMAC()
		if err != nil {
			return assigned, err
		}
		config.NICMACs[i] = mac
		assigned = true
	}
	return assigned, nil
}

// randomMAC generates a locally administered unicast address, so two VMs on the
// same host never collide.
func randomMAC() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate a MAC address: %w", err)
	}
	buf[0] = (buf[0] | 0x02) &^ 0x01 // Locally administered, unicast.

	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5]), nil
}
