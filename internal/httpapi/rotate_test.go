package httpapi

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"testing"
)

func encodeTestPNG(t *testing.T, w, h int, colorAt func(x, y int) color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, colorAt(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func hexAt(t *testing.T, data []byte, x, y int) (int, int, int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() {
		t.Fatalf("pixel (%d,%d) outside %v", x, y, img.Bounds())
	}
	r, g, b, _ := img.At(x, y).RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}

func TestFilesRotateBakesBytes(t *testing.T) {
	env := testServer(t)
	c, _ := seedUser(t, env, "rotator")

	red, green, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}
	pngData := encodeTestPNG(t, 3, 2, func(x, y int) color.Color {
		switch {
		case x == 0 && y == 0:
			return red
		case x == 2 && y == 0:
			return green
		case x == 1 && y == 1:
			return blue
		}
		return color.RGBA{}
	})

	res, data := upload(t, c, "pic.png", pngData, nil)
	wantStatus(t, res, data, http.StatusCreated, "upload")
	var created struct {
		ID       string `json:"id"`
		Rotation int    `json:"rotation"`
		Size     int64  `json:"size"`
	}
	if err := mustUnmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	if created.Rotation != 0 {
		t.Fatalf("fresh upload rotation = %d, want 0", created.Rotation)
	}

	// Rotate 90° clockwise: bytes must be re-encoded and rotation reset to 0.
	res, data = c.do("PATCH", "/api/files/"+created.ID, map[string]any{"rotation": 90})
	wantStatus(t, res, data, http.StatusOK, "rotate 90")
	var rot struct {
		Rotation int    `json:"rotation"`
		Size     int64  `json:"size"`
		Mime     string `json:"mime"`
	}
	if err := mustUnmarshal(data, &rot); err != nil {
		t.Fatal(err)
	}
	if rot.Rotation != 0 {
		t.Fatalf("rotation after save = %d, want 0", rot.Rotation)
	}
	if rot.Size == created.Size || rot.Size == 0 {
		t.Fatalf("size not updated: %d", rot.Size)
	}
	if rot.Mime != "image/png" {
		t.Fatalf("mime changed to %s, want image/png", rot.Mime)
	}

	res, raw := c.do("GET", "/api/files/"+created.ID+"/raw", nil)
	wantStatus(t, res, raw, http.StatusOK, "raw after rotate")
	if d := rawBounds(raw); d != "2x3" {
		t.Fatalf("rotated dims = %s, want 2x3", d)
	}
	if bytes.Equal(raw, pngData) {
		t.Fatalf("stored bytes were not rotated")
	}
	// red top-left -> top-right, green top-right -> bottom-right, blue center -> middle-left
	if r, g, b := hexAt(t, raw, 1, 0); r != 255 || g != 0 || b != 0 {
		t.Errorf("(1,0) = %d,%d,%d, want red", r, g, b)
	}
	if r, g, b := hexAt(t, raw, 1, 2); r != 0 || g != 255 || b != 0 {
		t.Errorf("(1,2) = %d,%d,%d, want green", r, g, b)
	}
	if r, g, b := hexAt(t, raw, 0, 1); r != 0 || g != 0 || b != 255 {
		t.Errorf("(0,1) = %d,%d,%d, want blue", r, g, b)
	}

	// Rotating the now-baked bytes by 270 must restore the original image.
	res, data = c.do("PATCH", "/api/files/"+created.ID, map[string]any{"rotation": 270})
	wantStatus(t, res, data, http.StatusOK, "rotate 270")
	res, raw = c.do("GET", "/api/files/"+created.ID+"/raw", nil)
	wantStatus(t, res, raw, http.StatusOK, "raw after 270")
	if d := rawBounds(raw); d != "3x2" {
		t.Fatalf("dims after 270 = %s, want 3x2", d)
	}
	if r, g, b := hexAt(t, raw, 0, 0); r != 255 || g != 0 || b != 0 {
		t.Errorf("(0,0) = %d,%d,%d, want red", r, g, b)
	}
	if r, g, b := hexAt(t, raw, 2, 0); r != 0 || g != 255 || b != 0 {
		t.Errorf("(2,0) = %d,%d,%d, want green", r, g, b)
	}
	if r, g, b := hexAt(t, raw, 1, 1); r != 0 || g != 0 || b != 255 {
		t.Errorf("(1,1) = %d,%d,%d, want blue", r, g, b)
	}

	// No-op save (rotation 0) is harmless.
	res, data = c.do("PATCH", "/api/files/"+created.ID, map[string]any{"rotation": 0})
	wantStatus(t, res, data, http.StatusOK, "rotate 0")
}

func rawBounds(data []byte) string {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return "ERR:" + err.Error()
	}
	b := img.Bounds()
	return string(rune('0'+b.Dx())) + "x" + string(rune('0'+b.Dy()))
}

func TestFilesRotateRejectsUnsupported(t *testing.T) {
	env := testServer(t)
	c, _ := seedUser(t, env, "rejector")

	res, data := upload(t, c, "doc.pdf", pdfBytes, nil)
	wantStatus(t, res, data, http.StatusCreated, "pdf upload")
	var f struct {
		ID string `json:"id"`
	}
	if err := mustUnmarshal(data, &f); err != nil {
		t.Fatal(err)
	}

	// PDFs cannot be rotated server-side.
	res, data = c.do("PATCH", "/api/files/"+f.ID, map[string]any{"rotation": 90})
	wantStatus(t, res, data, http.StatusBadRequest, "rotate pdf")
	if !bytes.Contains(data, []byte("cannot_rotate")) {
		t.Fatalf("expected cannot_rotate: %s", data)
	}

	// Invalid angle still rejected.
	res, _ = c.do("PATCH", "/api/files/"+f.ID, map[string]any{"rotation": 45})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad angle: got %d, want 400", res.StatusCode)
	}
}

func TestFilesReplaceBytes(t *testing.T) {
	env := testServer(t)
	c, _ := seedUser(t, env, "replacer")

	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	pngData := encodeTestPNG(t, 3, 2, func(x, y int) color.Color {
		if x == 0 && y == 0 {
			return red
		}
		return blue
	})
	res, data := upload(t, c, "pic.png", pngData, nil)
	wantStatus(t, res, data, http.StatusCreated, "upload")
	var created struct {
		ID       string `json:"id"`
		URL      string `json:"url"`
		Rotation int    `json:"rotation"`
	}
	if err := mustUnmarshal(data, &created); err != nil {
		t.Fatal(err)
	}

	// Replace with a fresh 2x2 PNG (what the client transcode produces).
	repl := encodeTestPNG(t, 2, 2, func(x, y int) color.Color { return red })
	req, err := http.NewRequest("PUT", env.ts.URL+"/api/files/"+created.ID+"/raw", bytes.NewReader(repl))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", env.cfg.Origin)
	req.Header.Set("Cookie", c.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replace: got %d, want 200: %s", resp.StatusCode, body)
	}
	var replaced struct {
		Size     int64  `json:"size"`
		SHA256   string `json:"sha256"`
		Rotation int    `json:"rotation"`
		Mime     string `json:"mime"`
	}
	if err := mustUnmarshal(body, &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.Mime != "image/png" || replaced.Rotation != 0 {
		t.Fatalf("replaced payload: %s", body)
	}

	res, raw := c.do("GET", "/api/files/"+created.ID+"/raw", nil)
	wantStatus(t, res, raw, http.StatusOK, "raw after replace")
	if d := rawBounds(raw); d != "2x2" {
		t.Fatalf("replaced dims = %s, want 2x2", d)
	}
	if r, g, b := hexAt(t, raw, 0, 0); r != 255 || g != 0 || b != 0 {
		t.Errorf("(0,0) = %d,%d,%d, want red", r, g, b)
	}

	// Non-image replacement rejected.
	req2, _ := http.NewRequest("PUT", env.ts.URL+"/api/files/"+created.ID+"/raw",
		bytes.NewReader([]byte("definitely not an image")))
	req2.Header.Set("Origin", env.cfg.Origin)
	req2.Header.Set("Cookie", c.session)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("replace with text: got %d, want 415", res2.StatusCode)
	}

	// Cross-user replace rejected.
	c2, _ := seedUser(t, env, "sneaky")
	req3, _ := http.NewRequest("PUT", env.ts.URL+"/api/files/"+created.ID+"/raw", bytes.NewReader(repl))
	req3.Header.Set("Origin", env.cfg.Origin)
	req3.Header.Set("Cookie", c2.session)
	res3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-user replace: got %d, want 404", res3.StatusCode)
	}
}

func TestRotateGIFAnimated(t *testing.T) {
	pal := color.Palette{color.Black, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}}
	f0 := image.NewPaletted(image.Rect(0, 0, 3, 2), pal)
	f0.Set(0, 0, pal[1]) // red top-left
	f1 := image.NewPaletted(image.Rect(1, 0, 3, 2), pal)
	f1.Set(1, 0, pal[2]) // green at frame offset (1,0)
	anim := &gif.GIF{
		Image:     []*image.Paletted{f0, f1},
		Delay:     []int{10, 20},
		Disposal:  []byte{gif.DisposalNone, gif.DisposalBackground},
		Config:    image.Config{Width: 3, Height: 2},
		LoopCount: 0,
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}

	out, err := rotateImageBytes("image/gif", buf.Bytes(), 90)
	if err != nil {
		t.Fatal(err)
	}
	back, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if back.Config.Width != 2 || back.Config.Height != 3 {
		t.Fatalf("rotated config = %dx%d, want 2x3", back.Config.Width, back.Config.Height)
	}
	if len(back.Image) != 2 || len(back.Delay) != 2 {
		t.Fatalf("frames lost: %d", len(back.Image))
	}
	if back.Delay[0] != 10 || back.Delay[1] != 20 {
		t.Fatalf("delays not preserved: %v", back.Delay)
	}
	// frame0 now covers the full 2x3 canvas; its red corner moved to (1,0).
	if !back.Image[0].Bounds().Eq(image.Rect(0, 0, 2, 3)) {
		t.Fatalf("frame0 bounds = %v, want (0,0)-(2,3)", back.Image[0].Bounds())
	}
	if r, g, b, _ := back.Image[0].At(1, 0).RGBA(); r != 0xFFFF || g != 0 || b != 0 {
		t.Errorf("frame0 (1,0) = %d,%d,%d, want red", r>>8, g>>8, b>>8)
	}
	// frame1's offset survived the rotation.
	if !back.Image[1].Bounds().Eq(image.Rect(0, 1, 2, 3)) {
		t.Fatalf("frame1 bounds = %v, want (0,1)-(2,3)", back.Image[1].Bounds())
	}
	if r, g, b, _ := back.Image[1].At(1, 1).RGBA(); r != 0 || g != 0xFFFF || b != 0 {
		t.Errorf("frame1 (1,1) = %d,%d,%d, want green", r>>8, g>>8, b>>8)
	}
}
