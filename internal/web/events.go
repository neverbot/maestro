package web

import (
	"fmt"
	"log/slog"
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
//
// Authentication and project membership are resolved once at connection
// time by requireCaller and requireProject, the same middleware every
// other request in this package goes through — authenticate's own doc
// comment (auth.go) says plainly that resolution happens once per
// request, and that remains true for the admission of this stream. What
// is different about this handler, since this task's own review, is
// that it does not treat that one admission as good for the entire
// life of the connection: every sseHeartbeatInterval tick,
// revalidateStreamAccess re-asks the exact question requireProject asked
// at admission — is this credential still live, and is its holder still
// a member of this game — using the same two resolvers authenticate
// itself uses (resolveBearerCaller / resolveSessionCaller) and the same
// resolveProjectScope requireProject calls (api_projects.go). A stream
// whose caller logged out, changed their password, had their token
// revoked, or was removed from this game gets closed within one
// heartbeat interval of that happening, not up to sseMaxLifetime later.
//
// This re-check, on the heartbeat tick, was chosen over the
// alternative a review of this task proposed: closing subscriptions
// actively from each mutation that could revoke access (logout, role
// change, token revocation, member removal). That approach is more
// precise — it can close a stream the instant access is revoked, not up
// to fifteen seconds later — but it means identity and projects would
// each need to know this hub exists and call into it on every path that
// can narrow someone's access, and it only protects the paths someone
// remembers to wire that call into. This project has spent fourteen
// tasks finding exactly that kind of gap — a check that exists in one
// place but was never reached from another. Re-asking the same
// admission question on a timer catches every revocation path
// uniformly, including ones added later that nobody remembers to wire
// into a hub, at the cost of one extra query pair every fifteen seconds
// on a connection that is otherwise idle anyway, and a fifteen-second
// window instead of an instant one. That trade was made deliberately,
// not because the instant version is wrong.
//
// sseMaxLifetime remains as a backstop beneath the heartbeat re-check,
// not a second, independent exposure window: see defaultSSEMaxLifetime's
// own doc comment for what it is actually guarding against once the
// re-check exists.
//
// A stream closed by the heartbeat re-check logs its reason
// (slog, "sse stream closed") so an operator can tell a revocation-
// driven disconnect from an ordinary network one (r.Context().Done(),
// which is not logged — a client going away is not an event this
// handler has anything useful to say about).
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
	heartbeat := time.NewTicker(s.sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-heartbeat.C:
			if reason := s.revalidateStreamAccess(r, scope); reason != "" {
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
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, ev.Payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// revalidateStreamAccess re-answers the question requireProject answered
// once at this stream's admission: does the credential this request
// carried still resolve to a live caller, and is that caller still a
// member of scope.ProjectID. It returns "" when access still checks out,
// or a short human-readable reason when it does not — logged by
// handleEvents, not returned as an error, because every outcome here
// ends the same way (close the stream) and the only thing worth telling
// an operator is why.
//
// r is the original request that admitted this stream: its Authorization
// header or session cookie is read again here exactly as authenticate
// (auth.go) read it the first time, rather than trusting the Caller this
// stream was handed at admission — a Caller is a value copied out of
// that first resolution and cannot itself go stale to reflect a
// revocation; only asking the identity service again can.
func (s *Server) revalidateStreamAccess(r *http.Request, scope ProjectScope) string {
	ctx := r.Context()

	var caller Caller
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		resolved, ok, err := s.resolveBearerCaller(ctx, token)
		switch {
		case err != nil:
			return "token re-check failed: " + err.Error()
		case !ok:
			return "token no longer resolves"
		}
		caller = resolved
	} else if cookie, err := r.Cookie(SessionCookie); err == nil {
		resolved, ok, err := s.resolveSessionCaller(ctx, cookie.Value)
		switch {
		case err != nil:
			return "session re-check failed: " + err.Error()
		case !ok:
			return "session no longer valid"
		}
		caller = resolved
	} else {
		return "no credential present"
	}

	if _, err := s.resolveProjectScope(ctx, caller, scope.ProjectID); err != nil {
		return "no longer has access to this game: " + err.Error()
	}
	return ""
}
