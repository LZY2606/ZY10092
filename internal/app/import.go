package app

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gsb/internal/geo"
)

// ImportManifest is the manifest.json at the root of an offline package tar.
type ImportManifest struct {
	Name       string           `json:"name"`
	Zoom       int              `json:"zoom"`
	Scheme     string           `json:"scheme"`     // "xyz" (default) or "tms"
	Projection string           `json:"projection"` // e.g. "EPSG:3857"
	License    string           `json:"license"`
	Source     string           `json:"source"`
	West       float64          `json:"west"`
	South      float64          `json:"south"`
	East       float64          `json:"east"`
	North      float64          `json:"north"`
	Captured   string           `json:"captured"` // RFC3339
	Tiles      []ImportTileSpec `json:"tiles"`
}

// ImportTileSpec references a tile image inside the tar.
type ImportTileSpec struct {
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Path     string `json:"path"`
	License  string `json:"license,omitempty"`
	Captured string `json:"captured,omitempty"`
}

// ImportResult reports what a package import did.
type ImportResult struct {
	PackageID   string       `json:"package_id"`
	Name        string       `json:"name"`
	Imported    int          `json:"imported_tiles"`
	Quarantined []Quarantine `json:"quarantined"`
}

// ImportResultEnvelope wraps the package view with quarantine detail.
type ImportEnvelope struct {
	Result  ImportResult `json:"result"`
	Package *Package     `json:"package"`
}

// normalizeProjection accepts only Web Mercator (with common aliases).
func normalizeProjection(p string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "", "EPSG:3857", "EPSG:900913", "WEB-MERCATOR", "WEBMERCATOR":
		return "EPSG:3857", nil
	default:
		return "", fmt.Errorf("%w: %s", geo.ErrUnsupportedProjection, p)
	}
}

// ImportPackage ingests a tar stream. Invalid/unsupported content is
// quarantined with an explicit reason; it never enters the unified index.
func (a *App) ImportPackage(r io.Reader, idemKey string) (*ImportEnvelope, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()

	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var env ImportEnvelope
			if err := json.Unmarshal(hit.Response, &env); err == nil {
				return &env, hit.Response, nil
			}
		}
	}

	tr := tar.NewReader(r)
	files := map[string][]byte{}
	var man ImportManifest
	gotManifest := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read package tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, err
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "manifest.json" {
			if err := json.Unmarshal(data, &man); err != nil {
				return nil, nil, fmt.Errorf("manifest.json invalid: %w", err)
			}
			gotManifest = true
			continue
		}
		files[name] = data
	}
	if !gotManifest {
		return nil, nil, errors.New("package tar missing manifest.json")
	}

	projection, err := normalizeProjection(man.Projection)
	if err != nil {
		return nil, nil, err
	}
	scheme := strings.ToLower(strings.TrimSpace(man.Scheme))
	if scheme == "" {
		scheme = "xyz"
	}
	if scheme != "xyz" && scheme != "tms" {
		return nil, nil, fmt.Errorf("%w: scheme %q", ErrInvalidInput, man.Scheme)
	}
	if man.Zoom < 0 || man.Zoom > geo.MaxZoom {
		return nil, nil, fmt.Errorf("%w: zoom %d", ErrInvalidInput, man.Zoom)
	}

	pkg := &Package{
		ID:         a.newPackageID(man.Name),
		Name:       man.Name,
		Zoom:       man.Zoom,
		Scheme:     scheme,
		Projection: projection,
		License:    man.License,
		Source:     man.Source,
		ImportedAt: nowUTC(),
	}
	declared := geo.Bounds{West: man.West, South: man.South, East: man.East, North: man.North}
	if berr := declared.Valid(); berr == nil {
		pkg.DeclaredBounds = &declared
		ext, err := geo.BoundsToExtent(declared, man.Zoom)
		if err != nil {
			return nil, nil, err
		}
		pkg.Extent = ext
		for _, ex := range ext.PolarExcluded {
			pkg.Quarantined = append(pkg.Quarantined, Quarantine{
				Reason: "polar_unrepresentable",
				Detail: fmt.Sprintf("strip %.4f..%.4f lat cannot be represented in EPSG:3857", ex.South, ex.North),
			})
		}
	} else {
		pkg.Quarantined = append(pkg.Quarantined, Quarantine{Reason: "invalid_declared_bounds", Detail: berr.Error()})
	}

	defaultCaptured := time.Now().UTC()
	if man.Captured != "" {
		if t, perr := time.Parse(time.RFC3339, man.Captured); perr == nil {
			defaultCaptured = t.UTC()
		}
	}

	cells := map[geo.TileID]bool{}
	for _, spec := range man.Tiles {
		data, ok := files[spec.Path]
		if !ok {
			pkg.Quarantined = append(pkg.Quarantined, Quarantine{Reason: "missing_blob", Detail: spec.Path})
			continue
		}
		t, terr := geo.NewTile(man.Zoom, spec.X, spec.Y)
		nativeScheme := "xyz"
		if scheme == "tms" {
			nativeScheme = "tms"
			ct, cerr := geo.TMStoXYZ(man.Zoom, spec.X, spec.Y)
			if cerr != nil {
				pkg.Quarantined = append(pkg.Quarantined, Quarantine{
					Reason: "out_of_range",
					Detail: fmt.Sprintf("tile z=%d x=%d tms_y=%d isolated (no modulo wrap)", man.Zoom, spec.X, spec.Y),
				})
				continue
			}
			t = ct
		} else {
			if terr != nil {
				pkg.Quarantined = append(pkg.Quarantined, Quarantine{
					Reason: "out_of_range",
					Detail: fmt.Sprintf("tile z=%d x=%d y=%d isolated (no modulo wrap)", man.Zoom, spec.X, spec.Y),
				})
				continue
			}
		}
		if err := errIfInvalid(t); err != nil {
			pkg.Quarantined = append(pkg.Quarantined, Quarantine{Reason: "out_of_range", Detail: t.String()})
			continue
		}
		if cells[t] {
			pkg.Quarantined = append(pkg.Quarantined, Quarantine{Reason: "duplicate_in_package", Detail: t.String()})
			continue
		}
		cells[t] = true
		hash := shaHex(data)
		if _, _, perr := a.st.Put(data); perr != nil {
			return nil, nil, fmt.Errorf("store tile blob: %w", perr)
		}
		license := spec.License
		if license == "" {
			license = man.License
		}
		captured := defaultCaptured
		if spec.Captured != "" {
			if tt, perr := time.Parse(time.RFC3339, spec.Captured); perr == nil {
				captured = tt.UTC()
			}
		}
		pkg.Tiles = append(pkg.Tiles, Tile{
			ID: t, SHA: hash, Size: len(data), License: license,
			Captured: captured, Source: man.Source, NativeZ: man.Zoom, Scheme: nativeScheme,
		})
	}

	// Recompute the registered extent from the actual accepted tile set so
	// coverage never claims tiles that were quarantined.
	pkg.Extent = extentFromTiles(pkg.Tiles, pkg.DeclaredBounds)

	env := ImportEnvelope{
		Result: ImportResult{
			PackageID:   pkg.ID,
			Name:        pkg.Name,
			Imported:    len(pkg.Tiles),
			Quarantined: pkg.Quarantined,
		},
		Package: pkg,
	}
	body := evPackageImported{Package: *pkg}
	resp, jerr := a.appendEvent(evNamePackageImported, idemKey, pkg.ImportedAt, body, env)
	if jerr != nil {
		return nil, nil, jerr
	}
	return &env, resp, nil
}

func extentFromTiles(tiles []Tile, declared *geo.Bounds) geo.Extent {
	if len(tiles) == 0 {
		return geo.Extent{}
	}
	z := tiles[0].NativeZ
	wrap := declared != nil && declared.CrossesAntimeridian()
	rows := map[int]map[int]bool{}
	for _, t := range tiles {
		if rows[t.ID.Y] == nil {
			rows[t.ID.Y] = map[int]bool{}
		}
		rows[t.ID.Y][t.ID.X] = true
	}
	var rects []geo.Rect
	for _, y := range sortedRows(rows) {
		xs := sortedKeys(rows[y])
		start, prev := xs[0], xs[0]
		flush := func(end int) {
			rects = append(rects, geo.Rect{Z: z, X0: start, Y0: y, X1: end, Y1: y + 1})
		}
		for _, x := range xs[1:] {
			if x == prev+1 {
				prev = x
				continue
			}
			flush(prev + 1)
			start, prev = x, x
		}
		flush(prev + 1)
	}
	return geo.Extent{Rects: rects, Wrap: wrap}
}

func sortedRows(rows map[int]map[int]bool) []int {
	out := make([]int, 0, len(rows))
	for y := range rows {
		out = append(out, y)
	}
	sortInts(out)
	return out
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortInts(out)
	return out
}

func storeSHA(data []byte) string {
	h := newSha(data)
	return h
}

func newSha(data []byte) string {
	return shaHex(data)
}
