package ingest_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"tileforge/internal/geo"
	"tileforge/internal/ingest"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = uint8(i % 256)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func archive(t *testing.T, manifest any, tile []byte) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write(data)
	}
	add("manifest.json", data)
	add("tile.png", tile)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOutOfRangeTMSIsIsolatedAndPolarMercatorRejected(t *testing.T) {
	tile := pngBytes(t)
	now := time.Now().UTC()
	manifest := struct {
		Name      string           `json:"name"`
		Version   string           `json:"version"`
		CRS       string           `json:"crs"`
		Scheme    string           `json:"scheme"`
		License   string           `json:"license"`
		UpdatedAt time.Time        `json:"updated_at"`
		Tiles     []map[string]any `json:"tiles"`
	}{
		Name: "tms", Version: "1", CRS: geo.EPSG4326, Scheme: geo.TMS, License: "CC0", UpdatedAt: now,
		Tiles: []map[string]any{{"z": 1, "x": 4, "y": 0, "path": "tile.png", "blob_hash": ingest.HashForTest(tile)}},
	}
	result, err := ingest.ParseArchive(archive(t, manifest, tile))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Package.Tiles) != 0 || len(result.Package.Isolated) != 1 {
		t.Fatalf("out-of-range x should be isolated: valid=%d isolated=%d", len(result.Package.Tiles), len(result.Package.Isolated))
	}
	if !strings.Contains(result.Package.Isolated[0].Reason, "isolated") {
		t.Fatalf("isolation reason must explain no modulo: %s", result.Package.Isolated[0].Reason)
	}

	bounds := geo.BBox{West: -10, South: 80, East: 10, North: 89}
	polarManifest := manifest
	polarManifest.Name = "polar-mercator"
	polarManifest.CRS = geo.EPSG3857
	polarManifest.Tiles[0]["x"] = 0
	polarManifest.Tiles[0]["z"] = 0
	envelope := struct {
		Name      string           `json:"name"`
		Version   string           `json:"version"`
		CRS       string           `json:"crs"`
		Scheme    string           `json:"scheme"`
		License   string           `json:"license"`
		UpdatedAt time.Time        `json:"updated_at"`
		Bounds    geo.BBox         `json:"bounds"`
		Tiles     []map[string]any `json:"tiles"`
	}{
		Name: polarManifest.Name, Version: "1", CRS: geo.EPSG3857, Scheme: geo.TMS, License: "CC0",
		UpdatedAt: now, Bounds: bounds, Tiles: polarManifest.Tiles,
	}
	_, err = ingest.ParseArchive(archive(t, envelope, tile))
	if err == nil || !strings.Contains(err.Error(), "polar") {
		t.Fatalf("expected polar Mercator rejection, got %v", err)
	}
}

func TestHashMismatchRejectsBeforeState(t *testing.T) {
	manifest := map[string]any{
		"name": "bad", "version": "1", "crs": geo.EPSG4326, "license": "CC0",
		"updated_at": time.Now().UTC().Format(time.RFC3339),
		"tiles":      []map[string]any{{"z": 0, "x": 0, "y": 0, "path": "tile.png", "blob_hash": "sha256:00"}},
	}
	if _, err := ingest.ParseArchive(archive(t, manifest, pngBytes(t))); err == nil {
		t.Fatal("declared hash mismatch must reject archive")
	}
}

var _ = color.RGBA{}
