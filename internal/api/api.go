// Package api implements the HTTP API and static asset serving for
// unbound-dash.
package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/airkingbr/unbound-dash/internal/auth"
	"github.com/airkingbr/unbound-dash/internal/blocklist"
	"github.com/airkingbr/unbound-dash/internal/config"
	"github.com/airkingbr/unbound-dash/internal/querylog"
	"github.com/airkingbr/unbound-dash/internal/unboundctl"
)

// Server holds the dependencies for the HTTP API.
type Server struct {
	cfg      config.Config
	client   *unboundctl.Client
	static   fs.FS
	querylog *querylog.Tailer
}

// New creates a new API server. tailer may be nil if query log
// aggregation is unavailable.
func New(cfg config.Config, client *unboundctl.Client, static fs.FS, tailer *querylog.Tailer) *Server {
	return &Server{cfg: cfg, client: client, static: static, querylog: tailer}
}

// Routes builds the top-level HTTP handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/login", s.handleLogin)

	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/logout", s.handleLogout)
	protected.HandleFunc("GET /api/stats", s.handleStats)
	protected.HandleFunc("GET /api/status", s.handleStatus)
	protected.HandleFunc("GET /api/top-domains", s.handleTopDomains)
	protected.HandleFunc("GET /api/top-clients", s.handleTopClients)
	protected.HandleFunc("GET /api/logs", s.handleLogs)
	protected.HandleFunc("GET /api/logs/stream", s.handleLogStream)
	protected.HandleFunc("GET /api/blocklist", s.handleListBlocklist)
	protected.HandleFunc("POST /api/blocklist", s.handleAddBlocklist)
	protected.HandleFunc("DELETE /api/blocklist/{domain}", s.handleDeleteBlocklist)
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

const defaultTopN = 10

func (s *Server) handleTopDomains(w http.ResponseWriter, r *http.Request) {
	if s.querylog == nil {
		writeError(w, http.StatusServiceUnavailable, "log de consultas (log-queries) nao habilitado")
		return
	}
	n := intParam(r, "n", defaultTopN)
	top, since := s.querylog.TopDomains(n)
	writeJSON(w, http.StatusOK, map[string]any{"items": top, "since": since})
}

func (s *Server) handleTopClients(w http.ResponseWriter, r *http.Request) {
	if s.querylog == nil {
		writeError(w, http.StatusServiceUnavailable, "log de consultas (log-queries) nao habilitado")
		return
	}
	n := intParam(r, "n", defaultTopN)
	top, since := s.querylog.TopClients(n)
	writeJSON(w, http.StatusOK, map[string]any{"items": top, "since": since})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UnboundLogFile == "" {
		writeError(w, http.StatusServiceUnavailable, "unbound_log_file nao configurado")
		return
	}
	n := intParam(r, "lines", 200)
	lines, err := querylog.Tail(s.cfg.UnboundLogFile, n)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if s.querylog == nil {
		writeError(w, http.StatusServiceUnavailable, "log de consultas (log-queries) nao habilitado")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming nao suportado")
		return
	}

	lines, cancel := s.querylog.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleListBlocklist(w http.ResponseWriter, _ *http.Request) {
	entries, err := blocklist.Load(s.cfg.BlocklistFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func (s *Server) handleAddBlocklist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		Origem string `json:"origem"`
		Fonte  string `json:"fonte"`
		Data   string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Domain), "."))
	if !blocklist.ValidDomain(domain) {
		writeError(w, http.StatusBadRequest, "dominio invalido")
		return
	}
	req.Origem = strings.TrimSpace(req.Origem)
	if req.Origem == "" {
		writeError(w, http.StatusBadRequest, "origem e obrigatoria")
		return
	}
	req.Fonte = strings.TrimSpace(req.Fonte)
	req.Data = strings.TrimSpace(req.Data)
	if !blocklist.ValidMeta(req.Origem) || !blocklist.ValidMeta(req.Fonte) || !blocklist.ValidMeta(req.Data) {
		writeError(w, http.StatusBadRequest, "campos nao podem conter '#' ou quebras de linha")
		return
	}
	if req.Data == "" {
		req.Data = time.Now().Format("2006-01-02")
	}

	entries, err := blocklist.Load(s.cfg.BlocklistFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range entries {
		if e.Domain == domain {
			writeError(w, http.StatusConflict, "dominio ja esta na lista de bloqueios")
			return
		}
	}
	entries = append(entries, blocklist.Entry{Domain: domain, Origem: req.Origem, Fonte: req.Fonte, Data: req.Data})

	if err := blocklist.Save(s.cfg.BlocklistFile, entries); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteBlocklist(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(strings.TrimSuffix(r.PathValue("domain"), "."))

	entries, err := blocklist.Load(s.cfg.BlocklistFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := entries[:0]
	found := false
	for _, e := range entries {
		if e.Domain == domain {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		writeError(w, http.StatusNotFound, "dominio nao encontrado na lista de bloqueios")
		return
	}

	if err := blocklist.Save(s.cfg.BlocklistFile, out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) reloadUnbound() {
	if _, err := unboundctl.RunControl(s.client, "reload", nil); err != nil {
		log.Printf("reload unbound after blocklist change: %v", err)
	}
}

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
