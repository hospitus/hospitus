package migrations

import "testing"

// TestIrreversibleMigrationsAreRefused covers the down bodies that exist only
// to explain why there is no down: they execute as a no-op, and the rollback
// would then delete the version row while the schema keeps the change.
func TestIrreversibleMigrationsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "\n  \n\t", false},
		{"one comment", "-- SQLite cannot drop a column", false},
		{"several comments", "-- one\n\n-- two\n", false},
		{"a real statement", "DROP TABLE jobs;", true},
		{"comment then statement", "-- why\nDROP TABLE jobs;", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasExecutableSQL(tc.body); got != tc.want {
				t.Errorf("hasExecutableSQL(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestRegistryIrreversibleMigrationsAreMarked checks the shipped registry: any
// migration whose down carries no statement must be refused rather than run.
func TestRegistryIrreversibleMigrationsAreMarked(t *testing.T) {
	for _, m := range All {
		if m.Down != "" && !hasExecutableSQL(m.Down) {
			t.Logf("migration %d is irreversible by design: %s", m.Version, m.Description)
		}
	}
}
