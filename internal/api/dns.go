package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/airkingbr/unbound-dash/internal/blocklist"
	"github.com/airkingbr/unbound-dash/internal/dnstools"
)

// handleDNSQuery performs a single dig-like DNS query against the local
// Unbound resolver (or another server, if specified).
func (s *Server) handleDNSQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Server string `json:"server"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "informe um nome para consultar")
		return
	}

	res, err := dnstools.Query(req.Server, req.Name, req.Type)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDNSReverse performs a PTR (reverse) lookup for an IP address.
func (s *Server) handleDNSReverse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP     string `json:"ip"`
		Server string `json:"server"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	if req.IP == "" {
		writeError(w, http.StatusBadRequest, "informe um endereco IP")
		return
	}

	res, err := dnstools.Reverse(req.Server, req.IP)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDNSTrace performs an iterative resolution (dig +trace) of a name,
// following NS delegations from the root.
func (s *Server) handleDNSTrace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "informe um nome para consultar")
		return
	}

	res, err := dnstools.Trace(req.Name, req.Type)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDNSCompare runs the same query against the local resolver and one
// or more public resolvers, for side-by-side comparison.
func (s *Server) handleDNSCompare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "informe um nome para consultar")
		return
	}

	servers := []string{"127.0.0.1:53", "1.1.1.1:53", "8.8.8.8:53"}
	results := dnstools.Compare(req.Name, req.Type, servers)
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

// handleDNSBlocked checks whether a domain is present in the blocklist
// (directly or via a blocked parent zone) and confirms it by querying the
// local resolver.
func (s *Server) handleDNSBlocked(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Domain), "."))
	if domain == "" {
		writeError(w, http.StatusBadRequest, "informe um dominio")
		return
	}

	entries, err := blocklist.Load(s.cfg.BlocklistFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	blockedBy := ""
	for _, e := range entries {
		if domain == e.Domain || strings.HasSuffix(domain, "."+e.Domain) {
			if blockedBy == "" || len(e.Domain) < len(blockedBy) {
				blockedBy = e.Domain
			}
		}
	}

	res, err := dnstools.Query("", domain, "A")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"domain":       domain,
		"in_blocklist": blockedBy != "",
		"blocked_by":   blockedBy,
		"query":        res,
	})
}
