package app

import (
	"encoding/json"
	"sort"
	"time"

	"gsb/internal/geo"
)

// Seam describes a shared border between two neighbouring selected tiles.
type Seam struct {
	A                geo.TileID `json:"a"`
	B                geo.TileID `json:"b"`
	Horizontal       bool       `json:"horizontal"`
	Diff             float64    `json:"diff"`
	MetadataConflict bool       `json:"metadata_conflict"`
	Detail           string     `json:"detail,omitempty"`
}

// Analysis is the coverage report for one region at its zoom.
type Analysis struct {
	Region     string       `json:"region"`
	Zoom       int          `json:"zoom"`
	Covered    []string     `json:"covered"` // "z/x/y"
	Holes      []geo.TileID `json:"holes"`
	Duplicates []dupInfo    `json:"duplicates"`
	Warnings   []Warning    `json:"warnings"`
	Seams      []Seam       `json:"seams"`
}

type dupInfo struct {
	Tile     geo.TileID `json:"tile"`
	Packages []string   `json:"packages"`
	Hashes   []string   `json:"hashes"`
	Licenses []string   `json:"licenses"`
}

// cell aggregates candidates at one output position.
type cell struct {
	tile  geo.TileID
	cands []Candidate
}

func (a *App) packageRank(region *Region, pkgID string) int {
	if region.LockedPackage != "" {
		if pkgID == region.LockedPackage {
			return 0
		}
		return 1<<30 - 1
	}
	for i, p := range region.Priority {
		if p == pkgID {
			return i + 1
		}
	}
	return 1 << 20
}

// gatherCells collects every candidate inside the region, ranked.
func (a *App) gatherCells(region *Region) map[tileKey]*cell {
	type src struct {
		pkg *Package
		t   Tile
	}
	byTile := map[tileKey][]src{}
	for _, pkg := range a.state.packageList() {
		if pkg.Zoom != region.Zoom {
			continue
		}
		if region.LockedPackage != "" && pkg.ID != region.LockedPackage {
			continue
		}
		for _, t := range pkg.Tiles {
			if region.Extent.Contains(t.ID) {
				byTile[keyOf(t.ID)] = append(byTile[keyOf(t.ID)], src{pkg: pkg, t: t})
			}
		}
	}
	cells := map[tileKey]*cell{}
	for k, ss := range byTile {
		sort.SliceStable(ss, func(i, j int) bool {
			ri := a.packageRank(region, ss[i].pkg.ID)
			rj := a.packageRank(region, ss[j].pkg.ID)
			if ri != rj {
				return ri < rj
			}
			if !ss[i].t.Captured.Equal(ss[j].t.Captured) {
				return ss[i].t.Captured.After(ss[j].t.Captured)
			}
			return ss[i].pkg.ID < ss[j].pkg.ID
		})
		c := &cell{tile: geo.TileID{Z: k.Z, X: k.X, Y: k.Y}}
		for rank, s := range ss {
			c.cands = append(c.cands, Candidate{
				PackageID: s.pkg.ID, Tile: s.t, Rank: rank, Chosen: rank == 0,
			})
		}
		cells[k] = c
	}
	return cells
}

func tileStr(t geo.TileID) string { return t.String() }

// Analyze computes holes, duplicates, date inversions, license conflicts and
// seam heat for a region. It only reads state; changing rules is a separate
// command and invalidates only this region's derived plan.
func (a *App) Analyze(regionName string) (*Analysis, error) {
	a.mu.Lock()
	a.state.mu.Lock()
	unlock := func() {
		a.state.mu.Unlock()
		a.mu.Unlock()
	}
	region, ok := a.state.data.Regions[regionName]
	if !ok {
		unlock()
		return nil, errUnknown(regionName)
	}
	cells := a.gatherCells(region)
	a.state.mu.Unlock()
	a.mu.Unlock()

	an := &Analysis{Region: region.Name, Zoom: region.Zoom}
	cover := map[geo.TileID]bool{}
	keys := make([]tileKey, 0, len(cells))
	for k, c := range cells {
		keys = append(keys, k)
		cover[c.tile] = true
		an.Covered = append(an.Covered, tileStr(c.tile))
		if len(c.cands) > 1 {
			di := dupInfo{Tile: c.tile}
			lic := map[string]bool{}
			hashSet := map[string]bool{}
			for _, cand := range c.cands {
				di.Packages = append(di.Packages, cand.PackageID)
				di.Hashes = append(di.Hashes, cand.Tile.SHA)
				hashSet[cand.Tile.SHA] = true
				lic[cand.Tile.License] = true
			}
			for l := range lic {
				di.Licenses = append(di.Licenses, l)
			}
			sort.Strings(di.Licenses)
			an.Duplicates = append(an.Duplicates, di)

			chosen := c.cands[0]
			// date inversion: chosen candidate older than a rejected one.
			for _, cand := range c.cands[1:] {
				if cand.Tile.Captured.After(chosen.Tile.Captured) {
					an.Warnings = append(an.Warnings, Warning{
						Code: "date_inversion", Tile: c.tile,
						Detail:   "selected " + chosen.PackageID + " (" + chosen.Tile.Captured.Format(time.RFC3339) + ") is older than rejected " + cand.PackageID + " (" + cand.Tile.Captured.Format(time.RFC3339) + ")",
						Packages: []string{chosen.PackageID, cand.PackageID},
					})
				}
			}
			// Identical pixels do not imply identical authorization.
			if len(lic) > 1 {
				an.Warnings = append(an.Warnings, Warning{
					Code: "license_conflict", Tile: c.tile,
					Detail:   "candidates share position with differing license terms",
					Packages: di.Packages,
				})
			}
			if len(hashSet) == 1 && len(lic) > 1 {
				an.Warnings = append(an.Warnings, Warning{
					Code: "same_pixels_metadata_conflict", Tile: c.tile,
					Detail:   "boundary/payload pixels identical but metadata (license/source) conflicts",
					Packages: di.Packages,
				})
			}
		}
	}
	an.Warnings = dedupWarnings(an.Warnings)
	an.Holes = geo.Holes(region.Extent, cover)
	sort.Strings(an.Covered)
	sort.Slice(an.Duplicates, func(i, j int) bool { return tileStr(an.Duplicates[i].Tile) < tileStr(an.Duplicates[j].Tile) })

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Y != keys[j].Y {
			return keys[i].Y < keys[j].Y
		}
		return keys[i].X < keys[j].X
	})
	an.Seams = a.computeSeams(cells, keys, true)
	an.Seams = append(an.Seams, a.computeSeams(cells, keys, false)...)
	return an, nil
}

func (a *App) computeSeams(cells map[tileKey]*cell, keys []tileKey, horizontal bool) []Seam {
	var seams []Seam
	index := map[tileKey]*cell{}
	for k, c := range cells {
		index[k] = c
	}
	seen := map[string]bool{}
	for _, k := range keys {
		c := cells[k]
		nk := k
		if horizontal {
			nk.X++
		} else {
			nk.Y++
		}
		n, ok := index[nk]
		if !ok {
			continue
		}
		left := c.cands[0]
		right := n.cands[0]
		if left.PackageID == right.PackageID && left.Tile.SHA == right.Tile.SHA {
			continue
		}
		pairID := pairKey(c.tile, n.tile)
		if seen[pairID] {
			continue
		}
		seen[pairID] = true
		seam := Seam{A: c.tile, B: n.tile, Horizontal: horizontal}
		ldata, e1 := a.st.Get(left.Tile.SHA)
		rdata, e2 := a.st.Get(right.Tile.SHA)
		if e1 == nil && e2 == nil {
			seam.Diff = geo.EdgeDiff(ldata, rdata, horizontal)
		}
		if left.Tile.License != right.Tile.License || left.Tile.Source != right.Tile.Source {
			seam.MetadataConflict = true
			seam.Detail = "metadata differs across seam: license/source of " + left.PackageID + " vs " + right.PackageID
		}
		if seam.Diff > 0 || seam.MetadataConflict {
			seams = append(seams, seam)
		}
	}
	return seams
}

func pairKey(a, b geo.TileID) string {
	s1, s2 := a.String(), b.String()
	if s2 < s1 {
		s1, s2 = s2, s1
	}
	return s1 + "|" + s2
}

func errUnknown(name string) error {
	return &regionError{name: name}
}

type regionError struct{ name string }

func (e *regionError) Error() string { return "unknown region: " + e.name }

var _ = json.Marshal

func dedupWarnings(ws []Warning) []Warning {
	seen := map[string]bool{}
	out := ws[:0]
	for _, w := range ws {
		k := w.Code + "|" + w.Tile.String() + "|" + w.Detail
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, w)
	}
	return out
}
