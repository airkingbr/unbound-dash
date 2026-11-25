// Package domain holds domain-name validation shared by the config
// editors (forward zones, static entries) and the DNS tools.
package domain

import "regexp"

var domainRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// Valid reports whether s is a syntactically valid, lowercase domain name
// with at least two labels.
func Valid(s string) bool {
	return len(s) <= 253 && domainRE.MatchString(s)
}
