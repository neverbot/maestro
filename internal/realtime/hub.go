// Package realtime fans state changes out to connected browsers.
//
// Nothing in this package touches the database. Publish is called
// in-process, from whichever service just committed the change it wants
// observers to know about — internal/web's own mutation handlers are
// this hub's first real callers (see internal/web/publish.go), and the
// metamodel plan's entity.* and relation.* events are expected to follow
// the same pattern from services that own their own Hub reference
// directly. A subscriber therefore only ever hears about events
// published while its own process is up: there is no durable log behind
// this hub, no replay, and no "catch me up since event N". A
// reconnecting client sees only what is published after it reconnects;
// internal/web/events.go's own doc comment says explicitly what a client
// is expected to do about the gap that leaves.
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
	// regardless of role.
	MinRole string

	// HumanOnly restricts delivery to subscribers admitted with isToken
	// false (see Subscribe). It exists because a project role alone does
	// not capture this distinction: resolveProjectScope
	// (internal/web/api_projects.go) always grants a token caller
	// roles.Editor, meeting any MinRole up to Editor — but a token
	// caller's own REST access is refused outright, by
	// requireHumanCaller, from every member, token and invite listing
	// this hub's member/token/invite event kinds mirror (see
	// internal/web/publish.go's own doc comment on each kind's HumanOnly
	// setting). A quality review found the first version of this hub
	// wired with real publishers let exactly that slip through: an
	// agent's own token watched another agent's token get minted,
	// carrying that other token's hint, over a stream the minting
	// token's own bearer could not have read the equivalent listing
	// through. MinRole cannot express "no token caller, regardless of
	// role" — a token caller's Role is always Editor, never Owner, so
	// only an Owner-gated event was ever naturally excluded — which is
	// why this is a separate field rather than a convention layered onto
	// MinRole.
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
//
// Role is this subscriber's standing in ProjectID at the time of
// Subscribe, used by Publish to filter role-gated events (Event.MinRole
// above). It must never be written directly from outside this package —
// Publish reads it while holding Hub's own lock, so an unsynchronized
// write would race with that read. UpdateRole is the only supported way
// to change it after Subscribe.
//
// IsToken records whether this subscription was admitted under a bearer
// token rather than a browser session, exactly as passed to Subscribe.
// Unlike Role, it never changes for the life of a subscription — a
// connection admitted under a token stays a token connection until it
// reconnects, there is no "UpdateIsToken" the way there is an
// UpdateRole — so it is set once here and read directly by Publish
// (Event.HumanOnly's own doc comment), never mutated.
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
// There is no per-project sequence counter on this struct — an earlier
// version kept one (seqs map[uuid.UUID]uint64) and stamped every event
// with a single, project-wide Seq before checking any subscriber's
// gates. That made a gap ambiguous: a subscriber gated out of an event
// by role or HumanOnly saw the project's counter jump exactly the way a
// genuine buffer-overflow drop would, so events.go's gap detector fired
// a spurious resync for perfectly ordinary, filtered activity — a quality
// review found this live, watching an owner mint a token push an
// unrelated editor's stream into "resync" for an event that editor was
// never entitled to see in the first place. Moving the counter onto
// Subscription itself (its own unexported seq field) fixes this: a
// subscriber's Seq only ever advances for events it was actually gated
// in for, so a gap in what it observes means exactly one thing again.
// The cost is that Seq is no longer a project-wide ordering — two
// different subscribers of the same project now assign the Nth event
// they each individually receive different Seq values — which is a
// trade this hub takes deliberately: nothing in this package or its
// caller ever compared Seq across two different subscriptions, only a
// single subscription against its own previous value (sseSawGap,
// internal/web/events.go), so there was no real cross-client ordering
// contract to lose. A cross-client debugging handle, if one is ever
// needed, belongs in a log line built from Hub's own internal state, not
// on the wire.
//
// mu is a plain sync.Mutex, not a sync.RWMutex: an earlier version of
// this hub used RWMutex and let Publish take only a read lock, since it
// only read subs. That stopped being true the moment Publish started
// assigning Seq — incrementing a subscription's own seq is a write —
// so every operation on this hub needs the same exclusive lock now, and
// a second lock kind bought nothing once that was true.
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
// Use UpdateRole, not a second Subscribe/Unsubscribe pair, when role
// changes without the connection itself ending; isToken never changes
// for the life of a subscription, so there is no equivalent update for
// it.
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
//
// Also drops the project's own map entry once its last subscriber is
// gone, rather than leaving it behind: a hub whose subs map only ever
// grows, one entry per project ever subscribed to, for the life of the
// process, is a slow leak of its own once enough distinct projects have
// been visited even after every browser watching them left. There is no
// separate per-project sequence state to drop alongside it any more —
// see Hub's own doc comment for why Seq moved onto each Subscription —
// so a subscription's own seq simply goes with it when the struct itself
// is garbage collected, the same as every other field on it.
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
//
// Seq is assigned per subscription, under the same lock that reads subs,
// immediately after a subscription passes both gates above — not before
// the gates (a filtered subscriber's own counter must not move at all:
// see Hub's own doc comment for the spurious-resync bug that left), and
// not only on a successful send (a full buffer must still advance the
// counter, or the gap it leaves would never show up as a gap at all).
// This ordering — increment first, attempt the send second, keep the
// outcome of the send from influencing the counter either way — is what
// makes a gap in one subscription's own Seq sequence mean exactly one
// thing: an event that subscription was entitled to receive and did not.
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
