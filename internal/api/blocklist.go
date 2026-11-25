package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/airkingbr/unbound-dash/internal/dnstools"
	"github.com/airkingbr/unbound-dash/internal/isptools"
)

// handleBlocklistStatus returns the ISP Tools blocklist sync state. The
// token itself is never exposed.
func (s *Server) handleBlocklistStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.syncer.Status())
}

// handleBlocklistSync runs a sync now (also resumes after a 401 pause).
func (s *Server) handleBlocklistSync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	res, err := s.syncer.Sync(ctx, true)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, isptools.ErrBusy) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "status": s.syncer.Status()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "status": s.syncer.Status()})
}

// handleBlocklistSearch searches the applied list (substring match).
func (s *Server) handleBlocklistSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := min(intParam(r, "limit", 100), 500)
	items, total := s.syncer.Search(q, limit)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// handleBlocklistTest queries the local resolver for a listed domain (the
// given one, or a random one) to prove the block is effective.
func (s *Server) handleBlocklistTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Domain), "."))
	if name == "" {
		name = s.syncer.Sample()
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "nenhum dominio na lista aplicada")
		return
	}

	res, err := dnstools.Query("", name, "A")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"domain":     name,
		"blocked_by": s.syncer.Lookup(name),
		"query":      res,
	})
}
