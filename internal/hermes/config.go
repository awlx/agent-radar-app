// Package hermes reads the sessions of a remote Hermes Agent (Nous Research) through its
// dashboard or its API server. It only reads. It does not start, stop, or change sessions.
package hermes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config tells how to connect to a Hermes host. A Config without a URL turns the source off.
type Config struct {
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Profile  string `json:"profile,omitempty"`

	base string // scheme://host[:port]
	host string // the host name for the UI
}

// The environment variables override the values in the config file.
const (
	EnvURL      = "AGENT_RADAR_HERMES_URL"
	EnvToken    = "AGENT_RADAR_HERMES_TOKEN"
	EnvUser     = "AGENT_RADAR_HERMES_USER"
	EnvPassword = "AGENT_RADAR_HERMES_PASSWORD"
)

// ConfigPath gives the optional config file: <user config folder>/agent-radar/hermes.json.
func ConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "agent-radar", "hermes.json")
}

// LoadConfig reads the config file at path (if it exists), then the environment, then flagURL.
// It returns nil and no error when no Hermes URL is set.
func LoadConfig(path, flagURL string, getenv func(string) string) (*Config, error) {
	cfg := &Config{}
	if path != "" {
		if err := readFile(path, cfg); err != nil {
			return nil, err
		}
	}
	for _, kv := range []struct {
		env string
		dst *string
	}{{EnvURL, &cfg.URL}, {EnvToken, &cfg.Token}, {EnvUser, &cfg.Username}, {EnvPassword, &cfg.Password}} {
		if v := strings.TrimSpace(getenv(kv.env)); v != "" {
			*kv.dst = v
		}
	}
	if flagURL != "" {
		cfg.URL = flagURL
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, nil
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func readFile(path string, cfg *Config) error {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// The file can contain a password or a token. Other users must not read it.
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: other users can read this file; run: chmod 600 %q", path, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return fmt.Errorf("%s: invalid JSON", path)
	}
	return nil
}

// normalize accepts a base URL or a URL of a dashboard page, for example
// http://host:9119/sessions?profile=default. The profile comes from the query if it is not set.
func (c *Config) normalize() error {
	raw := strings.TrimSpace(c.URL)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("hermes: invalid URL (use http://host:port)")
	}
	if c.Profile == "" {
		c.Profile = u.Query().Get("profile")
	}
	c.base = u.Scheme + "://" + u.Host
	c.host = u.Hostname()
	c.URL = c.base
	return nil
}

// Host gives the host name of the Hermes URL.
func (c *Config) Host() string { return c.host }

// insecure tells if credentials go without TLS to a host that is not on this computer.
func (c *Config) insecure() bool {
	if !strings.HasPrefix(c.base, "http://") || (c.Token == "" && c.Password == "") {
		return false
	}
	if c.host == "localhost" {
		return false
	}
	ip := net.ParseIP(c.host)
	return ip == nil || !ip.IsLoopback()
}
