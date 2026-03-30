package state

import (
"encoding/json"
"fmt"
"sort"
"strings"
"sync"
"time"
)

type DependencyStatus struct {
Name      string     `json:"name"`
OK        bool       `json:"ok"`
Message   string     `json:"message,omitempty"`
CheckedAt *time.Time `json:"checked_at,omitempty"`
}

type Snapshot struct {
NodeID                string             `json:"node_id"`
Leader                bool               `json:"leader"`
LeaderSince           *time.Time         `json:"leader_since,omitempty"`
ActiveRunStartedAt    *time.Time         `json:"active_run_started_at,omitempty"`
LastAttemptAt         *time.Time         `json:"last_attempt_at,omitempty"`
LastSuccessAt         *time.Time         `json:"last_success_at,omitempty"`
LastFailureAt         *time.Time         `json:"last_failure_at,omitempty"`
LastFailureReason     string             `json:"last_failure_reason,omitempty"`
LastSnapshotSizeBytes int64              `json:"last_snapshot_size_bytes"`
LastSnapshotChecksum  string             `json:"last_snapshot_checksum,omitempty"`
BackupAttempts        uint64             `json:"backup_attempts"`
BackupSuccesses       uint64             `json:"backup_successes"`
BackupFailures        uint64             `json:"backup_failures"`
Dependencies          []DependencyStatus `json:"dependencies"`
}

type Store struct {
mu   sync.RWMutex
snap Snapshot
deps map[string]DependencyStatus
}

func New(nodeID string) *Store {
return &Store{
snap: Snapshot{NodeID: nodeID},
deps: map[string]DependencyStatus{},
}
}

func (s *Store) SetLeader(leader bool, now time.Time) {
s.mu.Lock()
defer s.mu.Unlock()

s.snap.Leader = leader
if leader {
ts := now.UTC()
s.snap.LeaderSince = &ts
return
}
s.snap.LeaderSince = nil
s.snap.ActiveRunStartedAt = nil
}

func (s *Store) MarkAttempt(now time.Time) {
s.mu.Lock()
defer s.mu.Unlock()
ts := now.UTC()
s.snap.BackupAttempts++
s.snap.LastAttemptAt = &ts
s.snap.ActiveRunStartedAt = &ts
}

func (s *Store) MarkSuccess(now time.Time, size int64, checksum string) {
s.mu.Lock()
defer s.mu.Unlock()
ts := now.UTC()
s.snap.BackupSuccesses++
s.snap.LastSuccessAt = &ts
s.snap.ActiveRunStartedAt = nil
s.snap.LastFailureReason = ""
s.snap.LastSnapshotSizeBytes = size
s.snap.LastSnapshotChecksum = checksum
}

func (s *Store) MarkFailure(now time.Time, reason string) {
s.mu.Lock()
defer s.mu.Unlock()
ts := now.UTC()
s.snap.BackupFailures++
s.snap.LastFailureAt = &ts
s.snap.ActiveRunStartedAt = nil
s.snap.LastFailureReason = strings.TrimSpace(reason)
}

func (s *Store) SetDependency(name string, ok bool, message string, checkedAt time.Time) {
s.mu.Lock()
defer s.mu.Unlock()
ts := checkedAt.UTC()
s.deps[name] = DependencyStatus{
Name:      name,
OK:        ok,
Message:   strings.TrimSpace(message),
CheckedAt: &ts,
}
}

func (s *Store) Snapshot() Snapshot {
s.mu.RLock()
defer s.mu.RUnlock()

copy := s.snap
copy.Dependencies = make([]DependencyStatus, 0, len(s.deps))
for _, dep := range s.deps {
copy.Dependencies = append(copy.Dependencies, dep)
}
sort.Slice(copy.Dependencies, func(i, j int) bool {
return copy.Dependencies[i].Name < copy.Dependencies[j].Name
})
return copy
}

func (s *Store) Ready() bool {
snap := s.Snapshot()
if len(snap.Dependencies) == 0 {
return false
}
for _, dep := range snap.Dependencies {
if !dep.OK {
return false
}
}
return true
}

func (s *Store) StatusJSON() ([]byte, error) {
return json.MarshalIndent(s.Snapshot(), "", "  ")
}

func (s *Store) Metrics() string {
snap := s.Snapshot()
var lines []string
lines = append(lines,
"# HELP vault_backup_cluster_is_leader Whether this node currently holds leadership.",
"# TYPE vault_backup_cluster_is_leader gauge",
fmt.Sprintf("vault_backup_cluster_is_leader %d", boolToInt(snap.Leader)),
"# HELP vault_backup_cluster_backup_attempts_total Total attempted backups.",
"# TYPE vault_backup_cluster_backup_attempts_total counter",
fmt.Sprintf("vault_backup_cluster_backup_attempts_total %d", snap.BackupAttempts),
"# HELP vault_backup_cluster_backup_success_total Total successful backups.",
"# TYPE vault_backup_cluster_backup_success_total counter",
fmt.Sprintf("vault_backup_cluster_backup_success_total %d", snap.BackupSuccesses),
"# HELP vault_backup_cluster_backup_failure_total Total failed backups.",
"# TYPE vault_backup_cluster_backup_failure_total counter",
fmt.Sprintf("vault_backup_cluster_backup_failure_total %d", snap.BackupFailures),
"# HELP vault_backup_cluster_last_snapshot_size_bytes Size of the last successful snapshot.",
"# TYPE vault_backup_cluster_last_snapshot_size_bytes gauge",
fmt.Sprintf("vault_backup_cluster_last_snapshot_size_bytes %d", snap.LastSnapshotSizeBytes),
)
if snap.LastSuccessAt != nil {
lines = append(lines,
"# HELP vault_backup_cluster_last_success_timestamp_seconds Unix time of the last successful backup.",
"# TYPE vault_backup_cluster_last_success_timestamp_seconds gauge",
fmt.Sprintf("vault_backup_cluster_last_success_timestamp_seconds %d", snap.LastSuccessAt.Unix()),
)
}
if snap.LastFailureAt != nil {
lines = append(lines,
"# HELP vault_backup_cluster_last_failure_timestamp_seconds Unix time of the last failed backup.",
"# TYPE vault_backup_cluster_last_failure_timestamp_seconds gauge",
fmt.Sprintf("vault_backup_cluster_last_failure_timestamp_seconds %d", snap.LastFailureAt.Unix()),
)
}
for _, dep := range snap.Dependencies {
lines = append(lines, fmt.Sprintf("vault_backup_cluster_dependency_up{dependency=%q} %d", dep.Name, boolToInt(dep.OK)))
}
return strings.Join(lines, "\n") + "\n"
}

func boolToInt(value bool) int {
if value {
return 1
}
return 0
}
