package web

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"time"

	"gsb/internal/app"
)

// GenerateRequest builds a synthetic offline package entirely at request
// time, so the UI never relies on baked-in example data.
type GenerateRequest struct {
	Name       string  `json:"name"`
	Zoom       int     `json:"zoom"`
	Scheme     string  `json:"scheme"`
	Projection string  `json:"projection"`
	License    string  `json:"license"`
	Source     string  `json:"source"`
	West       float64 `json:"west"`
	South      float64 `json:"south"`
	East       float64 `json:"east"`
	North      float64 `json:"north"`
	Captured   string  `json:"captured"`
	X0         int     `json:"x0"`
	Y0         int     `json:"y0"`
	W          int     `json:"w"`
	H          int     `json:"h"`
	Size       int     `json:"size"`
	OutOfRange int     `json:"out_of_range"` // include N deliberately invalid tiles
	Seed       int     `json:"seed"`
}

func makeTilePNG(size, x, y, z, seed int) []byte {
	if size < 4 {
		size = 16
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	base := color.RGBA{
		R: uint8((x*37 + seed*11) % 200),
		G: uint8((y*53 + seed*7) % 200),
		B: uint8((z*71 + seed*13) % 200),
		A: 255,
	}
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			c := base
			// Vary border pixels so seams are detectable across packages.
			if px == 0 || py == 0 {
				c.R = uint8(int(c.R) + (px*3+py*5+seed)%40)
			}
			if px == size-1 || py == size-1 {
				c.G = uint8(int(c.G) + (px+py+seed)%40)
			}
			img.SetRGBA(px, py, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func buildPackageTar(req GenerateRequest) (io.Reader, error) {
	size := req.Size
	if size == 0 {
		size = 16
	}
	captured := req.Captured
	if captured == "" {
		captured = time.Now().UTC().Format(time.RFC3339)
	}
	man := app.ImportManifest{
		Name: req.Name, Zoom: req.Zoom, Scheme: req.Scheme, Projection: req.Projection,
		License: req.License, Source: req.Source,
		West: req.West, South: req.South, East: req.East, North: req.North,
		Captured: captured,
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	for dy := 0; dy < req.H; dy++ {
		for dx := 0; dx < req.W; dx++ {
			x, y := req.X0+dx, req.Y0+dy
			pngData := makeTilePNG(size, x, y, req.Zoom, req.Seed+dx+dy)
			path := "tiles/" + itoa(req.Zoom) + "/" + itoa(x) + "/" + itoa(y) + ".png"
			man.Tiles = append(man.Tiles, app.ImportTileSpec{X: x, Y: y, Path: path})
			if err := add(path, pngData); err != nil {
				return nil, err
			}
		}
	}
	// Deliberately out-of-range tiles to exercise quarantine semantics.
	n := 1 << uint(req.Zoom)
	for i := 0; i < req.OutOfRange; i++ {
		badX := n + i
		path := "tiles/bad/" + itoa(i) + ".png"
		man.Tiles = append(man.Tiles, app.ImportTileSpec{X: badX, Y: 0, Path: path})
		if err := add(path, makeTilePNG(size, badX, i, req.Zoom, req.Seed+99)); err != nil {
			return nil, err
		}
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := add("manifest.json", mb); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [24]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
