package backup

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meza/vault-backup-cluster/internal/state"
	"github.com/meza/vault-backup-cluster/internal/storage"
	"github.com/meza/vault-backup-cluster/internal/vault"
)

type fakeSnapshotClient struct {
	content []byte
}

func (f fakeSnapshotClient) Snapshot(_ context.Context, writer io.Writer) (vault.SnapshotResult, error) {
	_, err := writer.Write(f.content)
	if err != nil {
		return vault.SnapshotResult{}, err
	}
	return vault.SnapshotResult{Size: int64(len(f.content)), SHA256: "checksum"}, nil
}

func TestExecuteOnceUploadsArtifactMetadataAndPrunesRetention(t *testing.T) {
	backupRoot := t.TempDir()
	stateStore := state.New("node-a")
	destination := storage.NewFileDestination(backupRoot)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := NewService("node-a", time.Hour, t.TempDir(), "snapshots/{{ .Timestamp }}-{{ .NodeID }}.snap", 1, 0, stateStore, fakeSnapshotClient{content: []byte("snapshot-data")}, destination, logger)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	older := filepath.Join(backupRoot, "snapshots", "older.snap")
	if err := os.MkdirAll(filepath.Dir(older), 0o750); err != nil {
		t.Fatalf("mkdir older artifact: %v", err)
	}
	if err := os.WriteFile(older, []byte("old"), 0o600); err != nil {
		t.Fatalf("write older artifact: %v", err)
	}
	if err := os.WriteFile(older+".metadata.json", []byte("{}"), 0o600); err != nil {
		t.Fatalf("write older metadata: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes older artifact: %v", err)
	}
	if err := os.Chtimes(older+".metadata.json", oldTime, oldTime); err != nil {
		t.Fatalf("chtimes older metadata: %v", err)
	}

	if err := service.ExecuteOnce(context.Background()); err != nil {
		t.Fatalf("ExecuteOnce returned error: %v", err)
	}

	objects, err := destination.List("snapshots")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected one artifact and one metadata file, got %d objects", len(objects))
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatalf("expected older artifact to be pruned")
	}
	if stateStore.Snapshot().BackupSuccesses != 1 {
		t.Fatalf("expected one successful backup")
	}

	var metadataPath string
	for _, object := range objects {
		if strings.HasSuffix(object.Name, ".metadata.json") {
			metadataPath = filepath.Join(backupRoot, object.Name)
		}
	}
	if metadataPath == "" {
		t.Fatal("expected metadata object")
	}
	content, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(content, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metadata.NodeID != "node-a" {
		t.Fatalf("expected metadata node id node-a, got %s", metadata.NodeID)
	}
}

func TestRenderArtifactNameRejectsTraversal(t *testing.T) {
	service, err := NewService("node-a", time.Hour, t.TempDir(), "../{{ .NodeID }}", 1, 0, state.New("node-a"), fakeSnapshotClient{}, storage.NewFileDestination(t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	_, err = service.renderArtifactName(time.Now())
	if err == nil || !strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}
