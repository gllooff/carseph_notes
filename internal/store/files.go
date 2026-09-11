package store

import (
	"context"
	"database/sql"
	"errors"
)

// File is an uploaded image, PDF or Markdown file (metadata; blob lives on disk).
type File struct {
	ID           string
	UserID       string
	FolderID     sql.NullString
	Kind         string // "image" | "pdf" | "markdown"
	OriginalName string
	Mime         string
	Size         int64
	SHA256       string
	Filename     string // on-disk name
	CreatedAt    int64
	Rotation     int // image viewer rotation in degrees: 0, 90, 180, 270
	Tags         []string
}

// FileFilter narrows file lists.
type FileFilter struct {
	Kind     string // "" both, "image", "pdf", "markdown"
	FolderID string
	Tag      string
	Q        string // original-name substring
}

// CreateFile inserts file metadata (+tags).
func (s *Store) CreateFile(ctx context.Context, f *File) error {
	f.CreatedAt = now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO files (id, user_id, folder_id, kind, original_name, mime, size, sha256, filename, created_at, rotation)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.UserID, f.FolderID, f.Kind, f.OriginalName, f.Mime, f.Size, f.SHA256, f.Filename, f.CreatedAt, f.Rotation); err != nil {
		return err
	}
	if err := setTagsTx(ctx, tx, "file", f.UserID, f.ID, f.Tags); err != nil {
		return err
	}
	return tx.Commit()
}

// FilesByUser lists files with tags.
func (s *Store) FilesByUser(ctx context.Context, userID string, fl FileFilter) ([]*File, error) {
	q := `SELECT id, user_id, folder_id, kind, original_name, mime, size, sha256, filename, created_at, rotation
	      FROM files WHERE user_id = ?`
	args := []any{userID}
	if fl.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, fl.Kind)
	}
	if fl.FolderID == "none" {
		q += ` AND folder_id IS NULL`
	} else if fl.FolderID != "" {
		q += ` AND folder_id = ?`
		args = append(args, fl.FolderID)
	}
	if fl.Tag != "" {
		q += ` AND EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id
		       WHERE it.item_type = 'file' AND it.item_id = files.id AND t.name = ? COLLATE NOCASE)`
		args = append(args, fl.Tag)
	}
	if fl.Q != "" {
		q += ` AND (instr(lower(original_name), lower(?)) > 0
		       OR EXISTS (SELECT 1 FROM folders fo WHERE fo.id = files.folder_id
		                  AND instr(lower(fo.name), lower(?)) > 0))`
		args = append(args, fl.Q, fl.Q)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*File
	for rows.Next() {
		f := &File{}
		if err := rows.Scan(&f.ID, &f.UserID, &f.FolderID, &f.Kind, &f.OriginalName,
			&f.Mime, &f.Size, &f.SHA256, &f.Filename, &f.CreatedAt, &f.Rotation); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// attach tags (reuse note helper via a shim)
	filesAsNotes := make([]*Note, 0, len(out))
	byID := map[string]*File{}
	for _, f := range out {
		byID[f.ID] = f
		filesAsNotes = append(filesAsNotes, &Note{ID: f.ID})
	}
	if err := s.attachTags(ctx, "file", userID, filesAsNotes); err != nil {
		return nil, err
	}
	for _, n := range filesAsNotes {
		if f, ok := byID[n.ID]; ok {
			f.Tags = n.Tags
		}
	}
	return out, nil
}

// FileByID returns one file scoped to the user, with tags.
func (s *Store) FileByID(ctx context.Context, userID, id string) (*File, error) {
	f := &File{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, folder_id, kind, original_name, mime, size, sha256, filename, created_at, rotation
		 FROM files WHERE id = ? AND user_id = ?`, id, userID,
	).Scan(&f.ID, &f.UserID, &f.FolderID, &f.Kind, &f.OriginalName,
		&f.Mime, &f.Size, &f.SHA256, &f.Filename, &f.CreatedAt, &f.Rotation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	single := []*Note{{ID: f.ID}}
	if err := s.attachTags(ctx, "file", userID, single); err != nil {
		return nil, err
	}
	f.Tags = single[0].Tags
	return f, nil
}

// UpdateFile moves a file between folders, renames it, and/or sets tags.
func (s *Store) UpdateFile(ctx context.Context, f *File) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE files SET folder_id = ?, original_name = ?, rotation = ? WHERE id = ? AND user_id = ?`,
		f.FolderID, f.OriginalName, f.Rotation, f.ID, f.UserID)
	if err != nil {
		return err
	}
	if !affected(res) {
		return ErrNotFound
	}
	if f.Tags != nil {
		if err := setTagsTx(ctx, tx, "file", f.UserID, f.ID, f.Tags); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteFile removes metadata + tag links, prunes orphaned tags.
func (s *Store) DeleteFile(ctx context.Context, userID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if !affected(res) {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM item_tags WHERE item_type = 'file' AND item_id = ? AND user_id = ?`,
		id, userID); err != nil {
		return err
	}
	if err := s.pruneOrphanTagsTx(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// NoteRefsForSweep returns sets for orphan sweeping at startup:
// kind -> "userID\x00id" -> true, matching blob.SweepOrphans' lookup shape.
// Rows whose user no longer exists (e.g. from manual DB surgery with FKs off)
// are treated as orphans, so the sweep self-heals.
func (s *Store) KeepSets(ctx context.Context) (notes, files map[string]map[string]bool, err error) {
	notes = map[string]map[string]bool{}
	files = map[string]map[string]bool{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT n.user_id, n.id FROM notes n JOIN users u ON u.id = n.user_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid, id string
		if err := rows.Scan(&uid, &id); err != nil {
			return nil, nil, err
		}
		if notes["notes"] == nil {
			notes["notes"] = map[string]bool{}
		}
		notes["notes"][uid+"\x00"+id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows2, err := s.db.QueryContext(ctx,
		`SELECT f.user_id, f.id FROM files f JOIN users u ON u.id = f.user_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var uid, id string
		if err := rows2.Scan(&uid, &id); err != nil {
			return nil, nil, err
		}
		if files["files"] == nil {
			files["files"] = map[string]bool{}
		}
		files["files"][uid+"\x00"+id] = true
	}
	return notes, files, rows2.Err()
}
