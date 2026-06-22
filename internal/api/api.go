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
	"github.com/airkingbr/unbound-dash/internal/forwardzone"
	"github.com/airkingbr/unbound-dash/internal/querylog"
	"github.com/airkingbr/unbound-dash/internal/staticentry"
	"github.com/airkingbr/unbound-dash/internal/unboundctl"
)

// Server holds the dependencies for the HTTP API.
type Server struct {
	cfg      config.Config
	client   *unboundctl.Client
	static   fs.FS
	querylog *querylog.Tailer
	version  string
}

// New creates a new API server. tailer may be nil if query log
// aggregation is unavailable.
func New(cfg config.Config, client *unboundctl.Client, static fs.FS, tailer *querylog.Tailer, version string) *Server {
	return &Server{cfg: cfg, client: client, static: static, querylog: tailer, version: version}
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
	protected.HandleFunc("POST /api/blocklist/bulk-delete", s.handleBulkDeleteBlocklist)
	protected.HandleFunc("GET /api/forwardzone", s.handleListForwardZones)
	protected.HandleFunc("POST /api/forwardzone", s.handleAddForwardZone)
	protected.HandleFunc("PUT /api/forwardzone/{domain}", s.handleUpdateForwardZone)
	protected.HandleFunc("DELETE /api/forwardzone/{domain}", s.handleDeleteForwardZone)
	protected.HandleFunc("GET /api/staticentry", s.handleListStaticEntries)
	protected.HandleFunc("POST /api/staticentry", s.handleAddStaticEntry)
	protected.HandleFunc("PUT /api/staticentry/{domain}", s.handleUpdateStaticEntry)
	protected.HandleFunc("DELETE /api/staticentry/{domain}", s.handleDeleteStaticEntry)
	protected.HandleFunc("POST /api/oficio/parse", s.handleOficioParse)
	protected.HandleFunc("POST /api/oficio/apply", s.handleOficioApply)
	protected.HandleFunc("POST /api/control/{command}", s.handleControl)
	protected.HandleFunc("POST /api/dns/query", s.handleDNSQuery)
	protected.HandleFunc("POST /api/dns/reverse", s.handleDNSReverse)
	protected.HandleFunc("POST /api/dns/trace", s.handleDNSTrace)
	protected.HandleFunc("POST /api/dns/compare", s.handleDNSCompare)
	protected.HandleFunc("POST /api/dns/blocked", s.handleDNSBlocked)
	protected.HandleFunc("POST /api/benchmark/cache", s.handleBenchmarkCache)
	protected.HandleFunc("POST /api/benchmark/batch", s.handleBenchmarkBatch)
	protected.HandleFunc("GET /api/benchmark/replay", s.handleBenchmarkReplay)
	protected.HandleFunc("POST /api/benchmark/load", s.handleBenchmarkLoad)
	protected.HandleFunc("GET /api/benchmark/histogram", s.handleBenchmarkHistogram)
	protected.HandleFunc("POST /api/benchmark/compare", s.handleBenchmarkCompare)

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
	info := unboundctl.ParseStatus(out)
	writeJSON(w, http.StatusOK, map[string]any{
		"output":         info.Output,
		"version":        info.Version,
		"uptime_seconds": info.UptimeSeconds,
		"dash_version":   s.version,
	})
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
	s.flushZone(domain)
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
	s.flushZone(domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleBulkDeleteBlocklist removes multiple domains from the blocklist in
// one shot, reloading Unbound and flushing the entire cache once at the end.
func (s *Server) handleBulkDeleteBlocklist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains []string `json:"domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	toRemove := make(map[string]bool, len(req.Domains))
	for _, raw := range req.Domains {
		toRemove[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))] = true
	}

	entries, err := blocklist.Load(s.cfg.BlocklistFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := entries[:0]
	removed := []string{}
	for _, e := range entries {
		if toRemove[e.Domain] {
			removed = append(removed, e.Domain)
			continue
		}
		out = append(out, e)
	}

	if len(removed) > 0 {
		if err := blocklist.Save(s.cfg.BlocklistFile, out); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.reloadUnbound()
		s.flushZone(".")
	}

	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) handleListForwardZones(w http.ResponseWriter, _ *http.Request) {
	entries, err := forwardzone.Load(s.cfg.ForwardZoneFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func validForwardAddrs(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !forwardzone.ValidAddr(a) {
			return false
		}
	}
	return true
}

func (s *Server) handleAddForwardZone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain       string   `json:"domain"`
		ForwardAddrs []string `json:"forward_addrs"`
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
	if !validForwardAddrs(req.ForwardAddrs) {
		writeError(w, http.StatusBadRequest, "informe ao menos um endereco IP valido")
		return
	}

	entries, err := forwardzone.Load(s.cfg.ForwardZoneFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range entries {
		if e.Domain == domain {
			writeError(w, http.StatusConflict, "dominio ja possui forward-zone configurada")
			return
		}
	}
	entries = append(entries, forwardzone.Entry{Domain: domain, ForwardAddrs: req.ForwardAddrs})

	if err := forwardzone.Save(s.cfg.ForwardZoneFile, entries); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	s.flushZone(domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUpdateForwardZone(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(strings.TrimSuffix(r.PathValue("domain"), "."))

	var req struct {
		ForwardAddrs []string `json:"forward_addrs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validForwardAddrs(req.ForwardAddrs) {
		writeError(w, http.StatusBadRequest, "informe ao menos um endereco IP valido")
		return
	}

	entries, err := forwardzone.Load(s.cfg.ForwardZoneFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	found := false
	for i, e := range entries {
		if e.Domain == domain {
			entries[i].ForwardAddrs = req.ForwardAddrs
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "dominio nao encontrado nas forward-zones")
		return
	}

	if err := forwardzone.Save(s.cfg.ForwardZoneFile, entries); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	s.flushZone(domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteForwardZone(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(strings.TrimSuffix(r.PathValue("domain"), "."))

	entries, err := forwardzone.Load(s.cfg.ForwardZoneFile)
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
		writeError(w, http.StatusNotFound, "dominio nao encontrado nas forward-zones")
		return
	}

	if err := forwardzone.Save(s.cfg.ForwardZoneFile, out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	s.flushZone(domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListStaticEntries(w http.ResponseWriter, _ *http.Request) {
	entries, err := staticentry.Load(s.cfg.StaticEntryFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func validStaticAddrs(ipv4, ipv6 string) bool {
	if ipv4 == "" && ipv6 == "" {
		return false
	}
	if ipv4 != "" && !staticentry.ValidIPv4(ipv4) {
		return false
	}
	if ipv6 != "" && !staticentry.ValidIPv6(ipv6) {
		return false
	}
	return true
}

func (s *Server) handleAddStaticEntry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		IPv4   string `json:"ipv4"`
		IPv6   string `json:"ipv6"`
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
	if !validStaticAddrs(req.IPv4, req.IPv6) {
		writeError(w, http.StatusBadRequest, "informe um IPv4 e/ou IPv6 valido")
		return
	}

	entries, err := staticentry.Load(s.cfg.StaticEntryFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range entries {
		if e.Domain == domain {
			writeError(w, http.StatusConflict, "dominio ja possui entrada estatica configurada")
			return
		}
	}
	entries = append(entries, staticentry.Entry{Domain: domain, IPv4: req.IPv4, IPv6: req.IPv6})

	if err := staticentry.Save(s.cfg.StaticEntryFile, entries); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	s.flushZone(domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUpdateStaticEntry(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(strings.TrimSuffix(r.PathValue("domain"), "."))

	var req struct {
		IPv4 string `json:"ipv4"`
		IPv6 string `json:"ipv6"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validStaticAddrs(req.IPv4, req.IPv6) {
		writeError(w, http.StatusBadRequest, "informe um IPv4 e/ou IPv6 valido")
		return
	}

	entries, err := staticentry.Load(s.cfg.StaticEntryFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	found := false
	for i, e := range entries {
		if e.Domain == domain {
			entries[i].IPv4 = req.IPv4
			entries[i].IPv6 = req.IPv6
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "dominio nao encontrado nas entradas estaticas")
		return
	}

	if err := staticentry.Save(s.cfg.StaticEntryFile, entries); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	s.flushZone(domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteStaticEntry(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(strings.TrimSuffix(r.PathValue("domain"), "."))

	entries, err := staticentry.Load(s.cfg.StaticEntryFile)
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
		writeError(w, http.StatusNotFound, "dominio nao encontrado nas entradas estaticas")
		return
	}

	if err := staticentry.Save(s.cfg.StaticEntryFile, out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadUnbound()
	s.flushZone(domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) reloadUnbound() {
	if _, err := unboundctl.RunControl(s.client, "reload", nil); err != nil {
		log.Printf("reload unbound after blocklist change: %v", err)
	}
}

// flushZone removes any cached records at or below domain so the
// reloaded local-zone configuration takes effect immediately, instead
// of waiting for the cached entries' TTL to expire.
func (s *Server) flushZone(domain string) {
	if _, err := unboundctl.RunControl(s.client, "flush_zone", []string{domain}); err != nil {
		log.Printf("flush_zone %s after blocklist change: %v", domain, err)
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
