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

	sub := hub.Subscribe(project)
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

	sub := hub.Subscribe(mine)
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
	sub := hub.Subscribe(project)
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
		sub := hub.Subscribe(uuid.New())
		hub.Unsubscribe(sub)
	}

	project := uuid.New()
	sub := hub.Subscribe(project)
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
