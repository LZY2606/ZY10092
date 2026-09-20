package web

import (
	"net/http"
)

func (s *Server) getBlock(w http.ResponseWriter, r *http.Request) {
	data, err := s.App.Store().Get(r.PathValue("sha"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.App.Events()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}
