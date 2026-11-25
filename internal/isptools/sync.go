package isptools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/airkingbr/unbound-dash/internal/staticentry"
	"github.com/airkingbr/unbound-dash/internal/unboundctl"
)

// MinDomains is the smallest list accepted from the API; anything smaller
// is treated as a broken/partial publication and the current list is kept.
const MinDomains = 1000

// historySize is how many sync results are kept for the dashboard.
const historySize = 30

// applyChunk is how many lines are fed to one local_zones(_remove) call.
const applyChunk = 10000

// apiDomainRE matches what the API publishes: [a-z0-9_-] labels, at least
// one dot (the guide's reference regex).
var apiDomainRE = regexp.MustCompile(`^[a-z0-9_](?:[a-z0-9_-]{0,61}[a-z0-9_])?(?:\.[a-z0-9_](?:[a-z0-9_-]{0,61}[a-z0-9_])?)+$`)

// zoneLineRE extracts the domain of an always_nxdomain local-zone line, used
// to read back our own file and the legacy anatel-blocklist.conf.
var zoneLineRE = regexp.MustCompile(`^\s*local-zone:\s*"([^"]+?)\.?"\s+always_nxdomain\b`)

// Options configures a Syncer.
type Options struct {
	Client          *Client
	Ctl             *unboundctl.Client
	CheckconfBin    string
	UnboundConf     string
	BlocklistFile   string // persistent file managed by the syncer
	LegacyFile      string // old anatel-blocklist.conf, migrated away once
	StaticEntryFile string // domains here are never touched by the syncer
	StateDir        string
	Interval        time.Duration
}

// HistoryEntry is one applied change.
type HistoryEntry struct {
	At       time.Time `json:"at"`
	Versao   int64     `json:"versao"`
	MD5      string    `json:"md5"`
	Dominios int       `json:"dominios"`
	Added    int       `json:"added"`
	Removed  int       `json:"removed"`
	Note     string    `json:"note,omitempty"`
}

type persisted struct {
	AppliedMD5     string         `json:"applied_md5"`
	AppliedVersao  *Versao        `json:"applied_versao,omitempty"`
	AppliedAt      time.Time      `json:"applied_at"`
	History        []HistoryEntry `json:"history"`
	LegacyMigrated bool           `json:"legacy_migrated"`
}

// Status is what the dashboard shows.
type Status struct {
	Configured     bool           `json:"configured"`
	State          string         `json:"state"` // nao_configurado, pendente, em_dia, desatualizado, erro
	Syncing        bool           `json:"syncing"`
	Dominios       int            `json:"dominios"`
	SkippedStatic  int            `json:"skipped_static"`
	AppliedMD5     string         `json:"applied_md5"`
	AppliedVersao  *Versao        `json:"applied_versao,omitempty"`
	AppliedAt      *time.Time     `json:"applied_at,omitempty"`
	Remote         *Versao        `json:"remote,omitempty"`
	LastCheckAt    *time.Time     `json:"last_check_at,omitempty"`
	NextCheckAt    *time.Time     `json:"next_check_at,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
	LastErrorAt    *time.Time     `json:"last_error_at,omitempty"`
	ErrorCode      int            `json:"error_code,omitempty"`
	PublicIP       string         `json:"public_ip,omitempty"`
	RetryUntil     *time.Time     `json:"retry_until,omitempty"`
	Paused         bool           `json:"paused"`
	LegacyMigrated bool           `json:"legacy_migrated"`
	History        []HistoryEntry `json:"history"`
}

// Result describes one sync run.
type Result struct {
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

// Syncer keeps the local Unbound in sync with the API.
type Syncer struct {
	opt Options

	syncMu sync.Mutex // one sync at a time

	mu            sync.RWMutex
	st            persisted
	domains       map[string]struct{}
	sorted        []string
	skippedStatic int
	remote        *Versao
	lastCheck     time.Time
	nextCheck     time.Time
	lastErr       string
	lastErrAt     time.Time
	errCode       int
	publicIP      string
	retryUntil    time.Time
	paused        bool // set on 401 until a manual sync succeeds
	syncing       bool
}

// NewSyncer loads previous state from disk.
func NewSyncer(opt Options) *Syncer {
	if opt.Interval <= 0 {
		opt.Interval = time.Hour
	}
	s := &Syncer{opt: opt, domains: map[string]struct{}{}}
	s.load()
	return s
}

func (s *Syncer) statePath() string   { return filepath.Join(s.opt.StateDir, "isptools-state.json") }
func (s *Syncer) domainsPath() string { return filepath.Join(s.opt.StateDir, "isptools-domains.txt") }

// Configured reports whether a token is set.
func (s *Syncer) Configured() bool { return s.opt.Client != nil && s.opt.Client.Token != "" }

func (s *Syncer) load() {
	if data, err := os.ReadFile(s.statePath()); err == nil {
		if err := json.Unmarshal(data, &s.st); err != nil {
			log.Printf("isptools: estado %s invalido, ignorando: %v", s.statePath(), err)
			s.st = persisted{}
		}
	}

	// The applied set comes from our own state file; if it is missing
	// (first run, or state wiped) fall back to the persistent Unbound file
	// so stale zones loaded from it can still be removed on the next diff.
	list, err := readLines(s.domainsPath())
	if err != nil || len(list) == 0 {
		list, _ = readZoneFile(s.opt.BlocklistFile)
	}
	s.setDomains(list)
}

func (s *Syncer) setDomains(list []string) {
	m := make(map[string]struct{}, len(list))
	for _, d := range list {
		m[d] = struct{}{}
	}
	sorted := make([]string, 0, len(m))
	for d := range m {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	s.domains = m
	s.sorted = sorted
}

// Run performs a first sync shortly after start and then one every
// Interval, with a few random minutes added so installations don't all
// hit the API at the same moment.
func (s *Syncer) Run(ctx context.Context) {
	if !s.Configured() {
		log.Printf("isptools: token nao configurado (isptools_token); sincronizacao da blocklist desativada")
		return
	}
	wait := 15 * time.Second
	for {
		s.mu.Lock()
		s.nextCheck = time.Now().Add(wait)
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		if res, err := s.Sync(ctx, false); err != nil {
			if !errors.Is(err, errSkipped) {
				log.Printf("isptools: sincronizacao falhou: %v", err)
			}
		} else if res.Changed {
			log.Printf("isptools: %s", res.Message)
		}

		wait = s.opt.Interval + time.Duration(rand.Int64N(int64(5*time.Minute)))
	}
}

var errSkipped = errors.New("sincronizacao automatica adiada")

// ErrBusy is returned when a sync is already running.
var ErrBusy = errors.New("sincronizacao ja em andamento")

// Sync checks the API and applies changes. manual=true ignores the 401
// pause and the 429 back-off window is still respected.
func (s *Syncer) Sync(ctx context.Context, manual bool) (Result, error) {
	if !s.Configured() {
		return Result{}, errors.New("token da API ISP Tools nao configurado (isptools_token no config.json)")
	}
	if !s.syncMu.TryLock() {
		return Result{}, ErrBusy
	}
	defer s.syncMu.Unlock()

	s.mu.Lock()
	if wait := time.Until(s.retryUntil); wait > 0 {
		s.mu.Unlock()
		return Result{}, fmt.Errorf("limite de requisicoes da API atingido; tente novamente em %s", wait.Round(time.Minute))
	}
	if s.paused && !manual {
		s.mu.Unlock()
		return Result{}, errSkipped
	}
	s.syncing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.mu.Unlock()
	}()

	res, err := s.sync(ctx)
	s.recordOutcome(ctx, err)
	return res, err
}

func (s *Syncer) recordOutcome(ctx context.Context, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCheck = time.Now()
	if err == nil {
		s.lastErr, s.errCode, s.publicIP, s.paused = "", 0, "", false
		return
	}
	s.lastErr = err.Error()
	s.lastErrAt = time.Now()
	s.errCode = StatusOf(err)

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 401:
			s.paused = true
		case 403:
			// Show which IP must be allowed on the token.
			s.mu.Unlock()
			ipCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			ip, ipErr := s.opt.Client.PublicIP(ipCtx)
			cancel()
			s.mu.Lock()
			if ipErr == nil {
				s.publicIP = ip
			}
		case 429:
			wait := apiErr.RetryAfter
			if wait <= 0 {
				wait = time.Hour
			}
			s.retryUntil = time.Now().Add(wait)
		}
	}
}

func (s *Syncer) sync(ctx context.Context) (Result, error) {
	v, err := s.opt.Client.Versao(ctx)
	if err != nil {
		return Result{}, err
	}
	remoteMD5 := v.MD5Texto()

	s.mu.Lock()
	s.remote = &v
	applied := s.st.AppliedMD5
	s.mu.Unlock()

	if remoteMD5 == applied {
		if err := s.migrateLegacy(nil); err != nil {
			return Result{}, err
		}
		return Result{Message: "sem mudanca"}, nil
	}

	body, err := s.opt.Client.Lista(ctx, remoteMD5)
	if err != nil {
		return Result{}, fmt.Errorf("%w — lista atual mantida", err)
	}

	novos := make(map[string]struct{}, v.Dominios)
	for _, line := range strings.Split(string(body), "\n") {
		d := strings.TrimSpace(line)
		if apiDomainRE.MatchString(d) && len(d) <= 253 {
			novos[d] = struct{}{}
		}
	}
	if len(novos) < MinDomains {
		return Result{}, fmt.Errorf("lista suspeita (%d dominios validos, minimo %d) — lista atual mantida", len(novos), MinDomains)
	}

	// Domains with an operator static entry are left alone: blocking them
	// here would override (or, on removal, delete) the static local-zone.
	static := s.staticDomains()
	skipped := 0
	for d := range static {
		if _, ok := novos[d]; ok {
			delete(novos, d)
			skipped++
		}
	}

	list := make([]string, 0, len(novos))
	for d := range novos {
		list = append(list, d)
	}
	sort.Strings(list)

	if err := s.writePersistent(list, v); err != nil {
		return Result{}, err
	}

	s.mu.RLock()
	antigos := s.domains
	s.mu.RUnlock()

	var remover, adicionar []string
	for d := range antigos {
		if _, ok := novos[d]; ok {
			continue
		}
		if _, ok := static[d]; ok {
			continue
		}
		remover = append(remover, d)
	}
	for _, d := range list {
		if _, ok := antigos[d]; !ok {
			adicionar = append(adicionar, d)
		}
	}
	sort.Strings(remover)

	if err := s.applyRuntime("local_zones_remove", remover, ""); err != nil {
		return Result{}, err
	}
	if err := s.applyRuntime("local_zones", adicionar, " always_nxdomain"); err != nil {
		return Result{}, err
	}

	note := ""
	if err := s.migrateLegacy(novos); err != nil {
		note = "migracao do anatel-blocklist.conf falhou: " + err.Error()
		log.Printf("isptools: %s", note)
	} else if s.st.LegacyMigrated && len(s.st.History) == 0 {
		note = "anatel-blocklist.conf desativado (substituido pela API)"
	}

	if err := writeLines(s.domainsPath(), list); err != nil {
		return Result{}, fmt.Errorf("lista aplicada, mas falhou ao gravar estado: %w", err)
	}

	now := time.Now()
	s.mu.Lock()
	s.setDomains(list)
	s.skippedStatic = skipped
	s.st.AppliedMD5 = remoteMD5
	s.st.AppliedVersao = &v
	s.st.AppliedAt = now
	s.st.History = append([]HistoryEntry{{
		At: now, Versao: v.Versao, MD5: remoteMD5, Dominios: len(list),
		Added: len(adicionar), Removed: len(remover), Note: note,
	}}, s.st.History...)
	if len(s.st.History) > historySize {
		s.st.History = s.st.History[:historySize]
	}
	stErr := s.saveStateLocked()
	s.mu.Unlock()
	if stErr != nil {
		return Result{}, fmt.Errorf("lista aplicada, mas falhou ao gravar estado: %w", stErr)
	}

	return Result{
		Changed: true,
		Message: fmt.Sprintf("aplicada: %d dominios (+%d / -%d)", len(list), len(adicionar), len(remover)),
	}, nil
}

// applyRuntime feeds names to a bulk unbound-control command in chunks.
func (s *Syncer) applyRuntime(cmd string, names []string, suffix string) error {
	for i := 0; i < len(names); i += applyChunk {
		end := min(i+applyChunk, len(names))
		var b strings.Builder
		for _, d := range names[i:end] {
			b.WriteString(d)
			b.WriteString(suffix)
			b.WriteByte('\n')
		}
		if _, err := s.opt.Ctl.RunStdin(b.String(), cmd); err != nil {
			return fmt.Errorf("aplicando no Unbound (%s): %w", cmd, err)
		}
	}
	return nil
}

// writePersistent writes the Unbound file (so the list survives a
// restart), makes sure unbound.conf includes it and validates the whole
// config with unbound-checkconf, rolling everything back on failure.
func (s *Syncer) writePersistent(list []string, v Versao) error {
	var b strings.Builder
	b.Grow(len(list) * 48)
	fmt.Fprintf(&b, "# ISP Tools — Blocklist DNS (%d dominios, versao %d, publicada em %s)\n", len(list), v.Versao, v.AtualizadoEm)
	b.WriteString("# Gerado pelo unbound-dash. Nao edite: sera substituido na proxima sincronizacao.\n")
	b.WriteString("server:\n")
	for _, d := range list {
		fmt.Fprintf(&b, "local-zone: \"%s.\" always_nxdomain\n", d)
	}

	previous, prevErr := os.ReadFile(s.opt.BlocklistFile)
	if err := writeFileAtomic(s.opt.BlocklistFile, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("gravando %s: %w", s.opt.BlocklistFile, err)
	}
	restoreFile := func() {
		if prevErr == nil {
			_ = writeFileAtomic(s.opt.BlocklistFile, previous, 0o644)
		} else {
			_ = os.Remove(s.opt.BlocklistFile)
		}
	}

	restoreConf, err := s.ensureInclude()
	if err != nil {
		restoreFile()
		return err
	}

	if err := s.opt.Ctl.Checkconf(s.opt.CheckconfBin); err != nil {
		restoreConf()
		restoreFile()
		return fmt.Errorf("%w — mudanca desfeita, lista atual mantida", err)
	}
	return nil
}

// ensureInclude appends an include for BlocklistFile to unbound.conf if
// none exists. It returns a function that undoes the change.
func (s *Syncer) ensureInclude() (func(), error) {
	noop := func() {}
	conf, err := os.ReadFile(s.opt.UnboundConf)
	if err != nil {
		return noop, fmt.Errorf("lendo %s: %w", s.opt.UnboundConf, err)
	}
	re := regexp.MustCompile(`(?m)^\s*include:\s*"?[^"\n]*` + regexp.QuoteMeta(filepath.Base(s.opt.BlocklistFile)))
	if re.Match(conf) {
		return noop, nil
	}
	backup := fmt.Sprintf("%s.bak.%d", s.opt.UnboundConf, time.Now().Unix())
	if err := os.WriteFile(backup, conf, 0o644); err != nil {
		return noop, fmt.Errorf("backup de %s: %w", s.opt.UnboundConf, err)
	}
	updated := append([]byte{}, conf...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, fmt.Sprintf("\n# Blocklist DNS ISP Tools (gerenciado pelo unbound-dash)\ninclude: \"%s\"\n", s.opt.BlocklistFile)...)
	if err := writeFileAtomic(s.opt.UnboundConf, updated, 0o644); err != nil {
		return noop, fmt.Errorf("gravando %s: %w", s.opt.UnboundConf, err)
	}
	log.Printf("isptools: include de %s adicionado em %s (backup em %s)", s.opt.BlocklistFile, s.opt.UnboundConf, backup)
	return func() { _ = writeFileAtomic(s.opt.UnboundConf, conf, 0o644) }, nil
}

// migrateLegacy disables the old anatel-blocklist.conf once the API list
// is in place: comments out its include, renames the file and removes
// from the running Unbound the zones that the API list doesn't cover.
// novos is the freshly applied set (nil means "use the current one").
func (s *Syncer) migrateLegacy(novos map[string]struct{}) error {
	s.mu.RLock()
	done := s.st.LegacyMigrated
	if novos == nil {
		novos = s.domains
	}
	s.mu.RUnlock()
	if done {
		return nil
	}
	if len(novos) < MinDomains {
		return nil // API list not applied yet; keep the old one active
	}

	markDone := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.st.LegacyMigrated = true
		return s.saveStateLocked()
	}

	if s.opt.LegacyFile == "" {
		return markDone()
	}
	legacy, err := readZoneFile(s.opt.LegacyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return markDone()
		}
		return err
	}

	conf, err := os.ReadFile(s.opt.UnboundConf)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`(?m)^([ \t]*include:[ \t]*"?[^"\n]*` + regexp.QuoteMeta(filepath.Base(s.opt.LegacyFile)) + `"?[ \t]*)$`)
	stamp := time.Now().Format("2006-01-02")
	updated := re.ReplaceAll(conf, []byte("# [unbound-dash "+stamp+"] substituido pela API ISP Tools: $1"))
	renamed := fmt.Sprintf("%s.migrado-%d", s.opt.LegacyFile, time.Now().Unix())

	if !bytesEqual(conf, updated) {
		backup := fmt.Sprintf("%s.bak.%d", s.opt.UnboundConf, time.Now().Unix())
		if err := os.WriteFile(backup, conf, 0o644); err != nil {
			return err
		}
		if err := writeFileAtomic(s.opt.UnboundConf, updated, 0o644); err != nil {
			return err
		}
	}
	if err := os.Rename(s.opt.LegacyFile, renamed); err != nil {
		_ = writeFileAtomic(s.opt.UnboundConf, conf, 0o644)
		return err
	}
	if err := s.opt.Ctl.Checkconf(s.opt.CheckconfBin); err != nil {
		_ = os.Rename(renamed, s.opt.LegacyFile)
		_ = writeFileAtomic(s.opt.UnboundConf, conf, 0o644)
		return err
	}

	static := s.staticDomains()
	var remover []string
	for _, d := range legacy {
		if _, ok := novos[d]; ok {
			continue
		}
		if _, ok := static[d]; ok {
			continue
		}
		remover = append(remover, d)
	}
	if err := s.applyRuntime("local_zones_remove", remover, ""); err != nil {
		return err
	}
	log.Printf("isptools: anatel-blocklist.conf desativado (%d dominios antigos, %d removidos do Unbound; arquivo em %s)", len(legacy), len(remover), renamed)
	return markDone()
}

func (s *Syncer) staticDomains() map[string]struct{} {
	out := map[string]struct{}{}
	if s.opt.StaticEntryFile == "" {
		return out
	}
	entries, err := staticentry.Load(s.opt.StaticEntryFile)
	if err != nil {
		return out
	}
	for _, e := range entries {
		out[e.Domain] = struct{}{}
	}
	return out
}

func (s *Syncer) saveStateLocked() error {
	if err := os.MkdirAll(s.opt.StateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.statePath(), data, 0o644)
}

// Status returns a snapshot for the dashboard.
func (s *Syncer) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := Status{
		Configured:     s.Configured(),
		Syncing:        s.syncing,
		Dominios:       len(s.sorted),
		SkippedStatic:  s.skippedStatic,
		AppliedMD5:     s.st.AppliedMD5,
		AppliedVersao:  s.st.AppliedVersao,
		Remote:         s.remote,
		LastError:      s.lastErr,
		ErrorCode:      s.errCode,
		PublicIP:       s.publicIP,
		Paused:         s.paused,
		LegacyMigrated: s.st.LegacyMigrated,
		History:        s.st.History,
	}
	if st.History == nil {
		st.History = []HistoryEntry{}
	}
	tp := func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	}
	st.AppliedAt = tp(s.st.AppliedAt)
	st.LastCheckAt = tp(s.lastCheck)
	st.LastErrorAt = tp(s.lastErrAt)
	if !s.paused {
		st.NextCheckAt = tp(s.nextCheck)
	}
	if time.Now().Before(s.retryUntil) {
		st.RetryUntil = tp(s.retryUntil)
	}

	switch {
	case !st.Configured:
		st.State = "nao_configurado"
	case s.lastErr != "":
		st.State = "erro"
	case s.st.AppliedMD5 == "":
		st.State = "pendente"
	case s.remote != nil && s.remote.MD5Texto() != s.st.AppliedMD5:
		st.State = "desatualizado"
	default:
		st.State = "em_dia"
	}
	return st
}

// Lookup returns the blocked zone covering name (the name itself or its
// closest-to-root blocked parent), or "" if not blocked.
func (s *Syncer) Lookup(name string) string {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	labels := strings.Split(name, ".")
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(labels) - 1; i >= 0; i-- {
		candidate := strings.Join(labels[i:], ".")
		if _, ok := s.domains[candidate]; ok {
			return candidate
		}
	}
	return ""
}

// Search returns up to limit applied domains containing q, plus the total
// number of matches.
func (s *Syncer) Search(q string, limit int) ([]string, int) {
	q = strings.ToLower(strings.TrimSpace(q))
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []string{}
	total := 0
	for _, d := range s.sorted {
		if q == "" || strings.Contains(d, q) {
			total++
			if len(out) < limit {
				out = append(out, d)
			}
		}
	}
	return out, total
}

// Sample returns a random applied domain ("" if none).
func (s *Syncer) Sample() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sorted) == 0 {
		return ""
	}
	return s.sorted[rand.IntN(len(s.sorted))]
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if d := strings.TrimSpace(sc.Text()); d != "" {
			out = append(out, d)
		}
	}
	return out, sc.Err()
}

func readZoneFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := zoneLineRE.FindStringSubmatch(sc.Text()); m != nil {
			out = append(out, strings.ToLower(m[1]))
		}
	}
	return out, sc.Err()
}

func writeLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return writeFileAtomic(path, []byte(b.String()), 0o644)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }
