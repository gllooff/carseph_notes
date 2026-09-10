package blob

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSweepOrphansDebug(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "notes", "userA"), 0o700)
	os.WriteFile(filepath.Join(root, "notes", "userA", "live.md"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(root, "notes", "userB"), 0o700)
	os.WriteFile(filepath.Join(root, "notes", "userB", "dead.md"), []byte("x"), 0o600)

	keep := map[string]bool{}
	keep["userA\x00live"] = true

	s := &Store{root: root}
	if err := s.SweepOrphans("notes", keep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes", "userA", "live.md")); err != nil {
		t.Fatal("live blob was removed!")
	}
	if _, err := os.Stat(filepath.Join(root, "notes", "userB", "dead.md")); err == nil {
		t.Fatal("orphan blob survived")
	}
}
