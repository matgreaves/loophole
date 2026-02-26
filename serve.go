package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/matgreaves/loophole/internal/daemon"
	"github.com/matgreaves/loophole/internal/resolver"
)

func runServe() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if !resolver.IsInstalled() {
		fmt.Fprintln(os.Stderr, "warning: /etc/resolver/loop.test not found")
		fmt.Fprintln(os.Stderr, "run 'sudo loophole resolve-install' to enable DNS resolution")
	}

	d, err := daemon.New()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return d.Run(ctx)
}
