// Command maestro runs the Maestro server.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/version"
	"github.com/neverbot/maestro/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("maestro stopped", "error", err)
		os.Exit(1)
	}
}

// run wires the process's database pool and domain services and starts the
// HTTP server. It exists as a minimal fix for web.NewServer's construction
// guard (Task 10): a Server built with a nil Identity or Projects service
// used to serve /healthz successfully right up until the first request
// carrying any credential, which panicked. This gives it real services
// instead of leaving that gap open.
//
// It is deliberately not the full process lifecycle: no signal-driven
// graceful shutdown, no first-admin bootstrap call, and no periodic sweep
// for identity.Service.PruneExpiredSessions / PruneExpiredInvites. All
// three are Task 16's job ("Wire everything into main, then Docker, CI
// and the quality gate" — its own plan section already claims the prune
// sweep by name); this only had to stop the panic, not finish main.
func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	ctx := context.Background()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	srv := web.NewServer(web.Options{
		Version:  version.Version,
		Config:   cfg,
		Identity: identity.New(pool, cfg),
		Projects: projects.New(pool),
	})
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("maestro listening", "addr", cfg.Addr, "version", version.Version)
	return httpServer.ListenAndServe()
}
