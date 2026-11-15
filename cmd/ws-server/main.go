package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ws-server/internal/app"
	"ws-server/internal/config"
	"ws-server/internal/relay"
	"ws-server/internal/whitelist"
	"ws-server/internal/winservice"
)

const serviceName = "SimpleWSRelay"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(executable)
	configPath := filepath.Join(baseDir, "config.json")
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	logDir := cfg.LogDir
	if !filepath.IsAbs(logDir) {
		logDir = filepath.Join(baseDir, logDir)
	}
	logger, closeLog, err := newLogger(logDir)
	if err != nil {
		return err
	}
	defer closeLog()
	allowedUIDs, err := whitelist.New(cfg.UIDWhitelist, func(uids []string) error {
		return config.UpdateWhitelist(configPath, uids)
	})
	if err != nil {
		return fmt.Errorf("invalid uid_whitelist: %w", err)
	}
	return winservice.Run(serviceName, func(ctx context.Context) error {
		return serve(ctx, cfg, allowedUIDs, logger)
	})
}

func serve(ctx context.Context, cfg config.Config, allowedUIDs *whitelist.Manager, logger *slog.Logger) error {
	api := app.New(app.Config{
		Token:             cfg.Token,
		SuperAdminToken:   cfg.SuperAdminToken,
		Whitelist:         allowedUIDs,
		RequestTimeout:    cfg.RequestTimeout,
		HeartbeatInterval: cfg.HeartbeatInterval,
		MaxMessageBytes:   cfg.MaxMessageBytes,
		Logger:            logger,
		Debug:             cfg.Debug,
	}, relay.New())
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}()

	logger.Info("WS relay starting", "address", cfg.ListenAddr, "request_timeout", cfg.RequestTimeout, "heartbeat_interval", cfg.HeartbeatInterval, "debug", cfg.Debug)
	logger.Warn("plain HTTP/WS enabled; X-Token can be intercepted on an untrusted public network")
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}

func newLogger(logDir string) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(filepath.Join(logDir, "ws-server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, file), &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, func() { _ = file.Close() }, nil
}
