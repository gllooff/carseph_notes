package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"testing"
)

// pngBytes is a tiny valid PNG (1x1 transparent).
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
	0x0D, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

var pdfBytes = []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< >>\n%%EOF\n")

// upload sends a multipart upload as the client's user.
func upload(t *testing.T, c *testClient, filename string, content []byte, extra map[string]string) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		_ = w.WriteField(k, v)
	}
	w.Close()

	req, err := http.NewRequest("POST", c.env.ts.URL+"/api/files", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", c.env.cfg.Origin)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.session != "" {
		req.Header.Set("Cookie", c.session)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data := readAll(t, res)
	if cs := extractCookie(res, "notes_session"); cs != "" {
		c.session = cs
	}
	return res, data
}

func readAll(t *testing.T, res *http.Response) []byte {
	t.Helper()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return buf.Bytes()
}

func TestFilesUploadFlow(t *testing.T) {
	env := testServer(t)
	c, _ := seedUser(t, env, "uploader")

	// Valid PNG upload.
	res, data := upload(t, c, "pic.png", pngBytes, nil)
	wantStatus(t, res, data, http.StatusCreated, "png upload")
	var created struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		URL  string `json:"url"`
		Mime string `json:"mime"`
	}
	if err := mustUnmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	if created.Kind != "image" || created.Mime != "image/png" || created.URL == "" {
		t.Fatalf("unexpected payload: %s", data)
	}

	// Valid PDF upload.
	res, data = upload(t, c, "doc.pdf", pdfBytes, nil)
	wantStatus(t, res, data, http.StatusCreated, "pdf upload")
	var pdfFile struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := mustUnmarshal(data, &pdfFile); err != nil {
		t.Fatal(err)
	}
	if pdfFile.Kind != "pdf" {
		t.Fatalf("expected pdf kind: %s", data)
	}

	// Valid Markdown upload (filename becomes the title).
	res, data = upload(t, c, "notes.md", []byte("# Hello\n\nSome **markdown**."), nil)
	wantStatus(t, res, data, http.StatusCreated, "markdown upload")
	var mdFile struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		Mime         string `json:"mime"`
		OriginalName string `json:"original_name"`
	}
	if err := mustUnmarshal(data, &mdFile); err != nil {
		t.Fatal(err)
	}
	if mdFile.Kind != "markdown" || mdFile.Mime != "text/plain" || mdFile.OriginalName != "notes.md" {
		t.Fatalf("unexpected markdown payload: %s", data)
	}

	// Valid .markdown extension too.
	res, data = upload(t, c, "draft.markdown", []byte("draft"), nil)
	wantStatus(t, res, data, http.StatusCreated, "markdown ext upload")

	// Rejected: .md extension but binary content.
	res, _ = upload(t, c, "evil.md", bytes.Repeat([]byte{0x00, 0x01, 0x02}, 400), nil)
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("binary as .md: got %d, want 415", res.StatusCode)
	}

	// Rejected: plain text without a markdown extension.
	res, _ = upload(t, c, "readme.txt", []byte("just some text"), nil)
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("txt upload: got %d, want 415", res.StatusCode)
	}

	// Rejected: text file pretending to be PNG (magic-byte mismatch).
	res, data = upload(t, c, "evil.png", []byte("alert('xss')"), nil)
	wantStatus(t, res, data, http.StatusUnsupportedMediaType, "fake png")
	if !bytes.Contains(data, []byte("bad_type")) {
		t.Fatalf("expected bad_type: %s", data)
	}

	// Rejected: random bytes.
	res, _ = upload(t, c, "rand.bin", bytes.Repeat([]byte{0xAB, 0xCD}, 400), nil)
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("random bytes: got %d, want 415", res.StatusCode)
	}

	// List: kind filter.
	res, data = c.do("GET", "/api/files?kind=pdf", nil)
	wantStatus(t, res, data, http.StatusOK, "list pdfs")
	if !contains(string(data), "doc.pdf") || contains(string(data), "pic.png") {
		t.Fatalf("kind filter wrong: %s", data)
	}
	res, data = c.do("GET", "/api/files?kind=markdown", nil)
	wantStatus(t, res, data, http.StatusOK, "list markdown")
	if !contains(string(data), "notes.md") || !contains(string(data), "draft.markdown") || contains(string(data), "doc.pdf") {
		t.Fatalf("markdown kind filter wrong: %s", data)
	}

	// Raw serving with correct content type + inline disposition.
	res, data = c.do("GET", "/api/files/"+created.ID+"/raw", nil)
	wantStatus(t, res, data, http.StatusOK, "raw png")
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("raw content type: %s", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); cd == "" || cd[:6] != "inline" {
		t.Errorf("raw disposition: %q", cd)
	}
	if !bytes.Equal(data, pngBytes) {
		t.Errorf("raw content mismatch")
	}

	// Download flag forces attachment.
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/files/"+created.ID+"/raw?download=1", nil)
	req.Header.Set("Cookie", c.session)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2data := readAll(t, res2)
	res2.Body.Close()
	if cd := res2.Header.Get("Content-Disposition"); cd[:10] != "attachment" {
		t.Errorf("download disposition: %q", cd)
	}
	_ = res2data

	// Second user cannot read or delete the file.
	c2, _ := seedUser(t, env, "sneaky")
	res, _ = c2.do("GET", "/api/files/"+created.ID+"/raw", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("cross-user raw: got %d, want 404", res.StatusCode)
	}
	res, _ = c2.do("DELETE", "/api/files/"+created.ID, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("cross-user delete: got %d, want 404", res.StatusCode)
	}

	// Delete works for owner; blob gone afterwards.
	res, _ = c.do("DELETE", "/api/files/"+created.ID, nil)
	wantStatus(t, res, nil, http.StatusOK, "delete file")
	res, _ = c.do("GET", "/api/files/"+created.ID, nil)
	wantStatus(t, res, nil, http.StatusNotFound, "get deleted file")
}

func TestFilesUploadValidation(t *testing.T) {
	env := testServer(t)
	c, _ := seedUser(t, env, "validator")

	// Upload size over limit is rejected.
	big := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x20}, env.cfg.MaxUploadMB<<20)...)
	req := func() *http.Response {
		res, _ := upload(t, c, "big.pdf", big, nil)
		return res
	}
	if res := req(); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: got %d, want 413", res.StatusCode)
	}

	// Unauthenticated upload rejected.
	cNoAuth := &testClient{env: env}
	res, _ := upload(t, cNoAuth, "x.png", pngBytes, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth upload: got %d, want 401", res.StatusCode)
	}
}
