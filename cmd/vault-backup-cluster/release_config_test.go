package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoReleaserUsesDirectMultiArchDockerPublishing(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", ".goreleaser.yml")))
	if err != nil {
		t.Fatalf("read goreleaser config: %v", err)
	}

	config := string(content)

	for _, expected := range []string{
		"dockers_v2:",
		"platforms:",
		"- linux/amd64",
		"- linux/arm64",
		"docker.io/{{ .Env.DOCKER_USER }}/vault-backup-cluster",
		"- \"{{ .Tag }}\"",
		"- \"v{{ .Major }}\"",
		"- \"v{{ .Major }}.{{ .Minor }}\"",
		"- latest",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("expected %q in .goreleaser.yml", expected)
		}
	}

	if strings.Contains(config, "docker_manifests:") {
		t.Fatal("expected .goreleaser.yml to avoid deprecated docker_manifests")
	}
}
