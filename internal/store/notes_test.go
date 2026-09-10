package store

import (
	"context"
	"testing"
)

func seedTwoUsers(t *testing.T, s *Store) (alice, bob string) {
	t.Helper()
	for _, u := range []string{"alice", "bob"} {
		if _, err := s.CreateUser(context.Background(), "id-"+u, u, mustInvite(t, s)); err != nil {
			t.Fatal(err)
		}
	}
	return "id-alice", "id-bob"
}

func TestNotesCRUDAndScoping(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	alice, bob := seedTwoUsers(t, s)

	n := &Note{ID: "n1", UserID: alice, Title: "Groceries", Tags: []string{"life", "shopping"}}
	if err := s.CreateNote(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNote(ctx, &Note{ID: "n2", UserID: alice, Title: "Work plan", Tags: []string{"work"}}); err != nil {
		t.Fatal(err)
	}

	// Read back: scoped, tags attached.
	got, err := s.NoteByID(ctx, alice, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Groceries" || len(got.Tags) != 2 || got.Tags[0] != "life" {
		t.Fatalf("unexpected note %+v", got)
	}

	// Bob cannot read Alice's note.
	if _, err := s.NoteByID(ctx, bob, "n1"); err != ErrNotFound {
		t.Fatalf("cross-user read: got %v, want ErrNotFound", err)
	}
	// Bob's list is empty.
	bobNotes, err := s.NotesByUser(ctx, bob, NoteFilter{})
	if err != nil || len(bobNotesSafe(bobNotes)) != 0 {
		t.Fatalf("bob sees foreign notes: %v %v", bobNotes, err)
	}
	if len(bobNotes) != 0 {
		t.Fatalf("bob's list not empty")
	}

	// Update title + tags.
	got.Title = "Groceries & stuff"
	got.Tags = []string{"life"}
	if err := s.UpdateNote(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.NoteByID(ctx, alice, "n1")
	if got.Title != "Groceries & stuff" || len(got.Tags) != 1 {
		t.Fatalf("update not applied: %+v", got)
	}

	// Cross-user update is a 404-style miss.
	other := *got
	other.UserID = bob
	if err := s.UpdateNote(ctx, &other); err != ErrNotFound {
		t.Fatalf("cross-user update: got %v, want ErrNotFound", err)
	}

	// Delete: bob cannot delete alice's note.
	if err := s.DeleteNote(ctx, bob, "n1"); err != ErrNotFound {
		t.Fatalf("cross-user delete: got %v, want ErrNotFound", err)
	}
	if err := s.DeleteNote(ctx, alice, "n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NoteByID(ctx, alice, "n1"); err != ErrNotFound {
		t.Fatal("note not deleted")
	}

	// Tag 'life' was pruned (no more items); 'work' remains.
	tags, _ := s.TagsByUser(ctx, alice)
	names := map[string]bool{}
	for _, ti := range tags {
		names[ti.Name] = true
	}
	if names["life"] {
		t.Fatalf("orphan tag not pruned: %v", tags)
	}
	if !names["work"] {
		t.Fatalf("live tag missing: %v", tags)
	}
}

func bobNotesSafe(ns []*Note) []*Note { return ns }

func TestNoteFilters(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	alice, _ := seedTwoUsers(t, s)

	f1, err := s.CreateFolder(ctx, alice, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNote(ctx, &Note{ID: "a", UserID: alice, Title: "Alpha", FolderID: f1.FolderRef(), Tags: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNote(ctx, &Note{ID: "b", UserID: alice, Title: "Beta"}); err != nil {
		t.Fatal(err)
	}

	// Folder filter.
	ns, err := s.NotesByUser(ctx, alice, NoteFilter{FolderID: f1.ID})
	if err != nil || len(ns) != 1 || ns[0].ID != "a" {
		t.Fatalf("folder filter: %v %v", ns, err)
	}
	// Unfiled filter.
	ns, _ = s.NotesByUser(ctx, alice, NoteFilter{FolderID: "none"})
	if len(ns) != 1 || ns[0].ID != "b" {
		t.Fatalf("unfiled filter: %v", ns)
	}
	// Tag filter.
	ns, _ = s.NotesByUser(ctx, alice, NoteFilter{Tag: "x"})
	if len(ns) != 1 || ns[0].ID != "a" {
		t.Fatalf("tag filter: %v", ns)
	}
	// Title substring, case-insensitive.
	ns, _ = s.NotesByUser(ctx, alice, NoteFilter{Q: "alp"})
	if len(ns) != 1 || ns[0].ID != "a" {
		t.Fatalf("title filter: %v", ns)
	}
}

func TestFolderLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	alice, bob := seedTwoUsers(t, s)

	f, err := s.CreateFolder(ctx, alice, "Work")
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate name.
	if _, err := s.CreateFolder(ctx, alice, "work"); err != ErrConflict {
		t.Fatalf("duplicate folder: %v", err)
	}
	// Bob cannot rename/delete it.
	if err := s.RenameFolder(ctx, bob, f.ID, "Nope"); err != ErrNotFound {
		t.Fatalf("cross-user rename: %v", err)
	}
	if err := s.DeleteFolder(ctx, bob, f.ID); err != ErrNotFound {
		t.Fatalf("cross-user delete: %v", err)
	}
	// Notes inside become unfiled on folder delete.
	if err := s.CreateNote(ctx, &Note{ID: "n1", UserID: alice, Title: "T", FolderID: f.FolderRef()}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFolder(ctx, alice, f.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.NoteByID(ctx, alice, "n1")
	if err != nil || n.FolderID.Valid {
		t.Fatalf("note not unfiled: %+v %v", n.FolderID, err)
	}
	// Ownership check.
	if _, err := s.FolderOwned(ctx, alice, f.ID); err != ErrNotFound {
		t.Fatalf("deleted folder fetch: %v", err)
	}
}
