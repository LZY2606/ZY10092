package app

import (
	"testing"
)

func setupTwo(t *testing.T) (*App, *Package, *Package) {
	t.Helper()
	a := newTestApp(t)
	old := baseSpec("old-pack", "CC-BY-4.0", "2025-01-01T00:00:00Z", 11)
	newer := baseSpec("new-pack", "PROPRIETARY", "2026-06-01T00:00:00Z", 23)
	newer.varyBorders = true
	p1 := importSpec(t, a, old, "")
	p2 := importSpec(t, a, newer, "")
	return a, p1, p2
}

func TestAnalysisDuplicatesInversionLicenseSeam(t *testing.T) {
	a, p1, _ := setupTwo(t)
	_ = p1
	_, _, err := a.UpsertRegion(RegionRequest{
		Name: "main", Zoom: 2, West: -120, South: 20, East: -60, North: 60,
		Priority: []string{p1.ID}, // older package wins by priority -> date inversion
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	an, err := a.Analyze("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(an.Duplicates) == 0 {
		t.Fatal("overlapping tiles must be reported as duplicates")
	}
	codes := map[string]int{}
	for _, w := range an.Warnings {
		codes[w.Code]++
	}
	if codes["date_inversion"] == 0 {
		t.Fatalf("older chosen over newer must flag date inversion: %+v", an.Warnings)
	}
	if codes["license_conflict"] == 0 {
		t.Fatal("different licenses must be flagged")
	}
	if codes["same_pixels_metadata_conflict"] > 0 {
		t.Fatal("pixels differ here, same_pixels conflict must not fire")
	}
	if len(an.Seams) == 0 {
		t.Fatal("border pixel differences must produce seam heat")
	}
}

func TestSamePixelsLicenseConflict(t *testing.T) {
	a := newTestApp(t)
	sp1 := baseSpec("a", "CC0", "2026-01-01T00:00:00Z", 7)
	sp2 := baseSpec("b", "PROPRIETARY", "2026-01-01T00:00:00Z", 7)
	p1 := importSpec(t, a, sp1, "")
	p2 := importSpec(t, a, sp2, "")
	_, _, err := a.UpsertRegion(RegionRequest{
		Name: "main", Zoom: 2, West: -120, South: 20, East: -60, North: 60,
		Priority: []string{p1.ID, p2.ID},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	an, err := a.Analyze("main")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range an.Warnings {
		if w.Code == "same_pixels_metadata_conflict" {
			found = true
		}
	}
	if !found {
		t.Fatal("identical pixels with conflicting metadata must still warn")
	}
}

func TestHolesDetected(t *testing.T) {
	a := newTestApp(t)
	sp := baseSpec("small", "MIT", "2026-01-01T00:00:00Z", 3)
	sp.w, sp.h = 1, 1
	importSpec(t, a, sp, "")
	_, _, err := a.UpsertRegion(RegionRequest{
		Name: "main", Zoom: 2, West: -120, South: 20, East: -60, North: 60,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	an, err := a.Analyze("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(an.Holes) == 0 {
		t.Fatal("region larger than coverage must show holes")
	}
}

func TestLockPackage(t *testing.T) {
	a, p1, p2 := setupTwo(t)
	_, _, err := a.UpsertRegion(RegionRequest{
		Name: "main", Zoom: 2, West: -120, South: 20, East: -60, North: 60,
		LockedPackage: p2.ID,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := a.PreparePlan("main", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range plan.Entries {
		if cell.Candidates[0].PackageID != p2.ID {
			t.Fatalf("locked version must win every cell, got %s", cell.Candidates[0].PackageID)
		}
	}
	// p1 must be excluded from candidates entirely when p2 has the tile.
	for _, cell := range plan.Entries {
		for _, c := range cell.Candidates {
			if c.PackageID == p1.ID {
				t.Fatal("non-locked package must not compete while a lock is active")
			}
		}
	}
}
