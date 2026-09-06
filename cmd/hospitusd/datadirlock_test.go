package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLockDataDirRefusesASecondDaemon is the situation this lock exists for: an
// older daemon left running after an upgrade, still owning the same records,
// state files and VM processes as the new one. Each behaves correctly on its
// own terms, so neither log explains the result.
func TestLockDataDirRefusesASecondDaemon(t *testing.T) {
	dir := t.TempDir()

	release, err := lockDataDir(dir, "", "")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	_, err = lockDataDir(dir, "", "")
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

	release, err := lockDataDir(dir, "", "")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	release()

	release2, err := lockDataDir(dir, "", "")
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

	release, err := lockDataDir(dir, "", "")
	if err != nil {
		t.Fatalf("a leftover lock file blocked the start: %v", err)
	}
	release()
}

// A second daemon pointed at its own data directory but the same database
// used to start alongside the first and write to it.
func TestLockDataDirCoversTheDatabaseAndStateDir(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hospitus.db")
	state := t.TempDir()

	release, err := lockDataDir(t.TempDir(), db, state)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	if _, err := lockDataDir(t.TempDir(), db, t.TempDir()); err == nil {
		t.Error("a second daemon sharing only the database was allowed to start")
	}
	if _, err := lockDataDir(t.TempDir(), filepath.Join(t.TempDir(), "other.db"), state); err == nil {
		t.Error("a second daemon sharing only the state directory was allowed to start")
	}
}

// flock is per-file, not per-descriptor: locking the same path twice from this
// one process returns EWOULDBLOCK, which would have the daemon report itself
// as the other daemon holding the directory.
func TestLockDataDirDeduplicatesOverlappingPaths(t *testing.T) {
	dir := t.TempDir()

	release, err := lockDataDir(dir, filepath.Join(dir, "hospitus.db"), dir)
	if err != nil {
		t.Fatalf("data dir used as its own state dir: %v", err)
	}
	release()
}

// TestLockDataDirSeesThroughASymlink: with /alias a symlink to /real, naming
// the data directory one way and the state directory the other names one
// directory twice. Abs and Clean are lexical and saw two, so the daemon
// flocked the same file twice and the second attempt returned EWOULDBLOCK —
// reporting *itself* as the other daemon already holding the directory.
//
// Keeping two daemons apart needs no help from this: the kernel resolves both
// paths to one inode, so flock refuses the second daemon either way. It is
// the self-collision that canonicalization fixes.
func TestLockDataDirSeesThroughASymlink(t *testing.T) {
	realDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	release, err := lockDataDir(realDir, filepath.Join(t.TempDir(), "hospitus.db"), alias)
	if err != nil {
		t.Fatalf("a daemon blocked itself on one directory reached by two names: %v", err)
	}
	release()
}

// TestLockDataDirSeesThroughASymlinkBeforeTheDirectoriesExist is the first
// start of a daemon whose paths do not exist yet.
//
// EvalSymlinks fails when any component is missing, so resolving only the
// immediate parent left /real/new and /alias/new looking distinct. Both were
// kept as targets; lockOne then created the directory for the first, and the
// second reached that same lock file through the symlink and got EWOULDBLOCK —
// the daemon refusing its own second target on a fresh install.
func TestLockDataDirSeesThroughASymlinkBeforeTheDirectoriesExist(t *testing.T) {
	realDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	// "new" deliberately does not exist yet under either name.
	dataDir := filepath.Join(realDir, "new")
	stateDir := filepath.Join(alias, "new")

	release, err := lockDataDir(dataDir, filepath.Join(t.TempDir(), "hospitus.db"), stateDir)
	if err != nil {
		t.Fatalf("a daemon refused its own second target on a first start: %v", err)
	}
	release()
}

// TestLockOneRefusesASymlink: the lock path may sit in a directory someone
// else can write to, and the Truncate and PID write would land on the
// symlink's target instead (CWE-59).
func TestLockOneRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "other-file")
	const untouched = "do not modify\n"
	if err := os.WriteFile(target, []byte(untouched), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "hospitusd.lock")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	err := func() error { _, e := lockOne(lockPath, dir); return e }()
	if err == nil {
		t.Fatal("the lock followed a symlink and wrote through it")
	}
	// The message, not merely a failure: the errno differs by platform
	// (ELOOP on Linux and Darwin, EMLINK on FreeBSD) and only one of them was
	// matched, so the daemon's own platform got the generic "cannot open".
	if !strings.Contains(err.Error(), "it is a symbolic link") {
		t.Errorf("the symlink should be named as the reason, got: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != untouched {
		t.Errorf("the symlink's target was modified: %q", content)
	}
}

// A lock path that is a fifo is refused too: O_NOFOLLOW says nothing about it,
// and opening one for writing blocks until a reader shows up.
func TestLockOneRefusesANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "hospitusd.lock")
	if err := syscall.Mkfifo(lockPath, 0o600); err != nil {
		t.Skipf("mkfifo unavailable here: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := lockOne(lockPath, dir)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a fifo was accepted as a lock file")
		}
		// The reason matters, and it differs by platform. Darwin refuses the
		// fifo at the flock with EOPNOTSUPP, so "some error occurred" would
		// pass there with the check removed and prove nothing. FreeBSD — the
		// platform the daemon runs on — takes the flock happily: verified on
		// 15.1, removing the check makes lockOne return success on a fifo.
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("the fifo should be refused for what it is, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lockOne blocked on the fifo instead of refusing it")
	}
}
