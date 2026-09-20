package geo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/jpeg"
	_ "image/png"
)

// Side identifies one border of a tile image.
type Side int

const (
	Left Side = iota
	Right
	Top
	Bottom
)

// BorderHash is the content hash of one pixel border (RGBA, ordered along
// the edge). Two geometrically adjacent tiles stitch cleanly only when the
// shared borders match pixel for pixel.
type BorderHash struct {
	Side Side   `json:"side"`
	SHA  string `json:"sha"`
}

// EdgePixels decodes an image and returns the RGBA bytes of one border.
func EdgePixels(data []byte, side Side) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 0, (max(w, h))*4)
	put := func(x, y int) {
		r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
		out = append(out, byte(r>>8), byte(g>>8), byte(bl>>8), byte(a>>8))
	}
	switch side {
	case Left:
		for y := 0; y < h; y++ {
			put(0, y)
		}
	case Right:
		for y := 0; y < h; y++ {
			put(w-1, y)
		}
	case Top:
		for x := 0; x < w; x++ {
			put(x, 0)
		}
	case Bottom:
		for x := 0; x < w; x++ {
			put(x, h-1)
		}
	}
	return out, nil
}

func hashBytes(p []byte) string {
	s := sha256.Sum256(p)
	return hex.EncodeToString(s[:])
}

// EdgeHash returns the content hash of one tile border.
func EdgeHash(data []byte, side Side) (BorderHash, error) {
	pix, err := EdgePixels(data, side)
	if err != nil {
		return BorderHash{}, err
	}
	return BorderHash{Side: side, SHA: hashBytes(pix)}, nil
}

// EdgeDiff compares the shared border of two adjacent tiles and returns the
// fraction [0,1] of pixel positions that differ. A value of 0 means the seam
// is invisible; larger values drive the seam heat map.
//
// For horizontal adjacency pass the right edge of the left tile and the left
// edge of the right tile; both borders are extracted from the supplied image
// bytes. Returns 0 (clean seam) when either image cannot be decoded, which is
// reported separately by the caller via candidate metadata.
func EdgeDiff(leftData, rightData []byte, horizontal bool) float64 {
	aSide, bSide := Right, Left
	if !horizontal {
		aSide, bSide = Bottom, Top
	}
	a, err1 := EdgePixels(leftData, aSide)
	b, err2 := EdgePixels(rightData, bSide)
	if err1 != nil || err2 != nil || len(a) != len(b) || len(a) == 0 {
		return 0
	}
	diff := 0
	n := len(a) / 4
	for i := 0; i < n; i++ {
		off := i * 4
		if a[off] != b[off] || a[off+1] != b[off+1] || a[off+2] != b[off+2] || a[off+3] != b[off+3] {
			diff++
		}
	}
	return float64(diff) / float64(n)
}
