package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLockDataDirRefusesASecondDaemon is the situation this lock exists for: an
// older daemon left running after an upgrade, still owning the same records,
// state files and VM processes as the new one. Each behaves correctly on its
// own terms, so neither log explains the result.
func TestLockDataDirRefusesASecondDaemon(t *testing.T) {
	dir := t.TempDir()

	release, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	_, err = lockDataDir(dir)
	if err == nil {
		t.Fatal("a second daemon was allowed to take the same data directory")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error does not name the directory: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("error does not name the holder %s: %v", want, err)
	}
}

// TestLockDataDirIsReleased covers restarting a daemon: the successor must be
// able to take the directory the moment its predecessor lets go.
func TestLockDataDirIsReleased(t *testing.T) {
	dir := t.TempDir()

	release, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	release()

	release2, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}

// TestLockDataDirIgnoresAStaleFile covers the file left behind by a daemon that
// was killed. The lock lives on the descriptor, so the leftover must not stand
// in the way of a new start.
func TestLockDataDirIgnoresAStaleFile(t *testing.T) {
	dir := t.TempDir()

	stale := filepath.Join(dir, dataDirLockName)
	if err := os.WriteFile(stale, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("seed stale lock file: %v", err)
	}

	release, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("a leftover lock file blocked the start: %v", err)
	}
	release()
}
