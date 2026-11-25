package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Config holds all runtime configuration for unbound-dash.
type Config struct {
	ListenAddr        string `json:"listen_addr"`
	UnboundControlBin string `json:"unbound_control_bin"`
	UnboundConf       string `json:"unbound_conf"`
	UnboundLogFile    string `json:"unbound_log_file"`
	UnboundCheckconf  string `json:"unbound_checkconf_bin"`
	ForwardZoneFile   string `json:"forward_zone_file"`
	StaticEntryFile   string `json:"static_entry_file"`
	AdminPassword     string `json:"admin_password"`
	SessionSecret     string `json:"session_secret"`

	// Blocklist DNS do ISP Tools.
	ISPToolsToken         string `json:"isptools_token"`
	ISPToolsAPIURL        string `json:"isptools_api_url"`
	ISPToolsBlocklistFile string `json:"isptools_blocklist_file"`
	ISPToolsSyncMinutes   int    `json:"isptools_sync_minutes"`
	StateDir              string `json:"state_dir"`

	// LegacyBlocklistFile is the old manually managed anatel-blocklist.conf
	// (key kept as "blocklist_file" for existing installs). It is only read
	// once, to migrate it away after the first successful API sync.
	LegacyBlocklistFile string `json:"blocklist_file"`
}

func defaults() Config {
	return Config{
		ListenAddr:        ":8080",
		UnboundControlBin: "/usr/sbin/unbound-control",
		UnboundConf:       "/etc/unbound/unbound.conf",
		UnboundLogFile:    "/var/log/unbound/unbound.log",
		UnboundCheckconf:  "/usr/sbin/unbound-checkconf",
		ForwardZoneFile:   "/etc/unbound/unbound.conf.d/forwardzone.conf",
		StaticEntryFile:   "/etc/unbound/unbound.conf.d/staticentries.conf",

		ISPToolsAPIURL:        "https://isptools.gbti.net.br/api/v1/blocklist",
		ISPToolsBlocklistFile: "/etc/unbound/unbound.conf.d/isptools-blocklist.conf",
		ISPToolsSyncMinutes:   60,
		StateDir:              "/var/lib/unbound-dash",

		LegacyBlocklistFile: "/etc/unbound/unbound.conf.d/anatel-blocklist.conf",
	}
}

// Load reads the config from path, filling in defaults for any missing
// fields. If the file does not exist, defaults are returned along with a
// generated session secret and an error indicating the admin password must
// be set.
func Load(path string) (Config, error) {
	cfg := defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.SessionSecret, err = randomHex(32)
			if err != nil {
				return cfg, err
			}
			return cfg, fmt.Errorf("config file %s not found", path)
		}
		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if cfg.SessionSecret == "" {
		cfg.SessionSecret, err = randomHex(32)
		if err != nil {
			return cfg, err
		}
	}

	if cfg.AdminPassword == "" {
		return cfg, fmt.Errorf("admin_password must be set in %s", path)
	}

	return cfg, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
