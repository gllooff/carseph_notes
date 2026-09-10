package store

import (
	"context"
	"database/sql"
	"errors"
)

// Session is a session row. ID is the SHA-256 hex of the cookie token;
// the raw token never touches the database.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  int64
	ExpiresAt  int64
	LastSeenAt int64
	UserAgent  string
}

// CreateSession inserts a session.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt, sess.UserAgent)
	return err
}

// SessionByID returns a live (unexpired) session.
func (s *Store) SessionByID(ctx context.Context, id string) (*Session, error) {
	sess := &Session{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, created_at, expires_at, last_seen_at, user_agent
		 FROM sessions WHERE id = ? AND expires_at > ?`, id, now(),
	).Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt, &sess.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// TouchSession slides the expiry window forward.
func (s *Store) TouchSession(ctx context.Context, id string, ttl int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		now(), now()+ttl, id)
	return err
}

// DeleteSession removes one session (logout).
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteExpiredSessions removes sessions whose TTL has passed.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
