// Package staticentry reads and writes the unbound static local-zone file
// used to pin a domain to fixed IPv4/IPv6 addresses instead of resolving it
// normally, e.g. to keep a domain working even if its real DNS records
// change or become unreachable.
package staticentry

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Entry is a single domain pinned to static addresses. IPv4 and/or IPv6 may
// be empty, but at least one must be set.
type Entry struct {
	Domain string `json:"domain"`
	IPv4   string `json:"ipv4"`
	IPv6   string `json:"ipv6"`
}

var (
	zoneRE = regexp.MustCompile(`^local-zone:\s*"([^"]+?)\.?"\s+static\s*$`)
	dataRE = regexp.MustCompile(`^local-data:\s*"[^"]+?\.?\s+IN\s+(A|AAAA)\s+([^"]+)"\s*$`)
)

// ValidIPv4 reports whether s is a syntactically valid IPv4 address.
func ValidIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// ValidIPv6 reports whether s is a syntactically valid IPv6 address.
func ValidIPv6(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() == nil
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
		if m := zoneRE.FindStringSubmatch(line); m != nil {
			if current != nil && current.Domain != "" {
				entries = append(entries, *current)
			}
			current = &Entry{Domain: m[1]}
			continue
		}
		if m := dataRE.FindStringSubmatch(line); m != nil && current != nil {
			switch m[1] {
			case "A":
				current.IPv4 = m[2]
			case "AAAA":
				current.IPv6 = m[2]
			}
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

// Save writes entries to path in unbound.conf static local-zone format,
// sorted by domain.
func Save(path string, entries []Entry) error {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Domain < sorted[j].Domain })

	var b strings.Builder
	for _, e := range sorted {
		fqdn := e.Domain + "."
		fmt.Fprintf(&b, "local-zone: %q static\n", fqdn)
		if e.IPv4 != "" {
			fmt.Fprintf(&b, "local-data: %q\n", fqdn+" IN A "+e.IPv4)
		}
		if e.IPv6 != "" {
			fmt.Fprintf(&b, "local-data: %q\n", fqdn+" IN AAAA "+e.IPv6)
		}
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
