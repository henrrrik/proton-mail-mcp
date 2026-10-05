// Package config loads settings from an optional TOML file and the
// environment. Environment variables override the file.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const DefaultIMAPAddr = "127.0.0.1:1143"

type Config struct {
	User             string   `toml:"user"`
	Password         string   `toml:"-"`
	PasswordFile     string   `toml:"password_file"`
	IMAPAddr         string   `toml:"imap_addr"`
	BridgeCert       string   `toml:"bridge_cert"`
	InsecureLoopback bool     `toml:"insecure_loopback"`
	AllowRemoteIMAP  bool     `toml:"allow_remote_imap"`
	ReadOnly         bool     `toml:"read_only"`
	AllowFolders     []string `toml:"allow_folders"`
	DenyFolders      []string `toml:"deny_folders"`
	AuditLog         string   `toml:"audit_log"`
}

// Load reads the TOML file at path (if non-empty), applies environment
// overrides from getenv, and validates the result.
func Load(path string, getenv func(string) string) (*Config, error) {
	c := &Config{IMAPAddr: DefaultIMAPAddr, ReadOnly: true}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := toml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := c.applyEnv(getenv); err != nil {
		return nil, err
	}
	if c.Password == "" && c.PasswordFile != "" {
		b, err := os.ReadFile(c.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("read password file: %w", err)
		}
		c.Password = strings.TrimSpace(string(b))
	}
	return c, c.validate()
}

func (c *Config) applyEnv(getenv func(string) string) error {
	str := func(key string, dst *string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	list := func(key string, dst *[]string) {
		if v := getenv(key); v != "" {
			*dst = splitList(v)
		}
	}
	boolean := func(key string, dst *bool) error {
		v := getenv(key)
		if v == "" {
			return nil
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		*dst = b
		return nil
	}

	str("PROTON_USER", &c.User)
	str("PROTON_BRIDGE_PASSWORD", &c.Password)
	str("PROTON_BRIDGE_PASSWORD_FILE", &c.PasswordFile)
	str("IMAP_ADDR", &c.IMAPAddr)
	str("BRIDGE_CERT", &c.BridgeCert)
	str("AUDIT_LOG", &c.AuditLog)
	list("ALLOW_FOLDERS", &c.AllowFolders)
	list("DENY_FOLDERS", &c.DenyFolders)
	return errors.Join(
		boolean("BRIDGE_INSECURE_LOOPBACK", &c.InsecureLoopback),
		boolean("ALLOW_REMOTE_IMAP", &c.AllowRemoteIMAP),
		boolean("READ_ONLY", &c.ReadOnly),
	)
}

func (c *Config) validate() error {
	var errs []error
	if c.User == "" {
		errs = append(errs, errors.New("PROTON_USER is required"))
	}
	if c.Password == "" {
		errs = append(errs, errors.New("PROTON_BRIDGE_PASSWORD or PROTON_BRIDGE_PASSWORD_FILE is required"))
	}
	host, _, err := net.SplitHostPort(c.IMAPAddr)
	if err != nil {
		errs = append(errs, fmt.Errorf("IMAP_ADDR: %w", err))
	} else {
		loopback := IsLoopback(host)
		if !loopback && !c.AllowRemoteIMAP {
			errs = append(errs, fmt.Errorf("IMAP_ADDR %q is not loopback; set ALLOW_REMOTE_IMAP=true to permit it", c.IMAPAddr))
		}
		if c.InsecureLoopback && !loopback {
			errs = append(errs, errors.New("BRIDGE_INSECURE_LOOPBACK only applies to loopback hosts"))
		}
	}
	if c.BridgeCert == "" && !c.InsecureLoopback {
		errs = append(errs, errors.New("BRIDGE_CERT is required (or BRIDGE_INSECURE_LOOPBACK=true for a loopback Bridge)"))
	}
	return errors.Join(errs...)
}

// IsLoopback reports whether host is "localhost" or a loopback IP.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
