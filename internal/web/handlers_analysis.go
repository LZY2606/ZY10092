package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"gsb/internal/app"
	"gsb/internal/geo"
)

func (s *Server) listRegions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.App.Regions())
}

func (s *Server) getRegion(w http.ResponseWriter, r *http.Request) {
	rg, err := s.App.GetRegion(r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, rg)
}

func (s *Server) upsertRegion(w http.ResponseWriter, r *http.Request) {
	var req app.RegionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rg, _, err := s.App.UpsertRegion(req, idemKey(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, rg)
}

func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	an, err := s.App.Analyze(r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, an)
}

func (s *Server) preparePlan(w http.ResponseWriter, r *http.Request) {
	plan, _, err := s.App.PreparePlan(r.PathValue("name"), idemKey(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) provenance(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	z, _ := strconv.Atoi(q.Get("z"))
	x, _ := strconv.Atoi(q.Get("x"))
	y, _ := strconv.Atoi(q.Get("y"))
	prov, err := s.App.Provenance(r.PathValue("name"), geo.TileID{Z: z, X: x, Y: y})
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, prov)
}
