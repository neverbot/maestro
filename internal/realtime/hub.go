// Package realtime fans state changes out to connected browsers.
//
// Nothing in this package touches the database. Publish is called
// in-process, from whichever service just committed the change it wants
// observers to know about (nothing does yet — see Task 14's plan
// section — but the metamodel plan's entity.* and relation.* events are
// this hub's first real callers). A subscriber therefore only ever hears
// about events published while its own process is up: there is no
// durable log behind this hub, no replay, and no "catch me up since
// event N". A reconnecting client sees only what is published after it
// reconnects; internal/web/events.go's own doc comment says explicitly
// what a client is expected to do about the gap that leaves.
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
	// Empty means every subscriber of the project receives the event,
	// regardless of role — the only value any caller sets today, since
	// nothing publishes into this hub yet, but it exists now rather than
	// being added once the metamodel plan's nine event-publishing sites
	// exist: the alternative escape hatch (a publisher fans out one
	// event per role instead of setting MinRole) means every one of
	// those nine call sites has to remember to do it, which is exactly
	// the shape of gap this project keeps finding.
	MinRole string

	// Seq is a project-scoped, monotonically increasing sequence number
	// Publish assigns; any value set here by a caller is overwritten.
	// See Hub's own doc comment for what it is for.
	Seq uint64
}

// Subscription is one connected browser. C is buffered (see NewHub's own
// doc comment on subscriberBuffer) and is closed by Unsubscribe; a
// subscriber must stop reading from C once it observes the channel
// closed, not merely once it decides to stop.
//
// Role is this subscriber's standing in ProjectID at the time of
// Subscribe, used by Publish to filter role-gated events (Event.MinRole
// above). It must never be written directly from outside this package —
// Publish reads it while holding Hub's own lock, so an unsynchronized
// write would race with that read. UpdateRole is the only supported way
// to change it after Subscribe.
type Subscription struct {
	ProjectID uuid.UUID
	Role      string
	C         chan Event
}

// subscriberBuffer bounds how many events a subscriber can fall behind
// by before Publish starts dropping events for it rather than blocking.
// Sized for "a browser tab briefly not reading, not a browser tab gone
// for good" — a subscriber that stays behind by more than this many
// events is, for the purposes of this hub, indistinguishable from one
// that vanished, and Publish must never learn the difference by
// blocking: see Publish's own doc comment. A dropped event is not a
// silent loss forever, either: Event.Seq is what lets
// internal/web/events.go notice the gap it leaves and tell the client,
// even though this hub itself keeps no record of what it dropped.
const subscriberBuffer = 64

// Hub routes events to the subscriptions of their project.
//
// subs is keyed by project id so Publish only ever iterates the
// subscribers of the project an event belongs to, not every subscriber
// of every project on the instance — a flat set scanned and filtered on
// every Publish, which an earlier draft of this hub used, costs the same
// whether the event's own project has one subscriber or none, and that
// cost is paid on every single write the rest of the system makes once
// something actually publishes into this hub.
//
// seqs holds each project's own next-sequence-number counter, cleared
// (like subs' own per-project entry) once a project's last subscriber
// leaves — see Unsubscribe's own doc comment for why that mirrors subs'
// cleanup rather than being a separate, permanent map entry per project
// ever visited.
//
// mu is a plain sync.Mutex, not a sync.RWMutex: an earlier version of
// this hub used RWMutex and let Publish take only a read lock, since it
// only read subs. That stopped being true the moment Publish started
// assigning Seq — incrementing seqs is a write — so every operation on
// this hub needs the same exclusive lock now, and a second lock kind
// bought nothing once that was true.
//
// Subscribe and Publish never spawn a goroutine of their own: a
// subscriber's only goroutine is the HTTP handler goroutine net/http
// already allocated for its request (internal/web/events.go loops on
// sub.C in the same goroutine that accepted the connection), so a hub
// with a thousand subscribers costs a thousand map entries and a
// thousand buffered channels, not a thousand extra goroutines.
type Hub struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[*Subscription]struct{}
	seqs map[uuid.UUID]uint64
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[uuid.UUID]map[*Subscription]struct{}),
		seqs: make(map[uuid.UUID]uint64),
	}
}

// Subscribe registers a listener for one project's events, with the
// role that listener is granted to receive role-gated events under (see
// Event.MinRole). Use UpdateRole, not a second Subscribe/Unsubscribe
// pair, when that role changes without the connection itself ending.
func (h *Hub) Subscribe(projectID uuid.UUID, role string) *Subscription {
	sub := &Subscription{ProjectID: projectID, Role: role, C: make(chan Event, subscriberBuffer)}
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
//
// Also drops the project's own map and sequence-counter entries once its
// last subscriber is gone, rather than leaving them behind: a hub whose
// subs and seqs maps only ever grow, one entry per project ever
// subscribed to, for the life of the process, is a slow leak of its own
// once enough distinct projects have been visited even after every
// browser watching them left. Dropping seqs here means a project's
// sequence numbering restarts at 1 the next time anyone subscribes to
// it, which is safe precisely because no subscriber survives to compare
// against the old numbering across that gap — every subscriber that did
// is, by definition, part of the "last subscriber" this method is
// handling.
func (h *Hub) Unsubscribe(sub *Subscription) {
	h.mu.Lock()
	if project, ok := h.subs[sub.ProjectID]; ok {
		if _, ok := project[sub]; ok {
			delete(project, sub)
			close(sub.C)
			if len(project) == 0 {
				delete(h.subs, sub.ProjectID)
				delete(h.seqs, sub.ProjectID)
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
// Role meets ev.MinRole (every subscriber, if MinRole is empty). A
// listener whose buffer is full simply misses the event: a stalled
// browser must never block a database write, and Publish's caller is
// never the browser's problem to wait on. This is a real, permanent gap
// for that one listener, not a "usually fine" approximation — but unlike
// before Event.Seq existed, it is no longer an undetectable one:
// internal/web/events.go compares each event's Seq against the last one
// it wrote and tells the client when a gap appears, even though this hub
// itself keeps no record of what it dropped to explain it.
//
// Seq is assigned here, once per project per Publish call, under the
// same lock that reads subs — not by the caller, and not by a separate
// atomic counter read outside the lock — so two concurrent Publish calls
// for the same project can never observe or assign the same value.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seqs[ev.ProjectID]++
	ev.Seq = h.seqs[ev.ProjectID]
	for sub := range h.subs[ev.ProjectID] {
		if ev.MinRole != "" && !roles.AtLeast(roles.Role(sub.Role), roles.Role(ev.MinRole)) {
			continue
		}
		select {
		case sub.C <- ev:
		default:
		}
	}
}
