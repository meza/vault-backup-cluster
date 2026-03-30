package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	consulapi "github.com/hashicorp/consul/api"

	"github.com/meza/vault-backup-cluster/internal/backup"
	"github.com/meza/vault-backup-cluster/internal/config"
	"github.com/meza/vault-backup-cluster/internal/consulx"
	"github.com/meza/vault-backup-cluster/internal/state"
	"github.com/meza/vault-backup-cluster/internal/vault"
)

type fakeServer struct {
	listenErr      error
	shutdownErr    error
	shutdownCalled bool
	blockOnListen  chan struct{}
}

func (f *fakeServer) ListenAndServe() error {
	if f.blockOnListen != nil {
		<-f.blockOnListen
	}
	return f.listenErr
}

func (f *fakeServer) Shutdown(context.Context) error {
	f.shutdownCalled = true
	if f.blockOnListen != nil {
		close(f.blockOnListen)
		f.blockOnListen = nil
	}
	return f.shutdownErr
}

type fakeElector struct {
	run func(context.Context, func(context.Context) error) error
}

func (f fakeElector) Run(ctx context.Context, onLeadership func(context.Context) error) error {
	return f.run(ctx, onLeadership)
}

type fakeBackupRunner struct {
	runErr error
	calls  int
}

func (f *fakeBackupRunner) Run(context.Context) error {
	f.calls++
	return f.runErr
}

type fakeVaultProber struct {
	err error
}

func (f fakeVaultProber) Health(context.Context) error {
	return f.err
}

type fakeDestinationProber struct {
	err error
}

func (f fakeDestinationProber) Check(context.Context) error {
	return f.err
}

type failingResponseWriter struct {
	header http.Header
	status int
}

func (f *failingResponseWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}

func (f *failingResponseWriter) WriteHeader(statusCode int) {
	f.status = statusCode
}

func (f *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("boom")
}

type fakeVaultTokenSource struct{}

func (fakeVaultTokenSource) Token() (string, error) {
	return "token", nil
}

func restoreAppHooks() {
	loadConfig = config.Load
	newTokenSource = vault.NewTokenSource
	newVaultClient = vault.NewClient
	newConsulClient = consulx.NewClient
	newBackup = backup.NewService
	newElector = consulx.NewElector
	checkConsul = consulx.Check
}

func healthyConsulClient(t *testing.T) *consulapi.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/status/leader" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(`"10.0.0.1:8300"`)); err != nil {
			t.Fatalf("write leader response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client, err := consulx.NewClient(server.URL, nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	return client
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func validConfig() config.Config {
	return config.Config{
		NodeID:               "node-a",
		HTTPBindAddress:      ":0",
		LogLevel:             "debug",
		VaultAddr:            "http://vault.local",
		VaultToken:           "vault-token",
		VaultRequestTimeout:  time.Second,
		ConsulAddr:           "http://consul.local",
		ConsulLockKey:        "service/leader",
		ConsulSessionTTL:     time.Second,
		ConsulLockWait:       time.Second,
		BackupSchedule:       time.Second,
		BackupLocation:       "/tmp/backups",
		ArtifactNameTemplate: "snapshots/{{ .Timestamp }}.snap",
		RetentionCount:       1,
		ScratchDir:           "/tmp/scratch",
		ProbeInterval:        time.Second,
	}
}

func setValidEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("NODE_ID", "node-a")
	t.Setenv("HTTP_BIND_ADDRESS", ":0")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
	t.Setenv("VAULT_TOKEN", "vault-token")
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("CONSUL_ADDR", "http://127.0.0.1:8500")
	t.Setenv("CONSUL_HTTP_TOKEN", "")
	t.Setenv("CONSUL_HTTP_TOKEN_FILE", "")
	t.Setenv("CONSUL_LOCK_KEY", "service/leader")
	t.Setenv("CONSUL_SESSION_TTL", "15s")
	t.Setenv("CONSUL_LOCK_WAIT", "10s")
	t.Setenv("BACKUP_SCHEDULE", "1s")
	t.Setenv("BACKUP_LOCATION", filepath.Join(root, "backups"))
	t.Setenv("ARTIFACT_NAME_TEMPLATE", "snapshots/{{ .Timestamp }}.snap")
	t.Setenv("RETENTION_COUNT", "1")
	t.Setenv("RETENTION_MAX_AGE", "")
	t.Setenv("SCRATCH_DIR", filepath.Join(root, "scratch"))
	t.Setenv("PROBE_INTERVAL", "1s")
	return root
}

func TestNewBuildsApplicationWithConsulTokenFallback(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)
	setValidEnv(t)

	application, err := New()
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if application.server == nil || application.elector == nil || application.backup == nil {
		t.Fatal("expected application dependencies to be initialized")
	}
}

func TestNewReturnsConfigError(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)

	expected := errors.New("boom")
	loadConfig = func() (config.Config, error) {
		return config.Config{}, expected
	}

	_, err := New()
	if !errors.Is(err, expected) {
		t.Fatalf("expected %v, got %v", expected, err)
	}
}

func TestNewReturnsVaultTokenError(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)

	loadConfig = func() (config.Config, error) {
		cfg := validConfig()
		cfg.VaultToken = "vault-token"
		return cfg, nil
	}
	newTokenSource = func(staticToken string, tokenFile string) (vault.TokenSource, error) {
		if staticToken == "vault-token" {
			return nil, errors.New("boom")
		}
		return fakeVaultTokenSource{}, nil
	}

	_, err := New()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected vault token error, got %v", err)
	}
}

func TestNewReturnsConsulTokenErrorWhenConfigured(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)

	loadConfig = func() (config.Config, error) {
		cfg := validConfig()
		cfg.ConsulToken = "consul-token"
		return cfg, nil
	}
	newTokenSource = func(staticToken string, tokenFile string) (vault.TokenSource, error) {
		switch staticToken {
		case "vault-token":
			return fakeVaultTokenSource{}, nil
		case "consul-token":
			return nil, errors.New("boom")
		default:
			return fakeVaultTokenSource{}, nil
		}
	}

	_, err := New()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected consul token error, got %v", err)
	}
}

func TestNewReturnsConsulClientError(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)
	setValidEnv(t)

	expected := errors.New("boom")
	newConsulClient = func(string, consulx.TokenSource) (*consulapi.Client, error) {
		return nil, expected
	}

	_, err := New()
	if !errors.Is(err, expected) {
		t.Fatalf("expected %v, got %v", expected, err)
	}
}

func TestNewReturnsBackupConstructionError(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)
	setValidEnv(t)
	t.Setenv("ARTIFACT_NAME_TEMPLATE", "{{")

	_, err := New()
	if err == nil || !strings.Contains(err.Error(), "parse artifact name template") {
		t.Fatalf("expected template parse error, got %v", err)
	}
}

func TestRunReturnsServerError(t *testing.T) {
	application := &App{
		cfg:          config.Config{HTTPBindAddress: ":0", ProbeInterval: time.Hour},
		logger:       testLogger(),
		state:        state.New("node-a"),
		server:       &fakeServer{listenErr: errors.New("boom")},
		consulClient: healthyConsulClient(t),
		elector: fakeElector{run: func(ctx context.Context, onLeadership func(context.Context) error) error {
			<-ctx.Done()
			return nil
		}},
		backup:      &fakeBackupRunner{},
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}

	err := application.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected server error, got %v", err)
	}
	if !application.server.(*fakeServer).shutdownCalled {
		t.Fatal("expected shutdown on server error")
	}
}

func TestRunReturnsElectionError(t *testing.T) {
	server := &fakeServer{blockOnListen: make(chan struct{})}
	application := &App{
		cfg:          config.Config{HTTPBindAddress: ":0", ProbeInterval: time.Hour},
		logger:       testLogger(),
		state:        state.New("node-a"),
		server:       server,
		consulClient: healthyConsulClient(t),
		elector: fakeElector{run: func(context.Context, func(context.Context) error) error {
			return errors.New("boom")
		}},
		backup:      &fakeBackupRunner{},
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}

	err := application.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected election error, got %v", err)
	}
	if !server.shutdownCalled {
		t.Fatal("expected shutdown on election error")
	}
}

func TestRunIgnoresServerShutdownAndContextCancellation(t *testing.T) {
	server := &fakeServer{listenErr: http.ErrServerClosed}
	application := &App{
		cfg:          config.Config{HTTPBindAddress: ":0", ProbeInterval: time.Hour},
		logger:       testLogger(),
		state:        state.New("node-a"),
		server:       server,
		consulClient: healthyConsulClient(t),
		elector: fakeElector{run: func(ctx context.Context, onLeadership func(context.Context) error) error {
			<-ctx.Done()
			return nil
		}},
		backup:      &fakeBackupRunner{},
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := application.Run(ctx); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !server.shutdownCalled {
		t.Fatal("expected shutdown to be called")
	}
}

func TestRunExecutesLeadershipCallbackAndReturnsBackupError(t *testing.T) {
	server := &fakeServer{blockOnListen: make(chan struct{})}
	backupRunner := &fakeBackupRunner{runErr: errors.New("boom")}
	stateStore := state.New("node-a")
	application := &App{
		cfg:          config.Config{HTTPBindAddress: ":0", NodeID: "node-a", ProbeInterval: time.Hour},
		logger:       testLogger(),
		state:        stateStore,
		server:       server,
		consulClient: healthyConsulClient(t),
		elector: fakeElector{run: func(ctx context.Context, onLeadership func(context.Context) error) error {
			return onLeadership(ctx)
		}},
		backup:      backupRunner,
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}

	err := application.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected backup error, got %v", err)
	}
	if backupRunner.calls != 1 {
		t.Fatalf("expected one backup call, got %d", backupRunner.calls)
	}
	if stateStore.Snapshot().Leader {
		t.Fatal("expected leader state to be reset")
	}
}

func TestRunIgnoresCanceledBackupRun(t *testing.T) {
	server := &fakeServer{blockOnListen: make(chan struct{})}
	application := &App{
		cfg:          config.Config{HTTPBindAddress: ":0", NodeID: "node-a", ProbeInterval: time.Hour},
		logger:       testLogger(),
		state:        state.New("node-a"),
		server:       server,
		consulClient: healthyConsulClient(t),
		elector: fakeElector{run: func(ctx context.Context, onLeadership func(context.Context) error) error {
			return onLeadership(ctx)
		}},
		backup:      &fakeBackupRunner{runErr: context.Canceled},
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}

	if err := application.Run(context.Background()); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestRunIgnoresCanceledElector(t *testing.T) {
	server := &fakeServer{blockOnListen: make(chan struct{})}
	application := &App{
		cfg:          config.Config{HTTPBindAddress: ":0", ProbeInterval: time.Hour},
		logger:       testLogger(),
		state:        state.New("node-a"),
		server:       server,
		consulClient: healthyConsulClient(t),
		elector: fakeElector{run: func(context.Context, func(context.Context) error) error {
			return context.Canceled
		}},
		backup:      &fakeBackupRunner{},
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}

	if err := application.Run(context.Background()); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestRunProbesRecordsHealthyDependencies(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)

	stateStore := state.New("node-a")
	checkConsul = func(context.Context, *consulapi.Client) error {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	application := &App{
		cfg:         config.Config{ProbeInterval: time.Hour},
		logger:      testLogger(),
		state:       stateStore,
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}
	application.destination = fakeDestinationProber{}
	application.vaultClient = fakeVaultProber{}
	checkConsul = func(context.Context, *consulapi.Client) error {
		cancel()
		return nil
	}

	application.runProbes(ctx)

	snapshot := stateStore.Snapshot()
	if len(snapshot.Dependencies) != 3 {
		t.Fatalf("expected three dependencies, got %#v", snapshot.Dependencies)
	}
	for _, dependency := range snapshot.Dependencies {
		if !dependency.OK {
			t.Fatalf("expected healthy dependency state, got %#v", snapshot.Dependencies)
		}
	}
}

func dependencyMessage(snapshot state.Snapshot, name string) string {
	for _, dependency := range snapshot.Dependencies {
		if dependency.Name == name {
			return dependency.Message
		}
	}
	return ""
}

func TestRunProbesRecordsFailures(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)

	stateStore := state.New("node-a")
	checkConsul = func(context.Context, *consulapi.Client) error {
		return errors.New("consul down")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	application := &App{
		cfg:         config.Config{ProbeInterval: time.Hour},
		logger:      testLogger(),
		state:       stateStore,
		vaultClient: fakeVaultProber{err: errors.New("vault down")},
		destination: fakeDestinationProber{err: errors.New("disk down")},
	}
	checkConsul = func(context.Context, *consulapi.Client) error {
		return errors.New("consul down")
	}
	application.destination = fakeDestinationProber{err: errors.New("disk down")}
	application.vaultClient = fakeVaultProber{err: errors.New("vault down")}
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	application.runProbes(ctx)

	snapshot := stateStore.Snapshot()
	if dependencyMessage(snapshot, "consul") != "consul down" {
		t.Fatalf("unexpected consul probe state: %#v", snapshot.Dependencies)
	}
	if dependencyMessage(snapshot, "vault") != "vault down" {
		t.Fatalf("unexpected vault probe state: %#v", snapshot.Dependencies)
	}
	if dependencyMessage(snapshot, "destination") != "disk down" {
		t.Fatalf("expected all dependencies up, got %#v", snapshot.Dependencies)
	}
}

func TestRunProbesRepeatsOnTicker(t *testing.T) {
	restoreAppHooks()
	t.Cleanup(restoreAppHooks)

	stateStore := state.New("node-a")
	checks := 0
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	application := &App{
		cfg:         config.Config{ProbeInterval: time.Millisecond},
		logger:      testLogger(),
		state:       stateStore,
		vaultClient: fakeVaultProber{},
		destination: fakeDestinationProber{},
	}
	checkConsul = func(context.Context, *consulapi.Client) error {
		checks++
		if checks == 2 {
			cancel()
		}
		return nil
	}

	application.runProbes(ctx)

	if checks < 2 {
		t.Fatalf("expected repeated probes, got %d", checks)
	}
}

func TestRoutesIgnoreResponseWriteErrors(t *testing.T) {
	application := &App{
		state: state.New("node-a"),
	}
	now := time.Now()
	application.state.SetDependency("consul", true, "", now)
	application.state.SetDependency("vault", true, "", now)
	application.state.SetDependency("destination", true, "", now)

	for _, path := range []string{"/healthz", "/readyz", "/status", "/metrics"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		writer := &failingResponseWriter{}
		application.routes().ServeHTTP(writer, request)
	}

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	writer := &failingResponseWriter{}
	application = &App{state: state.New("node-a")}
	application.routes().ServeHTTP(writer, request)
}

func TestWriteJSONErrorIgnoresEncodingErrors(t *testing.T) {
	writer := &failingResponseWriter{}
	writeJSONError(writer, http.StatusInternalServerError, errors.New("boom"))
	if writer.status != http.StatusInternalServerError {
		t.Fatalf("expected status code 500, got %d", writer.status)
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"INFO":  slog.LevelInfo,
	}

	for input, expected := range cases {
		if got := parseLogLevel(input); got != expected {
			t.Fatalf("expected %v for %q, got %v", expected, input, got)
		}
	}
}

func TestRoutesHealthz(t *testing.T) {
	application := &App{state: state.New("node-a"), logger: testLogger()}
	recorder := httptest.NewRecorder()

	application.routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if body := recorder.Body.String(); body != `{"status":"ok"}` {
		t.Fatalf("unexpected body %q", body)
	}
}
