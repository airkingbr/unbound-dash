package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/airkingbr/unbound-dash/internal/benchmark"
)

// resolveBenchmarkDomains returns req.Domains, trimmed and lowercased, or
// benchmark.DefaultDomains if none were given.
func resolveBenchmarkDomains(raw []string) []string {
	if len(raw) == 0 {
		return benchmark.DefaultDomains
	}
	domains := make([]string, 0, len(raw))
	for _, d := range raw {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			domains = append(domains, d)
		}
	}
	if len(domains) == 0 {
		return benchmark.DefaultDomains
	}
	return domains
}

// handleBenchmarkCache compares cold (just-flushed) vs warm (cached)
// resolution latency for a list of domains.
func (s *Server) handleBenchmarkCache(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains []string `json:"domains"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	domains := resolveBenchmarkDomains(req.Domains)
	if len(domains) > 50 {
		domains = domains[:50]
	}

	results := benchmark.CacheTest(s.client, "", domains)
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

// handleBenchmarkBatch runs a single lookup for each domain and returns
// per-domain timings plus aggregate stats.
func (s *Server) handleBenchmarkBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains []string `json:"domains"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	domains := resolveBenchmarkDomains(req.Domains)
	if len(domains) > 200 {
		domains = domains[:200]
	}

	items, stats := benchmark.BatchTest("", domains)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "stats": stats})
}

// handleBenchmarkReplay re-queries the most-requested domains seen recently
// in the Unbound query log.
func (s *Server) handleBenchmarkReplay(w http.ResponseWriter, r *http.Request) {
	if s.querylog == nil {
		writeError(w, http.StatusServiceUnavailable, "log de consultas (log-queries) nao habilitado")
		return
	}

	n := intParam(r, "n", 20)
	if n > 100 {
		n = 100
	}
	top, since := s.querylog.TopDomains(n)
	if len(top) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []benchmark.BatchResult{}, "stats": benchmark.Stats{}, "since": since})
		return
	}

	domains := make([]string, len(top))
	for i, c := range top {
		domains[i] = c.Key
	}

	items, stats := benchmark.BatchTest("", domains)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "stats": stats, "since": since})
}

// handleBenchmarkLoad fires a burst of concurrent queries for one domain
// and reports throughput (QPS) and latency stats.
func (s *Server) handleBenchmarkLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain      string `json:"domain"`
		Concurrency int    `json:"concurrency"`
		Total       int    `json:"total"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Domain = strings.TrimSpace(req.Domain)
	if req.Domain == "" {
		req.Domain = "google.com"
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 10
	}
	if req.Total <= 0 {
		req.Total = 200
	}
	if req.Concurrency > 50 {
		req.Concurrency = 50
	}
	if req.Total > 5000 {
		req.Total = 5000
	}

	result := benchmark.LoadTest("", req.Domain, req.Concurrency, req.Total)
	writeJSON(w, http.StatusOK, result)
}

// handleBenchmarkHistogram returns Unbound's internal response-time
// histogram (no extra DNS traffic is generated).
func (s *Server) handleBenchmarkHistogram(w http.ResponseWriter, _ *http.Request) {
	buckets, err := benchmark.Histogram(s.client)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": buckets})
}

// handleBenchmarkCompare compares average latency for a domain list against
// the local resolver and public resolvers (Cloudflare and Google).
func (s *Server) handleBenchmarkCompare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains []string `json:"domains"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	domains := resolveBenchmarkDomains(req.Domains)
	if len(domains) > 50 {
		domains = domains[:50]
	}

	servers := map[string]string{
		"local (127.0.0.1)":    "127.0.0.1:53",
		"Cloudflare (1.1.1.1)": "1.1.1.1:53",
		"Google (8.8.8.8)":     "8.8.8.8:53",
	}
	results := benchmark.CompareResolvers(domains, servers)
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}
