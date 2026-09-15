package main

import "testing"

func TestDedupeClauses(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// The message this exists for: the provider, the API and the CLI
			// command each named the operation, and the reason came fourth.
			name: "three layers name the same operation",
			in:   `failed to create snapshot: API error (500): Failed to create snapshot: exit status 125: repository name must be lowercase`,
			want: `failed to create snapshot: API error (500): exit status 125: repository name must be lowercase`,
		},
		{
			name: "short message is left alone",
			in:   "permission denied",
			want: "permission denied",
		},
		{
			// A colon with no space after it belongs to the value, not to the
			// structure of the message: splitting there would cut the URL up.
			name: "url survives intact",
			in:   "failed to connect: connection refused to http://127.0.0.1:8080",
			want: "failed to connect: connection refused to http://127.0.0.1:8080",
		},
		{
			// Same words, different subject — dropping the second clause would
			// hide which of the two paths actually failed.
			name: "similar clauses both survive",
			in:   "failed to open /var/db/a: failed to open /var/db/b: no such file",
			want: "failed to open /var/db/a: failed to open /var/db/b: no such file",
		},
		{
			name: "hint lines survive",
			in:   "server unavailable: connection refused\n  → Is hospitusd running?",
			want: "server unavailable: connection refused\n  → Is hospitusd running?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dedupeClauses(tt.in); got != tt.want {
				t.Errorf("dedupeClauses(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}
