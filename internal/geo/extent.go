package geo

import (
	"fmt"
	"math"
	"sort"
)

// Bounds is a geographic bounding box in degrees. Antimeridian crossing is
// expressed explicitly: when West > East the box crosses the 180th meridian.
// Values are always kept in [-180, 180].
type Bounds struct {
	West  float64 `json:"west"`
	South float64 `json:"south"`
	East  float64 `json:"east"`
	North float64 `json:"north"`
}

// CrossesAntimeridian reports whether the box spans the 180th meridian.
func (b Bounds) CrossesAntimeridian() bool { return b.West > b.East }

// Valid performs basic range and ordering checks.
func (b Bounds) Valid() error {
	if b.West < -180 || b.West > 180 || b.East < -180 || b.East > 180 {
		return fmt.Errorf("longitude outside [-180,180]: %v", b)
	}
	if b.South < -90 || b.South > 90 || b.North < -90 || b.North > 90 {
		return fmt.Errorf("latitude outside [-90,90]: %v", b)
	}
	if b.South > b.North {
		return fmt.Errorf("south > north: %v", b)
	}
	return nil
}

// Rect is a half-open rectangle of tile indices [X0,X1) x [Y0,Y1) at one
// zoom. Indices always satisfy 0 <= X0 < X1 <= 2^z (likewise for Y); a
// wrapping extent is stored as several rectangles plus Wrap=true.
type Rect struct {
	Z  int `json:"z"`
	X0 int `json:"x0"`
	Y0 int `json:"y0"`
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
}

// Extent is the unified, non-wrapping representation of a package footprint.
// PolarExcluded lists strips that could not be represented in Web Mercator.
type Extent struct {
	Rects         []Rect   `json:"rects"`
	Wrap          bool     `json:"wrap"`
	PolarExcluded []Bounds `json:"polar_excluded,omitempty"`
}

// Cells enumerates every tile covered by the extent.
func (e Extent) Cells() []TileID {
	var out []TileID
	for _, r := range e.Rects {
		for y := r.Y0; y < r.Y1; y++ {
			for x := r.X0; x < r.X1; x++ {
				out = append(out, TileID{Z: r.Z, X: x, Y: y})
			}
		}
	}
	return out
}

// Contains reports whether the extent covers a tile.
func (e Extent) Contains(t TileID) bool {
	for _, r := range e.Rects {
		if r.Z == t.Z && t.X >= r.X0 && t.X < r.X1 && t.Y >= r.Y0 && t.Y < r.Y1 {
			return true
		}
	}
	return false
}

// lonInterval is a non-wrapping interval in "unwrapped" longitude space
// [0,360), where 0 corresponds to -180 degrees.
type lonInterval struct{ a, b float64 }

// splitLongitude cuts a bounds into zero or more non-crossing intervals in
// unwrapped longitude space.
func splitLongitude(b Bounds) []lonInterval {
	a := b.West + 180
	bb := b.East + 180
	if b.CrossesAntimeridian() {
		return []lonInterval{{a, 360}, {0, bb}}
	}
	if a == bb {
		return nil
	}
	return []lonInterval{{a, bb}}
}

// ClipPolar removes the non-representable polar strips and returns the
// indexable remainder plus the excluded strips (nil when fully indexable).
func ClipPolar(b Bounds) (Bounds, []Bounds) {
	excluded := []Bounds{}
	indexable := b
	if b.North > MaxLatitude {
		excluded = append(excluded, Bounds{West: b.West, South: MaxLatitude, East: b.East, North: b.North})
		indexable.North = MaxLatitude
	}
	if b.South < -MaxLatitude {
		excluded = append(excluded, Bounds{West: b.West, South: b.South, East: b.East, North: -MaxLatitude})
		indexable.South = -MaxLatitude
	}
	if indexable.South >= indexable.North {
		return Bounds{}, excluded
	}
	return indexable, excluded
}

// BoundsToExtent converts geographic bounds to a tile extent at zoom z. The
// antimeridian is handled by explicit splitting, never modulo wrapping; polar
// strips outside Web Mercator are returned separately.
func BoundsToExtent(b Bounds, z int) (Extent, error) {
	if z < 0 || z > MaxZoom {
		return Extent{}, ErrBadZoom
	}
	if err := b.Valid(); err != nil {
		return Extent{}, err
	}
	indexable, polar := ClipPolar(b)
	ext := Extent{Wrap: b.CrossesAntimeridian(), PolarExcluded: polar}
	if (indexable == Bounds{}) {
		return ext, nil
	}
	n := float64(int(1) << uint(z))
	for _, iv := range splitLongitude(indexable) {
		x0 := int(math.Floor(iv.a * n / 360.0))
		x1 := int(math.Ceil(iv.b * n / 360.0))
		y0 := latToYFloor(indexable.North, n)
		y1 := latToYCeil(indexable.South, n)
		if x1 <= x0 || y1 <= y0 {
			continue
		}
		if x0 < 0 {
			x0 = 0
		}
		if x1 > int(n) {
			x1 = int(n)
		}
		if y0 < 0 {
			y0 = 0
		}
		if y1 > int(n) {
			y1 = int(n)
		}
		ext.Rects = append(ext.Rects, Rect{Z: z, X0: x0, Y0: y0, X1: x1, Y1: y1})
	}
	return ext, nil
}

// latToY maps a latitude to the top-aligned XYZ tile boundary, returned as
// an index that may sit just outside [0,n] for extreme latitudes.
func latToY(lat, n float64) int {
	rad := lat * math.Pi / 180.0
	frac := (1.0 - math.Log(math.Tan(rad)+1.0/math.Cos(rad))/math.Pi) / 2.0
	return int(frac * n)
}

// latToYFloor is the top boundary tile (north edge); latToYCeil is the
// exclusive bottom boundary tile (south edge).
func latToYFloor(lat, n float64) int { return int(math.Floor(latFrac(lat) * n)) }
func latToYCeil(lat, n float64) int  { return int(math.Ceil(latFrac(lat) * n)) }

func latFrac(lat float64) float64 {
	rad := lat * math.Pi / 180.0
	return (1.0 - math.Log(math.Tan(rad)+1.0/math.Cos(rad))/math.Pi) / 2.0
}

// UnionExtent builds a coverage set of all tiles supplied by extents.
func UnionExtent(extents []Extent) map[TileID]bool {
	cover := map[TileID]bool{}
	for _, e := range extents {
		for _, t := range e.Cells() {
			cover[t] = true
		}
	}
	return cover
}

// Holes returns tiles inside region that are absent from cover. The region is
// explicit so that "hole" always has a defined semantic boundary.
func Holes(region Extent, cover map[TileID]bool) []TileID {
	var holes []TileID
	seen := map[TileID]bool{}
	for _, t := range region.Cells() {
		if !cover[t] && !seen[t] {
			seen[t] = true
			holes = append(holes, t)
		}
	}
	sort.Slice(holes, func(i, j int) bool {
		a, b := holes[i], holes[j]
		if a.Z != b.Z {
			return a.Z < b.Z
		}
		if a.X != b.X {
			return a.X < b.X
		}
		return a.Y < b.Y
	})
	return holes
}
