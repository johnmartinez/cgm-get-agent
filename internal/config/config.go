// Package config loads and validates application configuration from an optional
// YAML file and GA_* environment variable overrides.
//
// Loading order:
//  1. Apply built-in defaults.
//  2. Parse GA_CONFIG_PATH YAML (default: /data/config.yaml) if the file exists.
//  3. Override any field set by a GA_* environment variable.
//  4. Validate required fields; fail fast if any are missing or malformed.
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// GlucoseZones defines mg/dL thresholds for zone classification.
// All values are inclusive lower bounds for that zone.
type GlucoseZones struct {
	Low        int `yaml:"low"`
	TargetLow  int `yaml:"target_low"`
	TargetHigh int `yaml:"target_high"`
	Elevated   int `yaml:"elevated"`
	High       int `yaml:"high"`
}

// Config is the fully resolved application configuration.
// EncryptionKey is always sourced from GA_ENCRYPTION_KEY and never written to YAML.
type Config struct {
	Dexcom struct {
		ClientID     string `yaml:"client_id"`
		ClientSecret string `yaml:"client_secret"`
		Environment  string `yaml:"environment"`
		RedirectURI  string `yaml:"redirect_uri"`
	} `yaml:"dexcom"`
	Server struct {
		Port int    `yaml:"port"`
		Host string `yaml:"host"`
	} `yaml:"server"`
	Storage struct {
		DBPath    string `yaml:"db_path"`
		TokenPath string `yaml:"token_path"`
	} `yaml:"storage"`
	EncryptionKey []byte       // decoded from GA_ENCRYPTION_KEY; never in YAML
	GlucoseZones  GlucoseZones `yaml:"glucose_zones"`
}

// Load builds a Config by applying defaults, YAML file (if present), env overrides,
// and finally validating required fields. It returns an error on the first violation.
func Load() (*Config, error) {
	cfg := defaults()

	if path := envOr("GA_CONFIG_PATH", "/data/config.yaml"); fileExists(path) {
		if err := loadYAML(path, cfg); err != nil {
			return nil, fmt.Errorf("config: loading yaml %q: %w", path, err)
		}
	}

	applyEnv(cfg)

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func defaults() *Config {
	cfg := &Config{}
	cfg.Dexcom.Environment = "sandbox"
	cfg.Dexcom.RedirectURI = "http://localhost:8080/callback"
	cfg.Server.Port = 8080
	cfg.Server.Host = "0.0.0.0"
	cfg.Storage.DBPath = "/data/data.db"
	cfg.Storage.TokenPath = "/data/tokens.enc"
	cfg.GlucoseZones = GlucoseZones{
		Low:        70,
		TargetLow:  80,
		TargetHigh: 120,
		Elevated:   140,
		High:       180,
	}
	return cfg
}

func loadYAML(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return yaml.NewDecoder(f).Decode(cfg)
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("GA_DEXCOM_CLIENT_ID"); v != "" {
		cfg.Dexcom.ClientID = v
	}
	if v := os.Getenv("GA_DEXCOM_CLIENT_SECRET"); v != "" {
		cfg.Dexcom.ClientSecret = v
	}
	if v := os.Getenv("GA_DEXCOM_ENV"); v != "" {
		cfg.Dexcom.Environment = v
	}
	if v := os.Getenv("GA_DEXCOM_REDIRECT_URI"); v != "" {
		cfg.Dexcom.RedirectURI = v
	}
	if v := os.Getenv("GA_SERVER_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = p
		}
	}
	if v := os.Getenv("GA_DB_PATH"); v != "" {
		cfg.Storage.DBPath = v
	}
	if v := os.Getenv("GA_TOKEN_PATH"); v != "" {
		cfg.Storage.TokenPath = v
	}
}

func validate(cfg *Config) error {
	if cfg.Dexcom.ClientID == "" {
		return fmt.Errorf("config: GA_DEXCOM_CLIENT_ID is required")
	}
	if cfg.Dexcom.ClientSecret == "" {
		return fmt.Errorf("config: GA_DEXCOM_CLIENT_SECRET is required")
	}
	keyHex := os.Getenv("GA_ENCRYPTION_KEY")
	if keyHex == "" {
		return fmt.Errorf("config: GA_ENCRYPTION_KEY is required")
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return fmt.Errorf("config: GA_ENCRYPTION_KEY must be 64 hex chars (32 bytes); got %d chars", len(keyHex))
	}
	cfg.EncryptionKey = key
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
