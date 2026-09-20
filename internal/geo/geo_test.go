package geo

import (
	"math"
	"testing"
)

func TestTMSDirectionAndNoWrapping(t *testing.T) {
	tile, err := TMStoXYZ(2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tile.Y != 3 {
		t.Fatalf("TMS bottom row must map to XYZ y=3, got %d", tile.Y)
	}
	tile, err = TMStoXYZ(2, 0, 3)
	if err != nil || tile.Y != 0 {
		t.Fatalf("TMS top row must map to XYZ y=0, got %+v err=%v", tile, err)
	}
	for _, bad := range [][3]int{{2, 4, 0}, {2, -1, 0}, {2, 0, 4}, {31, 0, 0}} {
		if _, err := TMStoXYZ(bad[0], bad[1], bad[2]); err == nil {
			t.Fatalf("out-of-range %v must be rejected, never modulo-wrapped", bad)
		}
	}
	if _, err := NewTile(2, 4, 0); err != ErrOutOfRange {
		t.Fatalf("want ErrOutOfRange, got %v", err)
	}
}

func TestAntimeridianExtent(t *testing.T) {
	b := Bounds{West: 170, South: -10, East: -170, North: 10}
	if !b.CrossesAntimeridian() {
		t.Fatal("west>east must mean antimeridian crossing")
	}
	ext, err := BoundsToExtent(b, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !ext.Wrap || len(ext.Rects) != 2 {
		t.Fatalf("crossing extent must split into two rects, got %+v", ext.Rects)
	}
	// left strip reaches x=7, right strip starts at x=0 at z=2 (n=4 scale... check both sides present)
	x0 := ext.Rects[0].X0
	x1last := ext.Rects[len(ext.Rects)-1]
	if x0 == 0 || x1last.X1 == 0 {
		t.Fatalf("expected strips on both sides of the world edge: %+v", ext.Rects)
	}
	for _, c := range ext.Cells() {
		if !ValidIndex(c.Z, c.X, c.Y) {
			t.Fatalf("extent produced wrapped/invalid cell %v", c)
		}
	}
}

func TestPolarExclusion(t *testing.T) {
	b := Bounds{West: 0, South: -89, East: 10, North: 89}
	ext, err := BoundsToExtent(b, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(ext.PolarExcluded) != 2 {
		t.Fatalf("both polar strips must be reported, got %+v", ext.PolarExcluded)
	}
	for _, r := range ext.Rects {
		for _, c := range rCells(r) {
			_, s, _, n := TileBounds(c)
			if math.Abs(s) > MaxLatitude+0.01 || math.Abs(n) > MaxLatitude+0.01 {
				t.Fatalf("indexed cell beyond mercator limit: %v", c)
			}
		}
	}
	if _, err := LonLatToTile(0, 86, 2); err != ErrPolar {
		t.Fatalf("polar latitude must fail: %v", err)
	}
}

func rCells(r Rect) []TileID {
	var out []TileID
	for y := r.Y0; y < r.Y1; y++ {
		for x := r.X0; x < r.X1; x++ {
			out = append(out, TileID{Z: r.Z, X: x, Y: y})
		}
	}
	return out
}

func TestHoles(t *testing.T) {
	region := Extent{Rects: []Rect{{Z: 1, X0: 0, Y0: 0, X1: 2, Y1: 2}}}
	cover := map[TileID]bool{{Z: 1, X: 0, Y: 0}: true, {Z: 1, X: 1, Y: 0}: true, {Z: 1, X: 0, Y: 1}: true}
	holes := Holes(region, cover)
	if len(holes) != 1 || holes[0] != (TileID{Z: 1, X: 1, Y: 1}) {
		t.Fatalf("unexpected holes %+v", holes)
	}
}
