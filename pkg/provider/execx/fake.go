package execx

import (
	"context"
	"sync"
)

// Call records a single invocation made through a Fake.
type Call struct {
	Name string
	Args []string
}

// Fake is a programmable Runner for tests. Set Func to control the returned
// output and error; every call is recorded in Calls. When Func is nil, calls
// succeed with empty output.
//
// Recording is mutex-guarded, so provider code may run commands from several
// goroutines. Calls itself is not: read it once the operation under test has
// returned, which is what a sequential test does anyway. To look while
// goroutines are still running, use Snapshot.
type Fake struct {
	// Func, if set, produces the output/error for a call. It is shared by
	// CombinedOutput and Output; Run ignores the returned bytes.
	Func func(name string, args []string) ([]byte, error)

	mu    sync.Mutex
	Calls []Call
}

func (f *Fake) record(name string, args []string) {
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	f.mu.Unlock()
}

func (f *Fake) run(name string, args []string) ([]byte, error) {
	f.record(name, args)
	if f.Func == nil {
		return nil, nil
	}

	// Trim like the real Runner does, so a test that hands back canned output
	// ending in a newline sees exactly what production would see.
	out, err := f.Func(name, args)
	return TrimOutput(out), err
}

// CombinedOutput implements Runner.
func (f *Fake) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	return f.run(name, args)
}

// Output implements Runner.
func (f *Fake) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	return f.run(name, args)
}

// Run implements Runner.
func (f *Fake) Run(_ context.Context, name string, args ...string) error {
	_, err := f.run(name, args)
	return err
}

// Snapshot returns a copy of the recorded calls, safe to read while commands
// are still being issued.
func (f *Fake) Snapshot() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.Calls...)
}

// CallCount returns how many commands were run through the Fake.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}
