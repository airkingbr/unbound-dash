package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/airkingbr/unbound-dash/internal/blocklist"
	"github.com/airkingbr/unbound-dash/internal/oficio"
	"github.com/airkingbr/unbound-dash/internal/unboundctl"
)

const maxOficioUpload = 32 << 20 // 32 MB

// handleOficioParse accepts a multipart upload with a PDF oficio, extracts
// its text with pdftotext and applies the matching oficio.Template,
// returning the detected origem/fonte/data and the list of domains found.
func (s *Server) handleOficioParse(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxOficioUpload); err != nil {
		writeError(w, http.StatusBadRequest, "arquivo invalido ou maior que 32MB")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "arquivo nao enviado")
		return
	}
	defer file.Close()

	text, err := extractPDFText(s.cfg.PdftotextBin, file)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	result, err := oficio.Parse(text)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func extractPDFText(bin string, r io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", "oficio-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if _, err := io.Copy(tmp, r); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	if bin == "" {
		bin = "pdftotext"
	}
	out, err := exec.Command(bin, "-enc", "UTF-8", "-layout", tmp.Name(), "-").Output()
	if err != nil {
		return "", fmt.Errorf("falha ao extrair texto do PDF: %w", err)
	}
	return string(out), nil
}

// handleOficioApply adds a batch of domains extracted from an oficio to the
// blocklist, skipping domains already present. It reloads Unbound and
// flushes the entire cache once at the end, instead of per domain, since
// oficios commonly carry hundreds or thousands of domains.
func (s *Server) handleOficioApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains []string `json:"domains"`
		Origem  string   `json:"origem"`
		Fonte   string   `json:"fonte"`
		Data    string   `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
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
	existing := make(map[string]bool, len(entries))
	for _, e := range entries {
		existing[e.Domain] = true
	}

	added := []string{}
	skipped := []string{}
	invalid := []string{}
	for _, raw := range req.Domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if !blocklist.ValidDomain(domain) {
			invalid = append(invalid, raw)
			continue
		}
		if existing[domain] {
			skipped = append(skipped, domain)
			continue
		}
		entries = append(entries, blocklist.Entry{Domain: domain, Origem: req.Origem, Fonte: req.Fonte, Data: req.Data})
		existing[domain] = true
		added = append(added, domain)
	}

	if len(added) > 0 {
		if err := blocklist.Save(s.cfg.BlocklistFile, entries); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.reloadUnbound()
		if _, err := unboundctl.RunControl(s.client, "flush_zone", []string{"."}); err != nil {
			log.Printf("flush_zone . after oficio import: %v", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"added":   added,
		"skipped": skipped,
		"invalid": invalid,
	})
}
