package httpapi

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"notes/internal/blob"
	"notes/internal/config"
	"notes/internal/sessions"
	"notes/internal/store"
	"notes/internal/webauthn"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		ListenAddr:  "127.0.0.1:0",
		DataDir:     t.TempDir(),
		RPID:        "localhost",
		RPName:      "Test Notes",
		Origin:      "http://localhost:8080",
		SessionTTL:  720 * 3600 * 1000 * 1000 * 1000, // 720h as Duration
		MaxUploadMB: 25,
		Dev:         true, // plain-http cookies for httptest
	}
}

type testEnv struct {
	cfg *config.Config
	st  *store.Store
	ts  *httptest.Server
}

func testServer(t *testing.T) *testEnv {
	t.Helper()
	cfg := testConfig(t)

	// Bind a listener first so cfg.Origin can match the real port; the
	// WebAuthn service bakes the origin into ceremony validation.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin = "http://" + l.Addr().String()

	st, err := store.Open(cfg.DataDir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	wa, err := webauthn.New(cfg.RPID, cfg.RPName, cfg.Origin, st)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.New(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	sm := &sessions.Manager{TTL: cfg.SessionTTL, Secure: !cfg.Dev}
	s := New(cfg, st, wa, sm, blobs)
	ts := &httptest.Server{Listener: l, Config: &http.Server{Handler: s.Routes()}}
	ts.Start()
	t.Cleanup(ts.Close)
	return &testEnv{cfg: cfg, st: st, ts: ts}
}

func TestSecureHeaders(t *testing.T) {
	env := testServer(t)
	ts := env.ts
	res, err := http.Get(ts.URL + "/api/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := res.Header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	csp := res.Header.Get("Content-Security-Policy")
	if csp == "" || !contains(csp, "frame-ancestors 'none'") {
		t.Errorf("missing/short CSP: %q", csp)
	}
}

func TestOriginCheck(t *testing.T) {
	env := testServer(t)
	ts := env.ts

	mutate := func(origin string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/logout", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// 401 means the origin check passed and the auth gate rejected us.
	if res := mutate(env.cfg.Origin); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("same-origin POST: got %d, want 401 (origin check should pass)", res.StatusCode)
	}
	if res := mutate("https://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST: got %d, want 403", res.StatusCode)
	}
	if res := mutate(""); res.StatusCode != http.StatusForbidden {
		t.Errorf("missing Origin: got %d, want 403", res.StatusCode)
	}
	// GET requests are exempt.
	res, err := http.Get(ts.URL + "/api/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET without Origin: got %d, want 200", res.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	env := testServer(t)
	ts := env.ts
	res, err := http.Get(ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("me without session: got %d, want 401", res.StatusCode)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
