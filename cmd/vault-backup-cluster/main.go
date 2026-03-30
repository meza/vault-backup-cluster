package main

import (
"context"
"log"
"os/signal"
"syscall"

"github.com/meza/vault-backup-cluster/internal/app"
)

func main() {
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

application, err := app.New()
if err != nil {
log.Fatal(err)
}

if err := application.Run(ctx); err != nil {
log.Fatal(err)
}
}
