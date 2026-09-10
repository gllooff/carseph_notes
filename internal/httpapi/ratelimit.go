package httpapi

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a per-key fixed-window counter with periodic expiry.
// It caps abusive clients on unauthenticated endpoints (per DESIGN §6).
type rateLimiter struct {
	mu       sync.Mutex
	hits     map[string]*window
	limit    int           // max hits per window
	window   time.Duration // window length
	lastGC   time.Time
	maxKeys  int // hard cap on tracked keys
}

type window struct {
	count int
	start time.Time
}

func newRateLimiter(limit int, d time.Duration) *rateLimiter {
	return &rateLimiter{
		hits:    map[string]*window{},
		limit:   limit,
		window:  d,
		lastGC:  time.Now(),
		maxKeys: 10_000,
	}
}

// allow records a hit for key and reports whether it is within the limit.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	nowT := time.Now()
	rl.gcLocked(nowT)

	w, ok := rl.hits[key]
	if !ok || nowT.Sub(w.start) >= rl.window {
		if !ok && len(rl.hits) >= rl.maxKeys {
			// Refuse to grow unboundedly under a flood of spoofed keys.
			return false
		}
		rl.hits[key] = &window{count: 1, start: nowT}
		return true
	}
	w.count++
	return w.count <= rl.limit
}

// gcLocked drops expired windows; called under lock.
func (rl *rateLimiter) gcLocked(nowT time.Time) {
	if nowT.Sub(rl.lastGC) < time.Minute && len(rl.hits) < rl.maxKeys/2 {
		return
	}
	for k, w := range rl.hits {
		if nowT.Sub(w.start) >= rl.window {
			delete(rl.hits, k)
		}
	}
	rl.lastGC = nowT
}

// clientKey identifies a client: X-Forwarded-For's first hop when present
// (Caddy sets it), else RemoteAddr.
func clientKey(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client per Caddy's reverse_proxy.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	for i := 0; i < len(host); i++ {
		if host[i] == ':' && (i == 0 || host[i-1] != ':') {
			return host[:i]
		}
	}
	return host
}

// rateLimit wraps a handler with per-IP limiting.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	// Auth endpoints: strict (they are the only unauthenticated surface).
	authLimiter := newRateLimiter(30, time.Minute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 10 && r.URL.Path[:10] == "/api/auth/" {
			if !authLimiter.allow(clientKey(r)) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again shortly")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
