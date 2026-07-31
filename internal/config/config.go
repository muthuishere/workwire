// Package config loads workwire configuration: auto-created
// ~/.config/workwire/workwire.json defaults with WORKWIRE_* env overrides
// for every key (hub-core R11, ADR-006), and the declared-exposure
// fail-closed check (auth R6).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config is every tunable of the hub and clients. JSON tags are the
// workwire.json keys. Secret VALUES never live here (auth R2).
type Config struct {
	Bind              string `json:"bind"`
	Port              int    `json:"port"`
	DataDir           string `json:"dataDir"`
	HubURL            string `json:"hubUrl"`
	AuthMode          string `json:"authMode"` // "token" | "open"
	TokenEnv          string `json:"tokenEnv"` // NAME of the env var holding the token
	LastMessages      int    `json:"lastMessages"`
	ContextCap        int    `json:"contextCap"`
	WaitDefault       int    `json:"waitDefault"` // seconds
	WaitMax           int    `json:"waitMax"`     // seconds
	RetentionDays     int    `json:"retentionDays"`
	RetentionMaxBytes int64  `json:"retentionMaxBytes"`
	SegmentMaxBytes   int64  `json:"segmentMaxBytes"`
	MaxThreadMessages int    `json:"maxThreadMessages"`
	HeartbeatSeconds  int    `json:"heartbeatSeconds"`
	TTLSeconds        int    `json:"ttlSeconds"`

	// Exposed is env/flag only (WORKWIRE_EXPOSED) — declared exposure, auth R6.
	Exposed bool `json:"-"`
	// ConfigDir is where workwire.json, admin-token and credentials.json live.
	ConfigDir string `json:"-"`
}

// Defaults returns the documented default configuration.
func Defaults() Config {
	return Config{
		Bind:              "127.0.0.1",
		Port:              14411,
		HubURL:            "http://127.0.0.1:14411",
		AuthMode:          "token",
		TokenEnv:          "WORKWIRE_TOKEN",
		LastMessages:      5,
		ContextCap:        20,
		WaitDefault:       25,
		WaitMax:           60,
		RetentionDays:     30,
		RetentionMaxBytes: 1 << 30, // 1 GB
		SegmentMaxBytes:   64 << 20,
		MaxThreadMessages: 24,
		HeartbeatSeconds:  30,
		TTLSeconds:        120,
	}
}

// Load resolves config: file (auto-created with defaults when a config dir is
// resolvable), then env overrides. A container with no home dir and
// WORKWIRE_* env vars operates env-only — no file is ever written.
func Load() (Config, error) {
	cfg := Defaults()

	dir := os.Getenv("WORKWIRE_CONFIG_DIR")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = filepath.Join(home, ".config", "workwire")
		}
	}
	cfg.ConfigDir = dir

	if dir != "" {
		path := filepath.Join(dir, "workwire.json")
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			// Auto-create with defaults on first run of any verb (R11).
			// Best-effort: env-only hosts may have a read-only FS.
			if err := os.MkdirAll(dir, 0o755); err == nil {
				if b, err := json.MarshalIndent(Defaults(), "", "  "); err == nil {
					_ = os.WriteFile(path, append(b, '\n'), 0o644)
				}
			}
		}
	}

	if cfg.DataDir == "" {
		if dir != "" {
			cfg.DataDir = filepath.Join(dir, "data")
		} else {
			cfg.DataDir = "/data"
		}
	}

	applyEnv(&cfg)
	return cfg, nil
}

func applyEnv(cfg *Config) {
	str := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	num := func(key string, dst *int) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	num64 := func(key string, dst *int64) {
		if v := os.Getenv(key); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				*dst = n
			}
		}
	}
	str("WORKWIRE_BIND", &cfg.Bind)
	num("WORKWIRE_PORT", &cfg.Port)
	str("WORKWIRE_DATA_DIR", &cfg.DataDir)
	str("WORKWIRE_HUB_URL", &cfg.HubURL)
	str("WORKWIRE_AUTHMODE", &cfg.AuthMode)
	str("WORKWIRE_TOKEN_ENV", &cfg.TokenEnv)
	num("WORKWIRE_LAST_MESSAGES", &cfg.LastMessages)
	num("WORKWIRE_CONTEXT_CAP", &cfg.ContextCap)
	num("WORKWIRE_WAIT_DEFAULT", &cfg.WaitDefault)
	num("WORKWIRE_WAIT_MAX", &cfg.WaitMax)
	num("WORKWIRE_RETENTION_DAYS", &cfg.RetentionDays)
	num64("WORKWIRE_RETENTION_MAX_BYTES", &cfg.RetentionMaxBytes)
	num64("WORKWIRE_SEGMENT_MAX_BYTES", &cfg.SegmentMaxBytes)
	num("WORKWIRE_MAX_THREAD_MESSAGES", &cfg.MaxThreadMessages)
	num("WORKWIRE_HEARTBEAT_SECONDS", &cfg.HeartbeatSeconds)
	num("WORKWIRE_TTL_SECONDS", &cfg.TTLSeconds)
	if v := os.Getenv("WORKWIRE_EXPOSED"); v == "1" || v == "true" {
		cfg.Exposed = true
	}
}

// Validate enforces invariants before serving. authMode=open combined with
// declared exposure fails closed (auth R6).
func (c Config) Validate() error {
	switch c.AuthMode {
	case "token", "open":
	default:
		return fmt.Errorf("invalid authMode %q: must be \"token\" or \"open\"", c.AuthMode)
	}
	if c.AuthMode == "open" && c.Exposed {
		return errors.New("refusing to start: authMode=open cannot be combined with declared exposure (WORKWIRE_EXPOSED=1); unset the exposure flag or use authMode=token")
	}
	return nil
}

// TokenFromEnv returns the token value from the env var NAMED by TokenEnv
// (value is used, never logged or stored).
func (c Config) TokenFromEnv() string {
	if c.TokenEnv == "" {
		return ""
	}
	return os.Getenv(c.TokenEnv)
}
