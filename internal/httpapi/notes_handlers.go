package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"notes/internal/blob"
	"notes/internal/store"
)

// NotePayload is the JSON shape for a note.
type NotePayload struct {
	ID        string   `json:"id"`
	FolderID  *string  `json:"folder_id"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"` // only on single GET
	Tags      []string `json:"tags,omitempty"`
	Size      int64    `json:"size"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

func noteFromStore(n *store.Note, body string, withBody bool) NotePayload {
	p := NotePayload{
		ID: n.ID, Title: n.Title, Size: n.Size,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, Tags: n.Tags,
	}
	if n.FolderID.Valid {
		fid := n.FolderID.String
		p.FolderID = &fid
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if withBody {
		p.Body = body
	}
	return p
}

// listNotes handles GET /api/notes?folder=&tag=&q=
func (s *Server) listNotes(w http.ResponseWriter, r *http.Request, u *store.User) {
	f := store.NoteFilter{
		FolderID: r.URL.Query().Get("folder"),
		Tag:      r.URL.Query().Get("tag"),
		Q:        r.URL.Query().Get("q"),
	}
	notes, err := s.st.NotesByUser(r.Context(), u.ID, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list notes")
		return
	}
	out := make([]NotePayload, 0, len(notes))
	for _, n := range notes {
		out = append(out, noteFromStore(n, "", false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": out})
}

// createNote handles POST /api/notes {title, body, folder_id?, tags?}
func (s *Server) createNote(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Title    string   `json:"title"`
		Body     string   `json:"body"`
		FolderID *string  `json:"folder_id"`
		Tags     []string `json:"tags"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !utf8.ValidString(req.Body) || len(req.Body) > s.cfg.MaxNoteBytes() {
		writeError(w, http.StatusBadRequest, "bad_body", "note body too large or not UTF-8")
		return
	}
	// Validate folder ownership if provided.
	var folder sqlNull
	if req.FolderID != nil && *req.FolderID != "" {
		if _, err := s.st.FolderOwned(r.Context(), u.ID, *req.FolderID); err != nil {
			writeError(w, http.StatusBadRequest, "bad_folder", "unknown folder")
			return
		}
		folder = sqlNullOf(*req.FolderID)
	}
	n := &store.Note{
		ID:       newNoteID(),
		UserID:   u.ID,
		FolderID: folder,
		Title:    trimTitle(req.Title),
		Size:     int64(len(req.Body)),
		Tags:     req.Tags,
	}
	if err := s.st.CreateNote(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save note")
		return
	}
	if err := s.blobs.WriteNote(u.ID, n.ID, []byte(req.Body)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not write note body")
		return
	}
	writeJSON(w, http.StatusCreated, noteFromStore(n, req.Body, true))
}

// getNote handles GET /api/notes/{id} — includes body from disk.
func (s *Server) getNote(w http.ResponseWriter, r *http.Request, u *store.User) {
	n, err := s.st.NoteByID(r.Context(), u.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "note not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load note")
		return
	}
	body, err := s.blobs.ReadNote(u.ID, n.ID)
	if errors.Is(err, blob.ErrNotFound) {
		body = []byte{} // row exists, body lost: show empty rather than 500
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not read note body")
		return
	}
	writeJSON(w, http.StatusOK, noteFromStore(n, string(body), true))
}

// updateNote handles PUT /api/notes/{id} {title?, body?, folder_id?, tags?}
func (s *Server) updateNote(w http.ResponseWriter, r *http.Request, u *store.User) {
	id := r.PathValue("id")
	n, err := s.st.NoteByID(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "note not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load note")
		return
	}
	var req struct {
		Title    *string  `json:"title"`
		Body     *string  `json:"body"`
		FolderID **string `json:"folder_id"` // null = keep; [null] = unfile; [id] = move
		Tags     *[]string `json:"tags"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Title != nil {
		n.Title = trimTitle(*req.Title)
	}
	if req.Body != nil {
		if !utf8Valid(*req.Body) || len(*req.Body) > s.cfg.MaxNoteBytes() {
			writeError(w, http.StatusBadRequest, "bad_body", "note body too large or not UTF-8")
			return
		}
	}
	if req.FolderID != nil && *req.FolderID != nil {
		if _, err := s.st.FolderOwned(r.Context(), u.ID, **req.FolderID); err != nil {
			writeError(w, http.StatusBadRequest, "bad_folder", "unknown folder")
			return
		}
	}
	if req.Body != nil {
		if err := s.blobs.WriteNote(u.ID, n.ID, []byte(*req.Body)); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not write note body")
			return
		}
		n.Size = int64(len(*req.Body))
	}
	if req.FolderID != nil {
		n.FolderID = sqlNullOfPtr(*req.FolderID)
	}
	if req.Tags != nil {
		n.Tags = *req.Tags
	} else {
		n.Tags = nil // keep existing tags untouched
	}
	if err := s.st.UpdateNote(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update note")
		return
	}
	writeJSON(w, http.StatusOK, noteFromStore(n, "", true))
}

// deleteNote handles DELETE /api/notes/{id}.
func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request, u *store.User) {
	id := r.PathValue("id")
	if err := s.st.DeleteNote(r.Context(), u.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "note not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not delete note")
		return
	}
	if err := s.blobs.DeleteNote(u.ID, id); err != nil {
		// Row is gone; orphan sweep will pick the file up. Log only.
		slogWarn("delete note body", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----- folders -----

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request, u *store.User) {
	fs, err := s.st.FoldersByUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list folders")
		return
	}
	type folderJSON struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Count     int64  `json:"count"`
		CreatedAt int64  `json:"created_at"`
	}
	out := make([]folderJSON, 0, len(fs))
	for _, f := range fs {
		out = append(out, folderJSON{ID: f.ID, Name: f.Name, Count: f.Count, CreatedAt: f.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	f, err := s.st.CreateFolder(r.Context(), u.ID, req.Name)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "folder_exists", "folder name already used")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", friendly(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": f.ID, "name": f.Name, "count": 0, "created_at": f.CreatedAt,
	})
}

func (s *Server) renameFolder(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	err := s.st.RenameFolder(r.Context(), u.ID, r.PathValue("id"), req.Name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "folder not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "folder_exists", "folder name already used")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", friendly(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request, u *store.User) {
	err := s.st.DeleteFolder(r.Context(), u.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "folder not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not delete folder")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----- tags -----

func (s *Server) listTags(w http.ResponseWriter, r *http.Request, u *store.User) {
	tags, err := s.st.TagsByUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list tags")
		return
	}
	out := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		out = append(out, map[string]any{"name": t.Name, "count": t.Count})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": out})
}

// ----- small helpers -----

func trimTitle(t string) string {
	t = strings.TrimSpace(t)
	if len(t) > 200 {
		t = t[:200]
	}
	if t == "" {
		return "Untitled"
	}
	return t
}

func utf8Valid(s string) bool { return utf8.ValidString(s) }

func newNoteID() string { return store.NewID() }
