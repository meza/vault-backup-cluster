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
	"text/template"
	"time"

	"github.com/meza/vault-backup-cluster/internal/schedule"
	"github.com/meza/vault-backup-cluster/internal/state"
	"github.com/meza/vault-backup-cluster/internal/storage"
	"github.com/meza/vault-backup-cluster/internal/vault"
)

type stubSnapshotClient struct {
	content   []byte
	result    vault.SnapshotResult
	err       error
	afterCall func()
}

func (s stubSnapshotClient) Snapshot(_ context.Context, writer io.Writer) (vault.SnapshotResult, error) {
	if len(s.content) > 0 {
		if _, err := writer.Write(s.content); err != nil {
			return vault.SnapshotResult{}, err
		}
	}
	if s.afterCall != nil {
		s.afterCall()
	}
	if s.result.Size == 0 && len(s.content) > 0 {
		s.result.Size = int64(len(s.content))
	}
	if s.result.SHA256 == "" && len(s.content) > 0 {
		s.result.SHA256 = "checksum"
	}
	return s.result, s.err
}

type stubDestination struct {
	checkErr       error
	checkFn        func(context.Context) error
	uploadFileErr  error
	uploadBytesErr error
	listErr        error
	listObjects    []storage.Object
	deleteErrs     map[string]error
	deleted        []string
}

func (s *stubDestination) Check(ctx context.Context) error {
	if s.checkFn != nil {
		return s.checkFn(ctx)
	}
	return s.checkErr
}

func (s *stubDestination) UploadFile(context.Context, string, string) error {
	return s.uploadFileErr
}

func (s *stubDestination) UploadBytes(context.Context, string, []byte) error {
	return s.uploadBytesErr
}

func (s *stubDestination) List(string) ([]storage.Object, error) {
	return s.listObjects, s.listErr
}

func (s *stubDestination) Delete(name string) error {
	s.deleted = append(s.deleted, name)
	if s.deleteErrs != nil {
		if wildcardErr, ok := s.deleteErrs["*"]; ok {
			return wildcardErr
		}
		return s.deleteErrs[name]
	}
	return nil
}

type stubScratchFile struct {
	name     string
	syncErr  error
	closeErr error
	closeFn  func() error
	closeCnt *int
}

func (s *stubScratchFile) Write(p []byte) (int, error) {
	return len(p), nil
}

func (s *stubScratchFile) Name() string {
	return s.name
}

func (s *stubScratchFile) Sync() error {
	return s.syncErr
}

func (s *stubScratchFile) Close() error {
	if s.closeCnt != nil {
		*s.closeCnt++
	}
	if s.closeFn != nil {
		return s.closeFn()
	}
	return s.closeErr
}

type agePruneDestination struct {
	objects []storage.Object
	deleted []string
}

func (d *agePruneDestination) Check(context.Context) error {
	return nil
}

func (d *agePruneDestination) UploadFile(context.Context, string, string) error {
	return nil
}

func (d *agePruneDestination) UploadBytes(context.Context, string, []byte) error {
	return nil
}

func (d *agePruneDestination) List(string) ([]storage.Object, error) {
	return d.objects, nil
}

func (d *agePruneDestination) Delete(name string) error {
	d.deleted = append(d.deleted, name)
	return nil
}

func restoreBackupHooks() {
	makeScratchDir = os.MkdirAll
	createScratchTmp = func(dir string, pattern string) (scratchFile, error) { return os.CreateTemp(dir, pattern) }
	removeScratch = os.Remove
	marshalMetadata = json.MarshalIndent
}

func newServiceForCoverage(t *testing.T, snapshot SnapshotClient, destination Destination) (*Service, *state.Store) {
	t.Helper()

	stateStore := state.New("node-a")
	service, err := NewService("node-a", time.Hour, t.TempDir(), "snapshots/{{ .Timestamp }}.snap", 1, time.Hour, stateStore, snapshot, destination, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	return service, stateStore
}

func TestNewServiceRejectsInvalidTemplate(t *testing.T) {
	if _, err := NewService("node-a", time.Hour, t.TempDir(), "{{", 1, 0, state.New("node-a"), stubSnapshotClient{}, &stubDestination{}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil || !strings.Contains(err.Error(), "parse artifact name template") {
		t.Fatalf("expected template parse error, got %v", err)
	}
}

func TestRunHandlesCancellationAndSingleExecution(t *testing.T) {
	service, _ := newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Run(ctx); err != nil {
		t.Fatalf("expected nil error on canceled context, got %v", err)
	}

	calls := 0
	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service, stateStore := newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot"), afterCall: cancel}, &stubDestination{
		checkFn: func(context.Context) error {
			calls++
			return nil
		},
	})
	service.schedule = schedule.New(0)
	if err := service.Run(ctx); err != nil {
		t.Fatalf("expected nil error after one run, got %v", err)
	}
	if calls == 0 || stateStore.Snapshot().BackupAttempts == 0 {
		t.Fatalf("expected at least one execution, got %d checks and snapshot %#v", calls, stateStore.Snapshot())
	}
}

//nolint:gocyclo // The test keeps the backup error matrix together so the seams stay readable.
func TestExecuteOnceErrorPaths(t *testing.T) {
	restoreBackupHooks()
	t.Cleanup(restoreBackupHooks)

	service, stateStore := newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{checkErr: errors.New("boom")})
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected destination check error, got %v", err)
	}
	if stateStore.Snapshot().BackupFailures != 1 {
		t.Fatalf("expected failure state update, got %#v", stateStore.Snapshot())
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{})
	makeScratchDir = func(string, os.FileMode) error {
		return errors.New("boom")
	}
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "create scratch dir") {
		t.Fatalf("expected scratch dir error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{})
	service.artifactTemplate = template.Must(template.New("artifact").Funcs(template.FuncMap{
		"boom": func() (string, error) { return "", errors.New("boom") },
	}).Parse(`{{ boom }}`))
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "render artifact name") {
		t.Fatalf("expected render error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{})
	service.artifactTemplate = template.Must(template.New("artifact").Parse(` `))
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "empty result") {
		t.Fatalf("expected empty render error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{})
	createScratchTmp = func(string, string) (scratchFile, error) {
		return nil, errors.New("boom")
	}
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "create scratch artifact") {
		t.Fatalf("expected scratch temp error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{err: errors.New("boom")}, &stubDestination{})
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected snapshot error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot")}, &stubDestination{})
	createScratchTmp = func(string, string) (scratchFile, error) {
		return &stubScratchFile{name: filepath.Join(t.TempDir(), "snap"), syncErr: errors.New("boom")}, nil
	}
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "sync scratch artifact") {
		t.Fatalf("expected sync error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot")}, &stubDestination{})
	createScratchTmp = func(string, string) (scratchFile, error) {
		return &stubScratchFile{name: filepath.Join(t.TempDir(), "snap"), closeErr: errors.New("boom")}, nil
	}
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "close scratch artifact") {
		t.Fatalf("expected close error, got %v", err)
	}

	restoreBackupHooks()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service, _ = newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot"), afterCall: cancel}, &stubDestination{})
	if err := service.ExecuteOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot")}, &stubDestination{})
	marshalMetadata = func(any, string, string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "marshal artifact metadata") {
		t.Fatalf("expected metadata marshal error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot")}, &stubDestination{uploadFileErr: errors.New("boom")})
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected upload file error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot")}, &stubDestination{listErr: errors.New("boom")})
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected retention error, got %v", err)
	}

	restoreBackupHooks()
	service, _ = newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot")}, &stubDestination{
		uploadBytesErr: errors.New("boom"),
		deleteErrs:     map[string]error{"*": errors.New("cleanup boom")},
	})
	if err := service.ExecuteOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected metadata upload error, got %v", err)
	}
}

func TestApplyRetentionErrorPaths(t *testing.T) {
	service, _ := newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{listErr: errors.New("boom")})
	if err := service.applyRetention("snapshots"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected list error, got %v", err)
	}

	destination := &stubDestination{
		listObjects: []storage.Object{{Name: "snapshots/a.snap", ModTime: time.Now().Add(-2 * time.Hour)}},
		deleteErrs:  map[string]error{"snapshots/a.snap": errors.New("boom")},
	}
	service, _ = newServiceForCoverage(t, stubSnapshotClient{}, destination)
	service.retentionCount = 0
	service.retentionMaxAge = time.Hour
	if err := service.applyRetention("snapshots"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected snapshot delete error, got %v", err)
	}

	destination = &stubDestination{
		listObjects: []storage.Object{{Name: "snapshots/a.snap", ModTime: time.Now().Add(-2 * time.Hour)}},
		deleteErrs:  map[string]error{"snapshots/a.snap.metadata.json": errors.New("boom")},
	}
	service, _ = newServiceForCoverage(t, stubSnapshotClient{}, destination)
	service.retentionCount = 0
	service.retentionMaxAge = time.Hour
	if err := service.applyRetention("snapshots"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected metadata delete error, got %v", err)
	}
}

func TestApplyRetentionPrunesOldSnapshotsWithinPrefix(t *testing.T) {
	destination := &agePruneDestination{
		objects: []storage.Object{
			{Name: "snapshots/new.snap", ModTime: time.Now()},
			{Name: "snapshots/old.snap", ModTime: time.Now().Add(-2 * time.Hour)},
			{Name: "shared/unrelated.snap", ModTime: time.Now().Add(-24 * time.Hour)},
		},
	}
	service, _ := newServiceForCoverage(t, stubSnapshotClient{}, destination)
	service.retentionCount = 0
	service.retentionMaxAge = time.Hour

	if err := service.applyRetention("snapshots"); err != nil {
		t.Fatalf("expected age pruning success, got %v", err)
	}

	if got := strings.Join(destination.deleted, ","); got != "snapshots/old.snap,snapshots/old.snap.metadata.json" {
		t.Fatalf("unexpected deleted objects %q", got)
	}
}

func TestCleanupScratchFileIgnoresCleanupErrors(t *testing.T) {
	restoreBackupHooks()
	t.Cleanup(restoreBackupHooks)

	service, _ := newServiceForCoverage(t, stubSnapshotClient{}, &stubDestination{})
	removeScratch = func(string) error {
		return errors.New("boom")
	}

	service.cleanupScratchFile(&stubScratchFile{name: "scratch", closeErr: errors.New("boom")}, "scratch")
}

func TestExecuteOnceClosesScratchArtifactOnceOnSuccess(t *testing.T) {
	restoreBackupHooks()
	t.Cleanup(restoreBackupHooks)

	closeCalls := 0
	service, _ := newServiceForCoverage(t, stubSnapshotClient{content: []byte("snapshot")}, &stubDestination{})
	createScratchTmp = func(string, string) (scratchFile, error) {
		return &stubScratchFile{name: filepath.Join(t.TempDir(), "snap"), closeCnt: &closeCalls}, nil
	}

	if err := service.ExecuteOnce(context.Background()); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("expected one close call, got %d", closeCalls)
	}
}

func TestWithinRetentionPrefix(t *testing.T) {
	if !withinRetentionPrefix(".", "shared/unrelated.snap") {
		t.Fatal("expected dot prefix to match all objects")
	}
	if !withinRetentionPrefix("", "shared/unrelated.snap") {
		t.Fatal("expected empty prefix to match all objects")
	}
	if !withinRetentionPrefix("snapshots", "snapshots/old.snap") {
		t.Fatal("expected snapshots prefix to match nested object")
	}
	if withinRetentionPrefix("snapshots", "shared/unrelated.snap") {
		t.Fatal("expected unrelated object to be excluded")
	}
}

func TestScratchArtifactPath(t *testing.T) {
	scratchRoot := filepath.Join(t.TempDir(), "scratch")
	expected := filepath.Join(scratchRoot, "file.snap")
	if got := ScratchArtifactPath(scratchRoot, "snapshots/path/file.snap"); got != expected {
		t.Fatalf("unexpected scratch path %q", got)
	}
}

func TestStopTimer(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	stopTimer(timer)

	timer = time.NewTimer(0)
	<-timer.C
	stopTimer(timer)
}
