package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/hospitus/hospitus/pkg/logging"
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
//
// It covers the database and the state directory as well: those are separate
// flags, and a second daemon pointed at its own --data-dir but the same --db
// wrote to that database alongside the first with nothing in the way.
func lockDataDir(dataDir, dbPath, stateDir string) (func(), error) {
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, target := range lockTargets(dataDir, dbPath, stateDir) {
		unlock, err := lockOne(target.lockPath, target.name)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, unlock)
	}
	return release, nil
}

// lockTarget is one thing to claim: the file to flock, and the name to use
// when telling the operator who already holds it.
type lockTarget struct {
	lockPath string
	name     string
}

// lockTargets lists the distinct locks a daemon with these flags must hold.
//
// Deduplicated on the resolved path: with --state-dir inside --data-dir the
// same file would otherwise be flocked twice from this same process, and the
// second attempt returns EWOULDBLOCK — which would have this daemon report
// itself as the other daemon already holding the directory.
func lockTargets(dataDir, dbPath, stateDir string) []lockTarget {
	candidates := []lockTarget{
		{filepath.Join(dataDir, dataDirLockName), dataDir},
	}
	if dbPath != "" {
		// Beside the database file, not inside a directory it may share with
		// unrelated files.
		candidates = append(candidates, lockTarget{dbPath + ".lock", dbPath})
	}
	if stateDir != "" {
		candidates = append(candidates, lockTarget{filepath.Join(stateDir, dataDirLockName), stateDir})
	}

	seen := make(map[string]bool, len(candidates))
	var targets []lockTarget
	for _, c := range candidates {
		key := canonical(c.lockPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, c)
	}
	return targets
}

// canonical resolves a path as far as the filesystem allows, so that two names
// for one file compare equal.
//
// filepath.Abs and filepath.Clean are purely lexical: with /alias a symlink to
// /real, "/alias/hospitus.db" and "/real/hospitus.db" name one database and
// produced two different lock files, which is exactly the pair of daemons this
// lock exists to keep apart.
//
// EvalSymlinks fails outright when any component is missing, and on a first
// start nothing exists yet. So the deepest ancestor that *does* exist is what
// gets resolved, with the rest appended: resolving only the immediate parent
// left "/real/new" and "/alias/new" looking distinct until the moment lockOne
// created the directory, at which point the second lock reached the first
// one's file through the symlink and the daemon refused its own target.
func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	abs = filepath.Clean(abs)

	rest := ""
	for dir := abs; ; {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Walked up to the root without resolving anything: unreadable,
			// or not a real filesystem. The lexical form still locks, it is
			// merely less deduplicated.
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// lockOne takes the exclusive lock on a single lock file.
func lockOne(path, name string) (func(), error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", dir, err)
		}
	}

	// O_NOFOLLOW: a lock path under a directory someone else can write to may
	// already be a symlink, and the Truncate and PID write below would then
	// land on its target instead (CWE-59).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		// Two spellings of the same refusal: Linux and Darwin report ELOOP
		// for O_NOFOLLOW on a symlink, FreeBSD reports EMLINK (verified on
		// 15.1). Matching only ELOOP left the daemon's own platform with
		// "cannot open ...: too many links", which names neither the problem
		// nor the fix.
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, fmt.Errorf("refusing to use %s as a lock file: it is a symbolic link", path)
		}
		return nil, fmt.Errorf("cannot open %s: %w", path, err)
	}

	// O_NOFOLLOW only refuses a symlink at the final component; it says
	// nothing about a fifo or a device left there instead. This is not
	// belt-and-braces: measured on FreeBSD 15.1, flock() on a fifo *succeeds*,
	// so without this the daemon would hold what it thinks is its lock on
	// something it can never write a pid to, and the next daemon would see
	// nothing in its way. Darwin happens to refuse it at the flock with
	// EOPNOTSUPP, which is why the gap is invisible when testing there.
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("refusing to use %s as a lock file: it is not a regular file", path)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readLockHolder(f)
		_ = f.Close()
		// Only EWOULDBLOCK means the lock is held. Anything else — a
		// filesystem with no flock support, EINTR, a bad descriptor — was
		// reported as another daemon, and the operator went looking for a
		// process that does not exist.
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another hospitusd already owns %s%s: stop it before starting this one",
				name, holder)
		}
		return nil, fmt.Errorf("cannot lock %s: %w", name, err)
	}

	// Record who holds it, for the message the next daemon will print.
	//
	// A failure here is not fatal — the lock is held either way — but it is
	// said out loud: a stale pid leaves the next daemon naming a process that
	// is not the one holding the directory.
	if err := f.Truncate(0); err != nil {
		slog.Warn("could not record the lock holder", "path", path, logging.FieldError, err)
	} else if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		slog.Warn("could not record the lock holder", "path", path, logging.FieldError, err)
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
