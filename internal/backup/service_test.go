package backup

import (
	"context"
	"encoding/json"
	"errors"
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

type recordingDestination struct {
	uploadBytesErr error
	uploadedFiles  []string
	deleted        []string
}

func (d *recordingDestination) Check(context.Context) error {
	return nil
}

func (d *recordingDestination) UploadFile(_ context.Context, name string, _ string) error {
	d.uploadedFiles = append(d.uploadedFiles, name)
	return nil
}

func (d *recordingDestination) UploadBytes(_ context.Context, _ string, _ []byte) error {
	return d.uploadBytesErr
}

func (d *recordingDestination) List(string) ([]storage.Object, error) {
	return []storage.Object{}, nil
}

func (d *recordingDestination) Delete(name string) error {
	d.deleted = append(d.deleted, name)
	return nil
}

func TestExecuteOnceDeletesArtifactWhenMetadataUploadFails(t *testing.T) {
	stateStore := state.New("node-a")
	destination := &recordingDestination{uploadBytesErr: errors.New("metadata failed")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := NewService("node-a", time.Hour, t.TempDir(), "snapshots/{{ .Timestamp }}-{{ .NodeID }}.snap", 1, 0, stateStore, fakeSnapshotClient{content: []byte("snapshot-data")}, destination, logger)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	err = service.ExecuteOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "metadata failed") {
		t.Fatalf("expected metadata upload error, got %v", err)
	}
	if len(destination.deleted) != 1 {
		t.Fatalf("expected uploaded artifact cleanup, got %v", destination.deleted)
	}
	if destination.deleted[0] != destination.uploadedFiles[0] {
		t.Fatalf("expected cleanup of uploaded artifact, got deleted=%v uploaded=%v", destination.deleted, destination.uploadedFiles)
	}
}

//nolint:gocyclo // The test verifies the full backup and retention path in one place.
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
	if mkdirErr := os.MkdirAll(filepath.Dir(older), 0o750); mkdirErr != nil {
		t.Fatalf("mkdir older artifact: %v", mkdirErr)
	}
	if writeErr := os.WriteFile(older, []byte("old"), 0o600); writeErr != nil {
		t.Fatalf("write older artifact: %v", writeErr)
	}
	if writeErr := os.WriteFile(older+".metadata.json", []byte("{}"), 0o600); writeErr != nil {
		t.Fatalf("write older metadata: %v", writeErr)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if chtimesErr := os.Chtimes(older, oldTime, oldTime); chtimesErr != nil {
		t.Fatalf("chtimes older artifact: %v", chtimesErr)
	}
	if chtimesErr := os.Chtimes(older+".metadata.json", oldTime, oldTime); chtimesErr != nil {
		t.Fatalf("chtimes older metadata: %v", chtimesErr)
	}

	if executeErr := service.ExecuteOnce(context.Background()); executeErr != nil {
		t.Fatalf("ExecuteOnce returned error: %v", executeErr)
	}

	objects, err := destination.List("snapshots")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected one artifact and one metadata file, got %d objects", len(objects))
	}
	if _, statErr := os.Stat(older); !os.IsNotExist(statErr) {
		t.Fatal("expected older artifact to be pruned")
	}
	if stateStore.Snapshot().BackupSuccesses != 1 {
		t.Fatal("expected one successful backup")
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

func TestExecuteOncePrunesOnlyManagedArtifactsByAge(t *testing.T) {
	backupRoot := t.TempDir()
	stateStore := state.New("node-a")
	destination := storage.NewFileDestination(backupRoot)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := NewService("node-a", time.Hour, t.TempDir(), "snapshots/{{ .Timestamp }}-{{ .NodeID }}.snap", 0, time.Hour, stateStore, fakeSnapshotClient{content: []byte("snapshot-data")}, destination, logger)
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	managed := filepath.Join(backupRoot, "snapshots", "old.snap")
	unmanaged := filepath.Join(backupRoot, "shared", "old.snap")
	for _, path := range []string{managed, unmanaged} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir artifact directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
		if err := os.WriteFile(path+".metadata.json", []byte("{}"), 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
	}

	oldTime := time.Now().Add(-2 * time.Hour)
	for _, path := range []string{managed, managed + ".metadata.json", unmanaged, unmanaged + ".metadata.json"} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("chtimes artifact: %v", err)
		}
	}

	if err := service.ExecuteOnce(context.Background()); err != nil {
		t.Fatalf("ExecuteOnce returned error: %v", err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("expected managed artifact to be pruned, got %v", err)
	}
	if _, err := os.Stat(managed + ".metadata.json"); !os.IsNotExist(err) {
		t.Fatalf("expected managed metadata to be pruned, got %v", err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("expected unmanaged artifact to remain, got %v", err)
	}
	if _, err := os.Stat(unmanaged + ".metadata.json"); err != nil {
		t.Fatalf("expected unmanaged metadata to remain, got %v", err)
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

func TestRenderArtifactNameRejectsParentDirectoryResult(t *testing.T) {
	service, err := NewService("node-a", time.Hour, t.TempDir(), "..", 1, 0, state.New("node-a"), fakeSnapshotClient{}, storage.NewFileDestination(t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	_, err = service.renderArtifactName(time.Now())
	if err == nil || !strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}

func TestNewServiceRequiresLogger(t *testing.T) {
	_, err := NewService("node-a", time.Hour, t.TempDir(), "snapshots/{{ .Timestamp }}.snap", 1, 0, state.New("node-a"), fakeSnapshotClient{}, storage.NewFileDestination(t.TempDir()), nil)
	if err == nil || !errors.Is(err, ErrNoLogger) {
		t.Fatalf("expected ErrNoLogger, got %v", err)
	}
}
