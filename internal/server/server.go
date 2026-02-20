package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Parsons-ai/comms-server/internal/config"
	"github.com/Parsons-ai/comms-server/internal/identity"
	"github.com/Parsons-ai/comms-server/internal/store"
)

// Service is the interface all server subsystems implement.
type Service interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Health() error
}

// App is the top-level server that orchestrates all services.
type App struct {
	Config   *config.Config
	Identity *identity.Identity
	DB       *store.DB
	Logger   *slog.Logger

	services []Service
}

// New creates a new App with the given config.
func New(cfg *config.Config) (*App, error) {
	// Set up structured logging
	var handler slog.Handler
	opts := &slog.HandlerOptions{}
	switch cfg.LogLevel {
	case "debug":
		opts.Level = slog.LevelDebug
	case "warn":
		opts.Level = slog.LevelWarn
	case "error":
		opts.Level = slog.LevelError
	default:
		opts.Level = slog.LevelInfo
	}

	if cfg.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)

	// Ensure data directory exists
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, fmt.Errorf("ensure data dir: %w", err)
	}

	// Load or generate identity
	id, isNew, err := identity.LoadOrGenerate(cfg.KeyDir())
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	if isNew {
		logger.Info("generated new server identity", "pubkey", id.ShortID())
	} else {
		logger.Info("loaded server identity", "pubkey", id.ShortID())
	}

	// Open database
	db, err := store.Open(cfg.DBPath(), logger)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}

	app := &App{
		Config:   cfg,
		Identity: id,
		DB:       db,
		Logger:   logger,
	}

	return app, nil
}

// Register adds a service to the startup/shutdown lifecycle.
// Services start in registration order and stop in reverse order.
func (a *App) Register(svc Service) {
	a.services = append(a.services, svc)
	a.Logger.Debug("registered service", "name", svc.Name())
}

// Run starts all services and blocks until shutdown signal.
func (a *App) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start services in order
	for _, svc := range a.services {
		a.Logger.Info("starting service", "name", svc.Name())
		if err := svc.Start(ctx); err != nil {
			// Stop already-started services
			a.shutdown(ctx)
			return fmt.Errorf("start %s: %w", svc.Name(), err)
		}
	}

	a.Logger.Info("COMMS server started",
		"pubkey", a.Identity.ShortID(),
		"api_port", a.Config.APIPort,
		"ws_port", a.Config.WSPort,
		"services", len(a.services),
	)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	a.Logger.Info("received shutdown signal", "signal", sig)
	a.shutdown(ctx)
	return nil
}

// shutdown stops all services in reverse order.
func (a *App) shutdown(ctx context.Context) {
	for i := len(a.services) - 1; i >= 0; i-- {
		svc := a.services[i]
		a.Logger.Info("stopping service", "name", svc.Name())
		if err := svc.Stop(ctx); err != nil {
			a.Logger.Error("failed to stop service", "name", svc.Name(), "error", err)
		}
	}

	// Close database last
	if a.DB != nil {
		a.Logger.Info("closing database")
		a.DB.Close()
	}
}

// HealthCheck returns the health status of all services.
func (a *App) HealthCheck() map[string]error {
	results := make(map[string]error)
	for _, svc := range a.services {
		results[svc.Name()] = svc.Health()
	}
	return results
}
