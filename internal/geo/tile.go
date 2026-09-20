// Package geo defines the unified tile index, geographic bounds and the
// semantics shared by the whole pipeline.
//
// Two tile schemes are accepted as input:
//
//   - XYZ (slippy map): tile (0,0) is the top-left corner; y grows downward.
//   - TMS: tile (0,0) is the bottom-left corner; y grows upward.
//
// Everything stored internally is normalized to XYZ at the tile's native
// zoom. Out-of-range tile coordinates are always rejected: they never wrap
// around the antimeridian via modulo arithmetic. Antimeridian crossing is
// represented explicitly on the extent (a set of disjoint rectangles whose
// x spans may wrap), never by normalising tile indices.
//
// Only the Web Mercator projection (EPSG:3857) can be indexed. The Mercator
// projection has no finite representation of the poles, so any package whose
// declared bounds touch |lat| >= MaxLatitude is partially polar; the
// non-representable portion is reported and excluded from indexing.
package geo

import (
	"errors"
	"fmt"
	"math"
)

// MaxZoom is the deepest zoom the registry accepts.
const MaxZoom = 30

// MaxLatitude is the geographic latitude at which Web Mercator becomes
// infinite (about 85.05112878 degrees).
const MaxLatitude = 85.0511287798066

var (
	// ErrOutOfRange is returned when a tile index falls outside [0, 2^z).
	// Callers must quarantine such tiles rather than wrapping them.
	ErrOutOfRange = errors.New("tile index out of range")
	// ErrBadZoom is returned for zoom levels outside [0, MaxZoom].
	ErrBadZoom = errors.New("zoom level out of range")
	// ErrUnsupportedProjection is returned for any projection other than
	// EPSG:3857 (or its legacy aliases).
	ErrUnsupportedProjection = errors.New("unsupported projection")
	// ErrPolar is returned when a region cannot be represented in Mercator.
	ErrPolar = errors.New("latitude outside web mercator range")
)

// TileID is a tile in the unified XYZ index.
type TileID struct {
	Z int `json:"z"`
	X int `json:"x"`
	Y int `json:"y"`
}

// String renders the canonical z/x/y coordinate.
func (t TileID) String() string { return fmt.Sprintf("%d/%d/%d", t.Z, t.X, t.Y) }

// ValidIndex reports whether the XYZ indices are valid at zoom z.
func ValidIndex(z, x, y int) bool {
	if z < 0 || z > MaxZoom {
		return false
	}
	n := 1 << uint(z)
	return x >= 0 && x < n && y >= 0 && y < n
}

// TMStoXYZ converts a TMS y coordinate (origin bottom-left) to XYZ
// (origin top-left). It does not perform modulo wrapping: an out-of-range
// index yields ErrOutOfRange.
func TMStoXYZ(z, x, yTMS int) (TileID, error) {
	if z < 0 || z > MaxZoom {
		return TileID{}, ErrBadZoom
	}
	n := 1 << uint(z)
	if x < 0 || x >= n || yTMS < 0 || yTMS >= n {
		return TileID{}, ErrOutOfRange
	}
	return TileID{Z: z, X: x, Y: n - 1 - yTMS}, nil
}

// NewTile validates an XYZ coordinate without any wrapping.
func NewTile(z, x, y int) (TileID, error) {
	if z < 0 || z > MaxZoom {
		return TileID{}, ErrBadZoom
	}
	if !ValidIndex(z, x, y) {
		return TileID{}, ErrOutOfRange
	}
	return TileID{Z: z, X: x, Y: y}, nil
}

// LonLatToTile returns the XYZ tile covering (lon, lat) at zoom z.
// Longitudes outside [-180,180] are wrapped to a longitude, but the caller
// is expected to reject tiles that fall outside the declared projection
// extent; this function only maps a point to a tile.
func LonLatToTile(lon, lat float64, z int) (TileID, error) {
	if z < 0 || z > MaxZoom {
		return TileID{}, ErrBadZoom
	}
	if math.Abs(lat) >= MaxLatitude {
		return TileID{}, ErrPolar
	}
	n := float64(int(1) << uint(z))
	xf := (lon + 180.0) / 360.0 * n
	yf := (1.0 - math.Log(math.Tan(lat*math.Pi/180.0)+1.0/math.Cos(lat*math.Pi/180.0))/math.Pi) / 2.0 * n
	x := int(math.Floor(xf))
	y := int(math.Floor(yf))
	if x < 0 {
		x = 0
	}
	if x >= int(n) {
		x = int(n) - 1
	}
	if y < 0 {
		y = 0
	}
	if y >= int(n) {
		y = int(n) - 1
	}
	return TileID{Z: z, X: x, Y: y}, nil
}

// TileBounds returns the geographic bounds of a tile in (west, south, east,
// north) order. West may be greater than east only when the caller is
// enumerating a wrapping rectangle; a single tile itself never wraps.
func TileBounds(t TileID) (west, south, east, north float64) {
	n := float64(int(1) << uint(t.Z))
	west = float64(t.X)/n*360.0 - 180.0
	east = float64(t.X+1)/n*360.0 - 180.0
	latN := math.Atan(math.Sinh(math.Pi*(1-float64(t.Y)/n))) * 180.0 / math.Pi
	latS := math.Atan(math.Sinh(math.Pi*(1-float64(t.Y+1)/n))) * 180.0 / math.Pi
	return west, latS, east, latN
}
