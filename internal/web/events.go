package web

import (
	"encoding/json"
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
// a member of this game — using the read-only counterparts of the two
// resolvers authenticate itself uses (resolveBearerCallerReadOnly /
// resolveSessionCallerReadOnly, not resolveBearerCaller /
// resolveSessionCaller: see those methods' own doc comments in auth.go
// for why an idle re-check must not record use or slide a session) and
// the same resolveProjectScope requireProject calls (api_projects.go).
// A stream whose caller logged out, changed their password, had their
// token revoked, or was removed from this game gets closed within one
// heartbeat interval of that happening, not up to sseMaxLifetime later
// — tolerating exactly one transient (database-error) failure of that
// re-check before closing, so a passing blip is not treated as a
// revocation; see revalidateStreamAccess's own doc comment.
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
// Subscribe is called with scope.Role, and the heartbeat re-check keeps
// it current via Hub.UpdateRole whenever revalidateStreamAccess reports
// a changed one — so a future publisher can restrict an event to
// subscribers of at least some role (realtime.Event.MinRole) and have it
// enforced here, at delivery, rather than needing every publish call
// site to remember to fan out a role-specific payload itself. Nothing
// sets MinRole yet — every event today reaches every subscriber of its
// game regardless of role — but the mechanism exists now, while there is
// exactly one Subscribe call site to get it right in, rather than being
// retrofitted once nine publishers already exist that would each need to
// remember it.
//
// Event.Seq (assigned by Hub.Publish, realtime/hub.go) is what turns the
// hub's own silent-drop-on-a-full-buffer behaviour into a detectable
// one: this handler compares each event's Seq against the last one it
// wrote and, on a gap, emits a synthetic "resync" event before the real
// one — the client's signal that its view may be stale and it should
// refetch over the ordinary REST API rather than trust the stream blindly
// from here. This does not, and cannot, recover what was dropped: there
// is no replay log behind Seq, only a counter, so "resync" means "you
// missed something, go get the truth," not "here is what you missed."
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
	sub := s.hub.Subscribe(scope.ProjectID, scope.Role)
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
//
// The returned ProjectScope is not a formality: handleEvents assigns it
// back over its own scope variable on success, specifically so a role
// change picked up here (a demotion, most importantly) is not detected
// and then thrown away — Hub's role-scoped delivery (realtime/hub.go)
// reads Subscription.Role, which handleEvents keeps in sync with this
// return value via Hub.UpdateRole. A version of this method that
// returned only a reason would make that resync impossible without a
// second, redundant lookup.
//
// transient distinguishes two failure shapes that must be handled
// differently: a database error while re-resolving the credential or
// membership (identity or projects genuinely unreachable, not a verdict
// about this caller) is transient — handleEvents tolerates exactly one
// of those in a row, so a momentary blip does not close every open
// stream on the instance at once during, say, a connection-pool hiccup.
// "token no longer resolves", "session no longer valid" and "no longer
// has access to this game" are not transient: they are the identity or
// projects service answering the question definitively, and the first
// one closes the stream.
//
// r is the original request that admitted this stream: its Authorization
// header or session cookie is read again here exactly as authenticate
// (auth.go) read it the first time, rather than trusting the Caller this
// stream was handed at admission — a Caller is a value copied out of
// that first resolution and cannot itself go stale to reflect a
// revocation; only asking the identity service again can. It reads
// through resolveBearerCallerReadOnly / resolveSessionCallerReadOnly
// (auth.go), not resolveBearerCaller / resolveSessionCaller: the
// ordinary pair records use (a token's last_used_at, a session's sliding
// renewal), and an idle heartbeat re-check asking "are you still there"
// four times a minute must not itself count as the caller being there —
// see those methods' own doc comments for what that would otherwise do
// to a forgotten open browser tab.
//
// The final `else` branch (no Authorization header and no session
// cookie) is unreachable in practice — admission itself required one of
// the two to produce the Caller this stream was handed — and is kept
// anyway as defence in depth: a future change to admission that made
// that no longer strictly true would fail closed here rather than
// falling through to a nil caller.
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

	newScope, err := s.resolveProjectScope(ctx, caller, scope.ProjectID)
	if err != nil {
		// resolveProjectScope always reports errScopeViolation or
		// errNotMember here, never a bare database error distinctly —
		// that conflation predates this method (Task 12's RoleOf, folded
		// into errNotMember for any lookup failure) and is out of scope
		// to change here, so this branch is treated as definitive, not
		// transient, even though a fraction of the time it is really a
		// database blip wearing a membership error's name.
		return ProjectScope{}, "no longer has access to this game: " + err.Error(), false
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
