// Package benchmark implements simple performance tests against a local
// Unbound recursive resolver: cache hit/miss latency, batch lookups, load
// (QPS) tests and the resolver's own response-time histogram.
package benchmark

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/airkingbr/unbound-dash/internal/dnstools"
	"github.com/airkingbr/unbound-dash/internal/unboundctl"
)

// DefaultDomains is a small set of popular domains (global and Brazilian)
// used as a default sample set for batch and comparison tests.
var DefaultDomains = []string{
	"google.com",
	"youtube.com",
	"facebook.com",
	"instagram.com",
	"wikipedia.org",
	"globo.com",
	"uol.com.br",
	"mercadolivre.com.br",
	"cloudflare.com",
	"microsoft.com",
	"apple.com",
	"netflix.com",
	"amazon.com",
	"x.com",
	"whatsapp.com",
}

// Stats summarizes a set of latency measurements (in milliseconds).
type Stats struct {
	Count  int     `json:"count"`
	Errors int     `json:"errors"`
	MinMs  float64 `json:"min_ms"`
	MaxMs  float64 `json:"max_ms"`
	AvgMs  float64 `json:"avg_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
}

func computeStats(samples []float64, errors int) Stats {
	st := Stats{Count: len(samples) + errors, Errors: errors}
	if len(samples) == 0 {
		return st
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}
	st.MinMs = sorted[0]
	st.MaxMs = sorted[len(sorted)-1]
	st.AvgMs = sum / float64(len(sorted))
	st.P50Ms = percentile(sorted, 50)
	st.P95Ms = percentile(sorted, 95)
	return st
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := int(p/100*float64(len(sorted)-1) + 0.5)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// CacheResult compares the latency of a query before and after the entry is
// cached.
type CacheResult struct {
	Domain string  `json:"domain"`
	ColdMs float64 `json:"cold_ms"`
	WarmMs float64 `json:"warm_ms"`
	Error  string  `json:"error,omitempty"`
}

// CacheTest measures, for each domain, the latency of a fresh recursive
// resolution (after flushing it from the cache) versus the latency of an
// immediate repeat query served from cache.
func CacheTest(client *unboundctl.Client, server string, domains []string) []CacheResult {
	results := make([]CacheResult, 0, len(domains))
	for _, domain := range domains {
		if client != nil {
			_, _ = unboundctl.RunControl(client, "flush_zone", []string{domain})
		}

		cold, err := dnstools.Query(server, domain, "A")
		if err != nil || cold.Status == "ERROR" {
			msg := ""
			if err != nil {
				msg = err.Error()
			} else {
				msg = cold.Error
			}
			results = append(results, CacheResult{Domain: domain, Error: msg})
			continue
		}

		warm, err := dnstools.Query(server, domain, "A")
		if err != nil || warm.Status == "ERROR" {
			msg := ""
			if err != nil {
				msg = err.Error()
			} else {
				msg = warm.Error
			}
			results = append(results, CacheResult{Domain: domain, ColdMs: cold.DurationMs, Error: msg})
			continue
		}

		results = append(results, CacheResult{Domain: domain, ColdMs: cold.DurationMs, WarmMs: warm.DurationMs})
	}
	return results
}

// BatchResult is the outcome of a single lookup performed during a batch
// test.
type BatchResult struct {
	Domain string  `json:"domain"`
	Ms     float64 `json:"ms"`
	Status string  `json:"status"`
	Error  string  `json:"error,omitempty"`
}

// BatchTest queries each domain once against server and returns per-domain
// results plus aggregate statistics.
func BatchTest(server string, domains []string) ([]BatchResult, Stats) {
	results := make([]BatchResult, 0, len(domains))
	samples := make([]float64, 0, len(domains))
	errors := 0
	for _, domain := range domains {
		res, err := dnstools.Query(server, domain, "A")
		if err != nil || res.Status == "ERROR" {
			msg := ""
			if err != nil {
				msg = err.Error()
			} else {
				msg = res.Error
			}
			results = append(results, BatchResult{Domain: domain, Status: "ERROR", Error: msg})
			errors++
			continue
		}
		results = append(results, BatchResult{Domain: domain, Ms: res.DurationMs, Status: res.Status})
		samples = append(samples, res.DurationMs)
	}
	return results, computeStats(samples, errors)
}

// LoadResult is the outcome of a concurrent QPS test against a single
// domain.
type LoadResult struct {
	Domain      string  `json:"domain"`
	Concurrency int     `json:"concurrency"`
	Total       int     `json:"total"`
	DurationMs  float64 `json:"duration_ms"`
	QPS         float64 `json:"qps"`
	Stats       Stats   `json:"stats"`
}

// LoadTest fires `total` queries for domain at server using `concurrency`
// concurrent workers and reports throughput and latency statistics. Once
// the entry is cached the test mostly measures how fast the resolver and
// the host can answer from cache.
func LoadTest(server, domain string, concurrency, total int) LoadResult {
	if concurrency < 1 {
		concurrency = 1
	}
	if total < 1 {
		total = 1
	}

	jobs := make(chan struct{}, total)
	for i := 0; i < total; i++ {
		jobs <- struct{}{}
	}
	close(jobs)

	var (
		mu      sync.Mutex
		samples = make([]float64, 0, total)
		errors  int
	)

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				res, err := dnstools.Query(server, domain, "A")
				mu.Lock()
				if err != nil || res.Status == "ERROR" {
					errors++
				} else {
					samples = append(samples, res.DurationMs)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	return LoadResult{
		Domain:      domain,
		Concurrency: concurrency,
		Total:       total,
		DurationMs:  float64(elapsed) / float64(time.Millisecond),
		QPS:         float64(total) / elapsed.Seconds(),
		Stats:       computeStats(samples, errors),
	}
}

// HistogramBucket is one bucket of Unbound's response-time histogram.
type HistogramBucket struct {
	Label    string  `json:"label"`
	LowerSec float64 `json:"lower_sec"`
	UpperSec float64 `json:"upper_sec"`
	Count    int     `json:"count"`
}

var histogramKeyRE = regexp.MustCompile(`^histogram\.(\d+)\.(\d+)\.to\.(\d+)\.(\d+)$`)

// Histogram reads Unbound's internal response-time histogram via
// unbound-control stats_noreset.
func Histogram(client *unboundctl.Client) ([]HistogramBucket, error) {
	raw, err := client.Stats()
	if err != nil {
		return nil, err
	}

	buckets := make([]HistogramBucket, 0)
	for k, v := range raw {
		m := histogramKeyRE.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		count, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || count == 0 {
			continue
		}
		lower := secFromParts(m[1], m[2])
		upper := secFromParts(m[3], m[4])
		buckets = append(buckets, HistogramBucket{
			Label:    fmt.Sprintf("%s - %s", formatSec(lower), formatSec(upper)),
			LowerSec: lower,
			UpperSec: upper,
			Count:    count,
		})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].LowerSec < buckets[j].LowerSec })
	return buckets, nil
}

func secFromParts(secStr, usecStr string) float64 {
	sec, _ := strconv.Atoi(secStr)
	usec, _ := strconv.Atoi(usecStr)
	return float64(sec) + float64(usec)/1e6
}

func formatSec(sec float64) string {
	switch {
	case sec == 0:
		return "0ms"
	case sec < 0.001:
		return fmt.Sprintf("%.0fus", sec*1e6)
	case sec < 1:
		return fmt.Sprintf("%.0fms", sec*1000)
	default:
		return fmt.Sprintf("%.3gs", sec)
	}
}

// CompareResult is the aggregate result of a comparison test against a
// single resolver.
type CompareResult struct {
	Server string `json:"server"`
	Stats  Stats  `json:"stats"`
}

// CompareResolvers runs BatchTest for the same domain list against several
// resolvers and returns aggregate stats per server.
func CompareResolvers(domains []string, servers map[string]string) []CompareResult {
	type item struct {
		name   string
		server string
	}
	items := make([]item, 0, len(servers))
	for name, server := range servers {
		items = append(items, item{name: name, server: server})
	}

	results := make([]CompareResult, len(items))
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		go func(i int, it item) {
			defer wg.Done()
			_, stats := BatchTest(it.server, domains)
			results[i] = CompareResult{Server: it.name, Stats: stats}
		}(i, it)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Server < results[j].Server })
	return results
}
