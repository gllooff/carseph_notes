package store

import (
	"context"
	"database/sql"
	"errors"
)

// Note is a note row (metadata; body lives in the blob store).
type Note struct {
	ID        string
	UserID    string
	FolderID  sql.NullString
	Title     string
	Size      int64
	CreatedAt int64
	UpdatedAt int64
	Tags      []string // populated on read when requested
}

// NoteFilter narrows list queries.
type NoteFilter struct {
	FolderID string // "" = all, "none" = unfiled
	Tag      string
	Q        string // title substring
}

const (
	noteCols = `n.id, n.user_id, n.folder_id, n.title, n.size, n.created_at, n.updated_at`
)

// CreateNote inserts the metadata row. The caller writes the blob separately
// (or uses NotesFacade which coordinates both).
func (s *Store) CreateNote(ctx context.Context, n *Note) error {
	n.CreatedAt = now()
	n.UpdatedAt = n.CreatedAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertNoteTx(ctx, tx, n); err != nil {
		return err
	}
	if err := setTagsTx(ctx, tx, "note", n.UserID, n.ID, n.Tags); err != nil {
		return err
	}
	return tx.Commit()
}

func insertNoteTx(ctx context.Context, tx *sql.Tx, n *Note) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO notes (id, user_id, folder_id, title, size, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.UserID, n.FolderID, n.Title, n.Size, n.CreatedAt, n.UpdatedAt)
	return err
}

// NotesByUser lists notes ordered by most recently updated, with tags.
func (s *Store) NotesByUser(ctx context.Context, userID string, f NoteFilter) ([]*Note, error) {
	q := `SELECT ` + noteCols + ` FROM notes n WHERE n.user_id = ?`
	args := []any{userID}
	if f.FolderID == "none" {
		q += ` AND n.folder_id IS NULL`
	} else if f.FolderID != "" {
		q += ` AND n.folder_id = ?`
		args = append(args, f.FolderID)
	}
	if f.Tag != "" {
		q += ` AND EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id
		       WHERE it.item_type = 'note' AND it.item_id = n.id AND t.name = ? COLLATE NOCASE)`
		args = append(args, f.Tag)
	}
	if f.Q != "" {
		q += ` AND instr(lower(n.title), lower(?)) > 0`
		args = append(args, f.Q)
	}
	q += ` ORDER BY n.updated_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Note
	for rows.Next() {
		n := &Note{}
		if err := rows.Scan(&n.ID, &n.UserID, &n.FolderID, &n.Title, &n.Size, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachTags(ctx, "note", userID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// NoteByID returns one note scoped to the user, with tags.
func (s *Store) NoteByID(ctx context.Context, userID, id string) (*Note, error) {
	n := &Note{}
	err := s.db.QueryRowContext(ctx,
		`SELECT `+noteCols+` FROM notes n WHERE n.id = ? AND n.user_id = ?`, id, userID,
	).Scan(&n.ID, &n.UserID, &n.FolderID, &n.Title, &n.Size, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	single := []*Note{n}
	if err := s.attachTags(ctx, "note", userID, single); err != nil {
		return nil, err
	}
	return n, nil
}

// UpdateNote updates metadata (+tags). Size is maintained by the caller.
func (s *Store) UpdateNote(ctx context.Context, n *Note) error {
	n.UpdatedAt = now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE notes SET folder_id = ?, title = ?, size = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		n.FolderID, n.Title, n.Size, n.UpdatedAt, n.ID, n.UserID)
	if err != nil {
		return err
	}
	if !affected(res) {
		return ErrNotFound
	}
	if n.Tags != nil {
		if err := setTagsTx(ctx, tx, "note", n.UserID, n.ID, n.Tags); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteNote removes the row + its tag links, prunes orphaned tags.
func (s *Store) DeleteNote(ctx context.Context, userID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM notes WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if !affected(res) {
		return ErrNotFound
	}
	// item_tags has no FK on (item_id), so clean links explicitly.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM item_tags WHERE item_type = 'note' AND item_id = ? AND user_id = ?`,
		id, userID); err != nil {
		return err
	}
	if err := s.pruneOrphanTagsTx(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func affected(res sql.Result) bool {
	n, err := res.RowsAffected()
	return err == nil && n > 0
}

// attachTags fills Tags for each note/file row in one query.
func (s *Store) attachTags(ctx context.Context, itemType, userID string, items []*Note) error {
	if len(items) == 0 {
		return nil
	}
	byID := map[string]*Note{}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		byID[it.ID] = it
		ids = append(ids, it.ID)
	}
	q := `SELECT it.item_id, t.name FROM item_tags it
	      JOIN tags t ON t.id = it.tag_id
	      WHERE it.user_id = ? AND it.item_type = ?
	      ORDER BY t.name`
	rows, err := s.db.QueryContext(ctx, q, userID, itemType)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID, tag string
		if err := rows.Scan(&itemID, &tag); err != nil {
			return err
		}
		if n, ok := byID[itemID]; ok {
			n.Tags = append(n.Tags, tag)
		}
	}
	return rows.Err()
}

// setTagsTx replaces the tag set of an item; unknown tag names are created.
func setTagsTx(ctx context.Context, tx *sql.Tx, itemType, userID, itemID string, tags []string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM item_tags WHERE item_type = ? AND item_id = ? AND user_id = ?`,
		itemType, itemID, userID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range tags {
		name = normalizeTag(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		var tagID string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM tags WHERE user_id = ? AND name = ? COLLATE NOCASE`, userID, name,
		).Scan(&tagID)
		if errors.Is(err, sql.ErrNoRows) {
			tagID = newID()
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO tags (id, user_id, name) VALUES (?, ?, ?)`, tagID, userID, name); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO item_tags (item_type, item_id, tag_id, user_id) VALUES (?, ?, ?, ?)`,
			itemType, itemID, tagID, userID); err != nil {
			return err
		}
	}
	return nil
}

// pruneOrphanTagsTx removes tags with no remaining links.
func (s *Store) pruneOrphanTagsTx(ctx context.Context, tx *sql.Tx, userID string) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM tags WHERE user_id = ? AND NOT EXISTS
		 (SELECT 1 FROM item_tags it WHERE it.tag_id = tags.id)`, userID)
	return err
}

func normalizeTag(s string) string {
	s = trimSpaceBytes(s)
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

func trimSpaceBytes(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
