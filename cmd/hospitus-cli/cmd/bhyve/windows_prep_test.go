package bhyve

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutounattendBypassXMLIsValidXML(t *testing.T) {
	var v interface{}
	if err := xml.Unmarshal([]byte(autounattendXML), &v); err != nil {
		t.Fatalf("autounattendXML is not valid XML: %v", err)
	}
}

func TestAutounattendBypassXMLContainsRequiredKeys(t *testing.T) {
	required := []string{
		"BypassTPMCheck",
		"BypassSecureBootCheck",
		"BypassRAMCheck",
		"LabConfig",
	}
	for _, key := range required {
		if !strings.Contains(autounattendXML, key) {
			t.Errorf("autounattendXML missing required key: %s", key)
		}
	}
}

func TestGenerateBypassISO(t *testing.T) {
	tool, args, err := findISOTool()
	if err != nil {
		t.Skipf("no ISO tool available, skipping: %v", err)
	}

	out := filepath.Join(t.TempDir(), "bypass.iso")
	if err := generateBypassISO(out, tool, args); err != nil {
		t.Fatalf("generateBypassISO: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("bypass ISO not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("bypass ISO is empty")
	}
}

func TestFindISOToolPrefersMkisofs(t *testing.T) {
	// Stub both mkisofs and xorriso in an isolated PATH so findISOTool's
	// preference order is exercised deterministically, without executing them.
	dir := t.TempDir()
	writeFakeTool := func(name string) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	writeFakeTool("mkisofs")
	writeFakeTool("xorriso")
	t.Setenv("PATH", dir)

	// Both present: mkisofs must win.
	tool, args, err := findISOTool()
	if err != nil {
		t.Fatalf("findISOTool: %v", err)
	}
	if filepath.Base(tool) != "mkisofs" {
		t.Fatalf("expected mkisofs to be preferred, got %q", tool)
	}
	if strings.Join(args, " ") != "-J -r" {
		t.Fatalf("expected mkisofs args [-J -r], got %v", args)
	}

	// mkisofs absent: must fall back to xorriso's mkisofs-compatible mode.
	if err := os.Remove(filepath.Join(dir, "mkisofs")); err != nil {
		t.Fatalf("remove fake mkisofs: %v", err)
	}
	tool, args, err = findISOTool()
	if err != nil {
		t.Fatalf("findISOTool fallback: %v", err)
	}
	if filepath.Base(tool) != "xorriso" {
		t.Fatalf("expected xorriso fallback, got %q", tool)
	}
	if strings.Join(args, " ") != "-as mkisofs -J -r" {
		t.Fatalf("expected xorriso args [-as mkisofs -J -r], got %v", args)
	}
}

func TestWindowsPrepCommandRegistered(t *testing.T) {
	root := NewBhyveCommand()
	for _, sub := range root.Commands() {
		if sub.Use == "windows-prep <vm-name>" {
			return
		}
	}
	t.Fatal("windows-prep command not registered under bhyve")
}
