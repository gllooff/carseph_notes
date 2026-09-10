// Package httpapi wires the HTTP mux, middleware and all handlers.
package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-webauthn/webauthn/protocol"

	"notes/internal/blob"
	"notes/internal/config"
	"notes/internal/sessions"
	"notes/internal/store"
	"notes/internal/web"
	"notes/internal/webauthn"
)

// Server holds all dependencies for the HTTP layer.
type Server struct {
	cfg   *config.Config
	st    *store.Store
	wa    *webauthn.Service
	sm    *sessions.Manager
	blobs *blob.Store
}

// New creates the HTTP layer.
func New(cfg *config.Config, st *store.Store, wa *webauthn.Service, sm *sessions.Manager, blobs *blob.Store) *Server {
	return &Server{cfg: cfg, st: st, wa: wa, sm: sm, blobs: blobs}
}

// Routes builds the application mux, including middleware.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Health (no auth).
	mux.HandleFunc("GET /api/healthz", s.handleHealth)

	// Auth ceremonies (no session required).
	mux.HandleFunc("POST /api/auth/register/begin", s.handleRegisterBegin)
	mux.HandleFunc("POST /api/auth/register/finish", s.handleRegisterFinish)
	mux.HandleFunc("POST /api/auth/login/begin", s.handleLoginBegin)
	mux.HandleFunc("POST /api/auth/login/finish", s.handleLoginFinish)
	mux.HandleFunc("POST /api/auth/logout", s.auth(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.auth(s.handleMe))

	// Passkey management (authenticated).
	mux.HandleFunc("POST /api/passkeys/begin", s.auth(s.handlePasskeyBegin))
	mux.HandleFunc("POST /api/passkeys/finish", s.auth(s.handlePasskeyFinish))
	mux.HandleFunc("PATCH /api/passkeys/{id}", s.auth(s.handlePasskeyRename))
	mux.HandleFunc("DELETE /api/passkeys/{id}", s.auth(s.handlePasskeyDelete))

	// Notes (authenticated).
	mux.HandleFunc("GET /api/notes", s.auth(s.listNotes))
	mux.HandleFunc("POST /api/notes", s.auth(s.createNote))
	mux.HandleFunc("GET /api/notes/{id}", s.auth(s.getNote))
	mux.HandleFunc("PUT /api/notes/{id}", s.auth(s.updateNote))
	mux.HandleFunc("DELETE /api/notes/{id}", s.auth(s.deleteNote))

	// Folders & tags (authenticated).
	mux.HandleFunc("GET /api/folders", s.auth(s.listFolders))
	mux.HandleFunc("POST /api/folders", s.auth(s.createFolder))
	mux.HandleFunc("PATCH /api/folders/{id}", s.auth(s.renameFolder))
	mux.HandleFunc("DELETE /api/folders/{id}", s.auth(s.deleteFolder))
	mux.HandleFunc("GET /api/tags", s.auth(s.listTags))

	// Files: images & PDFs (authenticated).
	mux.HandleFunc("GET /api/files", s.auth(s.listFiles))
	mux.HandleFunc("POST /api/files", s.auth(s.uploadFile))
	mux.HandleFunc("GET /api/files/{id}", s.auth(s.getFileMeta))
	mux.HandleFunc("GET /api/files/{id}/raw", s.auth(s.serveFileRaw))
	mux.HandleFunc("PATCH /api/files/{id}", s.auth(s.updateFile))
	mux.HandleFunc("DELETE /api/files/{id}", s.auth(s.deleteFile))

	// Frontend (SPA) last: everything that is not /api falls through here.
	mux.Handle("/", web.Handler())

	return logMiddleware(secureHeaders(s.checkOrigin(s.rateLimit(mux))))
}

// slogWarn logs a warning without failing requests.
func slogWarn(msg string, err error) {
	slog.Warn(msg, "err", err)
}

// sqlNullString mirrors sql.NullString so handlers avoid importing database/sql.
type sqlNullString = sql.NullString

// sqlNullOf wraps a non-empty string as a nullable folder reference.
type sqlNull = nullString

func sqlNullOf(v string) nullString { return nullString{Valid: true, String: v} }

func sqlNullOfPtr(p *string) nullString {
	if p == nil || *p == "" {
		return nullString{}
	}
	return nullString{Valid: true, String: *p}
}

// nullString mirrors sql.NullString for JSON-facing code.
type nullString = sqlNullString

// ----- health -----

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.healthz(w, r)
}

// ----- middleware -----

// secureHeaders applies static response headers to every response.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self'; "+
				"script-src 'self'; frame-src 'self'; frame-ancestors 'none'; "+
				"connect-src 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// checkOrigin rejects mutating requests whose Origin header does not match
// the configured origin (CSRF defense for cookie-based sessions).
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != s.cfg.Origin {
				writeError(w, http.StatusForbidden, "bad_origin", "cross-origin request rejected")
				return
			}
			// No Origin header at all: same-origin fetches always send it in
			// browsers; absent Origin with a mutating method is suspicious.
			if origin == "" {
				writeError(w, http.StatusForbidden, "missing_origin", "missing Origin header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		slog.Debug("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
	})
}

// auth wraps a handler requiring a valid session; injects the user.
type authedHandler func(w http.ResponseWriter, r *http.Request, u *store.User)

func (s *Server) auth(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := sessions.FromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sign in required")
			return
		}
		sess, err := s.st.SessionByID(r.Context(), sessions.Hash(token))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sign in required")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "session lookup failed")
			return
		}
		u, err := s.st.UserByID(r.Context(), sess.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sign in required")
			return
		}
		if err := s.st.TouchSession(r.Context(), sess.ID, int64(s.cfg.SessionTTL.Seconds())); err != nil {
			slog.Warn("touch session", "err", err)
		}
		h(w, r, u)
	}
}

// ----- JSON helpers -----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

// decodeJSON parses a JSON body into dst with a hard size cap.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "invalid JSON body")
		return false
	}
	return true
}

// writeWebauthnError maps protocol errors to a JSON error response.
func writeWebauthnError(w http.ResponseWriter, err error) {
	var pErr *protocol.Error
	if errors.As(err, &pErr) {
		slog.Warn("webauthn protocol error", "err", err, "details", pErr.Details, "info", pErr.DevInfo)
		writeError(w, http.StatusBadRequest, "webauthn_error", pErr.Details)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
