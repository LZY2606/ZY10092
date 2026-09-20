package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) listPackages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.App.Packages())
}

func (s *Server) getPackage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.App.Package(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, errNotFound("package"))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) importPackage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		// Also accept a raw tar body.
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil || len(body) == 0 {
			writeErr(w, http.StatusBadRequest, errBad("multipart or raw tar body required"))
			return
		}
		s.doImport(w, r, bytes.NewReader(body))
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
	s.doImport(w, r, bytes.NewReader(data))
}

func (s *Server) doImport(w http.ResponseWriter, r *http.Request, rd io.Reader) {
	res, _, err := s.App.ImportPackage(rd, idemKey(r))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) generatePackage(w http.ResponseWriter, r *http.Request) {
	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rd, err := buildPackageTar(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, _, err := s.App.ImportPackage(rd, idemKey(r))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}
