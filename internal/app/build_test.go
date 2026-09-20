package app

import (
	"bytes"
	"errors"
	"testing"

	"gsb/internal/store"
)

func prepareBuild(t *testing.T, a *App) *Build {
	t.Helper()
	a2 := newTestApp(t)
	_ = a2
	sp1 := baseSpec("pri", "CC-BY-4.0", "2026-01-01T00:00:00Z", 4)
	sp2 := baseSpec("sec", "CC-BY-SA-4.0", "2025-01-01T00:00:00Z", 9)
	p1 := importSpec(t, a, sp1, "")
	p2 := importSpec(t, a, sp2, "")
	_, _, err := a.UpsertRegion(RegionRequest{
		Name: "main", Zoom: 2, West: -120, South: 20, East: -60, North: 60,
		Priority: []string{p1.ID, p2.ID},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.PreparePlan("main", ""); err != nil {
		t.Fatal(err)
	}
	b, _, err := a.CreateBuild("main", "")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildLifecycleAndManifestVisibility(t *testing.T) {
	a := newTestApp(t)
	b := prepareBuild(t, a)
	if b.Status != StatusPending {
		t.Fatalf("new build must be pending, got %s", b.Status)
	}
	// Before manifest commit the build must not be exportable/accepted.
	var buf bytes.Buffer
	if err := a.ExportBuild(b.ID, &buf); err == nil {
		t.Fatal("pending build must not be visible as export")
	}
	man, _, err := a.AcceptBuild(b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Tiles) == 0 || len(man.Sources) == 0 {
		t.Fatalf("manifest must list tiles and source summary: %+v", man)
	}
	got, err := a.GetBuild(b.ID)
	if err != nil || got.Status != StatusAccepted {
		t.Fatalf("build not accepted: %v %v", got, err)
	}
	// Accepted build is immutable: rules change does not change it.
	pkgs := a.Packages()
	_, _, err = a.UpsertRegion(RegionRequest{
		Name: "main", Zoom: 2, West: -120, South: 20, East: -60, North: 60,
		Priority: []string{pkgs[1].ID, pkgs[0].ID},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	got2, _ := a.GetBuild(b.ID)
	if got2.ManifestSHA != got.ManifestSHA {
		t.Fatal("published build must keep its index and source table when rules change")
	}
}

func TestFailedWriteKeepsOldStateThenResume(t *testing.T) {
	a := newTestApp(t)
	b := prepareBuild(t, a)

	failN := 0
	a.Fault = func(kind, id string) error {
		if kind == "block" {
			failN++
			if failN == 2 {
				return errors.New("simulated disk failure")
			}
		}
		return nil
	}
	if _, err := a.StartBuild(b.ID); err == nil {
		t.Fatal("expected build to fail mid-write")
	}
	a.Fault = nil
	failed, err := a.GetBuild(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed {
		t.Fatalf("expected failed state, got %s", failed.Status)
	}
	// Journal and previous packages remain readable.
	if len(a.Packages()) != 2 {
		t.Fatal("old state must remain readable after a write failure")
	}
	written := 0
	for _, tt := range failed.Tiles {
		if tt.Written {
			written++
		}
	}
	if written == 0 {
		t.Fatal("some blocks should have been committed before failure")
	}

	// Resume verifies committed blocks and continues.
	man, _, err := a.ResumeBuild(b.ID, "")
	if err != nil {
		t.Fatalf("resume must complete: %v", err)
	}
	if len(man.Tiles) != len(failed.Tiles) {
		t.Fatal("resumed manifest tile count mismatch")
	}
	for _, tt := range man.Tiles {
		if !a.Store().Has(tt.SHA) {
			t.Fatalf("tile %s content missing after resume", tt.Tile)
		}
	}
}

func TestRejectSeparateFromAccept(t *testing.T) {
	a := newTestApp(t)
	b := prepareBuild(t, a)
	out, _, err := a.RejectBuild(b.ID, "")
	if err != nil || out.Status != StatusRejected {
		t.Fatalf("reject failed: %v %v", out, err)
	}
	var buf bytes.Buffer
	if err := a.ExportBuild(b.ID, &buf); err == nil {
		t.Fatal("rejected build must not export")
	}
}

func TestRestartMarksRecoveringAndResumes(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	a1, err := NewWithStore(st)
	if err != nil {
		t.Fatal(err)
	}
	b := prepareBuild(t, a1)
	crashN := 0
	a1.Fault = func(kind, id string) error {
		if kind == "block" {
			crashN++
			if crashN == 2 {
				return ErrSimulatedCrash
			}
		}
		return nil
	}
	_, _ = a1.StartBuild(b.ID)
	a1.Fault = nil

	// Simulate process restart from the same directory.
	a2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a2.GetBuild(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRecovering {
		t.Fatalf("build interrupted by a crash must surface as recovering on restart, got %s", got.Status)
	}
	if _, _, err := a2.ResumeBuild(b.ID, ""); err != nil {
		t.Fatalf("resume after restart failed: %v", err)
	}
	accepted, _ := a2.GetBuild(b.ID)
	if accepted.Status != StatusAccepted {
		t.Fatalf("final state must be accepted, got %s", accepted.Status)
	}
}

func TestExportReimportRoundTrip(t *testing.T) {
	a := newTestApp(t)
	b := prepareBuild(t, a)
	man, _, err := a.AcceptBuild(b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := a.ExportBuild(b.ID, &buf); err != nil {
		t.Fatal(err)
	}
	res, _, err := a.ReimportBuild(bytes.NewReader(buf.Bytes()), b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.SameTiles || !res.SameBounds || !res.SameSources {
		t.Fatalf("round trip mismatch: tiles=%v bounds=%v sources=%v", res.SameTiles, res.SameBounds, res.SameSources)
	}
	if len(res.Manifest.Tiles) != len(man.Tiles) {
		t.Fatal("reimported manifest tile count differs")
	}
	// Idempotent reimport key returns the same result.
	buf2 := bytes.NewBuffer(buf.Bytes())
	res2, raw2, err := a.ReimportBuild(buf2, b.ID, "reimport-1")
	if err != nil {
		t.Fatal(err)
	}
	buf3 := bytes.NewBuffer(buf.Bytes())
	res3, raw3, err := a.ReimportBuild(buf3, b.ID, "reimport-1")
	if err != nil {
		t.Fatal(err)
	}
	if res2.BuildID != res3.BuildID || !bytes.Equal(raw2, raw3) {
		t.Fatal("idempotent reimport must return first result")
	}
}
