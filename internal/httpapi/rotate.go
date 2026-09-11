package httpapi

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// rotatableMIME lists image formats the server can re-encode in place.
// WebP and AVIF have no pure-Go encoder, so their rotation is done by the
// client, which transcodes them and replaces the blob via PUT /api/files/{id}/raw.
var rotatableMIME = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
}

// rotateImageBytes decodes data as mime, rotates it deg degrees clockwise, and
// re-encodes. Only PNG, JPEG and GIF are supported; anything else fails so the
// caller can fall back to client-side transcoding.
func rotateImageBytes(mime string, data []byte, deg int) ([]byte, error) {
	if deg == 0 {
		return data, nil
	}
	var (
		out []byte
		err error
	)
	switch mime {
	case "image/png":
		var img image.Image
		img, err = png.Decode(bytes.NewReader(data))
		if err == nil {
			var buf bytes.Buffer
			err = png.Encode(&buf, rotate(img, deg))
			out = buf.Bytes()
		}
	case "image/jpeg":
		var img image.Image
		img, err = jpeg.Decode(bytes.NewReader(data))
		if err == nil {
			var buf bytes.Buffer
			err = jpeg.Encode(&buf, rotate(img, deg), &jpeg.Options{Quality: 95})
			out = buf.Bytes()
		}
	case "image/gif":
		var g *gif.GIF
		g, err = gif.DecodeAll(bytes.NewReader(data))
		if err == nil {
			rotateGIF(g, deg)
			var buf bytes.Buffer
			err = gif.EncodeAll(&buf, g)
			out = buf.Bytes()
		}
	default:
		return nil, fmt.Errorf("cannot rotate %s", mime)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// rotate maps a source canvas point (sx,sy) of a w×h image to its position
// after a deg-degree clockwise rotation.
func rotPoint(sx, sy, w, h, deg int) (int, int) {
	switch deg {
	case 90:
		return h - 1 - sy, sx
	case 180:
		return w - 1 - sx, h - 1 - sy
	case 270:
		return sy, w - 1 - sx
	}
	return sx, sy
}

// rotate returns src rotated deg degrees clockwise as an *image.RGBA.
func rotate(src image.Image, deg int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if deg == 90 || deg == 270 {
		nw, nh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for sy := 0; sy < h; sy++ {
		for sx := 0; sx < w; sx++ {
			dx, dy := rotPoint(sx, sy, w, h, deg)
			dst.Set(dx, dy, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// rotRect returns the bounds of r after rotating a w×h canvas by deg degrees.
func rotRect(r image.Rectangle, w, h, deg int) image.Rectangle {
	ax, ay := rotPoint(r.Min.X, r.Min.Y, w, h, deg)
	bx, by := rotPoint(r.Max.X-1, r.Max.Y-1, w, h, deg)
	return image.Rect(min(ax, bx), min(ay, by), max(ax, bx)+1, max(ay, by)+1)
}

// rotateGIF rotates every frame of an animated GIF in place, preserving frame
// offsets, disposal and delays.
func rotateGIF(g *gif.GIF, deg int) {
	w, h := g.Config.Width, g.Config.Height
	for _, im := range g.Image {
		if im.Bounds().Max.X > w {
			w = im.Bounds().Max.X
		}
		if im.Bounds().Max.Y > h {
			h = im.Bounds().Max.Y
		}
	}
	for i, im := range g.Image {
		rect := rotRect(im.Bounds(), w, h, deg)
		nd := image.NewPaletted(rect, im.Palette)
		for y := im.Bounds().Min.Y; y < im.Bounds().Max.Y; y++ {
			for x := im.Bounds().Min.X; x < im.Bounds().Max.X; x++ {
				dx, dy := rotPoint(x, y, w, h, deg)
				nd.Set(dx, dy, im.At(x, y))
			}
		}
		g.Image[i] = nd
	}
	if deg == 90 || deg == 270 {
		g.Config.Width, g.Config.Height = h, w
	}
}
