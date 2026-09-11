package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"notes/internal/blob"
	"notes/internal/store"
)

// magicPrefix holds the leading bytes we require per accepted content type.
var magicPrefix = map[string][]byte{
	"image/png":       {0x89, 'P', 'N', 'G'},
	"image/jpeg":      {0xFF, 0xD8, 0xFF},
	"image/gif":       {'G', 'I', 'F'},
	"image/webp":      {'R', 'I', 'F', 'F'},
	"application/pdf": {'%', 'P', 'D', 'F'},
}

// fileTime adapts unix seconds for http.ServeContent.
type fileTime int64

func (t fileTime) ModTime() (tm time.Time) { return time.Unix(int64(t), 0) }

// allowed upload types -> kind
var allowedMIME = map[string]string{
	"image/png":       "image",
	"image/jpeg":      "image",
	"image/gif":       "image",
	"image/webp":      "image",
	"image/avif":      "image",
	"application/pdf": "pdf",
	"text/plain":      "markdown",
}

// magic prefixes for the sniffed content types we accept.
var magicPrefixes = map[string][]byte{
	"image/png":       {0x89, 'P', 'N', 'G'},
	"image/jpeg":      {0xFF, 0xD8, 0xFF},
	"image/gif":       {'G', 'I', 'F'},
	"image/webp":      {'R', 'I', 'F', 'F'},
	"application/pdf": {'%', 'P', 'D', 'F'},
	// avif starts with a ftyp box: offset 4..8
}

func sniffAllowed(head []byte, contentType string) (string, bool) {
	// http.DetectContentType result must be in the allowlist...
	ct := strings.SplitN(contentType, ";", 2)[0]
	if _, ok := allowedMIME[ct]; !ok {
		return "", false
	}
	// ...and the magic bytes must agree (avif checked separately).
	if p, ok := magicPrefix[ct]; ok {
		if len(head) < len(p) {
			return "", false
		}
		for i := range p {
			if head[i] != p[i] {
				return "", false
			}
		}
		if ct == "image/webp" { // RIFF....WEBP
			if len(head) < 12 || string(head[8:12]) != "WEBP" {
				return "", false
			}
		}
		return ct, true
	}
	// avif: ftyp brand at bytes 4..12 containing 'avif'/'avis'
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		brand := string(head[8:12])
		if brand == "avif" || brand == "avis" {
			return ct, true
		}
	}
	// text/plain (Markdown): no magic bytes; DetectContentType already
	// verified the data is valid text (no control characters).
	if ct == "text/plain" {
		return ct, true
	}
	return "", false
}

// listFiles handles GET /api/files?kind=&folder=&tag=&q=
func (s *Server) listFiles(w http.ResponseWriter, r *http.Request, u *store.User) {
	fl := store.FileFilter{
		Kind:     r.URL.Query().Get("kind"),
		FolderID: r.URL.Query().Get("folder"),
		Tag:      r.URL.Query().Get("tag"),
		Q:        r.URL.Query().Get("q"),
	}
	files, err := s.st.FilesByUser(r.Context(), u.ID, fl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list files")
		return
	}
	out := make([]FilePayload, 0, len(files))
	for _, f := range files {
		out = append(out, filePayload(f, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}

// FilePayload is the JSON shape for a file.
type FilePayload struct {
	ID           string   `json:"id"`
	FolderID     *string  `json:"folder_id"`
	Kind         string   `json:"kind"`
	OriginalName string   `json:"original_name"`
	Mime         string   `json:"mime"`
	Size         int64    `json:"size"`
	URL          string   `json:"url"`
	Tags         []string `json:"tags,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	Rotation     int      `json:"rotation"`
}

func filePayload(f *store.File, withTags bool) FilePayload {
	p := FilePayload{
		ID: f.ID, Kind: f.Kind, OriginalName: f.OriginalName,
		Mime: f.Mime, Size: f.Size, URL: "/api/files/" + f.ID + "/raw",
		CreatedAt: f.CreatedAt, Tags: f.Tags, Rotation: f.Rotation,
	}
	if f.FolderID.Valid {
		fid := f.FolderID.String
		p.FolderID = &fid
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return p
}

// uploadFile handles POST /api/files (multipart, field "file").
func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request, u *store.User) {
	maxBytes := int64(s.cfg.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20)) // headroom for multipart framing
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_upload", "multipart form invalid or too large")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_upload", "missing file field")
		return
	}
	defer file.Close()
	if header.Size > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds upload limit")
		return
	}

	// Read (bounded): reading maxBytes+1 lets us detect overflow.
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_upload", "could not read upload")
		return
	}
	if int64(len(data)) > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds upload limit")
		return
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	sniffed := http.DetectContentType(head)
	ct, ok := sniffAllowed(head, sniffed)
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "bad_type",
			"only PNG, JPEG, GIF, WebP, AVIF images, PDF files and Markdown (.md) files are allowed")
		return
	}
	// Markdown has no magic bytes; require a .md/.markdown extension to
	// avoid accepting arbitrary text files.
	if ct == "text/plain" && !isMarkdownName(header.Filename) {
		writeError(w, http.StatusUnsupportedMediaType, "bad_type",
			"markdown files must use a .md or .markdown extension")
		return
	}

	// Sanity: declared MIME (if any) must not disagree wildly — we trust sniffing.
	id := store.NewID()
	sum := sha256.Sum256(data)
	f := &store.File{
		ID:           id,
		UserID:       u.ID,
		Kind:         allowedMIME[ct],
		OriginalName: sanitizeFilename(header.Filename),
		Mime:         ct,
		Size:         int64(len(data)),
		SHA256:       hex.EncodeToString(sum[:]),
		Filename:     id + "." + mimeExt(ct),
	}
	if tagsStr := r.FormValue("tags"); tagsStr != "" {
		f.Tags = splitTags(tagsStr)
	}
	if folderID := r.FormValue("folder_id"); folderID != "" {
		if _, err := s.st.FolderOwned(r.Context(), u.ID, folderID); err != nil {
			writeError(w, http.StatusBadRequest, "bad_folder", "unknown folder")
			return
		}
		f.FolderID = sqlNullOf(folderID)
	}
	if err := s.st.CreateFile(r.Context(), f); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not save file metadata")
		return
	}
	if err := s.blobs.WriteFile(u.ID, f.Filename, data); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not store file")
		return
	}
	writeJSON(w, http.StatusCreated, filePayload(f, true))
}

// serveFileRaw handles GET /api/files/{id}/raw — streams with Range support.
func (s *Server) serveFileRaw(w http.ResponseWriter, r *http.Request, u *store.User) {
	f, err := s.st.FileByID(r.Context(), u.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load file metadata")
		return
	}
	fh, _, err := s.blobs.OpenFile(u.ID, f.Filename)
	if errors.Is(err, blob.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file data missing")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not open file")
		return
	}
	defer fh.Close()
	w.Header().Set("Content-Type", f.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(f.OriginalName)+`"`)
	} else {
		w.Header().Set("Content-Disposition", `inline; filename="`+sanitizeFilename(f.OriginalName)+`"`)
	}
	http.ServeContent(w, r, "", time.Unix(f.CreatedAt, 0), fh)
}

// getFileMeta handles GET /api/files/{id}.
func (s *Server) getFileMeta(w http.ResponseWriter, r *http.Request, u *store.User) {
	f, err := s.st.FileByID(r.Context(), u.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load file")
		return
	}
	writeJSON(w, http.StatusOK, filePayload(f, true))
}

// updateFile handles PATCH /api/files/{id} {folder_id?, tags?, rotation?}.
// A non-zero rotation bakes the image bytes (PNG/JPEG/GIF only) and resets the
// stored rotation to 0.
func (s *Server) updateFile(w http.ResponseWriter, r *http.Request, u *store.User) {
	id := r.PathValue("id")
	f, err := s.st.FileByID(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load file")
		return
	}
	var req struct {
		FolderID *string   `json:"folder_id"` // null = keep; "" = unfile; id = move
		Tags     *[]string `json:"tags"`
		Name     *string   `json:"name"` // null = keep; string = rename
		Rotation *int      `json:"rotation"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Rotation != nil && (*req.Rotation != 0 && *req.Rotation != 90 && *req.Rotation != 180 && *req.Rotation != 270) {
		writeError(w, http.StatusBadRequest, "bad_rotation", "rotation must be 0, 90, 180 or 270")
		return
	}
	if req.FolderID != nil && *req.FolderID != "" {
		if _, err := s.st.FolderOwned(r.Context(), u.ID, *req.FolderID); err != nil {
			writeError(w, http.StatusBadRequest, "bad_folder", "unknown folder")
			return
		}
	}
	if req.Name != nil {
		f.OriginalName = sanitizeFilename(*req.Name)
	}
	if req.FolderID != nil {
		f.FolderID = sqlNullOfPtr(req.FolderID)
	}
	if req.Tags != nil {
		f.Tags = *req.Tags
	} else {
		f.Tags = nil
	}
	if req.Rotation != nil {
		if *req.Rotation != 0 {
			if f.Kind != "image" || !rotatableMIME[f.Mime] {
				writeError(w, http.StatusBadRequest, "cannot_rotate",
					"this image type cannot be rotated by the server; the browser must re-encode it")
				return
			}
			if err := s.rotateFileInPlace(r.Context(), u.ID, f, *req.Rotation); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "could not rotate image")
				return
			}
		}
		f.Rotation = 0
	}
	if err := s.st.UpdateFile(r.Context(), f); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update file")
		return
	}
	f2, _ := s.st.FileByID(r.Context(), u.ID, id)
	writeJSON(w, http.StatusOK, filePayload(f2, true))
}

// rotateFileInPlace rotates the stored image bytes by deg degrees clockwise and
// refreshes the derived metadata (size, sha256). It does not touch the DB.
func (s *Server) rotateFileInPlace(ctx context.Context, userID string, f *store.File, deg int) error {
	fh, _, err := s.blobs.OpenFile(userID, f.Filename)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(fh)
	fh.Close()
	if err != nil {
		return err
	}
	rotated, err := rotateImageBytes(f.Mime, data, deg)
	if err != nil {
		return err
	}
	if err := s.blobs.WriteFile(userID, f.Filename, rotated); err != nil {
		return err
	}
	sum := sha256.Sum256(rotated)
	f.Size = int64(len(rotated))
	f.SHA256 = hex.EncodeToString(sum[:])
	return nil
}

// replaceFileBytes handles PUT /api/files/{id}/raw. It replaces the stored
// bytes with a client-transcoded image (used when the server cannot rotate the
// original format, e.g. WebP/AVIF).
func (s *Server) replaceFileBytes(w http.ResponseWriter, r *http.Request, u *store.User) {
	id := r.PathValue("id")
	f, err := s.st.FileByID(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load file")
		return
	}
	maxBytes := int64(s.cfg.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_upload", "could not read upload")
		return
	}
	if int64(len(data)) > maxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds upload limit")
		return
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	ct, ok := sniffAllowed(head, http.DetectContentType(head))
	if !ok || allowedMIME[ct] != "image" {
		writeError(w, http.StatusUnsupportedMediaType, "bad_type",
			"only image files can replace an existing file")
		return
	}

	oldName := f.Filename
	newName := id + "." + mimeExt(ct)
	f.Mime = ct
	f.Filename = newName
	f.Size = int64(len(data))
	sum := sha256.Sum256(data)
	f.SHA256 = hex.EncodeToString(sum[:])
	f.Rotation = 0

	if err := s.blobs.WriteFile(u.ID, newName, data); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not store file")
		return
	}
	if err := s.st.UpdateFile(r.Context(), f); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not update file")
		return
	}
	if newName != oldName {
		if err := s.blobs.DeleteFile(u.ID, oldName); err != nil {
			slogWarn("replace file: delete old blob", err)
		}
	}
	writeJSON(w, http.StatusOK, filePayload(f, true))
}

// deleteFile handles DELETE /api/files/{id}.
func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request, u *store.User) {
	id := r.PathValue("id")
	f, err := s.st.FileByID(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load file")
		return
	}
	if err := s.st.DeleteFile(r.Context(), u.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not delete file")
		return
	}
	if err := s.blobs.DeleteFile(u.ID, f.Filename); err != nil {
		slogWarn("delete file blob", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----- helpers -----

func mimeExt(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/avif":
		return "avif"
	case "application/pdf":
		return "pdf"
	case "text/plain":
		return "md"
	}
	return "bin"
}

// isMarkdownName reports whether a multipart filename looks like Markdown.
func isMarkdownName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

func sanitizeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r == '"' || r < 32 {
			return -1
		}
		return r
	}, name)
	if name == "" {
		return "file"
	}
	return name
}

func splitTags(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func modTimeOf(unix int64) (t fileTime) { return fileTime(unix) }
