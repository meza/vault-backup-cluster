package storage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeAtomicTempFile struct {
	name     string
	writeErr error
	syncErr  error
	closeErr error
}

func (f *fakeAtomicTempFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *fakeAtomicTempFile) Name() string {
	return f.name
}

func (f *fakeAtomicTempFile) Sync() error {
	return f.syncErr
}

func (f *fakeAtomicTempFile) Close() error {
	return f.closeErr
}

type fakeDirEntry struct {
	dir     bool
	infoErr error
}

func (f fakeDirEntry) Name() string               { return "entry" }
func (f fakeDirEntry) IsDir() bool                { return f.dir }
func (f fakeDirEntry) Type() fs.FileMode          { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return fakeFileInfo{}, f.infoErr }

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "entry" }
func (fakeFileInfo) Size() int64        { return 1 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Unix(1, 0) }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

func restoreStorageHooks() {
	makeDir = os.MkdirAll
	openFile = os.Open
	createTempFile = osCreateTempFile
	removeFile = os.Remove
	renameFile = os.Rename
	walkDir = filepath.WalkDir
	relativePath = filepath.Rel
}

func TestCheck(t *testing.T) {
	restoreStorageHooks()
	t.Cleanup(restoreStorageHooks)

	destination := NewFileDestination(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := destination.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}

	makeDir = func(string, os.FileMode) error {
		return errors.New("boom")
	}
	if err := destination.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "create backup location") {
		t.Fatalf("expected mkdir error, got %v", err)
	}

	restoreStorageHooks()
	createTempFile = func(string, string) (atomicFile, error) {
		return nil, errors.New("boom")
	}
	if err := destination.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "create probe file") {
		t.Fatalf("expected probe error, got %v", err)
	}

	restoreStorageHooks()
	if err := destination.Check(context.Background()); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestUploadFileAndBytesErrorPaths(t *testing.T) {
	restoreStorageHooks()
	t.Cleanup(restoreStorageHooks)

	destination := NewFileDestination(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := destination.UploadFile(ctx, "file.snap", "source"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}
	if err := destination.UploadBytes(ctx, "file.snap", []byte("data")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}
	if err := destination.UploadFile(context.Background(), "../file.snap", "source"); err == nil {
		t.Fatal("expected traversal error")
	}
	if err := destination.UploadBytes(context.Background(), "../file.snap", []byte("data")); err == nil {
		t.Fatal("expected traversal error")
	}

	makeDir = func(string, os.FileMode) error {
		return errors.New("boom")
	}
	if err := destination.UploadFile(context.Background(), "prod/file.snap", "source"); err == nil || !strings.Contains(err.Error(), "create destination directory") {
		t.Fatalf("expected mkdir error, got %v", err)
	}
	if err := destination.UploadBytes(context.Background(), "prod/file.snap", []byte("data")); err == nil || !strings.Contains(err.Error(), "create destination directory") {
		t.Fatalf("expected mkdir error, got %v", err)
	}

	restoreStorageHooks()
	if err := destination.UploadFile(context.Background(), "prod/file.snap", filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "open source artifact") {
		t.Fatalf("expected source open error, got %v", err)
	}
}

func TestListCoversErrorBranches(t *testing.T) {
	restoreStorageHooks()
	t.Cleanup(restoreStorageHooks)

	destination := NewFileDestination(t.TempDir())
	if _, err := destination.List("../bad"); err == nil {
		t.Fatal("expected traversal error")
	}

	if entries, err := destination.List("missing"); err != nil || len(entries) != 0 {
		t.Fatalf("expected empty missing list, got %v and %#v", err, entries)
	}

	walkDir = func(string, fs.WalkDirFunc) error {
		return errors.New("boom")
	}
	if _, err := destination.List(""); err == nil || !strings.Contains(err.Error(), "list destination objects") {
		t.Fatalf("expected walk error, got %v", err)
	}

	restoreStorageHooks()
	walkDir = func(root string, walkFn fs.WalkDirFunc) error {
		return walkFn(root, nil, errors.New("boom"))
	}
	if _, err := destination.List(""); err == nil || !strings.Contains(err.Error(), "list destination objects") {
		t.Fatalf("expected callback walk error, got %v", err)
	}

	restoreStorageHooks()
	walkDir = func(root string, walkFn fs.WalkDirFunc) error {
		return walkFn(root, fakeDirEntry{infoErr: errors.New("boom")}, nil)
	}
	if _, err := destination.List(""); err == nil || !strings.Contains(err.Error(), "list destination objects") {
		t.Fatalf("expected info error, got %v", err)
	}

	restoreStorageHooks()
	walkDir = func(root string, walkFn fs.WalkDirFunc) error {
		return walkFn(filepath.Join(root, "file.snap"), fakeDirEntry{}, nil)
	}
	relativePath = func(string, string) (string, error) {
		return "", errors.New("boom")
	}
	if _, err := destination.List(""); err == nil || !strings.Contains(err.Error(), "list destination objects") {
		t.Fatalf("expected relative path error, got %v", err)
	}
}

func TestDeleteAndResolve(t *testing.T) {
	destination := NewFileDestination(t.TempDir())
	if resolved, err := destination.resolve(""); err != nil || resolved != destination.root {
		t.Fatalf("expected root path, got %q and %v", resolved, err)
	}

	path := filepath.Join(destination.root, "artifact.snap")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := destination.Delete("artifact.snap"); err != nil {
		t.Fatalf("expected delete success, got %v", err)
	}
	if err := destination.Delete("missing.snap"); err != nil {
		t.Fatalf("expected missing file to be ignored, got %v", err)
	}
	if err := destination.Delete("../bad"); err == nil {
		t.Fatal("expected traversal error")
	}

	if err := os.Mkdir(filepath.Join(destination.root, "dir"), 0o750); err != nil {
		t.Fatalf("mkdir dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination.root, "dir", "nested"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	if err := destination.Delete("dir"); err == nil || !strings.Contains(err.Error(), "delete dir") {
		t.Fatalf("expected delete error, got %v", err)
	}
}

func TestWriteAtomicallyErrorPaths(t *testing.T) {
	restoreStorageHooks()
	t.Cleanup(restoreStorageHooks)

	createTempFile = func(string, string) (atomicFile, error) {
		return nil, errors.New("boom")
	}
	if err := writeAtomically(context.Background(), filepath.Join(t.TempDir(), "file.snap"), func(io.Writer) error { return nil }); err == nil || !strings.Contains(err.Error(), "create temp destination") {
		t.Fatalf("expected temp creation error, got %v", err)
	}

	restoreStorageHooks()
	createTempFile = func(string, string) (atomicFile, error) {
		return &fakeAtomicTempFile{name: filepath.Join(t.TempDir(), "temp")}, nil
	}
	if err := writeAtomically(context.Background(), filepath.Join(t.TempDir(), "file.snap"), func(io.Writer) error { return errors.New("boom") }); err == nil || !strings.Contains(err.Error(), "write destination content") {
		t.Fatalf("expected write error, got %v", err)
	}

	restoreStorageHooks()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	createTempFile = func(string, string) (atomicFile, error) {
		return &fakeAtomicTempFile{name: filepath.Join(t.TempDir(), "temp")}, nil
	}
	if err := writeAtomically(canceled, filepath.Join(t.TempDir(), "file.snap"), func(io.Writer) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}

	restoreStorageHooks()
	createTempFile = func(string, string) (atomicFile, error) {
		return &fakeAtomicTempFile{name: filepath.Join(t.TempDir(), "temp"), syncErr: errors.New("boom")}, nil
	}
	if err := writeAtomically(context.Background(), filepath.Join(t.TempDir(), "file.snap"), func(io.Writer) error { return nil }); err == nil || !strings.Contains(err.Error(), "sync destination content") {
		t.Fatalf("expected sync error, got %v", err)
	}

	restoreStorageHooks()
	createTempFile = func(string, string) (atomicFile, error) {
		return &fakeAtomicTempFile{name: filepath.Join(t.TempDir(), "temp"), closeErr: errors.New("boom")}, nil
	}
	if err := writeAtomically(context.Background(), filepath.Join(t.TempDir(), "file.snap"), func(io.Writer) error { return nil }); err == nil || !strings.Contains(err.Error(), "close destination content") {
		t.Fatalf("expected close error, got %v", err)
	}

	restoreStorageHooks()
	createTempFile = func(string, string) (atomicFile, error) {
		return &fakeAtomicTempFile{name: filepath.Join(t.TempDir(), "temp")}, nil
	}
	renameFile = func(string, string) error {
		return errors.New("boom")
	}
	if err := writeAtomically(context.Background(), filepath.Join(t.TempDir(), "file.snap"), func(io.Writer) error { return nil }); err == nil || !strings.Contains(err.Error(), "move destination content into place") {
		t.Fatalf("expected rename error, got %v", err)
	}
}
