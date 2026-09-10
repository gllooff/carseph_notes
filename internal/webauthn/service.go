// Package webauthn orchestrates WebAuthn ceremonies (registration, login,
// adding a passkey) on top of go-webauthn and the store. In-flight ceremony
// state is held in memory keyed by a single-use ceremony ID that the client
// echoes back via cookie.
package webauthn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"notes/internal/store"
)

// ErrCeremony is returned when a begin/finish pair cannot be matched
// (missing, expired, already used, or from another browser).
var ErrCeremony = errors.New("webauthn ceremony invalid or expired")

// ErrInviteUsed is returned when an invite code exists but was consumed.
var ErrInviteUsed = errors.New("invite code already used")

// Service runs WebAuthn ceremonies.
type Service struct {
	wa     *webauthn.WebAuthn
	st     *store.Store
	mu     sync.Mutex
	live   map[string]*ceremony
	ttl    time.Duration
	lastGC time.Time
}

type ceremony struct {
	kind       string // "register" | "login" | "add"
	username   string // register: requested name; login: "" for discoverable
	userID     string // register: ID fixed at begin (authenticator stores it as userHandle)
	inviteHash string // register only
	session    *webauthn.SessionData
	expires    time.Time
}

// New creates the service. rpID is the effective domain (eTLD+1 scope),
// origin the exact scheme://host[:port] the browser sees.
func New(rpID, rpName, origin string, st *store.Store) (*Service, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpName,
		RPOrigins:     []string{origin},
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn init: %w", err)
	}
	return &Service{
		wa:     wa,
		st:     st,
		live:   map[string]*ceremony{},
		ttl:    time.Minute,
		lastGC: time.Now(),
	}, nil
}

func newCeremonyID() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *Service) put(c *ceremony) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	id, err := newCeremonyID()
	if err != nil {
		return "", err
	}
	c.expires = time.Now().Add(s.ttl)
	s.live[id] = c
	return id, nil
}

func (s *Service) take(id, kind string) (*ceremony, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.live[id]
	if !ok || c.kind != kind || time.Now().After(c.expires) {
		delete(s.live, id)
		return nil, ErrCeremony
	}
	delete(s.live, id) // single-use
	return c, nil
}

// gcLocked drops expired ceremonies; called under lock.
func (s *Service) gcLocked() {
	if time.Since(s.lastGC) < time.Minute {
		return
	}
	nowT := time.Now()
	for id, c := range s.live {
		if nowT.After(c.expires) {
			delete(s.live, id)
		}
	}
	s.lastGC = nowT
}

// ----- registration -----

// BeginRegister starts account registration: validates the invite code is
// present/unused and the username free, then returns the credential creation
// options and ceremony ID. The user row is only written on successful Finish.
func (s *Service) BeginRegister(ctx context.Context, username, inviteHash string) (*protocol.CredentialCreation, string, error) {
	if err := s.st.CheckInviteCode(ctx, inviteHash); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, "", ErrInviteUsed
		}
		return nil, "", err
	}
	if err := validUsername(username); err != nil {
		return nil, "", err
	}
	if _, err := s.st.UserByUsername(ctx, username); err == nil {
		return nil, "", store.ErrConflict
	}
	// The user ID is fixed here: the authenticator stores it as userHandle and
	// echoes it on discoverable logins, so the created row must keep this ID.
	u := &store.User{ID: newUserID(), Username: username}
	opts, session, err := s.wa.BeginRegistration(
		&webUser{u: u},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(), // legacy field for older browsers
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		return nil, "", err
	}
	id, err := s.put(&ceremony{kind: "register", username: username, userID: u.ID, inviteHash: inviteHash, session: session})
	if err != nil {
		return nil, "", err
	}
	return opts, id, nil
}

// FinishRegister verifies the attestation response, creates the user and
// first passkey atomically, and returns them.
func (s *Service) FinishRegister(ctx context.Context, ceremonyID string, r *http.Request) (*store.User, *store.Passkey, error) {
	c, err := s.take(ceremonyID, "register")
	if err != nil {
		return nil, nil, err
	}
	cred, err := s.wa.FinishRegistration(&webUser{u: &store.User{ID: c.userID, Username: c.username}}, *c.session, r)
	if err != nil {
		return nil, nil, fmt.Errorf("attestation verification failed: %w", err)
	}
	u, err := s.st.RegisterWithPasskey(ctx, c.userID, c.username, c.inviteHash, credToPasskey(cred, ""))
	if err != nil {
		return nil, nil, err
	}
	return u, credToPasskey(cred, ""), nil
}

// BeginAddPasskey starts a passkey ceremony for an authenticated user.
func (s *Service) BeginAddPasskey(ctx context.Context, u *store.User) (*protocol.CredentialCreation, string, error) {
	creds, err := s.userCredentials(ctx, u.ID)
	if err != nil {
		return nil, "", err
	}
	opts, session, err := s.wa.BeginRegistration(
		&webUser{u: u, creds: creds},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		return nil, "", err
	}
	id, err := s.put(&ceremony{kind: "add", session: session})
	if err != nil {
		return nil, "", err
	}
	return opts, id, nil
}

// FinishAddPasskey verifies and stores a new passkey for the user.
func (s *Service) FinishAddPasskey(ctx context.Context, ceremonyID, userID string, r *http.Request) (*store.Passkey, error) {
	c, err := s.take(ceremonyID, "add")
	if err != nil {
		return nil, err
	}
	u, err := s.st.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	creds, err := s.userCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	cred, err := s.wa.FinishRegistration(&webUser{u: u, creds: creds}, *c.session, r)
	if err != nil {
		return nil, fmt.Errorf("attestation verification failed: %w", err)
	}
	p := credToPasskey(cred, "")
	p.UserID = userID
	if err := s.st.CreatePasskey(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ----- login -----

// BeginLogin starts an assertion ceremony. An empty username requests
// discoverable credentials (usernameless login).
func (s *Service) BeginLogin(ctx context.Context, username string) (*protocol.CredentialAssertion, string, error) {
	var (
		opts    *protocol.CredentialAssertion
		session *webauthn.SessionData
		err     error
	)
	if username == "" {
		opts, session, err = s.wa.BeginDiscoverableLogin(
			webauthn.WithUserVerification(protocol.VerificationPreferred))
	} else {
		u, uerr := s.st.UserByUsername(ctx, username)
		if uerr != nil {
			return nil, "", store.ErrNotFound
		}
		creds, cerr := s.userCredentials(ctx, u.ID)
		if cerr != nil {
			return nil, "", cerr
		}
		opts, session, err = s.wa.BeginLogin(&webUser{u: u, creds: creds},
			webauthn.WithUserVerification(protocol.VerificationPreferred))
	}
	if err != nil {
		return nil, "", err
	}
	id, err := s.put(&ceremony{kind: "login", username: username, session: session})
	if err != nil {
		return nil, "", err
	}
	return opts, id, nil
}

// LoginResult is the outcome of a verified assertion.
type LoginResult struct {
	User        *store.User
	CredID      []byte
	SignCount   uint32
	BackupState bool
}

// FinishLogin verifies the assertion and returns the authenticated user.
func (s *Service) FinishLogin(ctx context.Context, ceremonyID string, r *http.Request) (*LoginResult, error) {
	c, err := s.take(ceremonyID, "login")
	if err != nil {
		return nil, err
	}

	var user *store.User
	var cred *webauthn.Credential
	if c.username == "" {
		cred, err = s.wa.FinishDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
			u, lerr := s.st.UserByID(ctx, string(userHandle))
			if lerr != nil {
				return nil, store.ErrNotFound
			}
			user = u
			creds, cerr := s.userCredentials(ctx, u.ID)
			if cerr != nil {
				return nil, cerr
			}
			return &webUser{u: u, creds: creds}, nil
		}, *c.session, r)
	} else {
		u, uerr := s.st.UserByUsername(ctx, c.username)
		if uerr != nil {
			return nil, store.ErrNotFound
		}
		creds, cerr := s.userCredentials(ctx, u.ID)
		if cerr != nil {
			return nil, cerr
		}
		cred, err = s.wa.FinishLogin(&webUser{u: u, creds: creds}, *c.session, r)
		user = u
	}
	if err != nil {
		return nil, fmt.Errorf("assertion verification failed: %w", err)
	}
	if err := s.st.UpdatePasskeySignState(ctx, cred.ID, cred.Authenticator.SignCount, cred.Flags.BackupState); err != nil {
		slog.Warn("update sign state", "err", err)
	}
	return &LoginResult{
		User:        user,
		CredID:      cred.ID,
		SignCount:   cred.Authenticator.SignCount,
		BackupState: cred.Flags.BackupState,
	}, nil
}

// ----- helpers -----

// userCredentials loads a user's stored passkeys as go-webauthn credentials.
func (s *Service) userCredentials(ctx context.Context, userID string) ([]webauthn.Credential, error) {
	pks, err := s.st.PasskeysByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]webauthn.Credential, 0, len(pks))
	for _, p := range pks {
		out = append(out, passkeyToCredential(p))
	}
	return out, nil
}

func passkeyToCredential(p *store.Passkey) webauthn.Credential {
	return webauthn.Credential{
		ID:              p.ID,
		PublicKey:       p.PublicKey,
		AttestationType: p.Attestation,
		Transport:       parseTransports(p.Transports),
		Flags: webauthn.CredentialFlags{
			BackupEligible: p.BackupEligible,
			BackupState:    p.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    p.AAGUID,
			SignCount: p.SignCount,
		},
	}
}

func credToPasskey(c *webauthn.Credential, name string) *store.Passkey {
	return &store.Passkey{
		ID:             c.ID,
		PublicKey:      c.PublicKey,
		Attestation:    c.AttestationType,
		AAGUID:         c.Authenticator.AAGUID,
		Transports:     joinTransports(c.Transport),
		SignCount:      c.Authenticator.SignCount,
		BackupEligible: c.Flags.BackupEligible,
		BackupState:    c.Flags.BackupState,
		CreatedAt:      time.Now().Unix(),
		Name:           name,
	}
}

func parseTransports(s string) []protocol.AuthenticatorTransport {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]protocol.AuthenticatorTransport, 0, len(parts))
	for _, p := range parts {
		out = append(out, protocol.AuthenticatorTransport(p))
	}
	return out
}

func joinTransports(ts []protocol.AuthenticatorTransport) string {
	if len(ts) == 0 {
		return ""
	}
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = string(t)
	}
	return strings.Join(parts, ",")
}

func validUsername(name string) error {
	if len(name) < 3 || len(name) > 32 {
		return errors.New("username must be 3-32 characters")
	}
	for _, r := range name {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return errors.New("username may contain letters, digits, '.', '-', '_' only")
		}
	}
	return nil
}

func newUserID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// webUser adapts store.User to the go-webauthn User interface.
type webUser struct {
	u     *store.User
	creds []webauthn.Credential
}

func (w *webUser) WebAuthnID() []byte                         { return []byte(w.u.ID) }
func (w *webUser) WebAuthnName() string                       { return w.u.Username }
func (w *webUser) WebAuthnDisplayName() string                { return w.u.Username }
func (w *webUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }
