package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type fakeAppRunner struct {
	runErr error
	ctx    context.Context
}

func (f *fakeAppRunner) Run(ctx context.Context) error {
	f.ctx = ctx
	return f.runErr
}

func restoreMainHooks() {
	newApplication = func() (appRunner, error) {
		return &fakeAppRunner{}, nil
	}
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		return parent, func() {}
	}
	logFatal = func(...any) {}
}

func TestRunReturnsApplicationConstructionError(t *testing.T) {
	restoreMainHooks()
	t.Cleanup(restoreMainHooks)

	expected := errors.New("boom")
	newApplication = func() (appRunner, error) {
		return nil, expected
	}
	notifyContext = func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		if len(signals) != 2 || signals[0] != syscall.SIGINT || signals[1] != syscall.SIGTERM {
			t.Fatalf("unexpected signals: %v", signals)
		}
		return parent, func() {}
	}

	err := run()
	if !errors.Is(err, expected) {
		t.Fatalf("expected %v, got %v", expected, err)
	}
}

func TestRunReturnsApplicationRunError(t *testing.T) {
	restoreMainHooks()
	t.Cleanup(restoreMainHooks)

	expected := errors.New("boom")
	runner := &fakeAppRunner{runErr: expected}
	newApplication = func() (appRunner, error) {
		return runner, nil
	}

	err := run()
	if !errors.Is(err, expected) {
		t.Fatalf("expected %v, got %v", expected, err)
	}
	if runner.ctx == nil {
		t.Fatal("expected run context")
	}
}

func TestNewApplicationImplBuildsApplication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NODE_ID", "node-a")
	t.Setenv("HTTP_BIND_ADDRESS", ":0")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
	t.Setenv("VAULT_TOKEN", "vault-token")
	t.Setenv("CONSUL_ADDR", "http://127.0.0.1:8500")
	t.Setenv("CONSUL_LOCK_KEY", "service/leader")
	t.Setenv("BACKUP_SCHEDULE", "1s")
	t.Setenv("BACKUP_LOCATION", filepath.Join(root, "backups"))
	t.Setenv("SCRATCH_DIR", filepath.Join(root, "scratch"))

	application, err := newApplicationImpl()
	if err != nil {
		t.Fatalf("expected application, got %v", err)
	}
	if application == nil {
		t.Fatal("expected application instance")
	}
}

func TestMainUsesFatalOnRunError(t *testing.T) {
	restoreMainHooks()
	t.Cleanup(restoreMainHooks)

	expected := errors.New("boom")
	newApplication = func() (appRunner, error) {
		return nil, expected
	}

	var fatalArgs []any
	logFatal = func(v ...any) {
		fatalArgs = v
	}

	main()

	if len(fatalArgs) != 1 || !errors.Is(fatalArgs[0].(error), expected) {
		t.Fatalf("expected fatal to receive %v, got %v", expected, fatalArgs)
	}
}

func TestMainSkipsFatalOnSuccess(t *testing.T) {
	restoreMainHooks()
	t.Cleanup(restoreMainHooks)

	newApplication = func() (appRunner, error) {
		return &fakeAppRunner{}, nil
	}

	called := false
	logFatal = func(...any) {
		called = true
	}

	main()

	if called {
		t.Fatal("expected fatal to be skipped on success")
	}
}
