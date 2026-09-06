package cmdutil

import (
	"os"
	"testing"
)

// TestConfirmDownloadNeverBlocksWithoutATerminal covers the case that made
// `hospitus apply` unusable from a script: a missing image printed "[y/N]:" to a
// log nobody was reading, took EOF for "no", and failed.
func TestConfirmDownloadNeverBlocksWithoutATerminal(t *testing.T) {
	// Put a pipe on standard input rather than trusting whatever the test was
	// launched with: run from a terminal, stdin is one, and the case under
	// test would never be reached.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	saved := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = saved }()

	if ConfirmDownload("download?", false) {
		t.Error("without --pull and without a terminal, the download must be declined, not offered")
	}
	if !ConfirmDownload("download?", true) {
		t.Error("--pull must answer yes without asking anyone")
	}
}

// TestStdinIsTerminalRejectsAPipe guards the detection itself: a pipe is the
// shape every scripted invocation arrives in.
func TestStdinIsTerminalRejectsAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	saved := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = saved }()

	if stdinIsTerminal() {
		t.Error("a pipe was reported as a terminal")
	}
}

func TestPluralAgreesWithTheCount(t *testing.T) {
	// "Total: 1 images available" was what the catalog printed for OpenBSD.
	cases := map[int]string{0: "s", 1: "", 2: "s", 19: "s"}
	for n, want := range cases {
		if got := Plural(n); got != want {
			t.Errorf("Plural(%d) = %q, want %q", n, got, want)
		}
	}
}
