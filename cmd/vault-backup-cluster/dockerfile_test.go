package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfilesProvideCurlHealthcheckSupport(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		path string
	}{
		{name: "development image", path: filepath.Join("..", "..", "Dockerfile")},
		{name: "release image", path: filepath.Join("..", "..", "Dockerfile.release")},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			content, err := os.ReadFile(filepath.Clean(tc.path))
			if err != nil {
				t.Fatalf("read dockerfile: %v", err)
			}

			dockerfile := string(content)

			for _, expected := range []string{
				"FROM alpine:",
				"apk add --no-cache ca-certificates curl",
				"HEALTHCHECK",
				"curl -fsS http://127.0.0.1:8080/healthz",
			} {
				if !strings.Contains(dockerfile, expected) {
					t.Fatalf("expected %q in %s", expected, tc.path)
				}
			}
		})
	}
}

func TestReleaseDockerfileUsesTargetPlatformBuildContext(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", "Dockerfile.release")))
	if err != nil {
		t.Fatalf("read dockerfile: %v", err)
	}

	dockerfile := string(content)

	for _, expected := range []string{
		"ARG TARGETPLATFORM",
		"COPY $TARGETPLATFORM/vault-backup-cluster /vault-backup-cluster",
	} {
		if !strings.Contains(dockerfile, expected) {
			t.Fatalf("expected %q in Dockerfile.release", expected)
		}
	}
}
