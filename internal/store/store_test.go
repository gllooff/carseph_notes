package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsApplyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	// If migration logic double-applied, unique errors would surface above.
}

func TestInviteLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	code, err := s.CreateInviteCode(ctx)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	hash := HashInviteCode(code)
	if err := s.CheckInviteCode(ctx, hash); err != nil {
		t.Fatalf("check unused invite: %v", err)
	}

	// Unknown codes are rejected.
	if err := s.CheckInviteCode(ctx, HashInviteCode("bogus")); err != ErrNotFound {
		t.Fatalf("unknown invite: got %v, want ErrNotFound", err)
	}

	// Consume via RegisterWithPasskey.
	p := &Passkey{
		ID: []byte("cred-1"), UserID: "u1", PublicKey: []byte("pk"),
		Attestation: "none", AAGUID: make([]byte, 16), CreatedAt: 1,
	}
	u, err := s.RegisterWithPasskey(ctx, "u1", "alice", hash, p)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if u.Username != "alice" || u.ID != "u1" {
		t.Fatalf("unexpected user %+v", u)
	}

	// Same code cannot be reused.
	p2 := *p
	p2.ID = []byte("cred-2")
	if _, err := s.RegisterWithPasskey(ctx, "u2", "bob", hash, &p2); err != ErrConflict {
		t.Fatalf("reused invite: got %v, want ErrConflict", err)
	}

	// Username lookup is case-insensitive.
	if _, err := s.UserByUsername(ctx, "ALICE"); err != nil {
		t.Fatalf("case-insensitive lookup: %v", err)
	}

	// Duplicate usernames rejected.
	if _, err := s.CreateUser(ctx, "u3", "Alice", mustInvite(t, s)); err != ErrConflict {
		t.Fatalf("duplicate username: got %v, want ErrConflict", err)
	}
}

func mustInvite(t *testing.T, s *Store) string {
	t.Helper()
	code, err := s.CreateInviteCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return HashInviteCode(code)
}

func TestPasskeyOwnershipScoping(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	// Two users.
	for _, name := range []string{"alice", "bob"} {
		if _, err := s.CreateUser(ctx, "id-"+name, name, mustInvite(t, s)); err != nil {
			t.Fatal(err)
		}
	}
	pk := &Passkey{
		ID: []byte("cred-a"), UserID: "id-alice", PublicKey: []byte("k"),
		Attestation: "none", AAGUID: make([]byte, 16), CreatedAt: 2, SignCount: 5,
	}
	if err := s.CreatePasskey(ctx, pk); err != nil {
		t.Fatal(err)
	}

	// Bob cannot fetch Alice's passkey.
	if _, err := s.PasskeyByID(ctx, "id-bob", []byte("cred-a")); err != ErrNotFound {
		t.Fatalf("cross-user passkey fetch: got %v, want ErrNotFound", err)
	}
	// Alice can.
	if _, err := s.PasskeyByID(ctx, "id-alice", []byte("cred-a")); err != nil {
		t.Fatalf("own passkey fetch: %v", err)
	}
	// Bob cannot rename or delete it.
	if err := s.RenamePasskey(ctx, "id-bob", []byte("cred-a"), "x"); err != ErrNotFound {
		t.Fatalf("cross-user rename: got %v, want ErrNotFound", err)
	}
	if err := s.DeletePasskey(ctx, "id-bob", []byte("cred-a")); err != ErrNotFound {
		t.Fatalf("cross-user delete: got %v, want ErrNotFound", err)
	}
	// Sign-state update by credential ID works.
	if err := s.UpdatePasskeySignState(ctx, []byte("cred-a"), 42, true); err != nil {
		t.Fatal(err)
	}
	got, err := s.PasskeyByID(ctx, "id-alice", []byte("cred-a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.SignCount != 42 || !got.BackupState || got.LastUsedAt.Valid == false {
		t.Fatalf("unexpected passkey after update: %+v", got)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().Unix()

	if _, err := s.CreateUser(ctx, "u1", "alice", mustInvite(t, s)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, &Session{
		ID: "hash1", UserID: "u1", CreatedAt: now, ExpiresAt: now + 1000, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := s.SessionByID(ctx, "hash1")
	if err != nil {
		t.Fatalf("session fetch: %v", err)
	}
	if sess.UserID != "u1" {
		t.Fatalf("wrong user %q", sess.UserID)
	}
	// Touch extends expiry by the given TTL.
	if err := s.TouchSession(ctx, "hash1", 2000); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.SessionByID(ctx, "hash1")
	// Touch uses the server clock, which may have advanced slightly.
	if sess.ExpiresAt < now+2000 || sess.ExpiresAt > now+2100 {
		t.Fatalf("unexpected expiry after touch: %d", sess.ExpiresAt)
	}
	// Delete.
	if err := s.DeleteSession(ctx, "hash1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByID(ctx, "hash1"); err != ErrNotFound {
		t.Fatalf("deleted session fetch: got %v, want ErrNotFound", err)
	}
	// Expired sessions are invisible and swept.
	if err := s.CreateSession(ctx, &Session{
		ID: "expired", UserID: "u1", CreatedAt: now, ExpiresAt: now - 10, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByID(ctx, "expired"); err != ErrNotFound {
		t.Fatalf("expired session fetch: got %v, want ErrNotFound", err)
	}
	n, err := s.DeleteExpiredSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("expired sweep: n=%d err=%v", n, err)
	}
}

func TestSessionCascadeOnUserDelete(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.CreateUser(ctx, "u1", "alice", mustInvite(t, s)); err != nil {
		t.Fatal(err)
	}
	pk := &Passkey{
		ID: []byte("cred-1"), UserID: "u1", PublicKey: []byte("k"),
		Attestation: "none", AAGUID: make([]byte, 16), CreatedAt: 1,
	}
	if err := s.CreatePasskey(ctx, pk); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, &Session{
		ID: "s1", UserID: "u1", CreatedAt: 1, ExpiresAt: 100, LastSeenAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Hard-delete the user: sessions and passkeys must cascade.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = 'u1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByID(ctx, "s1"); err != ErrNotFound {
		t.Fatalf("session after user delete: got %v, want ErrNotFound", err)
	}
	n, err := s.CountPasskeys(ctx, "u1")
	if err != nil || n != 0 {
		t.Fatalf("passkeys after user delete: n=%d err=%v", n, err)
	}
}
