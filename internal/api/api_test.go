package api_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tileforge/internal/api"
	"tileforge/internal/geo"
	"tileforge/internal/imageproc"
	"tileforge/internal/model"
	"tileforge/internal/service"
	"tileforge/internal/store"
)

func TestWebStateUploadBuildAndProvenance(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st)
	server := httptest.NewServer(api.New(svc))
	defer server.Close()

	index, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if index.StatusCode != 200 || !strings.Contains(readBody(t, index.Body), "TileForge") {
		t.Fatal("web UI should be served")
	}

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		c := color.RGBA{R: uint8(i * 3), G: 20, B: 200, A: 255}
		img.Pix[i] = []byte{c.R, c.G, c.B, c.A}[i%4]
	}
	var pngBuffer bytes.Buffer
	if err := png.Encode(&pngBuffer, img); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name": "http-package", "version": "1", "crs": geo.EPSG4326, "scheme": geo.XYZ,
		"license": "CC-BY-4.0", "updated_at": time.Now().UTC().Format(time.RFC3339),
		"tiles": []map[string]any{{"z": 0, "x": 0, "y": 0, "path": "tile.png", "blob_hash": imageproc.HashBytes(pngBuffer.Bytes())}},
	}
	manifestData, _ := json.Marshal(manifest)
	var zipBuffer bytes.Buffer
	zipWriter := zip.NewWriter(&zipBuffer)
	writeZip := func(name string, data []byte) {
		file, err := zipWriter.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write(data)
	}
	writeZip("manifest.json", manifestData)
	writeZip("tile.png", pngBuffer.Bytes())
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	var form bytes.Buffer
	multipartWriter := multipart.NewWriter(&form)
	fileWriter, err := multipartWriter.CreateFormFile("package", "package.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fileWriter.Write(zipBuffer.Bytes())
	multipartWriter.Close()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/packages", &form)
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	req.Header.Set("Idempotency-Key", "upload-once")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var pkg model.Package
	decodeJSON(t, response.Body, &pkg)
	if response.StatusCode != 201 || pkg.Status != model.StatusPending {
		t.Fatalf("upload status=%d package=%+v", response.StatusCode, pkg)
	}

	decision := strings.NewReader(`{"status":"accepted"}`)
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/api/packages/"+pkg.ID+"/decision", decision)
	response, err = http.DefaultClient.Do(req)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("decision status=%d err=%v", response.StatusCode, err)
	}
	response.Body.Close()

	buildBody := strings.NewReader(`{"region":{"west":-180,"south":-90,"east":0,"north":90},"zoom":0}`)
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/api/builds", buildBody)
	response, err = http.DefaultClient.Do(req)
	if err != nil || response.StatusCode != 201 {
		t.Fatalf("build status=%d err=%v", response.StatusCode, err)
	}
	var build model.BuildRecord
	decodeJSON(t, response.Body, &build)
	if build.Status != model.StatusPublished || len(build.Manifest.Tiles) != 1 {
		t.Fatalf("unexpected build: %+v", build)
	}

	prov, err := http.Get(server.URL + "/api/builds/" + build.BuildID + "/tiles/0/0/0?provenance=1")
	if err != nil || prov.StatusCode != 200 {
		t.Fatalf("provenance status=%d err=%v", prov.StatusCode, err)
	}
	var tile model.TileManifest
	decodeJSON(t, prov.Body, &tile)
	if tile.Selected == nil || tile.Selected.PackageID != pkg.ID {
		t.Fatalf("click provenance must trace selected input: %+v", tile)
	}

	stateResponse, err := http.Get(server.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Counts map[string]int `json:"counts"`
	}
	decodeJSON(t, stateResponse.Body, &state)
	if state.Counts["package_accepted"] != 1 || state.Counts["build_published"] != 1 {
		t.Fatalf("UI state counts missing separated statuses: %+v", state.Counts)
	}
}

func readBody(t *testing.T, reader io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeJSON(t *testing.T, reader io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(target); err != nil {
		t.Fatal(err)
	}
}
