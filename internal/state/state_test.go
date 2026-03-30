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

func TestSnapshotTracksLeaderAttemptAndDependencyDetails(t *testing.T) {
	store := New("node-a")
	leaderSince := time.Date(2026, time.March, 30, 11, 0, 0, 0, time.UTC)
	attemptAt := leaderSince.Add(5 * time.Minute)
	store.SetLeader(true, leaderSince)
	store.MarkAttempt(attemptAt)
	store.SetDependency("vault", true, "", leaderSince)
	store.SetDependency("consul", false, " down ", leaderSince)

	snapshot := store.Snapshot()
	if !snapshot.Leader {
		t.Fatal("expected leader flag to be true")
	}
	if snapshot.LeaderSince == nil || !snapshot.LeaderSince.Equal(leaderSince) {
		t.Fatalf("expected leader since %s, got %#v", leaderSince, snapshot.LeaderSince)
	}
	if snapshot.ActiveRunStartedAt == nil || !snapshot.ActiveRunStartedAt.Equal(attemptAt) {
		t.Fatalf("expected active run start %s, got %#v", attemptAt, snapshot.ActiveRunStartedAt)
	}
	if len(snapshot.Dependencies) != 2 {
		t.Fatalf("expected two dependencies, got %d", len(snapshot.Dependencies))
	}
	if snapshot.Dependencies[0].Name != "consul" || snapshot.Dependencies[1].Name != "vault" {
		t.Fatalf("expected sorted dependencies, got %#v", snapshot.Dependencies)
	}
	if snapshot.Dependencies[0].Message != "down" {
		t.Fatalf("expected trimmed dependency message, got %q", snapshot.Dependencies[0].Message)
	}

	store.SetLeader(false, leaderSince.Add(10*time.Minute))
	snapshot = store.Snapshot()
	if snapshot.Leader {
		t.Fatal("expected leader flag to be false after release")
	}
	if snapshot.LeaderSince != nil {
		t.Fatalf("expected cleared leader since, got %#v", snapshot.LeaderSince)
	}
	if snapshot.ActiveRunStartedAt != nil {
		t.Fatalf("expected cleared active run, got %#v", snapshot.ActiveRunStartedAt)
	}
}

func TestMetricsIncludeLeaderSuccessFailureAndDependencyLines(t *testing.T) {
	store := New("node-a")
	now := time.Date(2026, time.March, 30, 11, 30, 0, 0, time.UTC)
	store.SetLeader(true, now)
	store.MarkAttempt(now)
	store.MarkSuccess(now, 64, "sum")
	store.MarkFailure(now, "boom")
	store.SetDependency("consul", false, "down", now)

	metrics := store.Metrics()
	for _, fragment := range []string{
		"vault_backup_cluster_is_leader 1",
		"vault_backup_cluster_backup_success_total 1",
		"vault_backup_cluster_backup_failure_total 1",
		"vault_backup_cluster_last_snapshot_size_bytes 64",
		"vault_backup_cluster_last_success_timestamp_seconds 1774870200",
		"vault_backup_cluster_last_failure_timestamp_seconds 1774870200",
		"vault_backup_cluster_dependency_up{dependency=\"consul\"} 0",
	} {
		if !strings.Contains(metrics, fragment) {
			t.Fatalf("expected metrics to contain %q, got %q", fragment, metrics)
		}
	}
}
