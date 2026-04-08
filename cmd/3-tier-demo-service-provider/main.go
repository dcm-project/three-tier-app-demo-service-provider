package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/dcm-project/3-tier-demo-service-provider/internal/api/server"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/apiserver"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/config"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/containerclient"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/handlers"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/registration"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/service"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/statusreport"
	"github.com/dcm-project/3-tier-demo-service-provider/internal/store"
	"github.com/go-chi/chi/v5"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger := buildLogger(cfg.SVCLogLevel)

	ln, err := net.Listen("tcp", cfg.SVCAddress)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.SVCAddress, err)
	}
	defer func() { _ = ln.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	st, err := store.New(cfg.Store, cfg.SVCLogLevel)
	if err != nil {
		return fmt.Errorf("building store: %w", err)
	}

	containerClient, err := containerclient.New(cfg, logger)
	if err != nil {
		return err
	}

	var statusReporter service.StatusReporter
	if cfg.NATS.URL != "" {
		pub, err := statusreport.NewPublisher(cfg.NATS.URL, cfg.Provider.Name, logger)
		if err != nil {
			return fmt.Errorf("creating status publisher: %w", err)
		}
		defer func() { _ = pub.Close() }()
		statusReporter = pub
		logger.Info("status reporting enabled", "nats_url", cfg.NATS.URL)
	}

	svc := service.New(st, containerClient, statusReporter)
	h := &handlers.Handlers{Svc: svc}

	r := chi.NewRouter()
	_ = server.HandlerFromMuxWithBaseURL(server.NewStrictHandler(h, nil), r, "/api/v1alpha1")

	srv := apiserver.New(cfg.SVCAddress, r, logger)

	if cfg.RegistrationEnabled() {
		registrar, err := registration.NewRegistrar(&cfg, logger)
		if err != nil {
			return fmt.Errorf("creating registrar: %w", err)
		}
		srv = srv.WithOnReady(func(ctx context.Context) {
			registrar.Start(ctx)
			logger.Info("DCM registration started")
		})
	}

	logger.Info("server ready", "address", ln.Addr().String())
	return srv.Run(ctx, ln)
}

func buildLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
