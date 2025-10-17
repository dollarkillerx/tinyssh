package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the JSON configuration expected by the tiny SSH server.
type Config struct {
	ListenAddress string `json:"listen_address"`
	ListenPort    int    `json:"listen_port"`
	HostKeyPath   string `json:"host_key_path"`
	Shell         string `json:"shell"`
	Users         []User `json:"users"`

	configDir string
}

// User describes an account allowed to log in to the SSH server.
type User struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Load reads and validates the configuration file at the provided path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.configDir = filepath.Dir(path)
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Credentials returns a map of username to password for quick lookup.
func (c *Config) Credentials() map[string]string {
	creds := make(map[string]string, len(c.Users))
	for _, user := range c.Users {
		creds[user.Username] = user.Password
	}
	return creds
}

// applyDefaults fills in reasonable defaults when values are omitted.
func (c *Config) applyDefaults() {
	if c.ListenAddress == "" {
		if c.ListenPort > 0 {
			c.ListenAddress = fmt.Sprintf(":%d", c.ListenPort)
		} else {
			c.ListenAddress = ":2222"
		}
	}

	if c.HostKeyPath == "" {
		c.HostKeyPath = filepath.Join(c.configDir, "tinyssh_host_key")
	} else if !filepath.IsAbs(c.HostKeyPath) {
		c.HostKeyPath = filepath.Join(c.configDir, c.HostKeyPath)
	}

	if c.Shell == "" {
		if shell := os.Getenv("SHELL"); shell != "" {
			c.Shell = shell
		} else {
			c.Shell = "/bin/sh"
		}
	}
}

// validate ensures the configuration values are sane.
func (c *Config) validate() error {
	if c.ListenAddress == "" {
		return errors.New("listen address is required")
	}

	if len(c.Users) == 0 {
		return errors.New("at least one user must be configured")
	}

	seen := make(map[string]struct{}, len(c.Users))
	for _, user := range c.Users {
		username := strings.TrimSpace(user.Username)
		if username == "" {
			return errors.New("user username cannot be empty")
		}
		if user.Password == "" {
			return fmt.Errorf("user %s must have a password", username)
		}
		if _, ok := seen[username]; ok {
			return fmt.Errorf("duplicate user %s", username)
		}
		seen[username] = struct{}{}
	}

	return nil
}

// ConfigDir exposes the directory where the configuration file lives.
func (c *Config) ConfigDir() string {
	return c.configDir
}
