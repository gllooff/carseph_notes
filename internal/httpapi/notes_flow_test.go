package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
)

func bg() context.Context { return context.Background() }

func mustUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// seedUser registers + logs in a user via the real WebAuthn flow, returning
// a client with a live session.
func seedUser(t *testing.T, env *testEnv, name string) (*testClient, *fakeAuthn) {
	t.Helper()
	code, err := env.st.CreateInviteCode(bg())
	if err != nil {
		t.Fatal(err)
	}
	c := &testClient{env: env}
	res, data := c.do("POST", "/api/auth/register/begin",
		map[string]any{"username": name, "invite_code": code})
	wantStatus(t, res, data, http.StatusOK, "register begin "+name)
	var opts protocol.PublicKeyCredentialCreationOptions
	if err := mustUnmarshal(data, &opts); err != nil {
		t.Fatal(err)
	}
	f := newFakeAuthn("localhost", env.cfg.Origin)
	handle, derr := base64.RawURLEncoding.DecodeString(opts.User.ID.(string))
	if derr != nil {
		t.Fatal(derr)
	}
	f.userHandle = handle
	att, err := f.register(&opts)
	if err != nil {
		t.Fatal(err)
	}
	res, data = c.do("POST", "/api/auth/register/finish",
		map[string]any{"response": att})
	wantStatus(t, res, data, http.StatusOK, "register finish "+name)
	return c, f
}

func TestNotesAPIFlow(t *testing.T) {
	env := testServer(t)
	c, _ := seedUser(t, env, "alice")

	// Create.
	res, data := c.do("POST", "/api/notes", map[string]any{
		"title": "Hello", "body": "# Title\n\nSome **markdown**.", "tags": []string{"demo"},
	})
	wantStatus(t, res, data, http.StatusCreated, "create note")
	var created struct {
		ID   string `json:"id"`
		Body string `json:"body"`
		Tags []string `json:"tags"`
	}
	if err := mustUnmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	if created.Body == "" || len(created.Tags) != 1 {
		t.Fatalf("unexpected payload %s", data)
	}

	// List shows it with tags.
	res, data = c.do("GET", "/api/notes", nil)
	wantStatus(t, res, data, http.StatusOK, "list notes")
	if !contains(string(data), "Hello") || !contains(string(data), "demo") {
		t.Fatalf("list missing data: %s", data)
	}

	// Get by id returns body.
	res, data = c.do("GET", "/api/notes/"+created.ID, nil)
	wantStatus(t, res, data, http.StatusOK, "get note")
	if !contains(string(data), "**markdown**") {
		t.Fatalf("get missing body: %s", data)
	}

	// Update body only.
	res, data = c.do("PUT", "/api/notes/"+created.ID, map[string]any{
		"body": "Updated body",
	})
	wantStatus(t, res, data, http.StatusOK, "update note")
	res, data = c.do("GET", "/api/notes/"+created.ID, nil)
	if !contains(string(data), "Updated body") || !contains(string(data), "Hello") {
		t.Fatalf("update lost fields: %s", data)
	}

	// Folders: create, move note, filter.
	res, data = c.do("POST", "/api/folders", map[string]any{"name": "Work"})
	wantStatus(t, res, data, http.StatusCreated, "create folder")
	var folder struct {
		ID string `json:"id"`
	}
	if err := mustUnmarshal(data, &folder); err != nil {
		t.Fatal(err)
	}
	res, data = c.do("PUT", "/api/notes/"+created.ID, map[string]any{
		"folder_id": &folder.ID,
	})
	wantStatus(t, res, data, http.StatusOK, "move note to folder")
	res, data = c.do("GET", "/api/notes?folder="+folder.ID, nil)
	wantStatus(t, res, data, http.StatusOK, "list in folder")
	if !contains(string(data), "Hello") {
		t.Fatalf("folder filter missing note: %s", data)
	}
	res, data = c.do("GET", "/api/notes?folder=none", nil)
	wantStatus(t, res, data, http.StatusOK, "list unfiled")
	if contains(string(data), "Hello") {
		t.Fatalf("unfiled filter should exclude moved note: %s", data)
	}

	// Delete folder → note becomes unfiled.
	res, data = c.do("DELETE", "/api/folders/"+folder.ID, nil)
	wantStatus(t, res, data, http.StatusOK, "delete folder")
	res, data = c.do("GET", "/api/notes/"+created.ID, nil)
	if contains(string(data), `"folder_id":"`+folder.ID+`"`) {
		t.Fatalf("folder_id should be null after folder delete: %s", data)
	}

	// Second user cannot see or modify alice's note.
	c2, _ := seedUser(t, env, "bob")
	res, data = c2.do("GET", "/api/notes/"+created.ID, nil)
	wantStatus(t, res, data, http.StatusNotFound, "cross-user read")
	res, data = c2.do("PUT", "/api/notes/"+created.ID, map[string]any{"title": "hax"})
	wantStatus(t, res, data, http.StatusNotFound, "cross-user write")
	res, data = c2.do("DELETE", "/api/notes/"+created.ID, nil)
	wantStatus(t, res, data, http.StatusNotFound, "cross-user delete")

	// Delete.
	res, data = c.do("DELETE", "/api/notes/"+created.ID, nil)
	wantStatus(t, res, data, http.StatusOK, "delete note")
	res, data = c.do("GET", "/api/notes/"+created.ID, nil)
	wantStatus(t, res, data, http.StatusNotFound, "get deleted note")
}

func TestTagsEndpoint(t *testing.T) {
	env := testServer(t)
	c, _ := seedUser(t, env, "carol")

	c.do("POST", "/api/notes", map[string]any{"title": "T1", "body": "b", "tags": []string{"red", "blue"}})
	c.do("POST", "/api/notes", map[string]any{"title": "T2", "body": "b", "tags": []string{"red"}})

	res, data := c.do("GET", "/api/tags", nil)
	wantStatus(t, res, data, http.StatusOK, "list tags")
	var got struct {
		Tags []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"tags"`
	}
	if err := mustUnmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, tg := range got.Tags {
		counts[tg.Name] = tg.Count
	}
	if counts["red"] != 2 || counts["blue"] != 1 {
		t.Fatalf("unexpected tag counts: %s", data)
	}
}
