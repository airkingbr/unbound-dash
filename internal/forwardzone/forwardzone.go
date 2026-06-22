// Package forwardzone reads and writes the unbound forward-zone file used
// to send resolution of specific domains to other recursive resolvers
// instead of the root, e.g. when a domain has problems resolving via the
// usual iterative path.
package forwardzone

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

// Entry is a single forwarded zone and the resolvers it forwards to.
type Entry struct {
	Domain       string   `json:"domain"`
	ForwardAddrs []string `json:"forward_addrs"`
}

// ValidAddr reports whether s is a syntactically valid IPv4 or IPv6 address.
func ValidAddr(s string) bool {
	return net.ParseIP(s) != nil
}

// Load reads and parses entries from path, sorted by domain. If the file
// does not exist, it returns an empty slice.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	var current *Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "forward-zone:":
			if current != nil && current.Domain != "" {
				entries = append(entries, *current)
			}
			current = &Entry{}
		case strings.HasPrefix(line, "name:"):
			if current == nil {
				continue
			}
			name := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			current.Domain = strings.Trim(name, `".`)
		case strings.HasPrefix(line, "forward-addr:"):
			if current == nil {
				continue
			}
			addr := strings.TrimSpace(strings.TrimPrefix(line, "forward-addr:"))
			current.ForwardAddrs = append(current.ForwardAddrs, addr)
		}
	}
	if current != nil && current.Domain != "" {
		entries = append(entries, *current)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Domain < entries[j].Domain })
	return entries, nil
}

// Save writes entries to path in unbound.conf forward-zone format, sorted
// by domain.
func Save(path string, entries []Entry) error {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Domain < sorted[j].Domain })

	var b strings.Builder
	for _, e := range sorted {
		b.WriteString("forward-zone:\n")
		fmt.Fprintf(&b, "    name: %q\n", e.Domain+".")
		for _, addr := range e.ForwardAddrs {
			fmt.Fprintf(&b, "    forward-addr: %s\n", addr)
		}
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
