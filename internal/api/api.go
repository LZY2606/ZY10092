package api

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"tileforge/internal/model"
	"tileforge/internal/service"
	"tileforge/internal/store"
)

//go:embed web/*
var webFiles embed.FS

type Handler struct {
	service *service.Service
}

func New(svc *service.Service) http.Handler {
	h := &Handler{service: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", h.state)
	mux.HandleFunc("POST /api/packages", h.uploadPackage)
	mux.HandleFunc("POST /api/packages/{id}/decision", h.decision)
	mux.HandleFunc("POST /api/policies", h.setPolicy)
	mux.HandleFunc("POST /api/builds", h.startBuild)
	mux.HandleFunc("POST /api/builds/{id}/recover", h.recoverBuild)
	mux.HandleFunc("GET /api/builds/{id}/export", h.exportBuild)
	mux.HandleFunc("POST /api/imports", h.importBuild)
	mux.HandleFunc("GET /api/builds/{id}/tiles/{z}/{x}/{y}", h.buildTile)
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /", h.web)
	return logging(mux)
}

type stateResponse struct {
	Packages []model.Package     `json:"packages"`
	Policies []model.Policy      `json:"policies"`
	Builds   []model.BuildRecord `json:"builds"`
	Counts   map[string]int      `json:"counts"`
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	packages := h.service.Store().Packages()
	builds := h.service.Store().Builds()
	counts := map[string]int{}
	for _, pkg := range packages {
		counts["package_"+pkg.Status]++
	}
	for _, build := range builds {
		counts["build_"+build.Status]++
	}
	writeJSON(w, http.StatusOK, stateResponse{Packages: packages, Policies: h.service.Store().Policies(), Builds: builds, Counts: counts})
}

func (h *Handler) uploadPackage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 * 1024 * 1024); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, _, err := r.FormFile("package")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("multipart field package is required"))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024*1024+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(data) > 64*1024*1024 {
		writeError(w, http.StatusBadRequest, errors.New("package exceeds 64 MiB"))
		return
	}
	pkg, status, err := h.service.IngestPackage(data, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, status, pkg)
}

func (h *Handler) decision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pkg, status, err := h.service.DecidePackage(r.PathValue("id"), req.Status, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, status, pkg)
}

func (h *Handler) setPolicy(w http.ResponseWriter, r *http.Request) {
	var policy model.Policy
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	saved, status, err := h.service.SetPolicy(policy, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, status, saved)
}

func (h *Handler) startBuild(w http.ResponseWriter, r *http.Request) {
	var req service.BuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, status, err := h.service.StartBuild(req, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, status, record)
}

func (h *Handler) recoverBuild(w http.ResponseWriter, r *http.Request) {
	record, status, err := h.service.RecoverBuild(r.PathValue("id"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, status, record)
}

func (h *Handler) exportBuild(w http.ResponseWriter, r *http.Request) {
	data, err := h.service.ExportBuild(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="build.zip"`)
	_, _ = w.Write(data)
}

func (h *Handler) importBuild(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(128 * 1024 * 1024); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, _, err := r.FormFile("build")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("multipart field build is required"))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128*1024*1024+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, status, err := h.service.ImportBuild(data, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, status, record)
}

func (h *Handler) buildTile(w http.ResponseWriter, r *http.Request) {
	record, err := h.service.Store().Build(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tileKey := strings.Join([]string{r.PathValue("z"), r.PathValue("x"), r.PathValue("y")}, "/")
	for _, tile := range record.Manifest.Tiles {
		current := strings.Join([]string{itoa(tile.Z), itoa(tile.X), itoa(tile.Y)}, "/")
		if current == tileKey {
			if r.URL.Query().Get("provenance") == "1" {
				writeJSON(w, http.StatusOK, tile)
				return
			}
			data, err := h.service.Store().ReadCAS(tile.BlobHash)
			if err != nil {
				writeError(w, http.StatusNotFound, err)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
			return
		}
	}
	writeError(w, http.StatusNotFound, errors.New("tile is outside the build manifest"))
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	events, err := h.service.Store().Events()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *Handler) web(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if r.URL.Path == "/" {
		data, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	http.FileServerFS(sub).ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func itoa(value int) string {
	return strings.TrimSpace(jsonNumber(value))
}

func jsonNumber(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
