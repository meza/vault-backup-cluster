package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/meza/vault-backup-cluster/internal/app"
)

type appRunner interface {
	Run(context.Context) error
}

func newApplicationImpl() (appRunner, error) {
	return app.New()
}

var (
	newApplication = newApplicationImpl
	notifyContext = signal.NotifyContext
	logFatal      = log.Fatal
)

func run() error {
	ctx, stop := notifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := newApplication()
	if err != nil {
		return err
	}

	return application.Run(ctx)
}

func main() {
	if err := run(); err != nil {
		logFatal(err)
	}
}
