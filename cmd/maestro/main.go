// Command maestro runs the Maestro server.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/version"
	"github.com/neverbot/maestro/internal/web"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	srv := web.NewServer(web.Options{Version: version.Version})
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("maestro listening", "addr", cfg.Addr, "version", version.Version)
	if err := httpServer.ListenAndServe(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
