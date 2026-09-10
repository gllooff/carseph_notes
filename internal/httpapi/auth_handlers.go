package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"notes/internal/sessions"
	"notes/internal/store"
	"notes/internal/webauthn"
)

const ceremonyCookie = "notes_ceremony"

// beginRequest is the body for both register/begin and login/begin.
type beginRequest struct {
	Username   string `json:"username"`
	InviteCode string `json:"invite_code"`
}

// finishRequest is the body for finish calls: the ceremony id and the
// PublicKeyCredential JSON produced by the browser.
type finishRequest struct {
	CeremonyID string          `json:"ceremony_id"`
	Response   json.RawMessage `json:"response"`
}

// ----- ceremony cookie plumbing -----

func (s *Server) setCeremonyCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     ceremonyCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   120, // single-use server-side; expires after 60s
		HttpOnly: true,
		Secure:   s.sm.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCeremonyCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ceremonyCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.sm.Secure, SameSite: http.SameSiteLaxMode,
	})
}

func ceremonyIDFromRequest(r *http.Request) string {
	if c, err := r.Cookie(ceremonyCookie); err == nil {
		return c.Value
	}
	return ""
}

// ----- registration -----

func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req beginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	inviteHash := store.HashInviteCode(req.InviteCode)
	opts, ceremonyID, err := s.wa.BeginRegister(r.Context(), req.Username, inviteHash)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusBadRequest, "bad_invite", "invite code is invalid")
	case errors.Is(err, webauthn.ErrInviteUsed):
		writeError(w, http.StatusBadRequest, "bad_invite", "invite code already used")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "username_taken", "username already registered")
	case err != nil:
		writeError(w, http.StatusBadRequest, "bad_request", friendly(err))
	}
	if err != nil {
		return
	}
	s.setCeremonyCookie(w, ceremonyID)
	// Return the inner options (the {rp, user, challenge, …} object), which is
	// what @simplewebauthn/browser expects as optionsJSON.
	writeJSON(w, http.StatusOK, opts.Response)
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CeremonyID == "" {
		req.CeremonyID = ceremonyIDFromRequest(r)
	}
	// go-webauthn parses the credential JSON from the request body; our API
	// wraps it, so hand it a body containing only the response object.
	r.Body = io.NopCloser(bytes.NewReader(req.Response))
	u, _, err := s.wa.FinishRegister(r.Context(), req.CeremonyID, r)
	if err != nil {
		s.clearCeremonyCookie(w)
		writeCeremonyError(w, err)
		return
	}
	s.clearCeremonyCookie(w)
	s.issueSession(w, r, u)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{"id": u.ID, "username": u.Username},
	})
}

// ----- login -----

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	var req beginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	opts, ceremonyID, err := s.wa.BeginLogin(r.Context(), req.Username)
	if errors.Is(err, store.ErrNotFound) {
		// Do not reveal whether the username exists.
		writeError(w, http.StatusNotFound, "no_credentials", "no credentials available")
		return
	}
	if err != nil {
		writeWebauthnError(w, err)
		return
	}
	s.setCeremonyCookie(w, ceremonyID)
	writeJSON(w, http.StatusOK, opts.Response)
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CeremonyID == "" {
		req.CeremonyID = ceremonyIDFromRequest(r)
	}
	r.Body = io.NopCloser(bytes.NewReader(req.Response))
	res, err := s.wa.FinishLogin(r.Context(), req.CeremonyID, r)
	if err != nil {
		s.clearCeremonyCookie(w)
		writeCeremonyError(w, err)
		return
	}
	s.clearCeremonyCookie(w)
	s.issueSession(w, r, res.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{"id": res.User.ID, "username": res.User.Username},
	})
}

// ----- session & user info -----

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	token, err := sessions.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not create session")
		return
	}
	now := time.Now().Unix()
	if err := s.st.CreateSession(r.Context(), &store.Session{
		ID:         sessions.Hash(token),
		UserID:     u.ID,
		CreatedAt:  now,
		ExpiresAt:  now + int64(s.cfg.SessionTTL.Seconds()),
		LastSeenAt: now,
		UserAgent:  sessions.SanitizeUserAgent(r.UserAgent()),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not persist session")
		return
	}
	s.sm.SetCookie(w, token, s.cfg.SessionTTL)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, _ *store.User) {
	if token, ok := sessions.FromRequest(r); ok {
		if err := s.st.DeleteSession(r.Context(), sessions.Hash(token)); err != nil {
			slog.Warn("logout delete session", "err", err)
		}
	}
	s.sm.ClearCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type passkeyInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
	LastUsed  int64  `json:"last_used_at"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u *store.User) {
	pks, err := s.st.PasskeysByUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list passkeys")
		return
	}
	ids := make([]passkeyInfo, 0, len(pks))
	for _, p := range pks {
		last := int64(0)
		if p.LastUsedAt.Valid {
			last = p.LastUsedAt.Int64
		}
		ids = append(ids, passkeyInfo{ID: b64(p.ID), Name: p.Name, CreatedAt: p.CreatedAt, LastUsed: last})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":     map[string]any{"id": u.ID, "username": u.Username},
		"passkeys": ids,
	})
}

// ----- passkey management -----

func (s *Server) handlePasskeyBegin(w http.ResponseWriter, r *http.Request, u *store.User) {
	opts, ceremonyID, err := s.wa.BeginAddPasskey(r.Context(), u)
	if err != nil {
		writeWebauthnError(w, err)
		return
	}
	s.setCeremonyCookie(w, ceremonyID)
	writeJSON(w, http.StatusOK, opts.Response)
}

func (s *Server) handlePasskeyFinish(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		CeremonyID string          `json:"ceremony_id"`
		Name       string          `json:"name"`
		Response   json.RawMessage `json:"response"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CeremonyID == "" {
		req.CeremonyID = ceremonyIDFromRequest(r)
	}
	r.Body = io.NopCloser(bytes.NewReader(req.Response))
	pk, err := s.wa.FinishAddPasskey(r.Context(), req.CeremonyID, u.ID, r)
	if err != nil {
		s.clearCeremonyCookie(w)
		writeCeremonyError(w, err)
		return
	}
	s.clearCeremonyCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": b64(pk.ID), "name": pk.Name, "created_at": pk.CreatedAt,
	})
}

func (s *Server) handlePasskeyRename(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	credID, ok := credIDFromPath(w, r)
	if !ok {
		return
	}
	if err := s.st.RenamePasskey(r.Context(), u.ID, credID, trimName(req.Name)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request, u *store.User) {
	credID, ok := credIDFromPath(w, r)
	if !ok {
		return
	}
	n, err := s.st.CountPasskeys(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not count passkeys")
		return
	}
	if n <= 1 {
		writeError(w, http.StatusConflict, "last_passkey", "cannot delete your only passkey")
		return
	}
	if err := s.st.DeletePasskey(r.Context(), u.ID, credID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----- shared helpers -----

// credIDFromPath decodes the {id} path segment (base64url credential ID).
func credIDFromPath(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw := r.PathValue("id")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "bad_id", "missing id")
		return nil, false
	}
	// Accept both base64url and raw hex for convenience.
	if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return b, true
	}
	if b, err := hex.DecodeString(raw); err == nil {
		return b, true
	}
	writeError(w, http.StatusBadRequest, "bad_id", "invalid credential id encoding")
	return nil, false
}

// writeCeremonyError maps ceremony/protocol failures to client errors.
func writeCeremonyError(w http.ResponseWriter, err error) {
	if errors.Is(err, webauthn.ErrCeremony) {
		writeError(w, http.StatusBadRequest, "ceremony", "ceremony expired or invalid; start again")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_credentials", "no credentials available")
		return
	}
	writeWebauthnError(w, err)
}

// writeStoreError maps store errors onto HTTP responses.
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

// friendly strips log-style prefixes from validation errors.
func friendly(err error) string {
	return strings.TrimPrefix(err.Error(), "")
}

func trimName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
