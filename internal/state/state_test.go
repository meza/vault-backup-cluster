package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReadyIsFalseWithoutDependencyChecks(t *testing.T) {
	if New("node-a").Ready() {
		t.Fatal("expected store without dependency checks to be unready")
	}
}

func TestReadyIsTrueWhenAllDependenciesAreHealthy(t *testing.T) {
	store := New("node-a")
	store.SetDependency("vault", true, "", time.Now().UTC())

	if !store.Ready() {
		t.Fatal("expected store with healthy dependencies to be ready")
	}
}

func TestMarkAttemptRecordsBackupAttempt(t *testing.T) {
	store := New("node-a")

	store.MarkAttempt(time.Now().UTC())

	if store.Snapshot().BackupAttempts != 1 {
		t.Fatalf("expected one backup attempt, got %d", store.Snapshot().BackupAttempts)
	}
}

func TestMarkSuccessRecordsSnapshotResult(t *testing.T) {
	store := New("node-a")

	store.MarkSuccess(time.Now().UTC(), 42, "checksum")

	snapshot := store.Snapshot()
	if snapshot.BackupSuccesses != 1 {
		t.Fatalf("expected one backup success, got %d", snapshot.BackupSuccesses)
	}
	if snapshot.LastSnapshotSizeBytes != 42 {
		t.Fatalf("expected snapshot size 42, got %d", snapshot.LastSnapshotSizeBytes)
	}
	if snapshot.LastSnapshotChecksum != "checksum" {
		t.Fatalf("expected checksum to be recorded, got %q", snapshot.LastSnapshotChecksum)
	}
}

func TestMarkFailureRecordsFailureReason(t *testing.T) {
	store := New("node-a")

	store.MarkFailure(time.Now().UTC(), "boom")

	snapshot := store.Snapshot()
	if snapshot.BackupFailures != 1 {
		t.Fatalf("expected one backup failure, got %d", snapshot.BackupFailures)
	}
	if snapshot.LastFailureReason != "boom" {
		t.Fatalf("expected failure reason to be recorded, got %q", snapshot.LastFailureReason)
	}
}

func TestReadyIsFalseWhenAnyDependencyIsUnhealthy(t *testing.T) {
	store := New("node-a")
	now := time.Now().UTC()
	store.SetDependency("vault", true, "", now)
	store.SetDependency("consul", false, "down", now)

	if store.Ready() {
		t.Fatal("expected store with an unhealthy dependency to be unready")
	}
}

func TestStatusJSONIncludesNodeID(t *testing.T) {
	store := New("node-a")

	status, err := store.StatusJSON()
	if err != nil {
		t.Fatalf("expected status json without error, got %v", err)
	}

	var payload struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(status, &payload); err != nil {
		t.Fatalf("expected valid status json, got %v", err)
	}
	if payload.NodeID != "node-a" {
		t.Fatalf("expected node id node-a, got %q", payload.NodeID)
	}
}

func TestMetricsIncludesBackupAttemptCount(t *testing.T) {
	store := New("node-a")
	store.MarkAttempt(time.Now().UTC())

	metrics := store.Metrics()
	if !strings.Contains(metrics, "vault_backup_cluster_backup_attempts_total 1") {
		t.Fatalf("expected backup attempt metric, got %q", metrics)
	}
}
