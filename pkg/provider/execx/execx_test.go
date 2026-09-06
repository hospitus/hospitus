package execx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestOSRunnerCombinedOutput(t *testing.T) {
	// "echo" is available on every supported platform (FreeBSD/Linux/macOS).
	out, err := execx.OS{}.CombinedOutput(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("CombinedOutput: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("got %q, want %q", string(out), "hello")
	}
}

func TestOSRunnerRunError(t *testing.T) {
	// A command that does not exist must surface an error.
	if err := (execx.OS{}).Run(context.Background(), "hospitus-no-such-binary-xyz"); err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
}

func TestDefaultReturnsOSRunner(t *testing.T) {
	if _, ok := execx.Default().(execx.OS); !ok {
		t.Fatalf("Default() = %T, want execx.OS", execx.Default())
	}
}

func TestFakeRecordsCallsAndReturnsFunc(t *testing.T) {
	wantErr := errors.New("boom")
	fake := &execx.Fake{
		Func: func(name string, args []string) ([]byte, error) {
			if name == "zfs" && len(args) > 0 && args[0] == "list" {
				return []byte("pool/dataset\n"), nil
			}
			return nil, wantErr
		},
	}

	out, err := fake.Output(context.Background(), "zfs", "list", "-H")
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if strings.TrimSpace(string(out)) != "pool/dataset" {
		t.Errorf("got %q", string(out))
	}

	if err := fake.Run(context.Background(), "false"); !errors.Is(err, wantErr) {
		t.Errorf("Run err = %v, want %v", err, wantErr)
	}

	if fake.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2", fake.CallCount())
	}
	if fake.Calls[0].Name != "zfs" || fake.Calls[0].Args[0] != "list" {
		t.Errorf("unexpected first call: %+v", fake.Calls[0])
	}
}

func TestFakeNilFuncSucceeds(t *testing.T) {
	fake := &execx.Fake{}
	out, err := fake.CombinedOutput(context.Background(), "anything")
	if err != nil || out != nil {
		t.Fatalf("got (%q, %v), want (nil, nil)", string(out), err)
	}
}

// Fake and OS must satisfy Runner.
var (
	_ execx.Runner = (*execx.Fake)(nil)
	_ execx.Runner = execx.OS{}
)

func TestTrimOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"trailing newline", "container state improper\n", "container state improper"},
		{"trailing CRLF", "line\r\n", "line"},
		{"several blank lines", "text\n\n\n", "text"},
		{"trailing spaces and tabs", "value \t\n", "value"},
		{"interior newlines kept", "one\ntwo\nthree\n", "one\ntwo\nthree"},
		{"leading whitespace kept", "  indented\n", "  indented"},
		{"empty", "", ""},
		{"only whitespace", "\n\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(execx.TrimOutput([]byte(tt.in))); got != tt.want {
				t.Errorf("TrimOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestFakeTrimsLikeOS keeps the fake honest: a test whose canned output ends in
// a newline must see what production sees, or the fake hides the formatting
// problem the trim exists to fix.
func TestFakeTrimsLikeOS(t *testing.T) {
	fake := &execx.Fake{Func: func(string, []string) ([]byte, error) {
		return []byte("jail: no such jail\n"), nil
	}}

	out, err := fake.CombinedOutput(context.Background(), "jls", "-j", "web")
	if err != nil {
		t.Fatalf("CombinedOutput: %v", err)
	}
	if got := string(out); got != "jail: no such jail" {
		t.Errorf("Fake returned %q, want the trimmed form", got)
	}
}
