package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadParsesEnvironment(t *testing.T) {
	t.Setenv("NODE_ID", "node-a")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
	t.Setenv("VAULT_TOKEN", "vault-token")
	t.Setenv("VAULT_CA_CERT_FILE", filepath.Join(t.TempDir(), "vault-ca.crt"))
	t.Setenv("CONSUL_ADDR", "http://127.0.0.1:8500")
	t.Setenv("CONSUL_LOCK_KEY", "service/leader")
	t.Setenv("BACKUP_SCHEDULE", "15m")
	t.Setenv("BACKUP_LOCATION", filepath.Join(t.TempDir(), "backups"))
	t.Setenv("SCRATCH_DIR", filepath.Join(t.TempDir(), "scratch"))
	t.Setenv("RETENTION_COUNT", "4")
	t.Setenv("RETENTION_MAX_AGE", "48h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.NodeID != "node-a" {
		t.Fatal("expected node id to be parsed")
	}
	if cfg.LogFormat != "json" {
		t.Fatalf("expected default log format json, got %q", cfg.LogFormat)
	}
	if cfg.RetentionCount != 4 {
		t.Fatalf("expected retention count 4, got %d", cfg.RetentionCount)
	}
	if cfg.RetentionMaxAge.Hours() != 48 {
		t.Fatalf("expected retention max age 48h, got %s", cfg.RetentionMaxAge)
	}
	if cfg.BackupSchedule.Minutes() != 15 {
		t.Fatalf("expected schedule 15m, got %s", cfg.BackupSchedule)
	}
	if !strings.HasSuffix(cfg.VaultCACertFile, "vault-ca.crt") {
		t.Fatalf("expected vault ca cert file to be parsed, got %q", cfg.VaultCACertFile)
	}
}

func TestLoadRequiresCriticalValues(t *testing.T) {
	for _, key := range []string{
		"NODE_ID",
		"VAULT_ADDR",
		"VAULT_TOKEN",
		"VAULT_TOKEN_FILE",
		"CONSUL_ADDR",
		"CONSUL_LOCK_KEY",
		"BACKUP_SCHEDULE",
		"BACKUP_LOCATION",
	} {
		_ = os.Unsetenv(key)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error")
	}

	message := err.Error()
	for _, fragment := range []string{"VAULT_ADDR is required", "CONSUL_ADDR is required", "BACKUP_SCHEDULE is required", "BACKUP_LOCATION is required"} {
		if !strings.Contains(message, fragment) {
			t.Fatalf("expected %q in %q", fragment, message)
		}
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NODE_ID", "node-a")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
	t.Setenv("VAULT_TOKEN", "vault-token")
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("VAULT_CA_CERT_FILE", "")
	t.Setenv("CONSUL_ADDR", "http://127.0.0.1:8500")
	t.Setenv("CONSUL_LOCK_KEY", "service/leader")
	t.Setenv("BACKUP_SCHEDULE", "15m")
	t.Setenv("BACKUP_LOCATION", filepath.Join(t.TempDir(), "backups"))
	t.Setenv("SCRATCH_DIR", filepath.Join(t.TempDir(), "scratch"))
}

func TestLoadReturnsParsingErrors(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		value    string
		fragment string
	}{
		{name: "vault timeout", key: "VAULT_REQUEST_TIMEOUT", value: "bad", fragment: "VAULT_REQUEST_TIMEOUT must be a valid duration"},
		{name: "consul session ttl", key: "CONSUL_SESSION_TTL", value: "bad", fragment: "CONSUL_SESSION_TTL must be a valid duration"},
		{name: "consul lock wait", key: "CONSUL_LOCK_WAIT", value: "bad", fragment: "CONSUL_LOCK_WAIT must be a valid duration"},
		{name: "backup schedule", key: "BACKUP_SCHEDULE", value: "bad", fragment: "BACKUP_SCHEDULE must be a valid duration"},
		{name: "probe interval", key: "PROBE_INTERVAL", value: "bad", fragment: "PROBE_INTERVAL must be a valid duration"},
		{name: "retention count", key: "RETENTION_COUNT", value: "bad", fragment: "RETENTION_COUNT must be a valid integer"},
		{name: "retention max age", key: "RETENTION_MAX_AGE", value: "bad", fragment: "RETENTION_MAX_AGE must be a valid duration"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.fragment) {
				t.Fatalf("expected %q, got %v", tc.fragment, err)
			}
		})
	}
}

func TestValidateRejectsInvalidValues(t *testing.T) {
	cfg := Config{
		NodeID:               "",
		VaultAddr:            "",
		VaultToken:           "",
		VaultTokenFile:       "",
		ConsulAddr:           "",
		ConsulLockKey:        "",
		BackupSchedule:       0,
		BackupLocation:       "relative",
		ArtifactNameTemplate: " ",
		RetentionCount:       -1,
		RetentionMaxAge:      -time.Second,
		ProbeInterval:        0,
		LogFormat:            "pretty",
		VaultCACertFile:      "relative",
		ScratchDir:           "relative",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}

	for _, fragment := range []string{
		"NODE_ID must resolve to a non-empty value",
		"VAULT_ADDR is required",
		"VAULT_TOKEN or VAULT_TOKEN_FILE is required",
		"CONSUL_ADDR is required",
		"CONSUL_LOCK_KEY is required",
		"BACKUP_SCHEDULE is required",
		"BACKUP_LOCATION must be an absolute path",
		"VAULT_CA_CERT_FILE must be an absolute path",
		"SCRATCH_DIR must be an absolute path",
		"RETENTION_COUNT must be zero or greater",
		"RETENTION_MAX_AGE must be zero or positive",
		"PROBE_INTERVAL must be greater than zero",
		"LOG_FORMAT must be one of: json, text",
		"ARTIFACT_NAME_TEMPLATE must be non-empty",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("expected %q in %q", fragment, err.Error())
		}
	}
}

func TestHostnameFallsBackWhenLookupFails(t *testing.T) {
	original := osHostname
	osHostname = func() (string, error) {
		return "", errors.New("boom")
	}
	defer func() {
		osHostname = original
	}()

	if got := hostname(); got != "unknown" {
		t.Fatalf("expected fallback hostname, got %q", got)
	}
}

func TestDurationEnv(t *testing.T) {
	t.Setenv("VALUE", "")
	if got, err := durationEnv("VALUE", time.Minute); err != nil || got != time.Minute {
		t.Fatalf("expected fallback minute, got %v and %v", got, err)
	}

	t.Setenv("VALUE", "2m")
	if got, err := durationEnv("VALUE", time.Minute); err != nil || got != 2*time.Minute {
		t.Fatalf("expected parsed duration, got %v and %v", got, err)
	}

	t.Setenv("VALUE", "bad")
	if _, err := durationEnv("VALUE", time.Minute); err == nil || !strings.Contains(err.Error(), "VALUE must be a valid duration") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestOptionalDurationEnv(t *testing.T) {
	t.Setenv("VALUE", "")
	if got, err := optionalDurationEnv("VALUE"); err != nil || got != 0 {
		t.Fatalf("expected zero duration, got %v and %v", got, err)
	}

	t.Setenv("VALUE", "3m")
	if got, err := optionalDurationEnv("VALUE"); err != nil || got != 3*time.Minute {
		t.Fatalf("expected parsed duration, got %v and %v", got, err)
	}

	t.Setenv("VALUE", "bad")
	if _, err := optionalDurationEnv("VALUE"); err == nil || !strings.Contains(err.Error(), "VALUE must be a valid duration") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestIntEnv(t *testing.T) {
	t.Setenv("VALUE", "")
	if got, err := intEnv("VALUE", 7); err != nil || got != 7 {
		t.Fatalf("expected fallback integer, got %d and %v", got, err)
	}

	t.Setenv("VALUE", "9")
	if got, err := intEnv("VALUE", 7); err != nil || got != 9 {
		t.Fatalf("expected parsed integer, got %d and %v", got, err)
	}

	t.Setenv("VALUE", "bad")
	if _, err := intEnv("VALUE", 7); err == nil || !strings.Contains(err.Error(), "VALUE must be a valid integer") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty(" ", " value ", "other"); got != "value" {
		t.Fatalf("expected first trimmed value, got %q", got)
	}
	if got := firstNonEmpty(" ", "\t"); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestLoadParsesLogFormat(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("LOG_FORMAT", "text")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.LogFormat != "text" {
		t.Fatalf("expected text log format, got %q", cfg.LogFormat)
	}
}
