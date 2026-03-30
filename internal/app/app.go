package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"

	"github.com/meza/vault-backup-cluster/internal/backup"
	"github.com/meza/vault-backup-cluster/internal/config"
	"github.com/meza/vault-backup-cluster/internal/consulx"
	"github.com/meza/vault-backup-cluster/internal/state"
	"github.com/meza/vault-backup-cluster/internal/storage"
	"github.com/meza/vault-backup-cluster/internal/vault"
)

type App struct {
	cfg          config.Config
	logger       *slog.Logger
	state        appState
	server       httpServer
	elector      electionRunner
	backup       backupRunner
	vaultClient  vaultProber
	consulClient *consulapi.Client
	destination  destinationProber
}

type httpServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

type electionRunner interface {
	Run(context.Context, func(context.Context) error) error
}

type backupRunner interface {
	Run(context.Context) error
}

type vaultProber interface {
	Health(context.Context) error
}

type destinationProber interface {
	Check(context.Context) error
}

var (
	loadConfig      = config.Load
	newTokenSource  = vault.NewTokenSource
	newVaultClient  = vault.NewClient
	newConsulClient = consulx.NewClient
	newBackup       = backup.NewService
	newElector      = consulx.NewElector
	checkConsul     = consulx.Check
)

type appState interface {
	SetLeader(bool, time.Time)
	SetDependency(string, bool, string, time.Time)
	Ready() bool
	StatusJSON() ([]byte, error)
	Metrics() string
}

func New() (*App, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	stateStore := state.New(cfg.NodeID)

	vaultTokens, err := newTokenSource(cfg.VaultToken, cfg.VaultTokenFile)
	if err != nil {
		return nil, err
	}
	vaultClient := newVaultClient(cfg.VaultAddr, cfg.VaultRequestTimeout, vaultTokens)
	consulTokens, err := newTokenSource(cfg.ConsulToken, cfg.ConsulTokenFile)
	if err != nil && cfg.ConsulToken == "" && cfg.ConsulTokenFile == "" {
		consulTokens = nil
	} else if err != nil {
		return nil, err
	}
	consulClient, err := newConsulClient(cfg.ConsulAddr, consulTokens)
	if err != nil {
		return nil, err
	}
	destination := storage.NewFileDestination(cfg.BackupLocation)
	backupService, err := newBackup(cfg.NodeID, cfg.BackupSchedule, cfg.ScratchDir, cfg.ArtifactNameTemplate, cfg.RetentionCount, cfg.RetentionMaxAge, stateStore, vaultClient, destination, logger)
	if err != nil {
		return nil, err
	}
	elector := newElector(consulClient, cfg.ConsulLockKey, cfg.NodeID, cfg.ConsulSessionTTL, cfg.ConsulLockWait)

	app := &App{
		cfg:          cfg,
		logger:       logger,
		state:        stateStore,
		elector:      elector,
		backup:       backupService,
		vaultClient:  vaultClient,
		consulClient: consulClient,
		destination:  destination,
	}
	app.server = &http.Server{Addr: cfg.HTTPBindAddress, Handler: app.routes()}
	return app, nil
}

func (a *App) Run(ctx context.Context) error {
	serverErrors := make(chan error, 1)
	go func() {
		a.logger.Info("http server starting", "address", a.cfg.HTTPBindAddress)
		serverErrors <- a.server.ListenAndServe()
	}()

	probeCtx, stopProbes := context.WithCancel(ctx)
	defer stopProbes()
	go a.runProbes(probeCtx)

	electionErrors := make(chan error, 1)
	go func() {
		electionErrors <- a.elector.Run(ctx, func(leaderCtx context.Context) error {
			a.state.SetLeader(true, time.Now().UTC())
			defer func() {
				a.state.SetLeader(false, time.Now().UTC())
			}()
			a.logger.Info("leadership acquired", "node_id", a.cfg.NodeID)
			err := a.backup.Run(leaderCtx)
			if err == nil || errors.Is(err, context.Canceled) {
				a.logger.Info("leadership released", "node_id", a.cfg.NodeID)
			}
			return err
		})
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case err := <-electionErrors:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.server.Shutdown(shutdownCtx)
}

func (a *App) runProbes(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.ProbeInterval)
	defer ticker.Stop()
	check := func() {
		now := time.Now().UTC()
		if err := checkConsul(ctx, a.consulClient); err != nil {
			a.state.SetDependency("consul", false, err.Error(), now)
		} else {
			a.state.SetDependency("consul", true, "", now)
		}
		if err := a.vaultClient.Health(ctx); err != nil {
			a.state.SetDependency("vault", false, err.Error(), now)
		} else {
			a.state.SetDependency("vault", true, "", now)
		}
		if err := a.destination.Check(ctx); err != nil {
			a.state.SetDependency("destination", false, err.Error(), now)
		} else {
			a.state.SetDependency("destination", true, "", now)
		}
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if !a.state.Ready() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"status":"degraded"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("/status", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		payload, err := a.state.StatusJSON()
		if err != nil {
			writeJSONError(writer, http.StatusInternalServerError, err)
			return
		}
		_, _ = writer.Write(payload)
	})
	mux.HandleFunc("/metrics", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = writer.Write([]byte(a.state.Metrics()))
	})
	return mux
}

func writeJSONError(writer http.ResponseWriter, statusCode int, err error) {
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": err.Error()})
}
