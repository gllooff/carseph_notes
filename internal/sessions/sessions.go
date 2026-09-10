// Package sessions manages session tokens and the browser cookie.
// The cookie holds a random token; only its SHA-256 is persisted.
package sessions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

const cookieName = "notes_session"

// Manager issues and validates session cookies.
type Manager struct {
	TTL    time.Duration
	Secure bool // Secure cookie flag (false only for plain-HTTP local dev)
}

// NewToken generates a random 32-byte, url-safe token.
func NewToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Hash returns the hex SHA-256 of a token, as stored in the DB.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SetCookie writes the session cookie on the response.
func (m *Manager) SetCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   m.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie expires the session cookie.
func (m *Manager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// FromRequest extracts the raw token from the request cookie jar.
func FromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// SanitizeUserAgent truncates a User-Agent string for storage.
func SanitizeUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if len(ua) > 200 {
		ua = ua[:200]
	}
	return ua
}
