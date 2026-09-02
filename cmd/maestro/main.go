// Command maestro runs the Maestro server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/version"
	"github.com/neverbot/maestro/internal/web"
)

// pruneInterval is how often the process sweeps expired sessions and
// invites out of the database. Neither table is queried by expiry alone
// on any hot path — GetSessionUser and GetLiveInvite already filter on
// expires_at themselves, so a stale row is inert, never wrong — so this
// exists purely to keep both tables from growing without bound on a
// long-lived instance, not to satisfy any correctness requirement. An
// hour is frequent enough that the tables never accumulate more than a
// day's worth of build-up between operator restarts, and infrequent
// enough that it never competes for connections with real traffic on
// the shared pool.
const pruneInterval = time.Hour

// shutdownTimeout bounds how long a graceful shutdown waits for in-flight
// requests to finish before giving up. It is deliberately longer than
// sseHeartbeatInterval's 15s re-check window (internal/web/events.go) so
// an open SSE stream that receives Server.Close's signal has time to
// observe it and return before this deadline forces the connection
// closed instead.
const shutdownTimeout = 15 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv); err != nil {
		slog.Error("maestro stopped", "error", err)
		os.Exit(1)
	}
}

// run wires the full process lifecycle: load config, open the database,
// migrate, bootstrap the first admin, serve, sweep expired rows
// periodically, and shut down cleanly once ctx is done. main wires ctx to
// SIGINT/SIGTERM; run takes it as a parameter, and getenv rather than
// reading os.Getenv directly, purely so main_test.go can drive a full
// start-stop cycle against a real listener with a context it controls
// (a plain context.WithCancel) instead of sending the test binary's own
// process a real signal.
//
// Migration failure and start-up ordering: db.Migrate runs to completion
// (or fails) before srv.ListenAndServe is ever called, so there is no
// window in which this process accepts a connection — /healthz included —
// while migrations are incomplete or failed; a caller either finds a
// fully migrated instance or finds nothing listening at all. goose (the
// migration runner db.Migrate wraps) runs each migration file in its own
// transaction by default, so a failure partway through one file rolls
// back that file's own statements and leaves every previously applied
// migration exactly as it was; run() reports the error and exits
// nonzero without starting the server, and the next start-up attempt
// resumes from the same first still-unapplied migration rather than
// re-running anything that already succeeded.
func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	ids := identity.New(pool, cfg)
	if err := ids.BootstrapFirstAdmin(ctx); err != nil {
		return err
	}

	// webServer is kept as its own *web.Server, not only as srv.Handler
	// below, so the shutdown goroutine can call its Close() — see
	// web.Server.Close's own doc comment for why http.Server.Shutdown
	// alone is not enough once an SSE stream is in the mix.
	// One hub, shared. The web server publishes its own membership and
	// token events into it and the SSE endpoint reads from it, and the
	// metamodel service publishes every content change into the same
	// one; built here rather than left to NewServer's own nil default
	// precisely because two of them would leave a designer's browser
	// watching a stream nothing writes to.
	hub := realtime.NewHub()
	webServer := web.NewServer(web.Options{
		Version:   version.Version,
		Config:    cfg,
		Identity:  ids,
		Projects:  projects.New(pool),
		Metamodel: metamodel.New(pool, hub),
		Hub:       hub,
	})
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           webServer,
		ReadHeaderTimeout: 10 * time.Second,
	}

	startPruneLoop(ctx, ids)

	// shutdownDone closes once the goroutine below has finished calling
	// srv.Shutdown, not merely started it — see the comment on the
	// <-shutdownDone wait at the bottom of this function for why run
	// blocks on it before returning: srv.ListenAndServe returns the
	// instant Shutdown is *called* (with http.ErrServerClosed), long
	// before Shutdown's own wait for active handlers to finish is done,
	// so returning from run as soon as ListenAndServe unblocks would run
	// this function's deferred pool.Close() out from under every request
	// Shutdown is still draining.
	shutdownDone := make(chan struct{})
	go func() { //nolint:gosec // G118: ctx is already Done by the time this reaches shutdownCtx below; a fresh context.Background() is required, not a bug.
		defer close(shutdownDone)
		<-ctx.Done()
		slog.Info("maestro shutting down")
		// Close every open SSE stream first: it only signals them and
		// returns immediately (it does not wait), so calling it before
		// Shutdown gives each stream the rest of this goroutine's own
		// work — building shutdownCtx below — as a head start to notice
		// and return before Shutdown starts waiting on them as ordinary
		// active handlers.
		webServer.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown did not finish cleanly", "error", err)
		}
	}()

	slog.Info("maestro listening", "addr", cfg.Addr, "version", version.Version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Block until Shutdown has actually finished waiting out every active
	// handler (or its own shutdownTimeout has forced them closed), not
	// only until it was called. Without this, ListenAndServe's return on
	// Shutdown's mere invocation would let this function return and run
	// its deferred pool.Close() while Shutdown is still draining
	// in-flight requests still using that same pool — the exact defect a
	// concurrent-request test against a real container caught: every
	// in-flight request came back empty instead of completing, because
	// the pool closed underneath them mid-drain.
	<-shutdownDone
	return nil
}

// startPruneLoop starts a background goroutine that sweeps expired
// sessions and invites once immediately, then again every pruneInterval,
// until ctx is done. Neither identity.Service method (Task 6, Task 7) had
// a process-lifecycle owner before this: both are tested in isolation but
// were deliberately left uncalled, with a comment on each pointing here.
//
// The immediate sweep before the ticker's first tick matters on its own:
// time.NewTicker's first tick does not fire until a full pruneInterval
// has elapsed, so without it a process restarted more often than that
// (ordinary deploy churn, a crash loop, a rolling update) would never
// prune at all across its whole lifetime — every restart resets the
// ticker before it ever fires once.
func startPruneLoop(ctx context.Context, ids *identity.Service) {
	go func() {
		pruneOnce(ctx, ids)
		ticker := time.NewTicker(pruneInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruneOnce(ctx, ids)
			}
		}
	}()
}

// pruneOnce runs a single sweep of PruneExpiredSessions and
// PruneExpiredInvites. A failed sweep is logged and never fatal: both
// prune queries are idempotent (a row either matches "expired and
// unprocessed" or it does not, no matter how many times the query runs),
// and a transient database error on one sweep is recovered by the next
// one, not by crashing a process that is otherwise serving traffic
// correctly. Split out from startPruneLoop so both the start-up sweep and
// the ticked ones share one implementation, and so a test can call it
// directly without waiting out a real ticker.
func pruneOnce(ctx context.Context, ids *identity.Service) {
	if n, err := ids.PruneExpiredSessions(ctx); err != nil {
		slog.Error("prune expired sessions", "error", err)
	} else if n > 0 {
		slog.Info("pruned expired sessions", "count", n)
	}
	if n, err := ids.PruneExpiredInvites(ctx); err != nil {
		slog.Error("prune expired invites", "error", err)
	} else if n > 0 {
		slog.Info("pruned expired invites", "count", n)
	}
}
