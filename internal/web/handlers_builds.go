package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) listBuilds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.App.Builds())
}

func (s *Server) getBuild(w http.ResponseWriter, r *http.Request) {
	b, err := s.App.GetBuild(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

type createBuildReq struct {
	Region string `json:"region"`
}

func (s *Server) createBuild(w http.ResponseWriter, r *http.Request) {
	var req createBuildReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Region == "" {
		writeErr(w, http.StatusBadRequest, errBad("region required"))
		return
	}
	b, _, err := s.App.CreateBuild(req.Region, idemKey(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) startBuild(w http.ResponseWriter, r *http.Request) {
	b, err := s.App.StartBuild(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) acceptBuild(w http.ResponseWriter, r *http.Request) {
	man, _, err := s.App.AcceptBuild(r.PathValue("id"), idemKey(r))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, man)
}

func (s *Server) rejectBuild(w http.ResponseWriter, r *http.Request) {
	b, _, err := s.App.RejectBuild(r.PathValue("id"), idemKey(r))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) resumeBuild(w http.ResponseWriter, r *http.Request) {
	man, _, err := s.App.ResumeBuild(r.PathValue("id"), idemKey(r))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, man)
}

func (s *Server) exportBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.tar"`)
	if err := s.App.ExportBuild(id, w); err != nil {
		w.Header().Set("Content-Type", "application/json")
		writeErr(w, http.StatusConflict, err)
		return
	}
}

func (s *Server) getManifest(w http.ResponseWriter, r *http.Request) {
	data, err := s.App.Manifest(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *Server) reimportBuild(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil || len(body) == 0 {
			writeErr(w, http.StatusBadRequest, errBad("tar upload required"))
			return
		}
		s.doReimport(w, r, bytes.NewReader(body))
		return
	}
	file, _, err := r.FormFile("package")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errBad("form field 'package' required"))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.doReimport(w, r, bytes.NewReader(data))
}

func (s *Server) doReimport(w http.ResponseWriter, r *http.Request, rd io.Reader) {
	origin := r.URL.Query().Get("origin")
	if origin == "" {
		origin = r.FormValue("origin")
	}
	res, _, err := s.App.ReimportBuild(rd, origin, idemKey(r))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}
