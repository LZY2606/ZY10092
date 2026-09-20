package service_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tileforge/internal/geo"
	"tileforge/internal/imageproc"
	"tileforge/internal/model"
	"tileforge/internal/service"
	"tileforge/internal/store"
)

func newTestService(t *testing.T) (*service.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return service.New(st), dir
}

func solidPNG(t *testing.T, rgba color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = []byte{rgba.R, rgba.G, rgba.B, rgba.A}[i%4]
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func packageZip(t *testing.T, name, version, license, crs, scheme string, z, x, y int, updated time.Time, pngData []byte, bounds *geo.BBox) []byte {
	t.Helper()
	tile := map[string]any{
		"z": z, "x": x, "y": y, "path": "tile.png",
		"blob_hash": imageproc.HashBytes(pngData),
	}
	manifest := map[string]any{
		"name": name, "version": version, "crs": crs, "scheme": scheme,
		"license": license, "updated_at": updated.UTC().Format(time.RFC3339),
		"tiles": []map[string]any{tile},
	}
	if bounds != nil {
		manifest["bounds"] = bounds
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	write := func(name string, data []byte) {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", manifestData)
	write("tile.png", pngData)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func ingestAccept(t *testing.T, svc *service.Service, data []byte) model.Package {
	t.Helper()
	pkg, status, err := svc.IngestPackage(data, "")
	if err != nil || status != 201 {
		t.Fatalf("ingest status=%d err=%v", status, err)
	}
	pkg, status, err = svc.DecidePackage(pkg.ID, model.StatusAccepted, "")
	if err != nil || status != 200 {
		t.Fatalf("accept status=%d err=%v", status, err)
	}
	return pkg
}

func TestBuildDetectsDuplicatesDateInversionAndEqualPixelsMetadataConflict(t *testing.T) {
	svc, _ := newTestService(t)
	pngData := solidPNG(t, color.RGBA{R: 10, G: 120, B: 220, A: 255})
	base := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	ingestAccept(t, svc, packageZip(t, "west", "1", "CC-BY-4.0", geo.EPSG4326, geo.XYZ, 0, 0, 0, base, pngData, nil))
	pkgB := ingestAccept(t, svc, packageZip(t, "east-old", "1", "CC-BY-SA-4.0", geo.EPSG4326, geo.XYZ, 0, 1, 0, base.AddDate(0, 1, 0), pngData, nil))
	pkgC := ingestAccept(t, svc, packageZip(t, "east-new", "1", "Proprietary", geo.EPSG4326, geo.XYZ, 0, 1, 0, base.AddDate(0, 2, 0), pngData, nil))

	record, status, err := svc.StartBuild(service.BuildRequest{Region: geo.BBox{West: -180, South: -90, East: 180, North: 90}, Zoom: 0}, "")
	if err != nil || status != 201 {
		t.Fatalf("build status=%d err=%v", status, err)
	}
	if record.Status != model.StatusPublished {
		t.Fatalf("expected published, got %s: %s", record.Status, record.Reason)
	}
	if len(record.Manifest.Tiles) != 2 {
		t.Fatalf("expected two canonical z0 tiles, got %d", len(record.Manifest.Tiles))
	}
	east := record.Manifest.Tiles[1]
	if east.Selected.PackageID != pkgC.ID {
		t.Fatalf("newest source should win without policy, got %s", east.Selected.PackageID)
	}
	issueTypes := map[string]bool{}
	for _, issue := range east.Issues {
		issueTypes[issue.Type] = true
	}
	for _, wanted := range []string{"duplicate_coverage", "date_inversion", "license_conflict"} {
		if !issueTypes[wanted] {
			t.Fatalf("east tile missing %s in %+v", wanted, east.Issues)
		}
	}
	var metadataConflict bool
	for _, tile := range record.Manifest.Tiles {
		for _, issue := range tile.Issues {
			if issue.Type == "metadata_conflict" {
				metadataConflict = true
			}
		}
	}
	if !metadataConflict {
		t.Fatalf("identical edge pixels with different package/license metadata must warn: %+v", record.Manifest.Tiles)
	}

	_, status, err = svc.SetPolicy(model.Policy{Region: geo.BBox{West: 0, South: -90, East: 180, North: 90}, PriorityPackage: pkgB.ID}, "policy-east")
	if err != nil || status != 200 {
		t.Fatalf("policy status=%d err=%v", status, err)
	}
	rebuilt, status, err := svc.StartBuild(service.BuildRequest{Region: geo.BBox{West: -180, South: -90, East: 180, North: 90}, Zoom: 0}, "build-priority")
	if err != nil || status != 201 {
		t.Fatalf("priority build status=%d err=%v", status, err)
	}
	if rebuilt.Manifest.Tiles[1].Selected.PackageID != pkgB.ID {
		t.Fatalf("priority should select older package %s, got %s", pkgB.ID, rebuilt.Manifest.Tiles[1].Selected.PackageID)
	}
	if rebuilt.BuildID == record.BuildID {
		t.Fatal("rule change must create a separately indexed output, not mutate published build")
	}
	original, _ := svc.Store().Build(record.BuildID)
	if original.Manifest.Tiles[1].Selected.PackageID != pkgC.ID {
		t.Fatal("published source table changed after policy update")
	}
}

func TestBlockWriteFailureLeavesStateReadableAndResumePublishes(t *testing.T) {
	svc, _ := newTestService(t)
	westPNG := solidPNG(t, color.RGBA{R: 200, G: 20, B: 40, A: 255})
	eastPNG := solidPNG(t, color.RGBA{R: 40, G: 200, B: 20, A: 255})
	base := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)
	ingestAccept(t, svc, packageZip(t, "west", "1", "CC-BY-4.0", geo.EPSG4326, geo.XYZ, 0, 0, 0, base, westPNG, nil))
	ingestAccept(t, svc, packageZip(t, "east", "1", "CC-BY-4.0", geo.EPSG4326, geo.XYZ, 0, 1, 0, base, eastPNG, nil))
	svc.Store().Fault.Reset()
	svc.Store().Fault.FailAfter(2)
	failed, status, err := svc.StartBuild(service.BuildRequest{Region: geo.BBox{West: -180, South: -90, East: 180, North: 90}, Zoom: 0}, "interrupted")
	if err == nil || status != 500 || failed.Status != model.StatusFailed {
		t.Fatalf("expected injected failure, status=%d record=%+v err=%v", status, failed, err)
	}
	packages := svc.Store().Packages()
	if len(packages) != 2 || packages[0].Status != model.StatusAccepted {
		t.Fatalf("old accepted state must remain readable: %+v", packages)
	}
	eventsBefore, _ := svc.Store().Events()
	var blocksBefore int
	for _, event := range eventsBefore {
		if event.Type == "build_block_written" {
			blocksBefore++
		}
	}
	if blocksBefore != 1 {
		t.Fatalf("one verified block should be traceable before failure, got %d", blocksBefore)
	}
	svc.Store().Fault.Reset()
	recovered, status, err := svc.RecoverBuild(failed.BuildID)
	if err != nil || status != 200 {
		t.Fatalf("recover status=%d err=%v", status, err)
	}
	if recovered.Status != model.StatusPublished {
		t.Fatalf("expected recovered publish, got %s: %s", recovered.Status, recovered.Reason)
	}
	manifestPath := filepath.Join(svc.Store().Root(), "builds", failed.BuildID+".json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("completed manifest should be visible: %v", err)
	}
}

func TestIdempotentUploadReturnsFirstResult(t *testing.T) {
	svc, _ := newTestService(t)
	pngData := solidPNG(t, color.RGBA{A: 255})
	first := packageZip(t, "first", "1", "CC-BY-4.0", geo.EPSG4326, geo.XYZ, 0, 0, 0, time.Now().UTC(), pngData, nil)
	second := packageZip(t, "second-different-body", "9", "Proprietary", geo.EPSG4326, geo.XYZ, 0, 1, 0, time.Now().UTC(), pngData, nil)
	pkg, status, err := svc.IngestPackage(first, "same-key")
	if err != nil || status != 201 {
		t.Fatalf("first status=%d err=%v", status, err)
	}
	again, status, err := svc.IngestPackage(second, "same-key")
	if err != nil || again.ID != pkg.ID {
		t.Fatalf("repeat idempotency key must return first package %s, got %s status=%d err=%v", pkg.ID, again.ID, status, err)
	}
}

func TestExportImportPreservesTileIdentityBoundsAndSourceSummary(t *testing.T) {
	first, dir1 := newTestService(t)
	pngData := solidPNG(t, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	pkg := ingestAccept(t, first, packageZip(t, "exportable", "1", "CC-BY-4.0", geo.EPSG4326, geo.XYZ, 0, 0, 0, time.Now().UTC(), pngData, nil))
	built, _, err := first.StartBuild(service.BuildRequest{Region: geo.BBox{West: -180, South: -90, East: 0, North: 90}, Zoom: 0}, "export")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := first.ExportBuild(built.BuildID)
	if err != nil {
		t.Fatal(err)
	}

	secondStore, err := store.Open(filepath.Join(dir1, "reimported"))
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	second := service.New(secondStore)
	imported, status, err := second.ImportBuild(archive, "import")
	if err != nil || status != 200 {
		t.Fatalf("import status=%d err=%v", status, err)
	}
	if imported.BuildID != built.BuildID || imported.Manifest.ManifestHash != built.Manifest.ManifestHash {
		t.Fatal("reimport changed build identity or manifest hash")
	}
	if len(imported.Manifest.Tiles) != 1 || imported.Manifest.Tiles[0].Key() != built.Manifest.Tiles[0].Key() {
		t.Fatal("reimport changed tile numbering")
	}
	if imported.Manifest.Tiles[0].BBox != built.Manifest.Tiles[0].BBox {
		t.Fatalf("reimport changed bounds: %+v vs %+v", imported.Manifest.Tiles[0].BBox, built.Manifest.Tiles[0].BBox)
	}
	if imported.Manifest.SourceSummary[pkg.ID] != 1 {
		t.Fatalf("source summary lost: %+v", imported.Manifest.SourceSummary)
	}
	importedPackages := secondStore.Packages()
	if len(importedPackages) != 1 || importedPackages[0].ID != pkg.ID || importedPackages[0].Status != model.StatusAccepted {
		t.Fatalf("imported provenance package must be reconstructed and accepted: %+v", importedPackages)
	}
}

func TestReopenStoreRecoversInterruptedBuild(t *testing.T) {
	svc, dir := newTestService(t)
	west := solidPNG(t, color.RGBA{R: 10, G: 10, B: 10, A: 255})
	east := solidPNG(t, color.RGBA{R: 20, G: 20, B: 20, A: 255})
	now := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	ingestAccept(t, svc, packageZip(t, "west", "1", "CC-BY-4.0", geo.EPSG4326, geo.XYZ, 0, 0, 0, now, west, nil))
	ingestAccept(t, svc, packageZip(t, "east", "1", "CC-BY-4.0", geo.EPSG4326, geo.XYZ, 0, 1, 0, now, east, nil))
	if err := svc.Store().Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	record := model.BuildRecord{
		BuildID: "build_simulated_interrupted",
		Status:  "building",
		Manifest: model.BuildManifest{
			Schema: "tileforge.build/v1", BuildID: "build_simulated_interrupted",
			Region: geo.BBox{West: -180, South: -90, East: 180, North: 90}, Zoom: 0,
			Status: "building", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			SourceSummary: map[string]int{},
		},
	}
	if err := reopened.Append(model.Event{Type: "build_started", Build: &record}); err != nil {
		t.Fatal(err)
	}

	restartedService := service.New(reopened)
	recovered := restartedService.RecoverInterrupted()
	if len(recovered) != 1 {
		t.Fatalf("expected one auto-recovered build, got %+v", recovered)
	}
	if recovered[0].Status != model.StatusPublished {
		t.Fatalf("expected reopened service to resume and publish, got %s: %s", recovered[0].Status, recovered[0].Reason)
	}
}
