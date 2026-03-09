package config

import (
	"os"
	"path/filepath"
	"testing"
)

// validKey is a 64-char hex string representing a 32-byte AES key.
const validKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GA_DEXCOM_CLIENT_ID", "test-client-id")
	t.Setenv("GA_DEXCOM_CLIENT_SECRET", "test-secret")
	t.Setenv("GA_ENCRYPTION_KEY", validKey)
}

func clearRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GA_DEXCOM_CLIENT_ID", "")
	t.Setenv("GA_DEXCOM_CLIENT_SECRET", "")
	t.Setenv("GA_ENCRYPTION_KEY", "")
}

func TestLoad_MissingClientID(t *testing.T) {
	clearRequiredEnv(t)
	t.Setenv("GA_DEXCOM_CLIENT_SECRET", "some-secret")
	t.Setenv("GA_ENCRYPTION_KEY", validKey)
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when GA_DEXCOM_CLIENT_ID is missing")
	}
}

func TestLoad_MissingClientSecret(t *testing.T) {
	clearRequiredEnv(t)
	t.Setenv("GA_DEXCOM_CLIENT_ID", "some-id")
	t.Setenv("GA_ENCRYPTION_KEY", validKey)
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when GA_DEXCOM_CLIENT_SECRET is missing")
	}
}

func TestLoad_MissingEncryptionKey(t *testing.T) {
	clearRequiredEnv(t)
	t.Setenv("GA_DEXCOM_CLIENT_ID", "some-id")
	t.Setenv("GA_DEXCOM_CLIENT_SECRET", "some-secret")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when GA_ENCRYPTION_KEY is missing")
	}
}

func TestLoad_InvalidEncryptionKey_TooShort(t *testing.T) {
	clearRequiredEnv(t)
	t.Setenv("GA_DEXCOM_CLIENT_ID", "some-id")
	t.Setenv("GA_DEXCOM_CLIENT_SECRET", "some-secret")
	t.Setenv("GA_ENCRYPTION_KEY", "tooshort")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for too-short encryption key")
	}
}

func TestLoad_InvalidEncryptionKey_NotHex(t *testing.T) {
	clearRequiredEnv(t)
	t.Setenv("GA_DEXCOM_CLIENT_ID", "some-id")
	t.Setenv("GA_DEXCOM_CLIENT_SECRET", "some-secret")
	// 64 chars but not valid hex
	t.Setenv("GA_ENCRYPTION_KEY", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-hex encryption key")
	}
}

func TestLoad_EnvOverridesDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GA_DEXCOM_ENV", "production")
	t.Setenv("GA_SERVER_PORT", "9090")
	t.Setenv("GA_DB_PATH", "/tmp/test.db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Dexcom.Environment != "production" {
		t.Errorf("expected environment=production, got %q", cfg.Dexcom.Environment)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port=9090, got %d", cfg.Server.Port)
	}
	if cfg.Storage.DBPath != "/tmp/test.db" {
		t.Errorf("expected db_path=/tmp/test.db, got %q", cfg.Storage.DBPath)
	}
	if len(cfg.EncryptionKey) != 32 {
		t.Errorf("expected 32-byte encryption key, got %d bytes", len(cfg.EncryptionKey))
	}
}

func TestLoad_Defaults(t *testing.T) {
	setRequiredEnv(t)
	// Clear optional overrides
	t.Setenv("GA_DEXCOM_ENV", "")
	t.Setenv("GA_SERVER_PORT", "")
	t.Setenv("GA_DB_PATH", "")
	t.Setenv("GA_TOKEN_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Dexcom.Environment != "sandbox" {
		t.Errorf("expected default environment=sandbox, got %q", cfg.Dexcom.Environment)
	}
	if cfg.Server.Port != 8090 {
		t.Errorf("expected default port=8090, got %d", cfg.Server.Port)
	}
	if cfg.Storage.DBPath != "/data/data.db" {
		t.Errorf("expected default db_path, got %q", cfg.Storage.DBPath)
	}
	if cfg.Storage.TokenPath != "/data/tokens.enc" {
		t.Errorf("expected default token_path, got %q", cfg.Storage.TokenPath)
	}
}

func TestLoad_GlucoseZoneDefaults(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	z := cfg.GlucoseZones
	if z.Low != 70 {
		t.Errorf("expected low=70, got %d", z.Low)
	}
	if z.TargetLow != 80 {
		t.Errorf("expected target_low=80, got %d", z.TargetLow)
	}
	if z.TargetHigh != 120 {
		t.Errorf("expected target_high=120, got %d", z.TargetHigh)
	}
	if z.Elevated != 140 {
		t.Errorf("expected elevated=140, got %d", z.Elevated)
	}
	if z.High != 180 {
		t.Errorf("expected high=180, got %d", z.High)
	}
}

func TestLoad_YAMLOverridesDefaults(t *testing.T) {
	setRequiredEnv(t)

	yaml := `
dexcom:
  environment: production
server:
  port: 7777
glucose_zones:
  target_high: 130
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GA_CONFIG_PATH", yamlPath)
	t.Setenv("GA_DEXCOM_ENV", "") // env does not override; YAML should win for env field

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Dexcom.Environment != "production" {
		t.Errorf("expected YAML environment=production, got %q", cfg.Dexcom.Environment)
	}
	if cfg.Server.Port != 7777 {
		t.Errorf("expected YAML port=7777, got %d", cfg.Server.Port)
	}
	if cfg.GlucoseZones.TargetHigh != 130 {
		t.Errorf("expected YAML target_high=130, got %d", cfg.GlucoseZones.TargetHigh)
	}
}

func TestLoad_LogLevelDefault(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GA_LOG_LEVEL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != 2 {
		t.Errorf("expected default log_level=2, got %d", cfg.LogLevel)
	}
}

func TestLoad_LogLevelEnvOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GA_LOG_LEVEL", "3")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != 3 {
		t.Errorf("expected log_level=3, got %d", cfg.LogLevel)
	}
}

func TestLoad_LogLevelInvalid(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GA_LOG_LEVEL", "5")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Invalid value should keep default
	if cfg.LogLevel != 2 {
		t.Errorf("expected default log_level=2 for invalid input, got %d", cfg.LogLevel)
	}
}

func TestLoad_EnvWinsOverYAML(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GA_DEXCOM_ENV", "sandbox") // env should win over YAML

	yaml := `
dexcom:
  environment: production
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GA_CONFIG_PATH", yamlPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Dexcom.Environment != "sandbox" {
		t.Errorf("env var should win over YAML: expected sandbox, got %q", cfg.Dexcom.Environment)
	}
}
