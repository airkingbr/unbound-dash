// Package isptools consumes the ISP Tools DNS blocklist API (Anatel
// orders, Operação 404, court decisions) and keeps a local Unbound in sync
// with it, applying only the differences at runtime so the cache is never
// flushed.
package isptools

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production API endpoint.
const DefaultBaseURL = "https://isptools.gbti.net.br/api/v1/blocklist"

// maxListSize caps the downloaded (decompressed) list; the real list is
// ~3 MB in the "texto" format.
const maxListSize = 64 << 20

var md5RE = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Client talks to the blocklist API. Token is sent only in the
// Authorization header, never in the URL.
type Client struct {
	BaseURL   string
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// NewClient builds a client with sane timeouts. The default transport
// sends Accept-Encoding: gzip and transparently decompresses the body.
func NewClient(baseURL, token, userAgent string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
	}
}

// APIError is a non-2xx answer from the API.
type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("API ISP Tools: HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("API ISP Tools: HTTP %d", e.Status)
}

// StatusOf returns the HTTP status of an *APIError, or 0.
func StatusOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// Versao is the answer of GET /versao.
type Versao struct {
	AtualizadoEm    string            `json:"atualizado_em"`
	Versao          int64             `json:"versao"`
	Dominios        int               `json:"dominios"`
	MD5             string            `json:"md5"`
	MD5Formatos     map[string]string `json:"md5_formatos"`
	ExcecoesCliente bool              `json:"excecoes_cliente"`
}

// MD5Texto returns the md5 of the "texto" format for this token.
func (v Versao) MD5Texto() string {
	return v.MD5Formatos["texto"]
}

func (c *Client) do(ctx context.Context, path string, auth bool, limit int64) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("API ISP Tools: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("API ISP Tools: lendo resposta: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, nil, fmt.Errorf("API ISP Tools: resposta maior que %d bytes", limit)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode}
		var payload struct {
			Erro string `json:"erro"`
		}
		if json.Unmarshal(body, &payload) == nil {
			apiErr.Message = payload.Erro
		}
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			apiErr.RetryAfter = time.Duration(secs) * time.Second
		}
		return nil, nil, apiErr
	}
	return body, resp.Header, nil
}

// Versao fetches the list state for this token.
func (c *Client) Versao(ctx context.Context) (Versao, error) {
	var v Versao
	body, _, err := c.do(ctx, "/versao", true, 1<<20)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return v, fmt.Errorf("API ISP Tools: /versao invalido: %w", err)
	}
	if !md5RE.MatchString(v.MD5Texto()) {
		return v, errors.New("API ISP Tools: /versao sem md5 valido do formato texto")
	}
	return v, nil
}

// Lista downloads the list in the "texto" format and verifies its md5
// against the X-Blocklist-MD5 header (when present) and expectedMD5.
func (c *Client) Lista(ctx context.Context, expectedMD5 string) ([]byte, error) {
	body, hdr, err := c.do(ctx, "/lista?formato=texto", true, maxListSize)
	if err != nil {
		return nil, err
	}
	sum := md5.Sum(body)
	got := hex.EncodeToString(sum[:])
	if h := strings.ToLower(strings.TrimSpace(hdr.Get("X-Blocklist-MD5"))); h != "" && h != got {
		return nil, fmt.Errorf("md5 do download (%s) nao confere com X-Blocklist-MD5 (%s)", got, h)
	}
	if expectedMD5 != "" && got != expectedMD5 {
		return nil, fmt.Errorf("md5 do download (%s) nao confere com o esperado (%s)", got, expectedMD5)
	}
	return body, nil
}

// PublicIP returns the source IP as seen by the API (no token needed).
func (c *Client) PublicIP(ctx context.Context) (string, error) {
	body, _, err := c.do(ctx, "/ip", false, 256)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}
