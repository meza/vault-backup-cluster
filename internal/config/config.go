package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPBindAddress  = ":8080"
	defaultLogFormat        = "json"
	defaultLogLevel         = "info"
	defaultSessionTTL       = 15 * time.Second
	defaultLockWait         = 10 * time.Second
	defaultProbeInterval    = 30 * time.Second
	defaultVaultTimeout     = 10 * time.Minute
	defaultScratchDir       = "/tmp/vault-snapshot-coordinator"
	defaultArtifactTemplate = "vault-snapshot-{{ .Timestamp }}-{{ .NodeID }}.snap"
	defaultRetentionCount   = 7
)

var osHostname = os.Hostname

type Config struct {
	NodeID               string
	HTTPBindAddress      string
	LogFormat            string
	LogLevel             string
	VaultAddr            string
	VaultToken           string
	VaultTokenFile       string
	VaultCACertFile      string
	VaultRequestTimeout  time.Duration
	ConsulAddr           string
	ConsulToken          string
	ConsulTokenFile      string
	ConsulLockKey        string
	ConsulSessionTTL     time.Duration
	ConsulLockWait       time.Duration
	BackupSchedule       time.Duration
	BackupLocation       string
	ArtifactNameTemplate string
	RetentionCount       int
	RetentionMaxAge      time.Duration
	ScratchDir           string
	ProbeInterval        time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		NodeID:               firstNonEmpty(os.Getenv("NODE_ID"), hostname()),
		HTTPBindAddress:      firstNonEmpty(os.Getenv("HTTP_BIND_ADDRESS"), defaultHTTPBindAddress),
		LogFormat:            firstNonEmpty(strings.ToLower(os.Getenv("LOG_FORMAT")), defaultLogFormat),
		LogLevel:             firstNonEmpty(strings.ToLower(os.Getenv("LOG_LEVEL")), defaultLogLevel),
		VaultAddr:            strings.TrimSpace(os.Getenv("VAULT_ADDR")),
		VaultToken:           strings.TrimSpace(os.Getenv("VAULT_TOKEN")),
		VaultTokenFile:       strings.TrimSpace(os.Getenv("VAULT_TOKEN_FILE")),
		VaultCACertFile:      strings.TrimSpace(os.Getenv("VAULT_CA_CERT_FILE")),
		ConsulAddr:           strings.TrimSpace(os.Getenv("CONSUL_ADDR")),
		ConsulToken:          strings.TrimSpace(os.Getenv("CONSUL_HTTP_TOKEN")),
		ConsulTokenFile:      strings.TrimSpace(os.Getenv("CONSUL_HTTP_TOKEN_FILE")),
		ConsulLockKey:        strings.TrimSpace(os.Getenv("CONSUL_LOCK_KEY")),
		BackupLocation:       strings.TrimSpace(os.Getenv("BACKUP_LOCATION")),
		ArtifactNameTemplate: firstNonEmpty(os.Getenv("ARTIFACT_NAME_TEMPLATE"), defaultArtifactTemplate),
		ScratchDir:           firstNonEmpty(os.Getenv("SCRATCH_DIR"), defaultScratchDir),
	}

	var err error
	if cfg.VaultRequestTimeout, err = durationEnv("VAULT_REQUEST_TIMEOUT", defaultVaultTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ConsulSessionTTL, err = durationEnv("CONSUL_SESSION_TTL", defaultSessionTTL); err != nil {
		return Config{}, err
	}
	if cfg.ConsulLockWait, err = durationEnv("CONSUL_LOCK_WAIT", defaultLockWait); err != nil {
		return Config{}, err
	}
	if cfg.BackupSchedule, err = optionalDurationEnv("BACKUP_SCHEDULE"); err != nil {
		return Config{}, err
	}
	if cfg.ProbeInterval, err = durationEnv("PROBE_INTERVAL", defaultProbeInterval); err != nil {
		return Config{}, err
	}
	if cfg.RetentionCount, err = intEnv("RETENTION_COUNT", defaultRetentionCount); err != nil {
		return Config{}, err
	}
	if cfg.RetentionMaxAge, err = optionalDurationEnv("RETENTION_MAX_AGE"); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	var problems []string
	c.validateRequiredFields(&problems)
	c.validatePaths(&problems)
	c.validateRetention(&problems)
	if c.ProbeInterval <= 0 {
		problems = append(problems, "PROBE_INTERVAL must be greater than zero")
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		problems = append(problems, "LOG_FORMAT must be one of: json, text")
	}
	if strings.TrimSpace(c.ArtifactNameTemplate) == "" {
		problems = append(problems, "ARTIFACT_NAME_TEMPLATE must be non-empty")
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}

	return nil
}

func (c Config) validateRequiredFields(problems *[]string) {
	if c.NodeID == "" {
		*problems = append(*problems, "NODE_ID must resolve to a non-empty value")
	}
	if c.VaultAddr == "" {
		*problems = append(*problems, "VAULT_ADDR is required")
	}
	if c.VaultToken == "" && c.VaultTokenFile == "" {
		*problems = append(*problems, "VAULT_TOKEN or VAULT_TOKEN_FILE is required")
	}
	if c.ConsulAddr == "" {
		*problems = append(*problems, "CONSUL_ADDR is required")
	}
	if c.ConsulLockKey == "" {
		*problems = append(*problems, "CONSUL_LOCK_KEY is required")
	}
	if c.BackupSchedule <= 0 {
		*problems = append(*problems, "BACKUP_SCHEDULE is required")
	}
}

func (c Config) validatePaths(problems *[]string) {
	if c.BackupLocation == "" {
		*problems = append(*problems, "BACKUP_LOCATION is required")
	}
	if c.BackupLocation != "" && !filepath.IsAbs(c.BackupLocation) {
		*problems = append(*problems, "BACKUP_LOCATION must be an absolute path")
	}
	if c.VaultCACertFile != "" && !filepath.IsAbs(c.VaultCACertFile) {
		*problems = append(*problems, "VAULT_CA_CERT_FILE must be an absolute path")
	}
	if !filepath.IsAbs(c.ScratchDir) {
		*problems = append(*problems, "SCRATCH_DIR must be an absolute path")
	}
}

func (c Config) validateRetention(problems *[]string) {
	if c.RetentionCount < 0 {
		*problems = append(*problems, "RETENTION_COUNT must be zero or greater")
	}
	if c.RetentionMaxAge < 0 {
		*problems = append(*problems, "RETENTION_MAX_AGE must be zero or positive")
	}
}

func hostname() string {
	name, err := osHostname()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(name)
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", key, err)
	}
	return parsed, nil
}

func optionalDurationEnv(key string) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", key, err)
	}
	return parsed, nil
}

//nolint:unparam // The fallback is part of the shared parsing helper contract used by tests and callers.
func intEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}
	return parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
