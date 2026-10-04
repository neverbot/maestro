package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// defaultSSEMaxLifetime bounds one GET /api/games/{game}/events
// connection before this handler closes it and the client has to
// reconnect, regardless of what the heartbeat re-check (below) finds.
// This is a backstop, not the primary defense against a revoked caller
// — see handleEvents's own doc comment for why sseHeartbeatInterval is —
// so its value is chosen for connection churn, not exposure window: long
// enough that a browser tab left open all day reconnects a few hundred
// times, not thousands, and short enough to bound whatever the
// heartbeat re-check cannot see (a bug in the re-check itself, or a
// re-check that silently stopped running).
const defaultSSEMaxLifetime = 5 * time.Minute

// sseHeartbeatInterval is how often handleEvents writes an SSE comment
// line (": ping\n\n") on an otherwise idle stream, and — since this is
// also when access is re-checked, see handleEvents's own doc comment —
// how often a revoked caller's stream actually gets cut off. The two are
// the same tick deliberately, not two constants that could drift apart:
// a comment line is ignored by every SSE client (it fires no "message"
// event) but is still a byte on the wire, which is the only thing that
// matters to a reverse proxy sitting between this server and the
// browser — most default idle timeouts (Traefik's, nginx's) are on the
// order of a minute, and this product's own deployment note names
// Traefik as the proxy in front of it. Fifteen seconds keeps the
// connection well under any of those defaults without paging it more
// than a browser tab can shrug off, and doubles as a fifteen-second
// exposure window for a revoked caller instead of five minutes.
const sseHeartbeatInterval = 15 * time.Second

// handleEvents streams one game's realtime events to one connected
// browser, for as long as the connection stays open, the caller's
// access keeps re-checking out, or sseMaxLifetime elapses — whichever
// comes first.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, _ Caller, scope ProjectScope) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errCodeInternal, "streaming is not supported")
		return
	}

	// Subscribed before a single byte of the response is written: a
	// client that has already received its 200 must never be able to
	// observe an event published in the gap between "headers sent" and
	// "subscription exists" as simply missing. An earlier version of
	// this handler flushed headers first and relied on a test sleep to
	// paper over the gap that left; the ordering below is what actually
	// closes it — see this handler's own history for the review that
	// found it.
	sub := s.hub.Subscribe(scope.ProjectID, scope.Role, scope.IsToken)
	defer s.hub.Unsubscribe(sub)

	// Overrides requireCaller's Cache-Control: no-store with the value
	// SSE itself needs (no-cache: do not let any intermediary treat this
	// stream as a cacheable resource), and disables nginx-style response
	// buffering — X-Accel-Buffering is ignored by everything except
	// nginx, so it is harmless to send unconditionally rather than
	// detecting the proxy in front of this instance.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// A comment line, the same shape as the heartbeat's own ": ping\n\n"
	// below (a comment fires no "message" event on an EventSource client,
	// so this changes nothing about what a consumer receives as data) —
	// written once, immediately, so a client — or a person watching raw
	// bytes with curl -N — can tell "connected, waiting for events" from
	// "stalled" the instant the connection opens, rather than only
	// finding out up to sseHeartbeatInterval (15s) later on the first
	// heartbeat, or never, if this stream happens to sit idle with no
	// events published to it at all.
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// jitteredSSEMaxLifetime, not s.sseMaxLifetime directly: see that
	// function's own doc comment for why a fixed bound would synchronize
	// every stream connected around the same moment (most visibly, every
	// browser reconnecting right after a deploy) into reconnecting in
	// lockstep forever after.
	deadline := time.NewTimer(jitteredSSEMaxLifetime(s.sseMaxLifetime))
	defer deadline.Stop()
	heartbeat := time.NewTicker(s.sseHeartbeatInterval)
	defer heartbeat.Stop()

	// recheckFailed tracks whether the *previous* heartbeat's
	// revalidateStreamAccess call failed transiently (a database error,
	// not a definitive "no longer has access"). One transient failure in
	// a row is tolerated — a blip is not a revocation — but two in a row
	// closes the stream: see sseRecheckOutcome's own doc comment for the
	// policy this implements.
	var recheckFailed bool

	// lastSeq is the Seq of the last event actually written to this
	// connection; 0 means "none yet" (realtime.Hub.Publish assigns Seq
	// starting at 1, so 0 is never a real value) and deliberately skips
	// the gap check below for the first event this stream ever sees —
	// that "gap" is just whatever was published before this subscriber
	// connected, not a drop.
	var lastSeq uint64

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			slog.InfoContext(r.Context(), "sse stream closed", "project_id", scope.ProjectID, "reason", "server shutting down")
			return
		case <-deadline.C:
			return
		case <-heartbeat.C:
			newScope, reason, transient := s.revalidateStreamAccess(r, scope)
			switch sseRecheckOutcome(reason, transient, recheckFailed) {
			case sseRecheckOK:
				recheckFailed = false
				if newScope.Role != scope.Role {
					s.hub.UpdateRole(sub, newScope.Role)
				}
				scope = newScope
			case sseRecheckTolerate:
				recheckFailed = true
				slog.WarnContext(r.Context(), "sse re-check failed transiently; tolerating one failure",
					"project_id", scope.ProjectID, "reason", reason)
			default:
				slog.InfoContext(r.Context(), "sse stream closed", "project_id", scope.ProjectID, "reason", reason)
				return
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-sub.C:
			if !open {
				return
			}
			if sseSawGap(lastSeq, ev.Seq) {
				// A gap: at least one event between lastSeq and ev.Seq
				// was dropped (subscriberBuffer overflow — the only way
				// realtime.Hub itself silently loses an event once a
				// subscription exists). "resync" carries no payload of
				// its own; it is purely a signal for the client to
				// refetch its state over the ordinary REST API before
				// trusting anything the stream says from here.
				if _, err := fmt.Fprint(w, "event: resync\ndata: {}\n\n"); err != nil {
					return
				}
			}
			lastSeq = ev.Seq
			payload, err := json.Marshal(ev.Payload)
			// The id: line is not a resumption token, though a browser
			// will resend it as Last-Event-ID: this handler never reads
			// that header, and ev.Seq is scoped to one subscription and
			// restarts at 1 on every Subscribe. It exists so this
			// connection's own gap check below has a value to compare,
			// and so curl -N shows the counter advancing. The answer to
			// "what did I miss" is a refetch over REST.
			if err != nil {
				// A malformed payload is the publisher's bug, not this
				// stream's: dropping just this one event and continuing
				// is what keeps one bad event from taking down an
				// otherwise-healthy connection. json.Marshal escaping
				// every control character is also what makes the wire
				// frame below safe to build with fmt.Fprintf despite
				// ev.Kind and payload both being arbitrary — see
				// realtime.Event.Payload's own doc comment for the
				// forged-event bug this closes.
				slog.ErrorContext(r.Context(), "sse payload marshal failed; dropping event",
					"project_id", scope.ProjectID, "kind", ev.Kind, "seq", ev.Seq, "error", err)
			} else if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// revalidateStreamAccess re-answers the question requireProject answered
// once at this stream's admission: does the credential this request
// carried still resolve to a live caller, and is that caller still a
// member of scope.ProjectID. On success it returns the freshly-resolved
// ProjectScope and an empty reason; on failure it returns the zero
// ProjectScope, a short human-readable reason (logged by handleEvents,
// not returned as an error — every failure here ends the same way, close
// the stream, and the only thing worth telling an operator is why), and
// whether the failure is transient.
func (s *Server) revalidateStreamAccess(r *http.Request, scope ProjectScope) (ProjectScope, string, bool) {
	ctx := r.Context()

	var caller Caller
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		resolved, ok, err := s.resolveBearerCallerReadOnly(ctx, token)
		switch {
		case err != nil:
			return ProjectScope{}, "token re-check failed: " + err.Error(), true
		case !ok:
			return ProjectScope{}, "token no longer resolves", false
		}
		caller = resolved
	} else if cookie, err := r.Cookie(SessionCookie); err == nil {
		resolved, ok, err := s.resolveSessionCallerReadOnly(ctx, cookie.Value)
		switch {
		case err != nil:
			return ProjectScope{}, "session re-check failed: " + err.Error(), true
		case !ok:
			return ProjectScope{}, "session no longer valid", false
		}
		caller = resolved
	} else {
		return ProjectScope{}, "no credential present", false
	}

	newScope, err := s.resolveProjectScope(ctx, caller, scope)
	switch {
	case errors.Is(err, errNotMember), errors.Is(err, errScopeViolation):
		// A real, definitive rejection — resolveProjectScope's own doc
		// comment names these two as the only outcomes that mean the
		// caller was actually evaluated and refused, as opposed to the
		// lookup itself failing. Close the stream.
		return ProjectScope{}, "no longer has access to this game: " + err.Error(), false
	case err != nil:
		// The lookup failed — a database error, not a verdict about
		// this caller. This used to be indistinguishable from the case
		// above, which meant a transient database error closed a live
		// stream with a reason claiming the caller had been removed
		// from the game (and, during test teardown, a request context
		// cancelled by the client disconnecting surfaced the same way —
		// a log line that misreports why a stream closed is exactly
		// what an operator would chase for an hour during a real
		// incident). Reported as transient so the retry tolerance
		// (sseRecheckOutcome) actually applies to it.
		return ProjectScope{}, "project scope re-check failed: " + err.Error(), true
	}
	return newScope, "", false
}

// sseRecheckAction is what handleEvents's heartbeat case does with one
// revalidateStreamAccess result.
type sseRecheckAction int

const (
	// sseRecheckOK means access still checks out: adopt the freshly
	// resolved ProjectScope and clear any pending transient-failure
	// tolerance.
	sseRecheckOK sseRecheckAction = iota
	// sseRecheckTolerate means this tick's re-check failed transiently
	// and the previous tick did not, so the stream stays open — one
	// failure in a row is tolerated, not treated as a revocation.
	sseRecheckTolerate
	// sseRecheckClose means either a definitive "no longer has access"
	// (any reason, first time) or a second transient failure in a row:
	// close the stream.
	sseRecheckClose
)

// sseRecheckOutcome decides what handleEvents's heartbeat case does with
// one revalidateStreamAccess result, given whether the previous tick's
// own re-check already failed transiently. Factored out of the select
// loop so the retry policy — tolerate exactly one transient failure in a
// row, never a non-transient one, and never two transient failures in a
// row — can be pinned by a plain table test (TestSSERecheckOutcome) with
// no real identity/projects service, no live connection, and no way to
// inject a database failure into a real *pgxpool.Pool: the alternative
// was an integration test that could not deterministically produce a
// "transient" outcome at all.
func sseRecheckOutcome(reason string, transient, previouslyFailed bool) sseRecheckAction {
	switch {
	case reason == "":
		return sseRecheckOK
	case transient && !previouslyFailed:
		return sseRecheckTolerate
	default:
		return sseRecheckClose
	}
}

// sseSawGap reports whether seq, the Seq of the event handleEvents just
// received, indicates at least one event was dropped since lastSeq, the
// Seq of the last event it actually wrote to the connection. lastSeq ==
// 0 means "no event written yet" (realtime.Hub.Publish assigns Seq
// starting at 1 — see its own doc comment — so 0 is never a real value)
// and is never a gap: whatever was published before this subscriber
// connected is not a drop, just history it never subscribed to receive.
// Factored out of the select loop, like sseRecheckOutcome, so the rule
// can be pinned by a plain table test rather than a test that has to
// actually overflow realtime.subscriberBuffer through TCP backpressure
// to observe it — which depends on OS socket buffer sizes this package
// has no control over and no test in it should depend on.
func sseSawGap(lastSeq, seq uint64) bool {
	return lastSeq != 0 && seq != lastSeq+1
}

// sseLifetimeJitterFraction is how much random variance
// jitteredSSEMaxLifetime applies to a stream's bounded lifetime, as a
// fraction of it. Without this, every browser that connects around the
// same wall-clock moment — most visibly, every browser on the product
// reconnecting within seconds of a deploy — would also hit its own
// bounded-lifetime cutoff at the same moment, forever: each reconnect
// would land the whole cohort back in lockstep for the next cutoff too,
// turning one deploy into a permanent synchronized reconnect storm
// instead of a one-time blip.
const sseLifetimeJitterFraction = 0.10

// jitteredSSEMaxLifetime returns base varied by up to
// ±sseLifetimeJitterFraction, spreading a cohort of streams that all
// started around the same time across a window of cutoffs instead of one
// shared instant. math/rand/v2's global source needs no seeding and is
// safe for concurrent use by every open stream calling this at once.
func jitteredSSEMaxLifetime(base time.Duration) time.Duration {
	spread := float64(base) * sseLifetimeJitterFraction
	offset := (rand.Float64()*2 - 1) * spread //nolint:gosec // G404: timing jitter for connection churn, not a security-sensitive value.
	return base + time.Duration(offset)
}
