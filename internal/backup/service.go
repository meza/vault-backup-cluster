package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/meza/vault-backup-cluster/internal/schedule"
	"github.com/meza/vault-backup-cluster/internal/state"
	"github.com/meza/vault-backup-cluster/internal/storage"
	"github.com/meza/vault-backup-cluster/internal/vault"
)

type SnapshotClient interface {
	Snapshot(ctx context.Context, writer io.Writer) (vault.SnapshotResult, error)
}

type Destination interface {
	Check(ctx context.Context) error
	UploadFile(ctx context.Context, name string, sourcePath string) error
	UploadBytes(ctx context.Context, name string, content []byte) error
	List(prefix string) ([]storage.Object, error)
	Delete(name string) error
}

type Service struct {
	nodeID           string
	schedule         schedule.Interval
	scratchDir       string
	artifactTemplate *template.Template
	retentionCount   int
	retentionMaxAge  time.Duration
	state            *state.Store
	vault            SnapshotClient
	destination      Destination
	logger           *slog.Logger
}

type Metadata struct {
	NodeID       string    `json:"node_id"`
	ArtifactName string    `json:"artifact_name"`
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at"`
	SizeBytes    int64     `json:"size_bytes"`
	SHA256       string    `json:"sha256"`
	Result       string    `json:"result"`
}

type TemplateData struct {
	NodeID    string
	Timestamp string
	Unix      int64
}

func NewService(nodeID string, every time.Duration, scratchDir string, artifactNameTemplate string, retentionCount int, retentionMaxAge time.Duration, stateStore *state.Store, snapshotClient SnapshotClient, destination Destination, logger *slog.Logger) (*Service, error) {
	if err := ValidateLogger(logger); err != nil {
		return nil, err
	}
	tmpl, err := template.New("artifact").Parse(strings.TrimSpace(artifactNameTemplate))
	if err != nil {
		return nil, fmt.Errorf("parse artifact name template: %w", err)
	}
	return &Service{
		nodeID:           nodeID,
		schedule:         schedule.New(every),
		scratchDir:       scratchDir,
		artifactTemplate: tmpl,
		retentionCount:   retentionCount,
		retentionMaxAge:  retentionMaxAge,
		state:            stateStore,
		vault:            snapshotClient,
		destination:      destination,
		logger:           logger,
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	for {
		next := s.schedule.Next(time.Now())
		wait := time.Until(next)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
		if err := s.ExecuteOnce(ctx); err != nil {
			s.logger.Error("backup run failed", "error", err)
		}
	}
}

func (s *Service) ExecuteOnce(ctx context.Context) error {
	startedAt := time.Now().UTC()
	s.state.MarkAttempt(startedAt)
	if err := s.destination.Check(ctx); err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return err
	}
	if err := os.MkdirAll(s.scratchDir, 0o750); err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return fmt.Errorf("create scratch dir: %w", err)
	}
	artifactName, err := s.renderArtifactName(startedAt)
	if err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return err
	}
	tempFile, err := os.CreateTemp(s.scratchDir, "snapshot-*.snap")
	if err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return fmt.Errorf("create scratch artifact: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	result, err := s.vault.Snapshot(ctx, tempFile)
	if err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return err
	}
	if err := tempFile.Sync(); err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return fmt.Errorf("sync scratch artifact: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return fmt.Errorf("close scratch artifact: %w", err)
	}
	if err := ctx.Err(); err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return err
	}

	metadata := Metadata{
		NodeID:       s.nodeID,
		ArtifactName: artifactName,
		StartedAt:    startedAt,
		CompletedAt:  time.Now().UTC(),
		SizeBytes:    result.Size,
		SHA256:       result.SHA256,
		Result:       "success",
	}
	metadataContent, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return fmt.Errorf("marshal artifact metadata: %w", err)
	}
	if err := s.destination.UploadFile(ctx, artifactName, tempPath); err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return err
	}
	if err := s.destination.UploadBytes(ctx, artifactName+".metadata.json", metadataContent); err != nil {
		_ = s.destination.Delete(artifactName)
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return err
	}
	if err := s.applyRetention(path.Dir(artifactName)); err != nil {
		s.state.MarkFailure(time.Now().UTC(), err.Error())
		return err
	}
	completedAt := time.Now().UTC()
	s.state.MarkSuccess(completedAt, result.Size, result.SHA256)
	s.logger.Info("backup run completed", "artifact", artifactName, "size_bytes", result.Size, "checksum_sha256", result.SHA256)
	return nil
}

func (s *Service) renderArtifactName(now time.Time) (string, error) {
	var builder strings.Builder
	data := TemplateData{NodeID: s.nodeID, Timestamp: now.UTC().Format("20060102T150405Z"), Unix: now.UTC().Unix()}
	if err := s.artifactTemplate.Execute(&builder, data); err != nil {
		return "", fmt.Errorf("render artifact name: %w", err)
	}
	name := path.Clean(strings.TrimSpace(builder.String()))
	if name == "." || name == "" {
		return "", fmt.Errorf("render artifact name: empty result")
	}
	if name == ".." || path.IsAbs(name) || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return "", fmt.Errorf("render artifact name: invalid path %q", name)
	}
	return name, nil
}

func (s *Service) applyRetention(prefix string) error {
	objects, err := s.destination.List(prefix)
	if err != nil {
		return err
	}
	snapshots := make([]storage.Object, 0)
	for _, object := range objects {
		if strings.HasSuffix(object.Name, ".snap") {
			snapshots = append(snapshots, object)
		}
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].ModTime.After(snapshots[j].ModTime)
	})

	for index, object := range snapshots {
		deleteForCount := s.retentionCount > 0 && index >= s.retentionCount
		deleteForAge := s.retentionMaxAge > 0 && time.Since(object.ModTime) > s.retentionMaxAge
		if !deleteForCount && !deleteForAge {
			continue
		}
		if err := s.destination.Delete(object.Name); err != nil {
			return err
		}
		if err := s.destination.Delete(object.Name + ".metadata.json"); err != nil {
			return err
		}
	}
	return nil
}

var ErrNoLogger = errors.New("logger is required")

func ValidateLogger(logger *slog.Logger) error {
	if logger == nil {
		return ErrNoLogger
	}
	return nil
}

func ScratchArtifactPath(root string, artifactName string) string {
	return filepath.Join(root, filepath.Base(artifactName))
}
