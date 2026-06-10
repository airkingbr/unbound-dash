// Package dnstools provides dig-like DNS query helpers (lookup, reverse
// lookup, iterative trace and side-by-side comparisons) used by the "Testes
// DNS" tab of unbound-dash.
package dnstools

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// DefaultServer is the local Unbound resolver used for normal lookups.
const DefaultServer = "127.0.0.1:53"

// queryTimeout bounds how long a single DNS query may take.
const queryTimeout = 5 * time.Second

// rootServers lists the IPv4 addresses of the 13 root name servers, used as
// the starting point for Trace.
var rootServers = []string{
	"198.41.0.4",     // a
	"199.9.14.201",   // b
	"192.33.4.12",    // c
	"199.7.91.13",    // d
	"192.203.230.10", // e
	"192.5.5.241",    // f
	"192.112.36.4",   // g
	"198.97.190.53",  // h
	"192.36.148.17",  // i
	"192.58.128.30",  // j
	"193.0.14.129",   // k
	"199.7.83.42",    // l
	"202.12.27.33",   // m
}

// Result is the outcome of a single DNS query.
type Result struct {
	Server      string   `json:"server"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	DurationMs  float64  `json:"duration_ms"`
	Truncated   bool     `json:"truncated"`
	Authentic   bool     `json:"authentic"` // AD flag (DNSSEC)
	Answers     []string `json:"answers"`
	Authorities []string `json:"authorities,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// normalizeName ensures name is fully qualified (ends with a dot).
func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

// normalizeServer appends the default DNS port if addr does not already
// specify one.
func normalizeServer(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return DefaultServer
	}
	if _, _, err := splitHostPort(addr); err == nil {
		return addr
	}
	return addr + ":53"
}

func splitHostPort(addr string) (string, string, error) {
	return net.SplitHostPort(addr)
}

// ParseType resolves a DNS record type name (e.g. "A", "mx") to its numeric
// value. It defaults to A if typ is empty.
func ParseType(typ string) (uint16, error) {
	typ = strings.ToUpper(strings.TrimSpace(typ))
	if typ == "" {
		typ = "A"
	}
	t, ok := dns.StringToType[typ]
	if !ok {
		return 0, fmt.Errorf("tipo de registro desconhecido: %s", typ)
	}
	return t, nil
}

// Query sends a single recursive DNS query to server and returns the parsed
// result.
func Query(server, name, typ string) (*Result, error) {
	qtype, err := ParseType(typ)
	if err != nil {
		return nil, err
	}
	server = normalizeServer(server)
	fqdn := normalizeName(name)

	m := new(dns.Msg)
	m.SetQuestion(fqdn, qtype)
	m.RecursionDesired = true
	m.SetEdns0(4096, true) // request DNSSEC OK so the AD flag is meaningful

	c := &dns.Client{Timeout: queryTimeout}
	resp, rtt, err := c.Exchange(m, server)

	res := &Result{
		Server: server,
		Name:   fqdn,
		Type:   strings.ToUpper(typ),
	}
	if typ == "" {
		res.Type = "A"
	}
	if err != nil {
		res.Status = "ERROR"
		res.Error = err.Error()
		return res, nil
	}

	res.Status = dns.RcodeToString[resp.Rcode]
	res.DurationMs = float64(rtt) / float64(time.Millisecond)
	res.Truncated = resp.Truncated
	res.Authentic = resp.AuthenticatedData
	for _, rr := range resp.Answer {
		res.Answers = append(res.Answers, rr.String())
	}
	for _, rr := range resp.Ns {
		res.Authorities = append(res.Authorities, rr.String())
	}
	return res, nil
}

// Reverse performs a PTR lookup for ip against server.
func Reverse(server, ip string) (*Result, error) {
	arpa, err := dns.ReverseAddr(strings.TrimSpace(ip))
	if err != nil {
		return nil, fmt.Errorf("endereco IP invalido: %w", err)
	}
	return Query(server, arpa, "PTR")
}

// Compare runs the same query against multiple servers concurrently and
// returns one Result per server, in the same order as servers.
func Compare(name, typ string, servers []string) []*Result {
	results := make([]*Result, len(servers))
	done := make(chan struct{}, len(servers))
	for i, srv := range servers {
		go func(i int, srv string) {
			defer func() { done <- struct{}{} }()
			res, err := Query(srv, name, typ)
			if err != nil {
				res = &Result{Server: normalizeServer(srv), Name: normalizeName(name), Type: strings.ToUpper(typ), Status: "ERROR", Error: err.Error()}
			}
			results[i] = res
		}(i, srv)
	}
	for range servers {
		<-done
	}
	return results
}

// TraceStep is a single step of an iterative resolution.
type TraceStep struct {
	Zone       string   `json:"zone"`
	Server     string   `json:"server"`
	ServerName string   `json:"server_name"`
	Status     string   `json:"status"`
	DurationMs float64  `json:"duration_ms"`
	Records    []string `json:"records"`
	Note       string   `json:"note,omitempty"`
}

// TraceResult is the full path of an iterative resolution from the root.
type TraceResult struct {
	Name  string      `json:"name"`
	Type  string      `json:"type"`
	Steps []TraceStep `json:"steps"`
	Error string      `json:"error,omitempty"`
}

// Trace performs an iterative resolution of name/typ starting from the root
// servers, following NS referrals (similar to "dig +trace").
func Trace(name, typ string) (*TraceResult, error) {
	qtype, err := ParseType(typ)
	if err != nil {
		return nil, err
	}
	fqdn := normalizeName(name)
	tr := &TraceResult{Name: fqdn, Type: strings.ToUpper(typ)}
	if tr.Type == "" {
		tr.Type = "A"
	}

	servers := append([]string(nil), rootServers...)
	zone := "."
	c := &dns.Client{Timeout: queryTimeout}

	const maxHops = 15
	for hop := 0; hop < maxHops; hop++ {
		if len(servers) == 0 {
			tr.Error = "nenhum servidor disponivel para continuar a delegacao"
			return tr, nil
		}
		server := servers[0] + ":53"

		m := new(dns.Msg)
		m.SetQuestion(fqdn, qtype)
		m.RecursionDesired = false

		resp, rtt, err := c.Exchange(m, server)
		step := TraceStep{Zone: zone, Server: server}
		if err != nil {
			step.Status = "ERROR"
			step.Note = err.Error()
			tr.Steps = append(tr.Steps, step)
			// try the next server at this delegation level
			servers = servers[1:]
			hop--
			continue
		}
		step.Status = dns.RcodeToString[resp.Rcode]
		step.DurationMs = float64(rtt) / float64(time.Millisecond)

		if len(resp.Answer) > 0 {
			for _, rr := range resp.Answer {
				step.Records = append(step.Records, rr.String())
			}
			tr.Steps = append(tr.Steps, step)
			if isCNAME(resp.Answer) && !hasType(resp.Answer, qtype) {
				target := cnameTarget(resp.Answer)
				if target != "" && target != fqdn {
					fqdn = target
					servers = append([]string(nil), rootServers...)
					zone = "."
					continue
				}
			}
			return tr, nil
		}

		// No answer: look for a delegation (NS records) in the authority section.
		var nextZone string
		var nsNames []string
		for _, rr := range resp.Ns {
			step.Records = append(step.Records, rr.String())
			if ns, ok := rr.(*dns.NS); ok {
				nextZone = ns.Header().Name
				nsNames = append(nsNames, ns.Ns)
			}
		}
		if nextZone == "" {
			// SOA or nothing useful: this is the authoritative answer (e.g. NXDOMAIN).
			for _, rr := range resp.Ns {
				step.Records = append(step.Records, rr.String())
			}
			tr.Steps = append(tr.Steps, step)
			return tr, nil
		}

		// Resolve next-hop server addresses, preferring glue records.
		var nextServers []string
		for _, rr := range resp.Extra {
			if a, ok := rr.(*dns.A); ok {
				for _, ns := range nsNames {
					if strings.EqualFold(a.Header().Name, ns) {
						nextServers = append(nextServers, a.A.String())
					}
				}
			}
		}
		if len(nextServers) == 0 && len(nsNames) > 0 {
			if res, err := Query("", nsNames[0], "A"); err == nil {
				for _, ans := range res.Answers {
					fields := strings.Fields(ans)
					if len(fields) >= 5 && fields[3] == "A" {
						nextServers = append(nextServers, fields[4])
					}
				}
			}
		}

		tr.Steps = append(tr.Steps, step)
		if len(nextServers) == 0 {
			tr.Error = fmt.Sprintf("nao foi possivel resolver os servidores de nomes da zona %s", nextZone)
			return tr, nil
		}
		servers = nextServers
		zone = nextZone
	}

	tr.Error = "numero maximo de saltos de delegacao atingido"
	return tr, nil
}

func isCNAME(rrs []dns.RR) bool {
	for _, rr := range rrs {
		if rr.Header().Rrtype == dns.TypeCNAME {
			return true
		}
	}
	return false
}

func hasType(rrs []dns.RR, t uint16) bool {
	for _, rr := range rrs {
		if rr.Header().Rrtype == t {
			return true
		}
	}
	return false
}

func cnameTarget(rrs []dns.RR) string {
	for _, rr := range rrs {
		if c, ok := rr.(*dns.CNAME); ok {
			return c.Target
		}
	}
	return ""
}
