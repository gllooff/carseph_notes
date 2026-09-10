// Package blob stores note bodies and file blobs on the file system.
// Layout: {Root}/notes/{userID}/{id}.md and {Root}/files/{userID}/{id}.{ext}
// All path components are server-generated IDs; user input never touches paths.
package blob

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store is a file-system blob store rooted at a data directory.
type Store struct {
	root string
}

// New creates the store and its directory layout.
func New(root string) (*Store, error) {
	for _, d := range []string{"notes", "files"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, fmt.Errorf("create %s dir: %w", d, err)
		}
	}
	return &Store{root: root}, nil
}

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = errors.New("blob not found")

func (s *Store) notePath(userID, id string) string {
	return filepath.Join(s.root, "notes", userID, id+".md")
}

// SaveNote writes a note body atomically (temp file + rename).
func (s *Store) SaveNote(userID, id string, body []byte) error {
	dir := filepath.Join(s.root, "notes", userID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, id+".md"), body)
}

// WriteNote is an alias for SaveNote.
func (s *Store) WriteNote(userID, id string, body []byte) error {
	return s.SaveNote(userID, id, body)
}

// ReadNote returns a note body.
func (s *Store) ReadNote(userID, id string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.root, "notes", userID, id+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

// DeleteNote removes a note body; missing files are not an error.
func (s *Store) DeleteNote(userID, id string) error {
	err := os.Remove(filepath.Join(s.root, "notes", userID, id+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// fileDir returns the directory for a user's file blobs.
func (s *Store) fileDir(userID string) string {
	return filepath.Join(s.root, "files", userID)
}

// extFor maps a MIME type to a file extension.
func extFor(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/avif":
		return "avif"
	case "application/pdf":
		return "pdf"
	default:
		return "bin"
	}
}

// SaveFile writes a file blob; returns the on-disk filename.
func (s *Store) SaveFile(userID, id, mime string, data []byte) (string, error) {
	dir := s.fileDir(userID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := id + "." + extFor(mime)
	if err := writeAtomic(filepath.Join(dir, name), data); err != nil {
		return "", err
	}
	return name, nil
}

// OpenFile opens a file blob for reading; caller closes.
func (s *Store) OpenFile(userID, filename string) (*os.File, os.FileInfo, error) {
	if strings.ContainsRune(filename, '/') || strings.ContainsRune(filename, 0) {
		return nil, nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(s.fileDir(userID), filename))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return f, nil, nil
}

// WriteFile stores an uploaded file blob under its pre-generated filename.
func (s *Store) WriteFile(userID, filename string, data []byte) error {
	dir := s.fileDir(userID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, filename), data)
}

// DeleteFile removes a file blob; missing files are not an error.
func (s *Store) DeleteFile(userID, filename string) error {
	if strings.ContainsRune(filename, '/') {
		return nil
	}
	err := os.Remove(filepath.Join(s.fileDir(userID), filename))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// SweepOrphans removes blobs under one subdir ("notes" or "files") whose
// "userID\x00id" key is not in the keep set. Used at startup to clean blobs
// whose DB rows were lost mid-delete.
func (s *Store) SweepOrphans(kind string, keep map[string]bool) error {
	return filepath.WalkDir(filepath.Join(s.root, kind), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 {
			return nil
		}
		_, userID, name := parts[0], parts[1], parts[2]
		id := strings.TrimSuffix(name, filepath.Ext(name))
		if !keep[userID+"\x00"+id] {
			_ = os.Remove(path)
		}
		return nil
	})
}

// writeAtomic writes data to a temp file then renames over dest.
func writeAtomic(dest string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, dest)
}
