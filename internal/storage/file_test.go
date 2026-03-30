package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileDestinationWritesAndListsObjects(t *testing.T) {
	root := t.TempDir()
	destination := NewFileDestination(root)
	ctx := context.Background()

	sourcePath := filepath.Join(t.TempDir(), "artifact.snap")
	if err := os.WriteFile(sourcePath, []byte("snapshot"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := destination.UploadFile(ctx, "prod/artifact.snap", sourcePath); err != nil {
		t.Fatalf("UploadFile returned error: %v", err)
	}
	if err := destination.UploadBytes(ctx, "prod/artifact.snap.metadata.json", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("UploadBytes returned error: %v", err)
	}

	objects, err := destination.List("prod")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected two objects, got %d", len(objects))
	}
}

func TestFileDestinationRejectsTraversal(t *testing.T) {
	destination := NewFileDestination(t.TempDir())
	if _, err := destination.resolve("../escape"); err == nil {
		t.Fatal("expected traversal error")
	}
}

func TestFileDestinationAcceptsRootsWithTrailingSeparator(t *testing.T) {
	root := t.TempDir() + string(os.PathSeparator)
	destination := NewFileDestination(root)

	resolved, err := destination.resolve("prod/artifact.snap")
	if err != nil {
		t.Fatalf("expected trailing separator root to resolve, got %v", err)
	}
	if resolved != filepath.Join(filepath.Clean(root), "prod", "artifact.snap") {
		t.Fatalf("unexpected resolved path %q", resolved)
	}
}
