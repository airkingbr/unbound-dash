// Package api implements the HTTP API and static asset serving for
// unbound-dash.
package api

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/airkingbr/unbound-dash/internal/auth"
	"github.com/airkingbr/unbound-dash/internal/config"
	"github.com/airkingbr/unbound-dash/internal/unboundctl"
)

// Server holds the dependencies for the HTTP API.
type Server struct {
	cfg    config.Config
	client *unboundctl.Client
	static fs.FS
}

// New creates a new API server.
func New(cfg config.Config, client *unboundctl.Client, static fs.FS) *Server {
	return &Server{cfg: cfg, client: client, static: static}
}

// Routes builds the top-level HTTP handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/login", s.handleLogin)

	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/logout", s.handleLogout)
	protected.HandleFunc("GET /api/stats", s.handleStats)
	protected.HandleFunc("GET /api/status", s.handleStatus)
	protected.HandleFunc("POST /api/control/{command}", s.handleControl)

	mux.Handle("/api/", auth.Middleware(s.cfg.SessionSecret)(protected))

	mux.Handle("/", http.FileServer(http.FS(s.static)))

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !auth.CheckPassword(s.cfg.AdminPassword, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}

	auth.SetSessionCookie(w, s.cfg.SessionSecret, isTLS(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	auth.ClearSessionCookie(w, isTLS(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	stats, err := s.client.StatsFloat()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	out, err := s.client.Status()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	command := r.PathValue("command")

	var req struct {
		Args []string `json:"args"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	out, err := unboundctl.RunControl(s.client, command, req.Args)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
