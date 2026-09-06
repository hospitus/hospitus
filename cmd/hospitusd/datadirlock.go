package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// dataDirLockName is the file whose lock marks a data directory as taken.
const dataDirLockName = "hospitusd.lock"

// lockDataDir takes an exclusive lock on the data directory and returns a
// function that releases it.
//
// Two daemons sharing a data directory do not merely duplicate work: they own
// the same instance records, the same provider state and the same VM
// processes, and each acts on what the other changes. An older daemon left
// running after an upgrade kept reconciling with its own idea of the world —
// killing processes the new one had no quarrel with, and restoring records the
// new one had removed. Neither daemon's log explains that, because from each
// one's side it is behaving correctly.
//
// The lock is advisory and held on an open descriptor, so it is released
// however the daemon dies, and a leftover file is never in the way.
func lockDataDir(dataDir string) (func(), error) {
	path := filepath.Join(dataDir, dataDirLockName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readLockHolder(f)
		_ = f.Close()
		return nil, fmt.Errorf("another hospitusd already owns %s%s: stop it before starting this one",
			dataDir, holder)
	}

	// Record who holds it, for the message the next daemon will print.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// readLockHolder returns " (pid N)" when the lock file names a process, or ""
// when it does not — the message reads naturally either way.
func readLockHolder(f *os.File) string {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	if n == 0 {
		return ""
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return ""
	}
	return fmt.Sprintf(" (pid %d)", pid)
}
