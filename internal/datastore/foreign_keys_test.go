package datastore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestForeignKeysEnabled verifies every pooled connection has foreign key
// enforcement on, so ON DELETE CASCADE constraints actually fire (audit HIGH
// datastore.go:122 / migrations: PRAGMA foreign_keys was never enabled).
//
// It uses a file-backed database and holds several connections open at once.
// A ":memory:" datastore is capped at one connection, so querying it in a loop
// asks the same connection five times and proves nothing about the pool.
func TestForeignKeysEnabled(t *testing.T) {
	ds, err := NewDatastore(filepath.Join(t.TempDir(), "fk.db"), nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()
	const want = 4
	conns := make([]*sql.Conn, 0, want)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	for i := 0; i < want; i++ {
		conn, err := ds.db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		conns = append(conns, conn)

		var fk int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("PRAGMA foreign_keys on connection %d: %v", i, err)
		}
		if fk != 1 {
			t.Fatalf("connection %d has foreign_keys = %d, want 1", i, fk)
		}
	}
}
