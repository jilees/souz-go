package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("souz-agent starting")

	// TODO Phase 2: load config, construct bus + channels + executor, start HTTP server
	// For now: block until signal.
	<-ctx.Done()

	slog.Info("souz-agent shutting down")
}
