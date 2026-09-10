package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
)

// testClient tracks cookies the way a browser would: session and ceremony
// cookies are stored independently and both sent with each request.
type testClient struct {
	env      *testEnv
	session  string
	ceremony string
}

func (c *testClient) do(method, path string, body any) (*http.Response, []byte) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(mustJSON(body))
	}
	req, err := http.NewRequest(method, c.env.ts.URL+path, rdr)
	if err != nil {
		panic(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", c.env.cfg.Origin)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	cookie := c.session
	if c.ceremony != "" {
		if cookie != "" {
			cookie += "; "
		}
		cookie += c.ceremony
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		panic(err)
	}
	if cs := extractCookie(res, "notes_session"); cs != "" {
		c.session = cs
	}
	if cs := extractCookie(res, ceremonyCookie); cs != "" {
		c.ceremony = cs
	}
	return res, data
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func extractCookie(res *http.Response, name string) string {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c.Name + "=" + c.Value
		}
	}
	return ""
}

func wantStatus(t *testing.T, res *http.Response, data []byte, want int, what string) {
	t.Helper()
	if res == nil || res.StatusCode != want {
		t.Fatalf("%s: status=%v body=%s, want %d", what, statusOf(res), string(data), want)
	}
}

func statusOf(res *http.Response) any {
	if res == nil {
		return "<nil>"
	}
	return res.StatusCode
}

func TestWebAuthnRegisterLoginFlow(t *testing.T) {
	env := testServer(t)
	ctx := context.Background()

	code, err := env.st.CreateInviteCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := &testClient{env: env}

	// Bad invite rejected at begin.
	res, data := c.do("POST", "/api/auth/register/begin",
		map[string]any{"username": "mallory", "invite_code": "nope"})
	wantStatus(t, res, data, http.StatusBadRequest, "bad invite")
	if !bytes.Contains(data, []byte("bad_invite")) {
		t.Errorf("expected bad_invite code, got %s", data)
	}

	// Good invite: registration begin returns creation options.
	res, data = c.do("POST", "/api/auth/register/begin",
		map[string]any{"username": "alice", "invite_code": code})
	wantStatus(t, res, data, http.StatusOK, "register begin")
	var createOpts protocol.PublicKeyCredentialCreationOptions
	if err := json.Unmarshal(data, &createOpts); err != nil {
		t.Fatalf("parse creation options: %v\n%s", err, data)
	}
	if len(createOpts.Challenge) == 0 {
		t.Fatal("no challenge in creation options")
	}
	if createOpts.RelyingParty.ID != "localhost" {
		t.Errorf("rp.id = %q, want localhost", createOpts.RelyingParty.ID)
	}

	// Finish: fake authenticator signs the attestation. The userHandle it
	// stores and echoes on discoverable logins is the ID from the options.
	authn := newFakeAuthn("localhost", env.cfg.Origin)
	userHandle, err := base64.RawURLEncoding.DecodeString(createOpts.User.ID.(string))
	if err != nil {
		t.Fatalf("decode user handle: %v", err)
	}
	authn.userHandle = userHandle
	attestation, err := authn.register(&createOpts)
	if err != nil {
		t.Fatal(err)
	}
	res, data = c.do("POST", "/api/auth/register/finish",
		map[string]any{"ceremony_id": "", "response": attestation})
	wantStatus(t, res, data, http.StatusOK, "register finish")

	// Session cookie now works.
	res, data = c.do("GET", "/api/auth/me", nil)
	wantStatus(t, res, data, http.StatusOK, "me after register")
	if !bytes.Contains(data, []byte("alice")) {
		t.Errorf("me payload missing username: %s", data)
	}

	// A second registration with the same (now used) invite fails.
	res, data = c.do("POST", "/api/auth/register/begin",
		map[string]any{"username": "bob", "invite_code": code})
	wantStatus(t, res, data, http.StatusBadRequest, "reused invite")

	// --- login: discoverable ceremony (empty username) ---
	c.session = ""; c.ceremony = "" // act like a fresh browser
	res, data = c.do("POST", "/api/auth/login/begin", map[string]any{"username": ""})
	wantStatus(t, res, data, http.StatusOK, "discoverable login begin")
	var assertOpts protocol.PublicKeyCredentialRequestOptions
	if err := json.Unmarshal(data, &assertOpts); err != nil {
		t.Fatalf("parse assertion options: %v\n%s", err, data)
	}
	if len(assertOpts.Challenge) == 0 {
		t.Fatal("no challenge in assertion options")
	}
	assertion, err := authn.assert(&assertOpts)
	if err != nil {
		t.Fatal(err)
	}
	res, data = c.do("POST", "/api/auth/login/finish",
		map[string]any{"ceremony_id": "", "response": assertion})
	wantStatus(t, res, data, http.StatusOK, "discoverable login finish")

	// Session works again after login.
	res, data = c.do("GET", "/api/auth/me", nil)
	wantStatus(t, res, data, http.StatusOK, "me after login")
	if !bytes.Contains(data, []byte("alice")) {
		t.Errorf("me payload missing username: %s", data)
	}

	// --- login with username (non-discoverable) ---
	c.session = ""; c.ceremony = ""
	res, data = c.do("POST", "/api/auth/login/begin", map[string]any{"username": "alice"})
	wantStatus(t, res, data, http.StatusOK, "login begin by username")
	if err := json.Unmarshal(data, &assertOpts); err != nil {
		t.Fatalf("parse assertion options: %v", err)
	}
	assertion, err = authn.assert(&assertOpts)
	if err != nil {
		t.Fatal(err)
	}
	res, data = c.do("POST", "/api/auth/login/finish",
		map[string]any{"ceremony_id": "", "response": assertion})
	wantStatus(t, res, data, http.StatusOK, "login finish by username")

	// Unknown username does not leak existence details but does 404.
	res, data = c.do("POST", "/api/auth/login/begin", map[string]any{"username": "ghost"})
	wantStatus(t, res, data, http.StatusNotFound, "login begin unknown user")

	// --- add a second passkey ---
	res, data = c.do("POST", "/api/passkeys/begin", map[string]any{})
	wantStatus(t, res, data, http.StatusOK, "passkey begin")
	if err := json.Unmarshal(data, &createOpts); err != nil {
		t.Fatalf("parse creation options: %v", err)
	}
	authn2 := newFakeAuthn("localhost", env.cfg.Origin)
	authn2.userHandle = authn.userHandle
	attestation2, err := authn2.register(&createOpts)
	if err != nil {
		t.Fatal(err)
	}
	res, data = c.do("POST", "/api/passkeys/finish",
		map[string]any{"ceremony_id": "", "response": attestation2})
	wantStatus(t, res, data, http.StatusOK, "passkey finish")

	// Two passkeys listed.
	res, data = c.do("GET", "/api/auth/me", nil)
	wantStatus(t, res, data, http.StatusOK, "me with two passkeys")
	var me struct {
		Passkeys []struct{ ID string } `json:"passkeys"`
	}
	if err := json.Unmarshal(data, &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Passkeys) != 2 {
		t.Fatalf("want 2 passkeys, got %d: %s", len(me.Passkeys), data)
	}

	// Deleting the last passkey is rejected; deleting one of two works.
	res, data = c.do("DELETE", "/api/passkeys/"+me.Passkeys[0].ID, nil)
	wantStatus(t, res, data, http.StatusOK, "delete a passkey")
	res, data = c.do("DELETE", "/api/passkeys/"+me.Passkeys[1].ID, nil)
	wantStatus(t, res, data, http.StatusConflict, "delete last passkey")

	// --- logout ---
	res, data = c.do("POST", "/api/auth/logout", map[string]any{})
	wantStatus(t, res, data, http.StatusOK, "logout")
	res, data = c.do("GET", "/api/auth/me", nil)
	wantStatus(t, res, data, http.StatusUnauthorized, "me after logout")
}
