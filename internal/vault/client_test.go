package vault

import (
"context"
"io"
"net/http"
"net/http/httptest"
"os"
"path/filepath"
"testing"
"time"
)

func TestSnapshotReadsTokenFileForEachRequest(t *testing.T) {
tokenPath := filepath.Join(t.TempDir(), "token")
if err := os.WriteFile(tokenPath, []byte("first-token"), 0o600); err != nil {
t.Fatalf("write token file: %v", err)
}

calls := make([]string, 0, 2)
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
calls = append(calls, r.Header.Get("X-Vault-Token"))
_, _ = w.Write([]byte("snapshot"))
}))
defer server.Close()

source, err := NewTokenSource("", tokenPath)
if err != nil {
t.Fatalf("NewTokenSource returned error: %v", err)
}
client := NewClient(server.URL, time.Minute, source)

if _, err := client.Snapshot(context.Background(), io.Discard); err != nil {
t.Fatalf("first Snapshot returned error: %v", err)
}
if err := os.WriteFile(tokenPath, []byte("second-token"), 0o600); err != nil {
t.Fatalf("rewrite token file: %v", err)
}
if _, err := client.Snapshot(context.Background(), io.Discard); err != nil {
t.Fatalf("second Snapshot returned error: %v", err)
}

if len(calls) != 2 {
t.Fatalf("expected two requests, got %d", len(calls))
}
if calls[0] != "first-token" || calls[1] != "second-token" {
t.Fatalf("expected rotated tokens, got %#v", calls)
}
}

func TestHealthAcceptsHealthyVault(t *testing.T) {
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
w.WriteHeader(http.StatusOK)
}))
defer server.Close()

client := NewClient(server.URL, time.Minute, StaticTokenSource{value: "token"})
if err := client.Health(context.Background()); err != nil {
t.Fatalf("Health returned error: %v", err)
}
}
