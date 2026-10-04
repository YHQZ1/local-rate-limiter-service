// Command ratelimiter is an HTTP token-bucket rate-limit service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"ratelimiter/internal/config"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/server"
)

// version is stamped at build time (-ldflags "-X main.version=1.2.3") and can
// be overridden at runtime with APP_VERSION.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv, version)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	lim, err := limiter.New(cfg.Rate, cfg.Burst, limiter.WithMaxKeys(cfg.MaxKeys))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go lim.RunJanitor(ctx, cfg.CleanupInterval)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           server.New(lim, cfg.Version, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	logger.Info("listening",
		"port", cfg.Port, "version", cfg.Version,
		"rate_per_sec", cfg.Rate, "burst", cfg.Burst, "max_keys", cfg.MaxKeys)

	select {
	case err := <-serveErr:
		return err // failed to start (e.g. port in use)
	case <-ctx.Done():
	}

	// Stop accepting, let in-flight requests finish, then exit.
	logger.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}
