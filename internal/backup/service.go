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

type scratchFile interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
}

var (
	makeScratchDir   = os.MkdirAll
	createScratchTmp = func(dir string, pattern string) (scratchFile, error) { return os.CreateTemp(dir, pattern) }
	removeScratch    = os.Remove
	marshalMetadata  = json.MarshalIndent
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

//nolint:revive // Constructor wiring is intentionally explicit for the service's required collaborators.
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
			stopTimer(timer)
			return nil
		case <-timer.C:
		}
		if err := s.ExecuteOnce(ctx); err != nil {
			s.logger.Error("backup run failed", "error", err)
		}
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (s *Service) recordFailure(err error) error {
	s.state.MarkFailure(time.Now().UTC(), err.Error())
	return err
}

func (s *Service) cleanupScratchFile(tempFile scratchFile, tempPath string) {
	if tempFile != nil {
		if err := tempFile.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			s.logger.Debug("close scratch artifact during cleanup", "path", tempPath, "error", err)
		}
	}
	if err := removeScratch(tempPath); err != nil && !os.IsNotExist(err) {
		s.logger.Warn("remove scratch artifact during cleanup", "path", tempPath, "error", err)
	}
}

//nolint:revive // The backup flow is kept linear so state transitions and cleanup stay in one place.
func (s *Service) ExecuteOnce(ctx context.Context) error {
	startedAt := time.Now().UTC()
	s.state.MarkAttempt(startedAt)
	if err := s.destination.Check(ctx); err != nil {
		return s.recordFailure(err)
	}
	if err := makeScratchDir(s.scratchDir, 0o750); err != nil {
		return s.recordFailure(fmt.Errorf("create scratch dir: %w", err))
	}
	artifactName, err := s.renderArtifactName(startedAt)
	if err != nil {
		return s.recordFailure(err)
	}
	tempFile, err := createScratchTmp(s.scratchDir, "snapshot-*.snap")
	if err != nil {
		return s.recordFailure(fmt.Errorf("create scratch artifact: %w", err))
	}
	tempPath := tempFile.Name()
	defer func() {
		s.cleanupScratchFile(tempFile, tempPath)
	}()

	result, err := s.vault.Snapshot(ctx, tempFile)
	if err != nil {
		return s.recordFailure(err)
	}
	if syncErr := tempFile.Sync(); syncErr != nil {
		return s.recordFailure(fmt.Errorf("sync scratch artifact: %w", syncErr))
	}
	if closeErr := tempFile.Close(); closeErr != nil {
		return s.recordFailure(fmt.Errorf("close scratch artifact: %w", closeErr))
	}
	tempFile = nil
	if ctxErr := ctx.Err(); ctxErr != nil {
		return s.recordFailure(ctxErr)
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
	metadataContent, err := marshalMetadata(metadata, "", "  ")
	if err != nil {
		return s.recordFailure(fmt.Errorf("marshal artifact metadata: %w", err))
	}
	if err := s.destination.UploadFile(ctx, artifactName, tempPath); err != nil {
		return s.recordFailure(err)
	}
	if err := s.destination.UploadBytes(ctx, artifactName+".metadata.json", metadataContent); err != nil {
		if deleteErr := s.destination.Delete(artifactName); deleteErr != nil {
			s.logger.Warn("cleanup uploaded artifact after metadata failure", "artifact", artifactName, "error", deleteErr)
		}
		return s.recordFailure(err)
	}
	if err := s.applyRetention(path.Dir(artifactName)); err != nil {
		return s.recordFailure(err)
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
		return "", errors.New("render artifact name: empty result")
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
	snapshots := s.retentionSnapshots(prefix, objects)
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].ModTime.After(snapshots[j].ModTime)
	})

	for index, object := range snapshots {
		if !s.shouldDeleteForRetention(index, object.ModTime) {
			continue
		}
		if err := s.deleteRetentionObject(object.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) retentionSnapshots(prefix string, objects []storage.Object) []storage.Object {
	scopedPrefix := path.Clean(prefix)
	snapshots := make([]storage.Object, 0, len(objects))
	for _, object := range objects {
		if !strings.HasSuffix(object.Name, ".snap") {
			continue
		}
		if !withinRetentionPrefix(scopedPrefix, object.Name) {
			continue
		}
		snapshots = append(snapshots, object)
	}
	return snapshots
}

func withinRetentionPrefix(prefix string, objectName string) bool {
	if prefix == "." || prefix == "" {
		return true
	}
	return objectName == prefix || strings.HasPrefix(objectName, prefix+"/")
}

func (s *Service) shouldDeleteForRetention(index int, modTime time.Time) bool {
	deleteForCount := s.retentionCount > 0 && index >= s.retentionCount
	deleteForAge := s.retentionMaxAge > 0 && time.Since(modTime) > s.retentionMaxAge
	return deleteForCount || deleteForAge
}

func (s *Service) deleteRetentionObject(objectName string) error {
	if err := s.destination.Delete(objectName); err != nil {
		return err
	}
	return s.destination.Delete(objectName + ".metadata.json")
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
