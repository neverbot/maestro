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

	sub := hub.Subscribe(project, "owner", false)
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

	sub := hub.Subscribe(mine, "owner", false)
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
	sub := hub.Subscribe(project, "owner", false)
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
		sub := hub.Subscribe(uuid.New(), "owner", false)
		hub.Unsubscribe(sub)
	}

	project := uuid.New()
	sub := hub.Subscribe(project, "owner", false)
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
// TestPublishAssignsPerSubscriptionSequence pins Event.Seq's new
// contract (moved off a per-project counter — see Hub's own doc comment
// for why): it starts at 1 for a subscription's first received event,
// increases by exactly 1 per event that subscription is gated in for,
// and is tracked independently per subscription, not per project — two
// subscribers of the very same project each see their own Seq start at
// 1.
func TestPublishAssignsPerSubscriptionSequence(t *testing.T) {
	hub := realtime.NewHub()
	a, b := uuid.New(), uuid.New()
	subA := hub.Subscribe(a, "owner", false)
	defer hub.Unsubscribe(subA)
	subB := hub.Subscribe(b, "owner", false)
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
		t.Fatalf("first event for project b: Seq = %d, want 1 (independent per project, and per subscription)", firstForB.Seq)
	}
}

// TestPublishSequenceSkipsEventsFilteredOutForThisSubscription is the
// regression test for the spurious-resync bug Hub's own doc comment
// describes: a subscriber gated out of an event (by MinRole or
// HumanOnly) must not see its own Seq advance for that event at all, or
// a later, actually-received event would look exactly like a dropped
// one to internal/web/events.go's gap detector. Two subscribers share a
// project; one is gated out of the first event, and both then receive a
// second, ungated event, which must arrive as Seq 1 for the gated-out
// subscriber (its first-ever received event) and Seq 2 for the one that
// received both.
func TestPublishSequenceSkipsEventsFilteredOutForThisSubscription(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()
	owner := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(owner)
	viewer := hub.Subscribe(project, "viewer", false)
	defer hub.Unsubscribe(viewer)

	hub.Publish(realtime.Event{ProjectID: project, Kind: "owner-only", MinRole: "owner"})
	hub.Publish(realtime.Event{ProjectID: project, Kind: "everyone"})

	first := <-owner.C
	second := <-owner.C
	if first.Seq != 1 || first.Kind != "owner-only" {
		t.Fatalf("owner's first event = %+v, want Seq 1, Kind owner-only", first)
	}
	if second.Seq != 2 || second.Kind != "everyone" {
		t.Fatalf("owner's second event = %+v, want Seq 2, Kind everyone", second)
	}

	onlyEvent := <-viewer.C
	if onlyEvent.Seq != 1 {
		t.Fatalf("viewer's only event: Seq = %d, want 1 (the owner-only event must not have advanced its counter)", onlyEvent.Seq)
	}
	if onlyEvent.Kind != "everyone" {
		t.Fatalf("viewer's only event: Kind = %q, want everyone", onlyEvent.Kind)
	}
}

// TestPublishSequenceAdvancesOnADropEvenThoughNothingWasSent pins the
// other half of the same contract: a subscriber whose buffer is full
// still has its Seq advance for the event it just missed (Publish's own
// doc comment: "increment first, attempt the send second, keep the
// outcome of the send from influencing the counter either way"), so the
// gap this leaves is exactly the size of what was actually dropped, not
// silently absorbed the way a filtered event correctly is.
func TestPublishSequenceAdvancesOnADropEvenThoughNothingWasSent(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()
	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	// Fill the buffer, then publish one more that can only be dropped.
	for i := 0; i < 64; i++ {
		hub.Publish(realtime.Event{ProjectID: project, Kind: "filler"})
	}
	hub.Publish(realtime.Event{ProjectID: project, Kind: "dropped"})

	// Drain one slot and publish a real, receivable event.
	<-sub.C
	hub.Publish(realtime.Event{ProjectID: project, Kind: "after-the-drop"})

	// 63 fillers (the first was already drained above) plus
	// after-the-drop are now buffered: draining all of them lands on
	// after-the-drop last.
	var last realtime.Event
	for i := 0; i < 64; i++ {
		last = <-sub.C
	}
	if last.Kind != "after-the-drop" {
		t.Fatalf("last received event Kind = %q, want after-the-drop", last.Kind)
	}
	if last.Seq != 66 {
		t.Fatalf("after-the-drop Seq = %d, want 66 (64 filler + 1 dropped + itself)", last.Seq)
	}
}

// TestPublishFiltersByHumanOnly pins the other delivery gate Publish
// checks alongside MinRole: a subscription admitted under a bearer
// token (IsToken true) never receives an event marked HumanOnly, even
// when its Role would otherwise meet the event's MinRole — the gap a
// quality review found a token caller's own MinRole-only gating left
// open (Event.HumanOnly's own doc comment).
func TestPublishFiltersByHumanOnly(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()

	human := hub.Subscribe(project, "editor", false)
	defer hub.Unsubscribe(human)
	token := hub.Subscribe(project, "editor", true)
	defer hub.Unsubscribe(token)

	hub.Publish(realtime.Event{ProjectID: project, Kind: "token.minted", MinRole: "editor", HumanOnly: true})
	hub.Publish(realtime.Event{ProjectID: project, Kind: "game.deleted"})

	first := <-human.C
	if first.Kind != "token.minted" {
		t.Fatalf("human subscriber's first Kind = %q, want token.minted", first.Kind)
	}

	onlyEvent := <-token.C
	if onlyEvent.Kind != "game.deleted" {
		t.Fatalf("token subscriber's only event Kind = %q, want game.deleted (token.minted should have been filtered by HumanOnly)", onlyEvent.Kind)
	}
}

// TestPublishFiltersByMinRole pins role-gated delivery: a subscriber
// whose Role does not meet an event's MinRole never receives it, one
// that does receives it, and an event with no MinRole reaches everyone
// regardless of role — the default every event uses today.
func TestPublishFiltersByMinRole(t *testing.T) {
	hub := realtime.NewHub()
	project := uuid.New()

	owner := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(owner)
	viewer := hub.Subscribe(project, "viewer", false)
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
	sub := hub.Subscribe(project, "viewer", false)
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

	sub1 := hub.Subscribe(a, "owner", false)
	sub2 := hub.Subscribe(a, "viewer", false)
	subB := hub.Subscribe(b, "owner", false)
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
