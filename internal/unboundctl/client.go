// Package unboundctl wraps the unbound-control CLI to read statistics and
// issue control commands against a local Unbound recursive resolver.
package unboundctl

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Client executes unbound-control commands.
type Client struct {
	Bin  string
	Conf string
}

// New creates a new Client.
func New(bin, conf string) *Client {
	return &Client{Bin: bin, Conf: conf}
}

func (c *Client) run(args ...string) (string, error) {
	fullArgs := append([]string{"-c", c.Conf}, args...)
	cmd := exec.Command(c.Bin, fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("unbound-control %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Stats runs "unbound-control stats_noreset" and returns the raw key/value
// pairs (e.g. "total.num.queries" -> "12345").
func (c *Client) Stats() (map[string]string, error) {
	out, err := c.run("stats_noreset")
	if err != nil {
		return nil, err
	}

	stats := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		stats[parts[0]] = parts[1]
	}
	return stats, scanner.Err()
}

// StatsFloat returns Stats() with values parsed as float64, dropping any
// values that fail to parse.
func (c *Client) StatsFloat() (map[string]float64, error) {
	raw, err := c.Stats()
	if err != nil {
		return nil, err
	}
	stats := make(map[string]float64, len(raw))
	for k, v := range raw {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		stats[k] = f
	}
	return stats, nil
}

// Status runs "unbound-control status" and returns its raw output.
func (c *Client) Status() (string, error) {
	return c.run("status")
}

// controlArgSpec describes how many arguments a control command expects.
type controlArgSpec struct {
	min, max int
}

// AllowedCommands lists the unbound-control subcommands exposed via the API,
// along with the number of arguments each accepts. Commands not in this map
// are rejected.
var AllowedCommands = map[string]controlArgSpec{
	"status":            {0, 0},
	"reload":            {0, 0},
	"flush_requestlist": {0, 0},
	"flush_infra":       {1, 1}, // <ip|all>
	"flush":             {1, 1}, // <name>
	"flush_zone":        {1, 1}, // <zone>
	"flush_type":        {2, 2}, // <name> <type>
	"flush_bogus":       {0, 0},
	"flush_negative":    {0, 0},
	"dump_cache":        {0, 0},
	"verbosity":         {1, 1}, // <level>
	"ratelimit_list":    {0, 0},
	"list_local_zones":  {0, 0},
	"list_local_data":   {0, 0},
	"list_insecure":     {0, 0},
}

// RunControl executes an allowlisted unbound-control command with the given
// arguments and returns its output.
func RunControl(c *Client, command string, args []string) (string, error) {
	spec, ok := AllowedCommands[command]
	if !ok {
		return "", fmt.Errorf("command %q is not allowed", command)
	}
	if len(args) < spec.min || len(args) > spec.max {
		return "", fmt.Errorf("command %q expects between %d and %d argument(s), got %d", command, spec.min, spec.max, len(args))
	}

	cmdArgs := append([]string{command}, args...)
	out, err := c.run(cmdArgs...)
	if err != nil {
		return "", err
	}
	return out, nil
}
