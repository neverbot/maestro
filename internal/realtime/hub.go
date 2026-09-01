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
)

// Event is one state change, scoped to a project.
type Event struct {
	ProjectID uuid.UUID `json:"-"`
	Kind      string    `json:"kind"`
	Payload   string    `json:"payload,omitempty"`
}

// Subscription is one connected browser. C is buffered (see NewHub's own
// doc comment on subscriberBuffer) and is closed by Unsubscribe; a
// subscriber must stop reading from C once it observes the channel
// closed, not merely once it decides to stop.
type Subscription struct {
	ProjectID uuid.UUID
	C         chan Event
}

// subscriberBuffer bounds how many events a subscriber can fall behind
// by before Publish starts dropping events for it rather than blocking.
// Sized for "a browser tab briefly not reading, not a browser tab gone
// for good" — a subscriber that stays behind by more than this many
// events is, for the purposes of this hub, indistinguishable from one
// that vanished, and Publish must never learn the difference by
// blocking: see Publish's own doc comment.
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
// Subscribe and Publish never spawn a goroutine of their own: a
// subscriber's only goroutine is the HTTP handler goroutine net/http
// already allocated for its request (internal/web/events.go loops on
// sub.C in the same goroutine that accepted the connection), so a hub
// with a thousand subscribers costs a thousand map entries and a
// thousand buffered channels, not a thousand extra goroutines.
type Hub struct {
	mu   sync.RWMutex
	subs map[uuid.UUID]map[*Subscription]struct{}
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[uuid.UUID]map[*Subscription]struct{})}
}

// Subscribe registers a listener for one project's events.
func (h *Hub) Subscribe(projectID uuid.UUID) *Subscription {
	sub := &Subscription{ProjectID: projectID, C: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	if h.subs[projectID] == nil {
		h.subs[projectID] = make(map[*Subscription]struct{})
	}
	h.subs[projectID][sub] = struct{}{}
	h.mu.Unlock()
	return sub
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
// gone, rather than leaving an empty map behind: a hub whose subs map
// only ever grows, one entry per project ever subscribed to, for the
// life of the process, is a slow leak of its own once enough distinct
// projects have been visited even after every browser watching them
// left.
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

// Publish delivers an event to every subscription of its project. A
// listener whose buffer is full simply misses the event: a stalled
// browser must never block a database write, and Publish's caller is
// never the browser's problem to wait on. This is a real, permanent gap
// for that one listener, not a "usually fine" approximation — a
// subscriber that misses an event has no way to know one was dropped,
// which is why internal/web/events.go bounds a stream's own lifetime and
// relies on the client's next reconnect (and whatever full-state fetch
// it does on load) to resynchronize, rather than this hub trying to
// paper over the gap with a replay log it does not keep.
func (h *Hub) Publish(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.subs[ev.ProjectID] {
		select {
		case sub.C <- ev:
		default:
		}
	}
}
