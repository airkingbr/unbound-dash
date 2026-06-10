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
	BlocklistFile     string `json:"blocklist_file"`
	AdminPassword     string `json:"admin_password"`
	SessionSecret     string `json:"session_secret"`
}

func defaults() Config {
	return Config{
		ListenAddr:        ":8080",
		UnboundControlBin: "/usr/sbin/unbound-control",
		UnboundConf:       "/etc/unbound/unbound.conf",
		UnboundLogFile:    "/var/log/unbound/unbound.log",
		BlocklistFile:     "/etc/unbound/unbound.conf.d/anatel-blocklist.conf",
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
