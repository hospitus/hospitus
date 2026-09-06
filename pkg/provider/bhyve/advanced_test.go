package bhyve

import (
	"strings"
	"testing"
)

func TestBhyvePCISlots(t *testing.T) {
	p := NewBhyveProvider()
	config := &vmConfig{
		Name:      "test-vm",
		DiskPaths: []string{"disk0.img"},
		TapDevs:   []string{"tap0"},
		PCISlots:  make(map[string]string),
	}

	// First build should assign default slots
	// disks are processed first, then network interfaces
	// disk0: slot = 0 + 4 = 4:0
	// nic0:  slot = len(disks) + 0 + 4 = 1 + 0 + 4 = 5:0
	_, _ = p.buildBhyveArgs(config)

	slot0 := config.PCISlots["disk0"]
	if slot0 != "4:0" {
		t.Errorf("Expected disk0 to be at 4:0, got %s", slot0)
	}

	nic0Slot := config.PCISlots["nic0"]
	if nic0Slot != "5:0" {
		t.Errorf("Expected nic0 to be at 5:0, got %s", nic0Slot)
	}

	// Add another disk and build again
	// Now: disk0 (4:0), disk1 (5:0), nic0 (len(disks=2) + 0 + 4 = 6:0)
	config.DiskPaths = append(config.DiskPaths, "disk1.img")
	config.DiskDrivers = append(config.DiskDrivers, "virtio-blk")
	_, _ = p.buildBhyveArgs(config)

	// disk0 should stay at 4:0
	if config.PCISlots["disk0"] != "4:0" {
		t.Errorf("disk0 slot changed to %s", config.PCISlots["disk0"])
	}

	// disk1 gets slot = 1 + 4 = 5:0
	if config.PCISlots["disk1"] != "5:0" {
		t.Errorf("Expected disk1 to be at 5:0, got %s", config.PCISlots["disk1"])
	}

	// nic0 gets recalculated: len(disks=2) + 0 + 4 = 6:0
	if config.PCISlots["nic0"] != "6:0" {
		t.Errorf("Expected nic0 to be at 6:0 (recalculated with 2 disks), got %s", config.PCISlots["nic0"])
	}
}

func TestBhyveSectorSize(t *testing.T) {
	p := NewBhyveProvider()
	config := &vmConfig{
		Name:        "test-vm",
		MemoryMB:    1024,
		CPUs:        1,
		DiskPaths:   []string{"disk0.img", "disk1.img"},
		DiskDrivers: []string{"virtio-blk", "virtio-blk"},
		DiskSectors: []int{512, 4096},
	}

	args, _ := p.buildBhyveArgs(config)

	// Find the disk arguments
	disk0Found := false
	disk1Found := false
	for i, arg := range args {
		if arg == "-s" && i+1 < len(args) {
			diskOpt := args[i+1]
			if strings.Contains(diskOpt, "disk0.img") {
				if !strings.Contains(diskOpt, "sectorsize=512") {
					t.Errorf("disk0 should have sectorsize=512, got: %s", diskOpt)
				}
				disk0Found = true
			}
			if strings.Contains(diskOpt, "disk1.img") {
				if !strings.Contains(diskOpt, "sectorsize=4096") {
					t.Errorf("disk1 should have sectorsize=4096, got: %s", diskOpt)
				}
				disk1Found = true
			}
		}
	}

	if !disk0Found {
		t.Error("disk0 argument not found")
	}
	if !disk1Found {
		t.Error("disk1 argument not found")
	}
}
