// Command maestro runs the Maestro server.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/neverbot/maestro/internal/version"
	"github.com/neverbot/maestro/internal/web"
)

func main() {
	addr := os.Getenv("MAESTRO_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := web.NewServer(web.Options{Version: version.Version})
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("maestro listening", "addr", addr, "version", version.Version)
	if err := httpServer.ListenAndServe(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
