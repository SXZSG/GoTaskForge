package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gotaskforge/internal/config"
	"gotaskforge/internal/gateway"
	"gotaskforge/internal/httpapi"
	"gotaskforge/internal/mysqlstore"
	"gotaskforge/internal/provider"
	"gotaskforge/internal/rediskit"
	"gotaskforge/internal/runner"
	"gotaskforge/internal/task"
)

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var repo task.Repository
	switch cfg.Backend {
	case "file":
		repo, err = task.Open(cfg.DataFile, cfg.MaxPending)
	case "mysql":
		repo, err = mysqlstore.Open(cfg.MySQLDSN, cfg.MaxPending, cfg.MaxAttempts, 2*cfg.TaskTimeout()+15*time.Second)
	}
	if err != nil {
		return err
	}
	defer repo.Close()
	var p provider.Provider
	switch cfg.Provider {
	case "mock":
		p = provider.Mock{Delay: 250 * time.Millisecond}
	case "openai":
		real := provider.OpenAICompatible{URL: cfg.OpenAIBaseURL, APIKey: cfg.OpenAIAPIKey, Model: cfg.OpenAIModel}
		if err = real.Validate(); err != nil {
			return err
		}
		p = real
	}
	var limiter rediskit.Limiter
	if cfg.RedisURL != "" {
		limiter, err = rediskit.NewRedis(cfg.RedisURL)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = limiter.Ping(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("redis unavailable: %w", err)
		}
	} else {
		limiter = rediskit.NewMemory()
	}
	var gw *gateway.Gateway
	if len(cfg.GatewayBackends) > 0 {
		gw, err = gateway.New(gateway.Config{
			Backends: cfg.GatewayBackends, UpstreamKey: cfg.GatewayKey,
			MaxPerBackend:    cfg.GatewayMaxPerBackend,
			Timeout:          time.Duration(cfg.GatewayTimeoutSec) * time.Second,
			FailureThreshold: cfg.GatewayFailureThreshold,
			Cooldown:         time.Duration(cfg.GatewayCooldownSec) * time.Second,
			AllowHTTP:        cfg.GatewayAllowHTTP,
		})
		if err != nil {
			return err
		}
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	service := runner.New(repo, p, cfg.Workers, cfg.MaxAttempts, cfg.TaskTimeout(), logger)
	service.Start(ctx)
	api := &httpapi.API{Store: repo, Runner: service, Limiter: limiter, APIKey: cfg.APIKey, RatePerMinute: cfg.RatePerMinute, Gateway: gw}
	srv := httpapi.NewHTTPServer(cfg.Addr, api.Handler())
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	logger.Info("GoTaskForge started", "addr", cfg.Addr, "backend", cfg.Backend, "workers", cfg.Workers, "provider", cfg.Provider, "gateway_backends", len(cfg.GatewayBackends), "rate_limiter", map[bool]string{true: "redis", false: "memory"}[cfg.RedisURL != ""])
	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			service.Shutdown()
			return err
		}
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(closeCtx)
	service.Shutdown()
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}
