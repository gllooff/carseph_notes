package store

// Regression: the SQLite DSN must enforce foreign keys so user deletion
// cascades to sessions, notes and files. (The sqlite3 CLI defaults to FK off;
// manual surgery without `PRAGMA foreign_keys=ON` leaves orphan rows.)

import (
	"context"
	"testing"
)

func TestForeignKeyCascade(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "u1", "alice", mustInvite(t, s)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNote(ctx, &Note{ID: "n1", UserID: "u1", Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM users WHERE id = 'u1'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("cascade failed: %d note rows remain", n)
	}
	var fk int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys pragma not applied: %d", fk)
	}
}
