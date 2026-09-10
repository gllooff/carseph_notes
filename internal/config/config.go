// Package config loads server configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the server.
type Config struct {
	ListenAddr   string        // HTTP listen address
	DataDir      string        // root directory for SQLite DB and blobs
	RPID         string        // WebAuthn Relying Party ID (domain)
	RPName       string        // WebAuthn Relying Party display name
	Origin       string        // public origin URL (scheme + host)
	SessionTTL   time.Duration // sliding session lifetime
	MaxUploadMB  int           // per-file upload limit
	Dev          bool          // relax cookie Secure flag for plain-HTTP local dev
}

// Load reads configuration from the environment, applying defaults.
func Load() (*Config, error) {
	c := &Config{
		ListenAddr:  env("LISTEN_ADDR", ":8080"),
		DataDir:     env("DATA_DIR", "./data"),
		RPID:        env("RP_ID", "localhost"),
		RPName:      env("RP_NAME", "Carseph Notes"),
		Origin:      strings.TrimRight(env("ORIGIN", "http://localhost:8080"), "/"),
		SessionTTL:  720 * time.Hour,
		MaxUploadMB: 25,
	}

	if v := os.Getenv("SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid SESSION_TTL %q", v)
		}
		c.SessionTTL = d
	}
	if v := os.Getenv("MAX_UPLOAD_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid MAX_UPLOAD_MB %q", v)
		}
		c.MaxUploadMB = n
	}
	if v := os.Getenv("DEV"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid DEV %q", v)
		}
		c.Dev = b
	}

	if c.RPID == "" {
		return nil, fmt.Errorf("RP_ID is required")
	}
	if !strings.HasPrefix(c.Origin, "http://") && !strings.HasPrefix(c.Origin, "https://") {
		return nil, fmt.Errorf("ORIGIN must include scheme: %q", c.Origin)
	}
	if !c.Dev && !strings.HasPrefix(c.Origin, "https://") {
		return nil, fmt.Errorf("ORIGIN must be https unless DEV=true (got %q)", c.Origin)
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
