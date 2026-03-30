package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadParsesEnvironment(t *testing.T) {
	t.Setenv("NODE_ID", "node-a")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
	t.Setenv("VAULT_TOKEN", "vault-token")
	t.Setenv("CONSUL_ADDR", "http://127.0.0.1:8500")
	t.Setenv("CONSUL_LOCK_KEY", "service/leader")
	t.Setenv("BACKUP_SCHEDULE", "15m")
	t.Setenv("BACKUP_LOCATION", filepath.Join(t.TempDir(), "backups"))
	t.Setenv("RETENTION_COUNT", "4")
	t.Setenv("RETENTION_MAX_AGE", "48h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.NodeID != "node-a" {
		t.Fatalf("expected node id to be parsed")
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
