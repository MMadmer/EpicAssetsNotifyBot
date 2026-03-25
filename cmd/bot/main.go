package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "epicassetsnotifybot/internal/app"
)

func main() {
    projectRoot, err := os.Getwd()
    if err != nil {
        log.Fatalf("resolve project root: %v", err)
    }

    log.Printf("Starting bot...")

    application, err := app.New(projectRoot)
    if err != nil {
        log.Fatalf("bootstrap application: %v", err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    if err := application.Run(ctx); err != nil {
        log.Fatalf("run bot: %v", err)
    }
}
