// Package store contains all SQLite access: migrations, users, passkeys,
// sessions and invite codes. Every query that touches user data is scoped
// by user_id; ownership checks live here, not in handlers.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a row does not exist (or belongs to another user).
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on unique-constraint violations (e.g. username taken).
var ErrConflict = errors.New("conflict")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// pending migrations.
func Open(path string) (*Store, error) {
	dsn := "file:" + url.PathEscape(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc/sqlite serializes writes per connection; a small pool is plenty.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("bootstrap meta: %w", err)
	}
	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(CAST(value AS INTEGER)), 0) FROM meta WHERE key = 'schema_version'`).Scan(&current); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "%03d_", &n); err != nil {
			return fmt.Errorf("bad migration name %q", e.Name())
		}
		if n <= current {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('schema_version', ?)`, fmt.Sprint(n)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema_version %d: %w", n, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

// ----- users -----

// User is an account row.
type User struct {
	ID        string
	Username  string
	CreatedAt int64
}

// CreateUser inserts a user inside a transaction, consuming the invite code
// hash atomically. If the invite code is missing/used, ErrNotFound is returned
// and nothing is written.
func (s *Store) CreateUser(ctx context.Context, id, username, inviteCodeHash string) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := consumeInviteCodeTx(ctx, tx, inviteCodeHash); err != nil {
		return nil, err
	}
	u, err := insertUserTx(ctx, tx, id, username)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return u, nil
}

// RegisterWithPasskey atomically: consumes the invite code, creates the user,
// and stores the first passkey. All-or-nothing, so a cancelled WebAuthn
// ceremony can never leave behind a passkey-less account.
func (s *Store) RegisterWithPasskey(ctx context.Context, id, username, inviteCodeHash string, p *Passkey) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := consumeInviteCodeTx(ctx, tx, inviteCodeHash); err != nil {
		return nil, err
	}
	u, err := insertUserTx(ctx, tx, id, username)
	if err != nil {
		return nil, err
	}
	p.UserID = u.ID
	if err := insertPasskeyTx(ctx, tx, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return u, nil
}

func consumeInviteCodeTx(ctx context.Context, tx *sql.Tx, inviteCodeHash string) error {
	var invUsed sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT used_at FROM invite_codes WHERE code_hash = ?`, inviteCodeHash,
	).Scan(&invUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if invUsed.Valid {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE invite_codes SET used_at = ? WHERE code_hash = ?`, now(), inviteCodeHash)
	return err
}

func insertUserTx(ctx context.Context, tx *sql.Tx, id, username string) (*User, error) {
	ts := now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, username, created_at) VALUES (?, ?, ?)`, id, username, ts); err != nil {
		if isUniqueErr(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return &User{ID: id, Username: username, CreatedAt: ts}, nil
}

// UserByID returns a user.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// UserByUsername returns a user by (case-insensitive) username.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, created_at FROM users WHERE username = ? COLLATE NOCASE`, username,
	).Scan(&u.ID, &u.Username, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}
