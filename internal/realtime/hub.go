// Package realtime fans state changes out to connected browsers.
package realtime

import (
	"sync"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/roles"
)

// Event is one state change, scoped to a project. No field here carries
// a json struct tag: nothing in this package or its one caller
// (internal/web/events.go) ever marshals an Event itself — Payload is
// marshalled on its own, once, by the SSE handler, and Kind and Seq are
// written into the wire frame directly, not through encoding/json — so a
// tag here would claim a serialisation contract that does not exist. An
// earlier version of this type had `json:"-"` on ProjectID and
// `json:"kind"`/`json:"payload,omitempty"` on the other two, implying
// exactly that contract; both were dropped for the same reason once one
// of them was noticed.
type Event struct {
	ProjectID uuid.UUID
	Kind      string

	// Payload is any JSON-marshalable value (nil is fine — it marshals
	// to "null"). Marshalling happens exactly once, in
	// internal/web/events.go's handleEvents, not here and not in
	// Publish's caller: a publisher that had to marshal its own payload
	// would also have to decide what to do with a marshal error in a
	// code path that has just committed a database transaction and can
	// do nothing useful with one. Centralising it in the one place that
	// writes the wire frame also closes a real bug an earlier, string-
	// typed Payload had: a caller-supplied string written directly into
	// `data: %s` let a payload containing a raw newline end that SSE
	// field early and a second newline end the frame, so anything after
	// it — including a forged `event:` line — would be parsed by the
	// client as a second, attacker-chosen event. json.Marshal escapes
	// control characters (including newline) inside a JSON string, so
	// the bytes handleEvents actually writes can never contain one.
	Payload any

	// MinRole optionally restricts delivery to subscribers whose Role
	// (the value they passed to Subscribe) meets it, per roles.AtLeast.
	MinRole string

	// HumanOnly restricts delivery to subscribers admitted with isToken
	// false (see Subscribe). A role cannot express it: a token caller is
	// always granted roles.Editor, so it meets any MinRole up to Editor,
	// while its REST access to the member, token and invite listings
	// these events mirror is refused outright. Without this field an
	// agent's token would watch another agent's token being minted over
	// a stream whose bearer could not read the equivalent listing.
	HumanOnly bool

	// Seq is a per-subscription, monotonically increasing sequence
	// number Publish assigns to this specific delivery attempt; any
	// value set here by a caller is overwritten, and the same Event
	// published once may carry a different Seq for each subscriber it
	// reaches (or would have reached — see Publish's own doc comment for
	// why "reaches" and "attempted, then dropped" both count). See Hub's
	// own doc comment for what it is for.
	Seq uint64
}

// Subscription is one connected browser. C is buffered (see NewHub's own
// doc comment on subscriberBuffer) and is closed by Unsubscribe; a
// subscriber must stop reading from C once it observes the channel
// closed, not merely once it decides to stop.
type Subscription struct {
	ProjectID uuid.UUID
	Role      string
	IsToken   bool
	C         chan Event

	// seq is this subscription's own last-assigned Seq, incremented
	// under Hub's lock in Publish immediately after this subscription
	// passes an event's gates (MinRole, HumanOnly) — whether or not the
	// buffered send that follows actually succeeds. See Publish's own
	// doc comment for why a gap in this counter must mean exactly one
	// thing (a dropped event), and Hub's own doc comment for why this
	// replaced a single counter shared by every subscriber of a project.
	seq uint64
}

// subscriberBuffer bounds how many events a subscriber can fall behind
// by before Publish starts dropping events for it rather than blocking.
const subscriberBuffer = 64

// Hub routes events to the subscriptions of their project.
type Hub struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[*Subscription]struct{}
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[uuid.UUID]map[*Subscription]struct{}),
	}
}

// Subscribe registers a listener for one project's events, with the
// role that listener is granted to receive role-gated events under (see
// Event.MinRole) and whether it was admitted under a bearer token rather
// than a browser session (see Event.HumanOnly and Subscription.IsToken).
func (h *Hub) Subscribe(projectID uuid.UUID, role string, isToken bool) *Subscription {
	sub := &Subscription{ProjectID: projectID, Role: role, IsToken: isToken, C: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	if h.subs[projectID] == nil {
		h.subs[projectID] = make(map[*Subscription]struct{})
	}
	h.subs[projectID][sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

// UpdateRole changes an existing subscription's Role — the counterpart
// to Subscription.Role's own doc comment on why a direct field write
// from outside this package would race with Publish. It is a no-op for
// a subscription this hub no longer knows about (already Unsubscribed),
// not a panic: a caller racing its own Unsubscribe against this is not
// an error, just a role update that no longer matters.
func (h *Hub) UpdateRole(sub *Subscription, role string) {
	h.mu.Lock()
	if project, ok := h.subs[sub.ProjectID]; ok {
		if _, ok := project[sub]; ok {
			sub.Role = role
		}
	}
	h.mu.Unlock()
}

// Unsubscribe removes a listener and closes its channel. It is safe to
// call more than once for the same subscription, and it is the caller's
// responsibility to call it exactly once its own reader is done — a
// deferred call right after Subscribe (as every caller in this codebase
// does) is what keeps a client that vanishes without a clean close (a
// killed browser tab, a dropped TCP connection) from leaking its map
// entry and channel forever: net/http cancels the request context on
// disconnect, internal/web/events.go's read loop exits on that
// cancellation, and its deferred Unsubscribe runs from there — nothing
// about a vanished client skips this deferred call.
func (h *Hub) Unsubscribe(sub *Subscription) {
	h.mu.Lock()
	if project, ok := h.subs[sub.ProjectID]; ok {
		if _, ok := project[sub]; ok {
			delete(project, sub)
			close(sub.C)
			if len(project) == 0 {
				delete(h.subs, sub.ProjectID)
			}
		}
	}
	h.mu.Unlock()
}

// SubscriberCount reports how many subscriptions projectID currently
// has. It exists for exactly one kind of caller: a test that needs to
// assert a subscription exists at a specific point deterministically
// (internal/web/events_test.go, right after handleEvents's 200 is
// received) rather than relying on code-reading the ordering
// handleEvents happens to use.
func (h *Hub) SubscriberCount(projectID uuid.UUID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[projectID])
}

// Publish delivers an event to every subscription of its project whose
// Role meets ev.MinRole (every subscriber, if MinRole is empty) and
// whose IsToken does not conflict with ev.HumanOnly. A listener whose
// buffer is full simply misses the event: a stalled browser must never
// block a database write, and Publish's caller is never the browser's
// problem to wait on. This is a real, permanent gap for that one
// listener, not a "usually fine" approximation — but unlike before
// Event.Seq existed, it is no longer an undetectable one:
// internal/web/events.go compares each event's Seq against the last one
// it wrote and tells the client when a gap appears, even though this hub
// itself keeps no record of what it dropped to explain it.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs[ev.ProjectID] {
		if ev.MinRole != "" && !roles.AtLeast(roles.Role(sub.Role), roles.Role(ev.MinRole)) {
			continue
		}
		if ev.HumanOnly && sub.IsToken {
			continue
		}
		sub.seq++
		out := ev
		out.Seq = sub.seq
		select {
		case sub.C <- out:
		default:
		}
	}
}
