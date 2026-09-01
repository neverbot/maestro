package web

import (
	"fmt"
	"net/http"
	"time"
)

// defaultSSEMaxLifetime bounds one GET /api/games/{game}/events
// connection before this handler closes it and the client has to
// reconnect. See handleEvents's own doc comment for why a bound exists
// at all; five minutes is short enough that a caller whose access was
// revoked mid-stream — a logout, a password change, a token revocation,
// removal from the game — is exposed for one bounded window rather than
// indefinitely, and long enough that a browser tab left open all day
// reconnects a few hundred times, not thousands.
const defaultSSEMaxLifetime = 5 * time.Minute

// sseHeartbeatInterval is how often handleEvents writes an SSE comment
// line (": ping\n\n") on an otherwise idle stream. A comment is ignored
// by every SSE client — it fires no "message" event — but it is still a
// byte on the wire, which is the only thing that matters to a reverse
// proxy sitting between this server and the browser: most default idle
// timeouts (Traefik's, nginx's) are on the order of a minute, and this
// product's own deployment note names Traefik as the proxy in front of
// it. Fifteen seconds keeps this well under any of those defaults
// without paging the connection more than a browser tab can shrug off.
const sseHeartbeatInterval = 15 * time.Second

// handleEvents streams one game's realtime events to one connected
// browser, for as long as the connection stays open or sseMaxLifetime
// elapses, whichever comes first.
//
// Authentication and project membership are both resolved once, at
// connection time, by requireCaller and requireProject — the same
// middleware every other request in this package goes through — and
// never again for the life of this connection. That is deliberate, not
// an oversight: authenticate's own doc comment (auth.go) says plainly
// that resolution happens once per request, and a stream that somehow
// re-checked on every event would mean a database query per event this
// hub fans out, defeating the point of an in-memory hub. The
// consequence is that an already-open stream does not notice a logout,
// a password change, a token revocation, or removal from this game that
// happens after the handshake — it keeps delivering this game's events
// to a caller whose access was just revoked, until one of two things
// closes it: the browser disconnects (r.Context() is Done — nothing
// here can force that from the server side), or sseMaxLifetime elapses
// and this handler closes the stream itself, forcing the client to
// reconnect. A reconnect is a brand new HTTP request, which runs
// requireCaller and requireProject again from scratch — that is the one
// point in this design where a revoked caller actually gets cut off, so
// sseMaxLifetime is the whole mitigation, not a convenience: see
// defaultSSEMaxLifetime's own doc comment for the window it leaves open.
//
// This hub keeps no history: a client that reconnects — whether because
// this handler closed the stream, the tab was asleep, or the network
// dropped — receives only events published after it reconnects, with a
// gap in between it has no way to detect from this stream alone (see
// realtime.Hub's own doc comment). A client is expected to treat "the
// stream (re)connected" as "refetch my current state over the ordinary
// REST API, then trust the stream from here" — this handler does not,
// and structurally cannot on its own, guarantee a gap-free feed.
//
// Every event this hub ever publishes for a game reaches every
// subscriber of that game equally: Subscribe and Publish key on project
// id only, never on scope.Role. A viewer and an owner watching the same
// game see byte-identical events. That is fine for now because nothing
// publishes into this hub yet, but it is a real constraint the first
// publisher has to design around, not something this handler enforces
// on their behalf: if a future event's payload carries something not
// every role in the game should see, the publisher must either leave
// that out of the payload entirely or publish a role-specific variant —
// this hub has no concept of a payload's fields and cannot filter one
// after the fact.
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
	sub := s.hub.Subscribe(scope.ProjectID)
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
	flusher.Flush()

	deadline := time.NewTimer(s.sseMaxLifetime)
	defer deadline.Stop()
	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-sub.C:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, ev.Payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
