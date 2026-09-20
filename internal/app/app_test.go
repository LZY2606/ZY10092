package app

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	"gsb/internal/geo"
	"gsb/internal/store"
)

type specPkg struct {
	name, scheme, projection, license, source, captured string
	zoom                                                int
	x0, y0, w, h                                        int
	west, south, east, north                            float64
	outOfRange                                          int
	seed                                                int
	varyBorders                                         bool
}

func pngTile(x, y, z, seed int, vary bool) []byte {
	size := 8
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	base := color.RGBA{R: uint8((x*37 + seed) % 200), G: uint8((y*53 + seed) % 200), B: uint8(z*30 + seed), A: 255}
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			c := base
			if vary && (px == size-1 || py == size-1) {
				c.R = uint8((int(c.R) + seed + px + py) % 255)
			}
			img.Set(px, py, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func makeTar(sp specPkg) io.Reader {
	man := ImportManifest{
		Name: sp.name, Zoom: sp.zoom, Scheme: sp.scheme, Projection: sp.projection,
		License: sp.license, Source: sp.source,
		West: sp.west, South: sp.south, East: sp.east, North: sp.north,
		Captured: sp.captured,
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, data []byte) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
		_, _ = tw.Write(data)
	}
	for dy := 0; dy < sp.h; dy++ {
		for dx := 0; dx < sp.w; dx++ {
			x, y := sp.x0+dx, sp.y0+dy
			path := "tiles/" + itoa(sp.zoom) + "/" + itoa(x) + "/" + itoa(y) + ".png"
			man.Tiles = append(man.Tiles, ImportTileSpec{X: x, Y: y, Path: path})
			add(path, pngTile(x, y, sp.zoom, sp.seed+dx+dy, sp.varyBorders))
		}
	}
	for i := 0; i < sp.outOfRange; i++ {
		n := 1 << uint(sp.zoom)
		path := "tiles/bad/" + itoa(i) + ".png"
		man.Tiles = append(man.Tiles, ImportTileSpec{X: n + i, Y: 0, Path: path})
		add(path, pngTile(n+i, i, sp.zoom, 99, sp.varyBorders))
	}
	mb, _ := json.Marshal(man)
	add("manifest.json", mb)
	_ = tw.Close()
	return &buf
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewWithStore(st)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func importSpec(t *testing.T, a *App, sp specPkg, idem string) *Package {
	t.Helper()
	env, _, err := a.ImportPackage(makeTar(sp), idem)
	if err != nil {
		t.Fatalf("import %s: %v", sp.name, err)
	}
	return env.Package
}

func baseSpec(name, license, captured string, seed int) specPkg {
	return specPkg{
		name: name, zoom: 2, scheme: "xyz", projection: "EPSG:3857",
		license: license, source: name + "-src", captured: captured,
		x0: 0, y0: 1, w: 2, h: 1,
		west: -120, south: 20, east: -60, north: 60,
		seed: seed,
	}
}

func TestImportQuarantineAndNormalization(t *testing.T) {
	a := newTestApp(t)
	// TMS package with an out-of-range tile.
	sp := baseSpec("tms-pack", "MIT", "2026-01-01T00:00:00Z", 5)
	sp.scheme = "tms"
	sp.w, sp.h = 3, 2
	sp.x0, sp.y0 = 0, 0
	sp.outOfRange = 1
	pkg := importSpec(t, a, sp, "")
	if len(pkg.Tiles) != 6 {
		t.Fatalf("expected 6 indexed tiles, got %d", len(pkg.Tiles))
	}
	if len(pkg.Quarantined) != 1 || pkg.Quarantined[0].Reason != "out_of_range" {
		t.Fatalf("OOR tile must be quarantined: %+v", pkg.Quarantined)
	}
	for _, tile := range pkg.Tiles {
		if tile.Scheme != "tms" {
			t.Fatal("native scheme must be recorded")
		}
		// TMS y=1..2 at z=2 normalize to XYZ y=2..1 (flipped), never wrapped.
		if !geo.ValidIndex(tile.ID.Z, tile.ID.X, tile.ID.Y) {
			t.Fatalf("normalized tile invalid: %v", tile.ID)
		}
	}
}

func TestUnsupportedProjectionRejected(t *testing.T) {
	a := newTestApp(t)
	sp := baseSpec("epsg4326", "MIT", "2026-01-01T00:00:00Z", 1)
	sp.projection = "EPSG:32633"
	if _, _, err := a.ImportPackage(makeTar(sp), ""); err == nil {
		t.Fatal("non-mercator projection must be rejected")
	}
}

func TestPolarQuarantine(t *testing.T) {
	a := newTestApp(t)
	sp := baseSpec("polar", "MIT", "2026-01-01T00:00:00Z", 1)
	sp.south, sp.north = -89, 89
	// place a tiny valid tile set near the middle anyway
	pkg := importSpec(t, a, sp, "")
	foundPolar := false
	for _, q := range pkg.Quarantined {
		if q.Reason == "polar_unrepresentable" {
			foundPolar = true
		}
	}
	if !foundPolar {
		t.Fatalf("polar strips must be reported: %+v", pkg.Quarantined)
	}
}

func TestIdempotencyReturnsFirstResult(t *testing.T) {
	a := newTestApp(t)
	sp := baseSpec("idem", "MIT", "2026-01-01T00:00:00Z", 1)
	env1, raw1, err := a.ImportPackage(makeTar(sp), "key-42")
	if err != nil {
		t.Fatal(err)
	}
	env2, raw2, err := a.ImportPackage(makeTar(sp), "key-42")
	if err != nil {
		t.Fatal(err)
	}
	if env1.Result.PackageID != env2.Result.PackageID {
		t.Fatal("same idempotency key must return the first determined package")
	}
	if !bytes.Equal(raw1, raw2) {
		t.Fatal("repeated key must return identical first response")
	}
	if len(a.Packages()) != 1 {
		t.Fatalf("repeated import must not create a second package, got %d", len(a.Packages()))
	}
}

var _ = time.Now

func itoa(i int) string {
	return strconvItoa(i)
}
