package store

import (
	"context"
	"database/sql"
	"errors"
)

// Passkey is a WebAuthn credential row.
type Passkey struct {
	ID             []byte // WebAuthn credential ID
	UserID         string
	Name           string
	PublicKey      []byte // COSE key
	Attestation    string
	AAGUID         []byte
	Transports     string // comma-separated
	SignCount      uint32
	BackupEligible bool
	BackupState    bool
	CreatedAt      int64
	LastUsedAt     sql.NullInt64
}

// CreatePasskey stores a new credential.
func (s *Store) CreatePasskey(ctx context.Context, p *Passkey) error {
	return insertPasskeyTx(ctx, s.db, p)
}

func insertPasskeyTx(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, p *Passkey) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO passkeys (id, user_id, name, public_key, attestation, aaguid, transports,
		 sign_count, backup_eligible, backup_state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.UserID, p.Name, p.PublicKey, p.Attestation, p.AAGUID, p.Transports,
		p.SignCount, boolInt(p.BackupEligible), boolInt(p.BackupState), p.CreatedAt)
	return err
}

// PasskeysByUser lists a user's passkeys (newest first).
func (s *Store) PasskeysByUser(ctx context.Context, userID string) ([]*Passkey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, name, public_key, attestation, aaguid, transports,
		        sign_count, backup_eligible, backup_state, created_at, last_used_at
		 FROM passkeys WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Passkey
	for rows.Next() {
		p := &Passkey{}
		var sc int64
		var be, bs int64
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.PublicKey, &p.Attestation, &p.AAGUID,
			&p.Transports, &sc, &be, &bs, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		p.SignCount = uint32(sc)
		p.BackupEligible = be != 0
		p.BackupState = bs != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// PasskeyByID returns a passkey, scoped to a user. A credential owned by
// someone else yields ErrNotFound.
func (s *Store) PasskeyByID(ctx context.Context, userID string, credID []byte) (*Passkey, error) {
	p := &Passkey{}
	var sc, be, bs int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, public_key, attestation, aaguid, transports,
		        sign_count, backup_eligible, backup_state, created_at, last_used_at
		 FROM passkeys WHERE id = ? AND user_id = ?`, credID, userID,
	).Scan(&p.ID, &p.UserID, &p.Name, &p.PublicKey, &p.Attestation, &p.AAGUID,
		&p.Transports, &sc, &be, &bs, &p.CreatedAt, &p.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.SignCount = uint32(sc)
	p.BackupEligible = be != 0
	p.BackupState = bs != 0
	return p, nil
}

// UpdatePasskeySignState updates the signature counter and backup flags
// after a successful assertion.
func (s *Store) UpdatePasskeySignState(ctx context.Context, credID []byte, signCount uint32, backupState bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE passkeys SET sign_count = ?, backup_state = ?, last_used_at = ? WHERE id = ?`,
		signCount, boolInt(backupState), now(), credID)
	return err
}

// RenamePasskey renames a passkey owned by user.
func (s *Store) RenamePasskey(ctx context.Context, userID string, credID []byte, name string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, credID, userID)
	return checkAffected(res, err)
}

// DeletePasskey removes a passkey owned by user.
func (s *Store) DeletePasskey(ctx context.Context, userID string, credID []byte) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM passkeys WHERE id = ? AND user_id = ?`, credID, userID)
	return checkAffected(res, err)
}

// CountPasskeys returns how many passkeys a user has.
func (s *Store) CountPasskeys(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM passkeys WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func checkAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
