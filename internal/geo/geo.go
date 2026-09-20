package geo

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

const (
	EPSG4326 = "EPSG:4326"
	EPSG3857 = "EPSG:3857"
	TMS      = "tms"
	XYZ      = "xyz"
)

type BBox struct {
	West  float64 `json:"west"`
	South float64 `json:"south"`
	East  float64 `json:"east"`
	North float64 `json:"north"`
}

func (b BBox) CrossesAntimeridian() bool { return b.West > b.East }

func (b BBox) Valid() bool {
	if math.IsNaN(b.West) || math.IsNaN(b.South) || math.IsNaN(b.East) || math.IsNaN(b.North) {
		return false
	}
	if b.West < -180 || b.West > 180 || b.East < -180 || b.East > 180 {
		return false
	}
	if b.South < -90 || b.South > 90 || b.North < -90 || b.North > 90 || b.South >= b.North {
		return false
	}
	return true
}

func (b BBox) Pieces() []BBox {
	if !b.Valid() {
		return nil
	}
	if !b.CrossesAntimeridian() {
		return []BBox{b}
	}
	return []BBox{{West: b.West, South: b.South, East: 180, North: b.North}, {West: -180, South: b.South, East: b.East, North: b.North}}
}

func (b BBox) String() string {
	return fmt.Sprintf("%.7f,%.7f,%.7f,%.7f", b.West, b.South, b.East, b.North)
}

func GridSize(crs string, z int) (int, int, error) {
	if z < 0 || z > 28 {
		return 0, 0, fmt.Errorf("zoom %d is outside [0,28]", z)
	}
	n := 1 << z
	switch crs {
	case EPSG4326:
		return 2 * n, n, nil
	case EPSG3857:
		return n, n, nil
	default:
		return 0, 0, fmt.Errorf("unsupported CRS %q; use EPSG:4326 or EPSG:3857", crs)
	}
}

func ValidateTileIndex(crs string, z, x, y int) error {
	width, height, err := GridSize(crs, z)
	if err != nil {
		return err
	}
	if x < 0 || x >= width {
		return fmt.Errorf("tile x=%d at z=%d is outside [0,%d); it is isolated instead of wrapped modulo %d", x, z, width, width)
	}
	if y < 0 || y >= height {
		return fmt.Errorf("tile y=%d at z=%d is outside [0,%d); it is isolated instead of wrapped modulo %d", y, z, height, height)
	}
	return nil
}

func CanonicalY(crs string, z, y int, scheme string) (int, error) {
	width, height, err := GridSize(crs, z)
	_ = width
	if err != nil {
		return 0, err
	}
	switch strings.ToLower(scheme) {
	case XYZ, "":
		return y, nil
	case TMS:
		return height - 1 - y, nil
	default:
		return 0, fmt.Errorf("unsupported tile scheme %q", scheme)
	}
}

func TileBBox(crs string, z, x, yIn int, scheme string) (BBox, error) {
	scheme = strings.ToLower(scheme)
	if scheme == "" {
		scheme = XYZ
	}
	y, err := CanonicalY(crs, z, yIn, scheme)
	if err != nil {
		return BBox{}, err
	}
	if err := ValidateTileIndex(crs, z, x, y); err != nil {
		return BBox{}, err
	}
	width, height, _ := GridSize(crs, z)
	if crs == EPSG4326 {
		return BBox{
			West:  -180 + float64(x)*360/float64(width),
			South: 90 - float64(y+1)*180/float64(height),
			East:  -180 + float64(x+1)*360/float64(width),
			North: 90 - float64(y)*180/float64(height),
		}, nil
	}
	latNorth := tileYToLat(float64(y), z)
	latSouth := tileYToLat(float64(y+1), z)
	return BBox{
		West:  -180 + float64(x)*360/float64(width),
		South: latSouth,
		East:  -180 + float64(x+1)*360/float64(width),
		North: latNorth,
	}, nil
}

func tileYToLat(y float64, z int) float64 {
	n := math.Pi - 2*math.Pi*y/float64(int(1)<<z)
	return math.Atan(math.Sinh(n)) * 180 / math.Pi
}

func WebMercatorLatitudeLimit() float64 { return tileYToLat(1, 0) }

func MercatorRepresentable(b BBox) bool {
	limit := WebMercatorLatitudeLimit()
	return b.South >= limit && b.North <= -limit
}

func CanonicalTilesInBBox(z int, b BBox) []TileCoord {
	var coords []TileCoord
	for _, piece := range b.Pieces() {
		width, height := 2*(1<<z), 1<<z
		x0 := int(math.Floor((piece.West + 180) / 360 * float64(width)))
		x1 := int(math.Ceil((piece.East+180)/360*float64(width))) - 1
		y0 := int(math.Floor((90 - piece.North) / 180 * float64(height)))
		y1 := int(math.Ceil((90-piece.South)/180*float64(height))) - 1
		x0 = max(0, min(width-1, x0))
		x1 = max(0, min(width-1, x1))
		y0 = max(0, min(height-1, y0))
		y1 = max(0, min(height-1, y1))
		for x := x0; x <= x1; x++ {
			for y := y0; y <= y1; y++ {
				tile := TileCoord{Z: z, X: x, Y: y}
				if !slices.Contains(coords, tile) {
					coords = append(coords, tile)
				}
			}
		}
	}
	slices.SortFunc(coords, func(a, b TileCoord) int {
		if a.Z != b.Z {
			return a.Z - b.Z
		}
		if a.X != b.X {
			return a.X - b.X
		}
		return a.Y - b.Y
	})
	return coords
}

type TileCoord struct {
	Z int `json:"z"`
	X int `json:"x"`
	Y int `json:"y"`
}

func (t TileCoord) Key() string { return fmt.Sprintf("%d/%d/%d", t.Z, t.X, t.Y) }

func BBoxesOverlap(a, b BBox) bool {
	ap, bp := a.Pieces(), b.Pieces()
	for _, x := range ap {
		for _, y := range bp {
			if x.West < y.East && x.East > y.West && x.South < y.North && x.North > y.South {
				return true
			}
		}
	}
	return false
}
