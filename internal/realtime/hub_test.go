package realtime_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/realtime"
)

func TestSubscriberReceivesItsProjectEvents(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()

	sub := hub.Subscribe(project, "owner")
	defer hub.Unsubscribe(sub)

	hub.Publish(realtime.Event{ProjectID: project, Kind: "project.updated", Payload: `{"slug":"azeroth"}`})

	select {
	case got := <-sub.C:
		if got.Kind != "project.updated" {
			t.Fatalf("Kind = %q", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the event")
	}
}

func TestSubscriberIgnoresOtherProjects(t *testing.T) {
	hub := realtime.NewHub()
	mine, theirs := uuid.New(), uuid.New()

	sub := hub.Subscribe(mine, "owner")
	defer hub.Unsubscribe(sub)

	hub.Publish(realtime.Event{ProjectID: theirs, Kind: "project.updated"})

	select {
	case got := <-sub.C:
		t.Fatalf("received an event for another project: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPublishDoesNotBlockOnASlowSubscriber(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()
	sub := hub.Subscribe(project, "owner")
	defer hub.Unsubscribe(sub)

	// Fill the buffer and keep going: a stuck browser must not stall writers.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			hub.Publish(realtime.Event{ProjectID: project, Kind: "noise"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
}

// TestUnsubscribeRemovesTheProjectEntryWhenEmpty pins the per-project
// indexing (Hub.subs is keyed by project id, not one flat set scanned on
// every Publish) at the one place a leak would be observable from outside
// the package: a Hub that never drops empty per-project maps would grow
// one stale entry for every distinct project ever subscribed to, for the
// life of the process, even after every subscriber of that project has
// gone. This does not (and cannot, from realtime_test) inspect the map
// directly; instead it subscribes and unsubscribes many distinct
// projects and confirms a fresh Subscribe/Publish/receive cycle still
// works afterward, which would hang if Unsubscribe left the hub in a
// broken state.
func TestUnsubscribeManyDistinctProjectsLeavesHubUsable(t *testing.T) {
	hub := realtime.NewHub()
	for i := 0; i < 200; i++ {
		sub := hub.Subscribe(uuid.New(), "owner")
		hub.Unsubscribe(sub)
	}

	project := uuid.New()
	sub := hub.Subscribe(project, "owner")
	defer hub.Unsubscribe(sub)
	hub.Publish(realtime.Event{ProjectID: project, Kind: "still.works"})

	select {
	case got := <-sub.C:
		if got.Kind != "still.works" {
			t.Fatalf("Kind = %q", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the event")
	}
}

// TestPublishAssignsIncrementingPerProjectSequence pins Event.Seq's
// contract: it starts at 1 for a project's first-ever Publish, increases
// by exactly 1 per Publish to that project, and is independent per
// project — this is what lets internal/web/events.go detect a gap left
// by a dropped event (a full buffer, the one mechanism this package
// itself can silently lose an event to) without this hub keeping any
// history of what it dropped.
func TestPublishAssignsIncrementingPerProjectSequence(t *testing.T) {
	hub := realtime.NewHub()
	a, b := uuid.New(), uuid.New()
	subA := hub.Subscribe(a, "owner")
	defer hub.Unsubscribe(subA)
	subB := hub.Subscribe(b, "owner")
	defer hub.Unsubscribe(subB)

	hub.Publish(realtime.Event{ProjectID: a, Kind: "first"})
	hub.Publish(realtime.Event{ProjectID: a, Kind: "second"})
	hub.Publish(realtime.Event{ProjectID: b, Kind: "first-for-b"})

	first := <-subA.C
	second := <-subA.C
	firstForB := <-subB.C

	if first.Seq != 1 {
		t.Fatalf("first event for project a: Seq = %d, want 1", first.Seq)
	}
	if second.Seq != 2 {
		t.Fatalf("second event for project a: Seq = %d, want 2", second.Seq)
	}
	if firstForB.Seq != 1 {
		t.Fatalf("first event for project b: Seq = %d, want 1 (independent per project)", firstForB.Seq)
	}
}

// TestPublishFiltersByMinRole pins role-gated delivery: a subscriber
// whose Role does not meet an event's MinRole never receives it, one
// that does receives it, and an event with no MinRole reaches everyone
// regardless of role — the default every event uses today.
func TestPublishFiltersByMinRole(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()

	owner := hub.Subscribe(project, "owner")
	defer hub.Unsubscribe(owner)
	viewer := hub.Subscribe(project, "viewer")
	defer hub.Unsubscribe(viewer)

	hub.Publish(realtime.Event{ProjectID: project, Kind: "owner-only", MinRole: "owner"})
	hub.Publish(realtime.Event{ProjectID: project, Kind: "everyone"})

	select {
	case got := <-owner.C:
		if got.Kind != "owner-only" {
			t.Fatalf("owner's first event Kind = %q, want owner-only", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("owner never received the owner-only event")
	}
	select {
	case got := <-owner.C:
		if got.Kind != "everyone" {
			t.Fatalf("owner's second event Kind = %q, want everyone", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("owner never received the everyone event")
	}

	select {
	case got := <-viewer.C:
		if got.Kind != "everyone" {
			t.Fatalf("viewer received %q, want only the everyone event (owner-only should have been filtered)", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("viewer never received the everyone event")
	}
	select {
	case got := <-viewer.C:
		t.Fatalf("viewer received a second event, want none: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestUpdateRoleChangesFutureFiltering pins the other half of role
// filtering: UpdateRole changes which events a subscription receives
// from that point on, without a new Subscribe/Unsubscribe pair — the
// mechanism internal/web/events.go's heartbeat re-check uses to keep a
// long-lived stream's role current after a mid-stream promotion or
// demotion.
func TestUpdateRoleChangesFutureFiltering(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()
	sub := hub.Subscribe(project, "viewer")
	defer hub.Unsubscribe(sub)

	hub.Publish(realtime.Event{ProjectID: project, Kind: "before-promotion", MinRole: "owner"})
	select {
	case got := <-sub.C:
		t.Fatalf("received an owner-only event before being promoted: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}

	hub.UpdateRole(sub, "owner")
	hub.Publish(realtime.Event{ProjectID: project, Kind: "after-promotion", MinRole: "owner"})
	select {
	case got := <-sub.C:
		if got.Kind != "after-promotion" {
			t.Fatalf("Kind = %q, want after-promotion", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive the owner-only event after UpdateRole")
	}
}

// TestSubscriberCountReflectsActiveSubscriptions pins SubscriberCount's
// contract directly: it starts at zero, tracks Subscribe and Unsubscribe
// exactly, and is scoped per project.
func TestSubscriberCountReflectsActiveSubscriptions(t *testing.T) {
	hub := realtime.NewHub()
	a, b := uuid.New(), uuid.New()

	if got := hub.SubscriberCount(a); got != 0 {
		t.Fatalf("SubscriberCount(a) = %d before any Subscribe, want 0", got)
	}

	sub1 := hub.Subscribe(a, "owner")
	sub2 := hub.Subscribe(a, "viewer")
	subB := hub.Subscribe(b, "owner")
	defer hub.Unsubscribe(subB)

	if got := hub.SubscriberCount(a); got != 2 {
		t.Fatalf("SubscriberCount(a) = %d, want 2", got)
	}
	if got := hub.SubscriberCount(b); got != 1 {
		t.Fatalf("SubscriberCount(b) = %d, want 1", got)
	}

	hub.Unsubscribe(sub1)
	if got := hub.SubscriberCount(a); got != 1 {
		t.Fatalf("SubscriberCount(a) = %d after one Unsubscribe, want 1", got)
	}
	hub.Unsubscribe(sub2)
	if got := hub.SubscriberCount(a); got != 0 {
		t.Fatalf("SubscriberCount(a) = %d after both Unsubscribed, want 0", got)
	}
}
