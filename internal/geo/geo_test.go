package geo

import (
	"strings"
	"testing"
)

func TestTMSFlipDoesNotWrapOutOfRange(t *testing.T) {
	bbox, err := TileBBox(EPSG4326, 1, 0, 1, TMS)
	if err != nil {
		t.Fatal(err)
	}
	if bbox.South != 0 || bbox.North != 90 {
		t.Fatalf("TMS y=3 at z=1 should flip to XYZ y=0, got %+v", bbox)
	}
	_, err = TileBBox(EPSG4326, 1, 4, 0, XYZ)
	if err == nil || !strings.Contains(err.Error(), "isolated") {
		t.Fatalf("out-of-range x should be isolated without modulo, got %v", err)
	}
	_, err = TileBBox(EPSG4326, 1, 0, 2, TMS)
	if err == nil || !strings.Contains(err.Error(), "isolated") {
		t.Fatalf("out-of-range flipped y should be isolated without modulo, got %v", err)
	}
}

func TestAntimeridianBBoxSplitsAndEnumeratesBothSides(t *testing.T) {
	b := BBox{West: 170, South: 0, East: -170, North: 90}
	if !b.CrossesAntimeridian() {
		t.Fatal("west>east should mean antimeridian crossing")
	}
	pieces := b.Pieces()
	if len(pieces) != 2 || pieces[0].East != 180 || pieces[1].West != -180 {
		t.Fatalf("unexpected pieces: %+v", pieces)
	}
	coords := CanonicalTilesInBBox(1, b)
	if len(coords) != 2 {
		t.Fatalf("z=1 northern strip crossing dateline should select x=3 and x=0, got %+v", coords)
	}
	if coords[0].X != 0 || coords[1].X != 3 {
		t.Fatalf("coordinates should be canonical and sorted, got %+v", coords)
	}
}

func TestPolarAndCRSSemantics(t *testing.T) {
	if WebMercatorLatitudeLimit() >= -85 || WebMercatorLatitudeLimit() <= -85.1 {
		t.Fatalf("unexpected mercator limit %v", WebMercatorLatitudeLimit())
	}
	b := BBox{West: -10, South: 80, East: 10, North: 89}
	if MercatorRepresentable(b) {
		t.Fatal("80..89 must not be representable in Web Mercator")
	}
	coords := CanonicalTilesInBBox(0, b)
	if len(coords) != 2 || coords[0].Y != 0 || coords[1].Y != 0 {
		t.Fatalf("EPSG:4326 z=0 polar band covers two longitude tiles in the top row, got %+v", coords)
	}
}
