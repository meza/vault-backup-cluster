package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meza/vault-backup-cluster/internal/state"
)

func TestRoutesReportReadinessAndStatus(t *testing.T) {
	stateStore := state.New("node-a")
	now := time.Date(2026, time.March, 30, 11, 30, 0, 0, time.UTC)
	stateStore.SetDependency("consul", true, "", now)
	stateStore.SetDependency("vault", true, "", now)
	stateStore.SetDependency("destination", true, "", now)

	application := &App{state: stateStore, logger: slog.New(slog.NewTextHandler(ioDiscard{}, nil))}
	handler := application.routes()

	readyRequest := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(readyRecorder, readyRequest)
	if readyRecorder.Code != http.StatusOK {
		t.Fatalf("expected readyz to return 200, got %d", readyRecorder.Code)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("expected status to return 200, got %d", statusRecorder.Code)
	}

	var payload struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if payload.NodeID != "node-a" {
		t.Fatalf("expected node id node-a, got %s", payload.NodeID)
	}
}

func TestRoutesExposeMetricsAndDegradedReadiness(t *testing.T) {
	stateStore := state.New("node-a")
	stateStore.SetDependency("consul", false, "down", time.Now().UTC())
	stateStore.MarkAttempt(time.Now().UTC())
	stateStore.MarkFailure(time.Now().UTC(), "boom")

	application := &App{state: stateStore, logger: slog.New(slog.NewTextHandler(ioDiscard{}, nil))}
	handler := application.routes()

	readyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(readyRecorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readyRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected readyz to return 503, got %d", readyRecorder.Code)
	}

	metricsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(metricsRecorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsRecorder.Body.String()
	if metricsRecorder.Code != http.StatusOK {
		t.Fatalf("expected metrics to return 200, got %d", metricsRecorder.Code)
	}
	for _, fragment := range []string{"vault_backup_cluster_backup_attempts_total 1", "vault_backup_cluster_backup_failure_total 1", "vault_backup_cluster_dependency_up{dependency=\"consul\"} 0"} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected metrics to contain %q, got %q", fragment, body)
		}
	}
}

func TestWriteJSONErrorProducesValidJSON(t *testing.T) {
	recorder := httptest.NewRecorder()

	writeJSONError(recorder, http.StatusInternalServerError, errors.New("bad \x1f data"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", recorder.Code)
	}

	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected valid JSON, got %v with body %q", err, recorder.Body.String())
	}
	if payload.Error != "bad \x1f data" {
		t.Fatalf("expected error message to round trip, got %q", payload.Error)
	}
}

func TestRoutesReturnValidJSONWhenStatusEncodingFails(t *testing.T) {
	application := &App{state: failingState{}, logger: slog.New(slog.NewTextHandler(ioDiscard{}, nil))}
	handler := application.routes()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/status", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status to return 500, got %d", recorder.Code)
	}

	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected valid JSON, got %v with body %q", err, recorder.Body.String())
	}
	if payload.Error != "bad \x1f data" {
		t.Fatalf("expected marshaled error message, got %q", payload.Error)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

type failingState struct{}

func (failingState) SetLeader(bool, time.Time) {
}

func (failingState) SetDependency(string, bool, string, time.Time) {
}

func (failingState) Ready() bool {
	return true
}

func (failingState) StatusJSON() ([]byte, error) {
	return nil, errors.New("bad \x1f data")
}

func (failingState) Metrics() string {
	return ""
}
