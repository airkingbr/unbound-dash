// Package querylog tails the Unbound query log (log-queries: yes) to
// provide aggregated top-domains/top-clients statistics and a recent-lines
// view of the log file.
package querylog

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// lineRE matches Unbound query log lines, e.g.:
// "Jun 10 14:26:34 unbound[1057638:1] info: 170.78.229.240 example.com. A IN"
var lineRE = regexp.MustCompile(`^\S+\s+\d+\s+\d+:\d+:\d+\s+unbound\[\d+:\d+\]\s+info:\s+(\S+)\s+(\S+)\.\s+(\S+)\s+(\S+)\s*$`)

const (
	maxTrackedKeys = 50000
	resetInterval  = time.Hour
)

// Count is a single key/count pair used for top-N results.
type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// Tailer follows the Unbound query log, aggregating counts of queried
// domain names and client addresses since the last reset.
type Tailer struct {
	path string

	mu      sync.Mutex
	domains map[string]int
	clients map[string]int
	since   time.Time
}

// NewTailer creates a Tailer for the log file at path.
func NewTailer(path string) *Tailer {
	return &Tailer{
		path:    path,
		domains: make(map[string]int),
		clients: make(map[string]int),
		since:   time.Now(),
	}
}

// Run follows the log file and aggregates query lines until the process
// exits. It is intended to be started in its own goroutine.
func (t *Tailer) Run() {
	go t.resetLoop()
	for {
		if err := t.follow(); err != nil {
			time.Sleep(5 * time.Second)
		}
	}
}

func (t *Tailer) resetLoop() {
	ticker := time.NewTicker(resetInterval)
	defer ticker.Stop()
	for range ticker.C {
		t.mu.Lock()
		t.domains = make(map[string]int)
		t.clients = make(map[string]int)
		t.since = time.Now()
		t.mu.Unlock()
	}
}

// follow opens the log file, seeks to its end, and reads new lines as they
// are appended. It returns nil (so the caller reopens the file) when the
// file is truncated or rotated.
func (t *Tailer) follow() error {
	f, err := os.Open(t.path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err == nil {
			t.processLine(line)
			continue
		}

		time.Sleep(500 * time.Millisecond)

		pos, _ := f.Seek(0, io.SeekCurrent)
		info, statErr := os.Stat(t.path)
		if statErr != nil {
			return statErr
		}
		if info.Size() < pos {
			return nil // truncated (e.g. logrotate copytruncate); reopen
		}
		if cur, statErr := f.Stat(); statErr == nil && !os.SameFile(cur, info) {
			return nil // rotated to a new file; reopen
		}
	}
}

func (t *Tailer) processLine(line string) {
	m := lineRE.FindStringSubmatch(line)
	if m == nil {
		return
	}
	client, qname := m[1], strings.ToLower(m[2])

	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.domains[qname]; ok || len(t.domains) < maxTrackedKeys {
		t.domains[qname]++
	}
	if _, ok := t.clients[client]; ok || len(t.clients) < maxTrackedKeys {
		t.clients[client]++
	}
}

// TopDomains returns the n most-queried domain names and the time the
// counters started accumulating.
func (t *Tailer) TopDomains(n int) ([]Count, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return topN(t.domains, n), t.since
}

// TopClients returns the n client addresses with the most queries and the
// time the counters started accumulating.
func (t *Tailer) TopClients(n int) ([]Count, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return topN(t.clients, n), t.since
}

func topN(m map[string]int, n int) []Count {
	counts := make([]Count, 0, len(m))
	for k, v := range m {
		counts = append(counts, Count{Key: k, Count: v})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count != counts[j].Count {
			return counts[i].Count > counts[j].Count
		}
		return counts[i].Key < counts[j].Key
	})
	if len(counts) > n {
		counts = counts[:n]
	}
	return counts
}

// Tail returns up to maxLines of the most recent lines from the file at
// path.
func Tail(path string, maxLines int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	const chunkSize = 64 * 1024
	var (
		offset = info.Size()
		lines  []string
		buf    []byte
	)

	for offset > 0 && len(lines) <= maxLines {
		readSize := int64(chunkSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize
		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil {
			return nil, err
		}
		buf = append(chunk, buf...)
		lines = strings.Split(string(buf), "\n")
	}

	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}
