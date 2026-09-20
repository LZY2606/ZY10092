// Package web exposes the application over HTTP and serves the browser UI.
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"

	"gsb/internal/app"
)

//go:embed app/index.html app/app.js app/style.css
var assets embed.FS

// Server wires HTTP routes to the application.
type Server struct {
	App *app.App
	Mux *http.ServeMux
}

// New builds a server with all routes registered.
func New(a *app.App) *Server {
	s := &Server{App: a, Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	sub, _ := fs.Sub(assets, "app")
	s.Mux.Handle("GET /", http.FileServer(http.FS(sub)))

	s.Mux.HandleFunc("GET /api/health", s.health)
	s.Mux.HandleFunc("GET /api/packages", s.listPackages)
	s.Mux.HandleFunc("GET /api/packages/{id}", s.getPackage)
	s.Mux.HandleFunc("POST /api/packages/import", s.importPackage)
	s.Mux.HandleFunc("POST /api/packages/generate", s.generatePackage)

	s.Mux.HandleFunc("GET /api/regions", s.listRegions)
	s.Mux.HandleFunc("POST /api/regions", s.upsertRegion)
	s.Mux.HandleFunc("GET /api/regions/{name}", s.getRegion)

	s.Mux.HandleFunc("GET /api/analysis/{name}", s.analyze)
	s.Mux.HandleFunc("POST /api/plans/{name}", s.preparePlan)
	s.Mux.HandleFunc("GET /api/provenance/{name}", s.provenance)

	s.Mux.HandleFunc("POST /api/builds", s.createBuild)
	s.Mux.HandleFunc("GET /api/builds", s.listBuilds)
	s.Mux.HandleFunc("GET /api/builds/{id}", s.getBuild)
	s.Mux.HandleFunc("POST /api/builds/{id}/start", s.startBuild)
	s.Mux.HandleFunc("POST /api/builds/{id}/accept", s.acceptBuild)
	s.Mux.HandleFunc("POST /api/builds/{id}/reject", s.rejectBuild)
	s.Mux.HandleFunc("POST /api/builds/{id}/resume", s.resumeBuild)
	s.Mux.HandleFunc("GET /api/builds/{id}/export", s.exportBuild)
	s.Mux.HandleFunc("GET /api/builds/{id}/manifest", s.getManifest)
	s.Mux.HandleFunc("POST /api/builds/reimport", s.reimportBuild)

	s.Mux.HandleFunc("GET /api/blocks/{sha}", s.getBlock)
	s.Mux.HandleFunc("GET /api/events", s.listEvents)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func idemKey(r *http.Request) string {
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("idem")
}
