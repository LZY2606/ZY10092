package web

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"gsb/internal/app"
	"gsb/internal/store"
)

func testServer(t *testing.T) (*Server, *app.App) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWithStore(st)
	if err != nil {
		t.Fatal(err)
	}
	return New(a), a
}

func do(t *testing.T, h http.Handler, method, path string, body any, idem, contentType string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		if contentType == "application/json" {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = body.(io.Reader)
		}
	}
	req := httptest.NewRequest(method, path, rd)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func genBody(t *testing.T, name, license, captured string, seed int, oor int) map[string]any {
	t.Helper()
	return map[string]any{
		"name": name, "zoom": 2, "scheme": "xyz", "projection": "EPSG:3857",
		"license": license, "source": name + "-src",
		"west": -120, "south": 20, "east": -60, "north": 60,
		"captured": captured, "x0": 0, "y0": 1, "w": 2, "h": 1,
		"size": 8, "out_of_range": oor, "seed": seed,
	}
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", string(b), err)
	}
	return v
}

func TestEndToEnd(t *testing.T) {
	s, a := testServer(t)

	// 1. generate+import two overlapping packages, same idempotency key twice.
	code, b := do(t, s.Mux, "POST", "/api/packages/generate", genBody(t, "alpha", "CC-BY-4.0", "2025-01-01T00:00:00Z", 11, 1), "idem-gen", "application/json")
	if code != http.StatusCreated {
		t.Fatalf("import1 %d: %s", code, b)
	}
	_ = decode[map[string]any](t, b)
	code, b2 := do(t, s.Mux, "POST", "/api/packages/generate", genBody(t, "alpha", "CC-BY-4.0", "2025-01-01T00:00:00Z", 11, 1), "idem-gen", "application/json")
	if code != http.StatusCreated {
		t.Fatalf("idempotent import %d: %s", code, b2)
	}
	if !bytes.Equal(b, b2) {
		t.Fatal("same idempotency key must return byte-identical first response")
	}
	code, b = do(t, s.Mux, "POST", "/api/packages/generate", genBody(t, "beta", "PROPRIETARY", "2026-06-01T00:00:00Z", 40, 0), "idem-gen2", "application/json")
	if code != http.StatusCreated {
		t.Fatalf("import2 %d: %s", code, b)
	}
	pkgs := a.Packages()
	if len(pkgs) != 2 {
		t.Fatalf("want 2 packages, got %d", len(pkgs))
	}
	if len(pkgs[0].Quarantined) == 0 {
		t.Fatal("out-of-range tile must be quarantined")
	}

	// 2. region + analysis
	region := map[string]any{"name": "main", "zoom": 2, "west": -130, "south": 10, "east": -40, "north": 70,
		"priority": []string{pkgs[0].ID, pkgs[1].ID}}
	if code, b = do(t, s.Mux, "POST", "/api/regions", region, "", "application/json"); code != http.StatusOK {
		t.Fatalf("region %d: %s", code, b)
	}
	code, b = do(t, s.Mux, "GET", "/api/analysis/main", nil, "", "")
	if code != http.StatusOK {
		t.Fatalf("analysis %d: %s", code, b)
	}
	an := decode[map[string]any](t, b)
	if len(an["duplicates"].([]any)) == 0 {
		t.Fatal("expected duplicate coverage")
	}
	if len(an["warnings"].([]any)) == 0 {
		t.Fatal("expected warnings (date inversion / license)")
	}

	// 3. plan + create build + accept
	if code, b = do(t, s.Mux, "POST", "/api/plans/main", nil, "", ""); code != http.StatusOK {
		t.Fatalf("plan %d: %s", code, b)
	}
	code, b = do(t, s.Mux, "POST", "/api/builds", map[string]any{"region": "main"}, "b-create", "application/json")
	if code != http.StatusCreated {
		t.Fatalf("create %d: %s", code, b)
	}
	created := decode[map[string]any](t, b)
	id := created["id"].(string)
	// repeated create returns first
	_, bAgain := do(t, s.Mux, "POST", "/api/builds", map[string]any{"region": "main"}, "b-create", "application/json")
	if !bytes.Equal(b, bAgain) {
		t.Fatal("create build idempotency must return first result")
	}
	if code, b = do(t, s.Mux, "POST", "/api/builds/"+id+"/accept", nil, "", ""); code != http.StatusOK {
		t.Fatalf("accept %d: %s", code, b)
	}

	// 4. export
	req := httptest.NewRequest("GET", "/api/builds/"+id+"/export", nil)
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export %d: %s", rec.Code, rec.Body.String())
	}
	exported := rec.Body.Bytes()
	if err := readTar(exported); err != nil {
		t.Fatal(err)
	}

	// 5. reimport -> identical tiles/bounds/sources
	body, contentType := multipartBody(t, exported)
	req = httptest.NewRequest("POST", "/api/builds/reimport?origin="+id, body)
	req.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reimport %d: %s", rec.Code, rec.Body.String())
	}
	rr := decode[map[string]any](t, rec.Body.Bytes())
	if rr["same_tiles"] != true || rr["same_bounds"] != true || rr["same_sources"] != true {
		t.Fatalf("round trip mismatch: %+v", rr)
	}

	// 6. events recorded and four status columns data present
	code, b = do(t, s.Mux, "GET", "/api/events", nil, "", "")
	if code != http.StatusOK || len(decode[[]any](t, b)) == 0 {
		t.Fatal("expected traceable events")
	}
	code, b = do(t, s.Mux, "GET", "/api/builds", nil, "", "")
	if code != http.StatusOK || len(decode[[]any](t, b)) < 2 {
		t.Fatal("expected original + reimported builds")
	}
}

func readTar(data []byte) error {
	tr := tar.NewReader(bytes.NewReader(data))
	sawManifest := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Name == "manifest.json" {
			sawManifest = true
		}
	}
	if !sawManifest {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func multipartBody(t *testing.T, tarData []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="package"; filename="build.tar"`)
	h.Set("Content-Type", "application/x-tar")
	w, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(tarData); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}
