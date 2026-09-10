package store

import (
	"context"
	"database/sql"
	"errors"
)

// Folder is a flat, single-level folder row.
type Folder struct {
	ID        string
	UserID    string
	Name      string
	CreatedAt int64
	Count     int64 // notes+files inside (filled on list)
}

// CreateFolder inserts a folder; duplicate names per user are a conflict.
func (s *Store) CreateFolder(ctx context.Context, userID, name string) (*Folder, error) {
	name = normalizeFolderName(name)
	if name == "" {
		return nil, errors.New("folder name required")
	}
	f := &Folder{ID: newID(), UserID: userID, Name: name, CreatedAt: now()}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO folders (id, user_id, name, created_at) VALUES (?, ?, ?, ?)`,
		f.ID, f.UserID, f.Name, f.CreatedAt)
	if err != nil {
		if isUniqueErr(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return f, nil
}

// FoldersByUser lists folders with note counts.
func (s *Store) FoldersByUser(ctx context.Context, userID string) ([]*Folder, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.id, f.user_id, f.name, f.created_at,
		        (SELECT COUNT(*) FROM notes n WHERE n.folder_id = f.id) AS cnt
		 FROM folders f WHERE f.user_id = ? ORDER BY f.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Folder
	for rows.Next() {
		f := &Folder{}
		if err := rows.Scan(&f.ID, &f.UserID, &f.Name, &f.CreatedAt, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RenameFolder renames if owned; ErrNotFound otherwise.
func (s *Store) RenameFolder(ctx context.Context, userID, id, name string) error {
	name = normalizeFolderName(name)
	if name == "" {
		return errors.New("folder name required")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE folders SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		if isUniqueErr(err) {
			return ErrConflict
		}
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFolder removes a folder; contained notes become unfiled (FK ON DELETE SET NULL).
func (s *Store) DeleteFolder(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM folders WHERE id = ? AND user_id = ?`, id, userID)
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrNotFound
	}
	return err
}

// TagInfo is a tag with its note+file usage count.
type TagInfo struct {
	Name  string
	Count int64
}

// TagsByUser lists all tags of a user with item counts.
func (s *Store) TagsByUser(ctx context.Context, userID string) ([]*TagInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.name, COUNT(it.item_id) FROM tags t
		 LEFT JOIN item_tags it ON it.tag_id = t.id
		 WHERE t.user_id = ?
		 GROUP BY t.id ORDER BY t.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TagInfo
	for rows.Next() {
		ti := &TagInfo{}
		if err := rows.Scan(&ti.Name, &ti.Count); err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, rows.Err()
}

// FolderRef returns the folder's ID as a nullable reference for notes.
func (f *Folder) FolderRef() sql.NullString {
	return sql.NullString{String: f.ID, Valid: true}
}

// FolderOwned verifies a folder exists and belongs to the user.
func (s *Store) FolderOwned(ctx context.Context, userID, id string) (*Folder, error) {
	f := &Folder{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, created_at FROM folders WHERE id = ? AND user_id = ?`,
		id, userID,
	).Scan(&f.ID, &f.UserID, &f.Name, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

func normalizeFolderName(name string) string {
	name = trimSpaceBytes(name)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
