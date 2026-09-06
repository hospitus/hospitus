package datastore

import "testing"

// TestForeignKeysEnabled verifies every pooled connection has foreign key
// enforcement on, so ON DELETE CASCADE constraints actually fire (audit HIGH
// datastore.go:122 / migrations: PRAGMA foreign_keys was never enabled).
func TestForeignKeysEnabled(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	defer ds.Close()

	// Check on a few connections from the pool.
	for i := 0; i < 5; i++ {
		var fk int
		if err := ds.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("PRAGMA foreign_keys: %v", err)
		}
		if fk != 1 {
			t.Fatalf("foreign_keys = %d, want 1", fk)
		}
	}
}
