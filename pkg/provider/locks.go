package provider

import (
	"context"
	"sort"
	"sync"
)

// InstanceLocks serializes operations that act on the same instance.
//
// Providers keep instance state in files and in ZFS, and read it, change it and
// write it back. Two requests for the same instance arriving together — a stop
// while a snapshot runs, two starts, a destroy racing a rename — interleave
// those steps and the last writer wins, silently.
//
// The zero value is ready to use.
type InstanceLocks struct {
	mu    sync.Mutex
	locks map[string]*instanceLock
}

// instanceLock holds one instance's lock and the number of callers that want
// it, so an idle entry can be dropped instead of accumulating one map entry per
// instance the daemon has ever seen.
type instanceLock struct {
	ch   chan struct{} // capacity 1; a token in the channel means the lock is held
	refs int
}

// heldLocks is the context key under which Acquire records the instances the
// current call chain already holds.
type heldLocks struct{ name string }

// Acquire serializes work on the named instance until the returned function is
// called.
//
// The returned context records that this lock is held, and Acquire on the same
// name with that context is a no-op. That is what lets a method built out of
// other locked methods — restart is stop then start — hold the instance for the
// whole operation instead of deadlocking on itself. It also means a goroutine
// that outlives the call and keeps the context is not serialized against it;
// background work takes its own lock.
//
// Waiting is cancellable: a caller whose context ends while another operation
// holds the instance gets that context's error and acquires nothing.
func (l *InstanceLocks) Acquire(ctx context.Context, name string) (context.Context, func(), error) {
	if ctx.Value(heldLocks{name}) != nil {
		return ctx, func() {}, nil
	}

	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*instanceLock)
	}
	entry, ok := l.locks[name]
	if !ok {
		entry = &instanceLock{ch: make(chan struct{}, 1)}
		l.locks[name] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case entry.ch <- struct{}{}:
	case <-ctx.Done():
		l.drop(name, entry)
		return ctx, func() {}, ctx.Err()
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			<-entry.ch
			l.drop(name, entry)
		})
	}
	return context.WithValue(ctx, heldLocks{name}, struct{}{}), release, nil
}

// drop forgets an instance nobody is holding or waiting for.
func (l *InstanceLocks) drop(name string, entry *instanceLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.locks, name)
	}
}

// AcquireAll serializes work on several instances at once, for an operation
// that touches more than one — a clone reads its source while writing its
// target, a rename frees one name while taking another.
//
// Names are sorted before locking, so two operations naming the same pair in
// opposite orders cannot each hold what the other waits for. A name given
// twice, or empty, is skipped.
func (l *InstanceLocks) AcquireAll(ctx context.Context, names ...string) (context.Context, func(), error) {
	unique := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		unique = append(unique, n)
	}
	sort.Strings(unique)

	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}

	for _, name := range unique {
		next, release, err := l.Acquire(ctx, name)
		if err != nil {
			releaseAll()
			return ctx, func() {}, err
		}
		ctx = next
		releases = append(releases, release)
	}
	return ctx, releaseAll, nil
}
