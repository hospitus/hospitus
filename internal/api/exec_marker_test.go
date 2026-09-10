package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExitCodeMarkerStartsItsOwnLine covers output that does not end in a
// newline, which is most of what a script asks for: a file with no trailing
// newline, printf without \n, a value read back from an API.
//
// The client strips the exit-code marker line by line. Written straight after
// such output the marker joins it, no line begins with "EXIT_CODE: ", and the
// caller is handed "the quick brown foxEXIT_CODE: 0" — which is what broke
// reading a file back out of Nextcloud.
func TestExitCodeMarkerStartsItsOwnLine(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "output without a trailing newline",
			output: "the quick brown fox",
			want:   "the quick brown fox\nEXIT_CODE: 0\n",
		},
		{
			name:   "output already ending in a newline",
			output: "one line\n",
			want:   "one line\nEXIT_CODE: 0\n",
		},
		{
			name:   "no output at all",
			output: "",
			want:   "EXIT_CODE: 0\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fw := newFlushingWriter(rec, rec)

			if tt.output != "" {
				if _, err := fw.Write([]byte(tt.output)); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			fw.startLine()
			if _, err := rec.Body.WriteString("EXIT_CODE: 0\n"); err != nil {
				t.Fatalf("write marker: %v", err)
			}

			if got := rec.Body.String(); got != tt.want {
				t.Errorf("stream = %q, want %q", got, tt.want)
			}

			// The marker must be findable the way the client looks for it.
			var marked bool
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if strings.HasPrefix(line, "EXIT_CODE: ") {
					marked = true
				}
			}
			if !marked {
				t.Error("no line begins with the marker, so the client cannot strip it")
			}
		})
	}
}

// TestFlushingWriterTracksTheLastByte guards the bookkeeping the fix rests on.
func TestFlushingWriterTracksTheLastByte(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := newFlushingWriter(rec, rec)

	if !fw.endsWithNewline {
		t.Error("a writer that has written nothing must not ask for a leading newline")
	}
	if _, err := fw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if fw.endsWithNewline {
		t.Error(`after "abc" the stream does not end in a newline`)
	}
	if _, err := fw.Write([]byte("def\n")); err != nil {
		t.Fatal(err)
	}
	if !fw.endsWithNewline {
		t.Error(`after "def\n" the stream does end in a newline`)
	}

	// A zero-length write must not be read as "the last byte was not a newline".
	if _, err := fw.Write(nil); err != nil {
		t.Fatal(err)
	}
	if !fw.endsWithNewline {
		t.Error("an empty write changed what the stream ends with")
	}

	if got := rec.Body.String(); got != "abcdef\n" {
		t.Errorf("body = %q", got)
	}
}
