// Package blocklist reads and writes the unbound local-zone blocklist file
// used to block domains via "always_nxdomain", along with metadata about
// the origin and date of each block recorded as a trailing comment.
package blocklist

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Entry is a single blocked domain and its metadata.
type Entry struct {
	Domain string `json:"domain"`
	Origem string `json:"origem"`
	Fonte  string `json:"fonte"`
	Data   string `json:"data"`
}

var (
	lineRE   = regexp.MustCompile(`^\s*local-zone:\s*"([^"]+?)\.?"\s+always_nxdomain\s*(?:#\s*(.*))?\s*$`)
	kvRE     = regexp.MustCompile(`(\w+)=(\S*)`)
	domainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// ValidDomain reports whether s is a syntactically valid domain name.
func ValidDomain(s string) bool {
	return domainRE.MatchString(s)
}

// ValidMeta reports whether s is safe to embed in a trailing config comment.
func ValidMeta(s string) bool {
	return !strings.ContainsAny(s, "#\r\n")
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
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := lineRE.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		entry := Entry{Domain: m[1]}
		for _, kv := range kvRE.FindAllStringSubmatch(m[2], -1) {
			switch kv[1] {
			case "origem":
				entry.Origem = kv[2]
			case "fonte":
				entry.Fonte = kv[2]
			case "data":
				entry.Data = kv[2]
			}
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Domain < entries[j].Domain })
	return entries, nil
}

// Save writes entries to path in unbound.conf format, sorted by domain.
func Save(path string, entries []Entry) error {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Domain < sorted[j].Domain })

	var b strings.Builder
	b.WriteString("server:\n")
	for _, e := range sorted {
		fmt.Fprintf(&b, "    local-zone: %q always_nxdomain", e.Domain+".")

		var meta []string
		if e.Origem != "" {
			meta = append(meta, "origem="+e.Origem)
		}
		if e.Fonte != "" {
			meta = append(meta, "fonte="+e.Fonte)
		}
		if e.Data != "" {
			meta = append(meta, "data="+e.Data)
		}
		if len(meta) > 0 {
			b.WriteString("  # " + strings.Join(meta, " "))
		}
		b.WriteString("\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
