// Package server exposes the review workspace over HTTP. Decoding and
// provenance rules run server-side; the browser renders and links them.
package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"

	"edgebench/internal/store"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	Store *store.Store
}

func New(s *store.Store) *Server { return &Server{Store: s} }

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/project", s.getProject)
	mux.HandleFunc("GET /api/capture", s.getCapture)
	mux.HandleFunc("GET /api/edges", s.getEdges)
	mux.HandleFunc("GET /api/edge", s.getEdge)
	mux.HandleFunc("GET /api/report", s.getReport)
	mux.HandleFunc("GET /api/events", s.listEvents)
	mux.HandleFunc("POST /api/vote", s.postVote)
	mux.HandleFunc("POST /api/params", s.postParams)
	mux.HandleFunc("POST /api/patch", s.postPatch)
	mux.HandleFunc("GET /api/history", s.getHistory)
	mux.HandleFunc("GET /api/archive", s.getArchive)
	mux.HandleFunc("GET /api/export", s.exportBundle)
	mux.HandleFunc("POST /api/import", s.importBundle)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
}

func writeJSON(w http.ResponseWriter, v any, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch store.ClassOf(err) {
	case "corrupt":
		code = http.StatusBadRequest
	case "budget":
		code = http.StatusInsufficientStorage
	case "semantic":
		code = http.StatusUnprocessableEntity
	}
	writeJSON(w, map[string]any{"error": err.Error(), "class": store.ClassOf(err)}, code)
}

func (s *Server) projectID(r *http.Request) string {
	if v := r.URL.Query().Get("projectId"); v != "" {
		return v
	}
	if ps, err := s.Store.ListProjects(); err == nil && len(ps) > 0 {
		return ps[0].ID
	}
	return ""
}

func atoiDefault(v string, d int) int {
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return d
	}
	return n
}
