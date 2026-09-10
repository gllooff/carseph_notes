package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// Invite code helpers. Codes are stored as SHA-256 hashes; the plaintext code
// is shown exactly once when minted.

// CreateInviteCode mints a one-time code and stores its hash.
// Returns the plaintext code.
func (s *Store) CreateInviteCode(ctx context.Context) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw) // 32 hex chars
	sum := sha256.Sum256([]byte(code))
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invite_codes (code_hash, created_at) VALUES (?, ?)`,
		hex.EncodeToString(sum[:]), now())
	if err != nil {
		return "", err
	}
	return code, nil
}

// CheckInviteCode returns nil if codeHash exists and is unused, without
// consuming it (used to validate before starting a ceremony).
func (s *Store) CheckInviteCode(ctx context.Context, codeHash string) error {
	var used sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT used_at FROM invite_codes WHERE code_hash = ?`, codeHash).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if used.Valid {
		return ErrConflict
	}
	return nil
}

// HashInviteCode hashes a plaintext code for lookup.
func HashInviteCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
