package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestInstanceLocksSerializeTheSameInstance is the property the lock exists
// for: two operations on one instance never overlap.
func TestInstanceLocksSerializeTheSameInstance(t *testing.T) {
	var locks InstanceLocks
	var mu sync.Mutex
	inside, peak := 0, 0

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := locks.Acquire(context.Background(), "web")
			if err != nil {
				t.Error(err)
				return
			}
			defer release()

			mu.Lock()
			inside++
			if inside > peak {
				peak = inside
			}
			mu.Unlock()

			time.Sleep(time.Millisecond)

			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if peak != 1 {
		t.Errorf("%d operations ran on one instance at once, want 1", peak)
	}
}

// TestInstanceLocksDoNotSerializeDifferentInstances keeps the lock from turning
// the daemon into a single-threaded one.
func TestInstanceLocksDoNotSerializeDifferentInstances(t *testing.T) {
	var locks InstanceLocks

	_, releaseA, err := locks.Acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, releaseB, err := locks.Acquire(ctx, "b"); err != nil {
		t.Errorf("a second instance had to wait for the first: %v", err)
	} else {
		releaseB()
	}
}

// TestInstanceLocksAreReentrantAlongTheCallChain covers restart, which is stop
// then start: without this the operation deadlocks against itself.
func TestInstanceLocksAreReentrantAlongTheCallChain(t *testing.T) {
	var locks InstanceLocks

	outer, release, err := locks.Acquire(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(outer, 2*time.Second)
		defer cancel()
		_, inner, err := locks.Acquire(ctx, "web")
		if err == nil {
			inner()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a nested acquire on the held instance failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a nested acquire deadlocked against the lock its own caller holds")
	}
}

// TestInstanceLocksWaitIsCancellable keeps a queued request from outliving the
// client that asked for it.
func TestInstanceLocksWaitIsCancellable(t *testing.T) {
	var locks InstanceLocks

	_, release, err := locks.Acquire(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, _, err := locks.Acquire(ctx, "web"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
}

// TestInstanceLocksForgetIdleInstances keeps the map from growing by one entry
// per instance the daemon has ever touched.
func TestInstanceLocksForgetIdleInstances(t *testing.T) {
	var locks InstanceLocks

	for _, name := range []string{"a", "b", "c"} {
		_, release, err := locks.Acquire(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}

	locks.mu.Lock()
	n := len(locks.locks)
	locks.mu.Unlock()
	if n != 0 {
		t.Errorf("%d lock entries left behind, want 0", n)
	}
}

// TestInstanceLocksReleaseIsIdempotent covers a deferred release that also runs
// on an error path.
func TestInstanceLocksReleaseIsIdempotent(t *testing.T) {
	var locks InstanceLocks

	_, release, err := locks.Acquire(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, again, err := locks.Acquire(ctx, "web"); err != nil {
		t.Errorf("the instance stayed locked after a double release: %v", err)
	} else {
		again()
	}
}

// TestAcquireAllOrdersNamesSoOppositePairsCannotDeadlock covers the clone and
// rename shape: two operations naming the same two instances the other way
// round.
func TestAcquireAllOrdersNamesSoOppositePairsCannotDeadlock(t *testing.T) {
	var locks InstanceLocks

	done := make(chan struct{}, 2)
	for _, pair := range [][2]string{{"a", "b"}, {"b", "a"}} {
		go func(names [2]string) {
			for i := 0; i < 200; i++ {
				ctx, release, err := locks.AcquireAll(context.Background(), names[0], names[1])
				if err != nil {
					t.Error(err)
					return
				}
				_ = ctx
				release()
			}
			done <- struct{}{}
		}(pair)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("two operations naming the same pair in opposite orders deadlocked")
		}
	}
}

// TestAcquireAllSkipsEmptyAndDuplicateNames keeps a self-clone or an empty
// target from blocking on itself.
func TestAcquireAllSkipsEmptyAndDuplicateNames(t *testing.T) {
	var locks InstanceLocks

	ctx, release, err := locks.AcquireAll(context.Background(), "web", "web", "")
	if err != nil {
		t.Fatalf("AcquireAll: %v", err)
	}
	_ = ctx
	release()

	locks.mu.Lock()
	n := len(locks.locks)
	locks.mu.Unlock()
	if n != 0 {
		t.Errorf("%d lock entries left behind, want 0", n)
	}
}
