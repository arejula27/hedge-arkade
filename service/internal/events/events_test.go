package events

import (
	"testing"
	"time"

	"github.com/arejula27/hedge/service/internal/apptest"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

func TestEveryoneListeningGetsTheEvent(t *testing.T) {
	b := NewBroker()

	first, stopFirst := b.Subscribe()
	defer stopFirst()
	second, stopSecond := b.Subscribe()
	defer stopSecond()

	id := uuid.New()
	b.Publish(Event{Contract: id, State: domain.Active})

	for i, feed := range []<-chan Event{first, second} {
		select {
		case got := <-feed:
			if got.Contract != id || got.State != domain.Active {
				t.Errorf("subscriber %d got %+v", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d got nothing", i)
		}
	}
}

// A browser tab that goes away must not leave its channel behind, or every
// transition after it goes on writing into a buffer nobody reads.
func TestUnsubscribingDoesNotLeak(t *testing.T) {
	b := NewBroker()

	feed, stop := b.Subscribe()
	if b.Subscribers() != 1 {
		t.Fatalf("%d subscribers, want 1", b.Subscribers())
	}

	stop()

	if b.Subscribers() != 0 {
		t.Errorf("%d subscribers after unsubscribing", b.Subscribers())
	}
	if _, open := <-feed; open {
		t.Error("the channel was not closed, so a reader ranging over it would hang")
	}

	// Unsubscribing twice is what a deferred stop plus an explicit one does.
	stop()

	// And publishing to nobody is not a panic.
	b.Publish(Event{Contract: uuid.New(), State: domain.Active})
}

// A subscriber that has stopped reading must not be able to block a
// transition — one wedged tab would otherwise take the service down.
func TestASubscriberThatStoppedReadingIsSkipped(t *testing.T) {
	b := NewBroker()

	_, stop := b.Subscribe()
	defer stop()

	done := make(chan struct{})
	go func() {
		for range backlog + 10 {
			b.Publish(Event{Contract: uuid.New(), State: domain.Active})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing blocked on a subscriber that was not reading")
	}
}

// Announcing is a wrapper: a transition the store refused must not be
// announced, or a browser would show a state the database never reached.
func TestOnlyTransitionsThatHappenedAreAnnounced(t *testing.T) {
	b := NewBroker()
	store := apptest.NewContracts(time.Now)
	announcing := Announce(store, b)

	feed, stop := b.Subscribe()
	defer stop()

	c := &domain.Contract{ID: uuid.New(), State: domain.Proposed}
	if err := store.Create(t.Context(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := announcing.Advance(t.Context(), c, domain.Accepted, "bob took the long"); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	select {
	case got := <-feed:
		if got.Contract != c.ID || got.State != domain.Accepted {
			t.Errorf("announced %+v", got)
		}
		if got.Detail != "bob took the long" {
			t.Errorf("the detail is %q", got.Detail)
		}
	case <-time.After(time.Second):
		t.Fatal("the transition was not announced")
	}

	// A move the lifecycle forbids: the store refuses, so nothing is announced.
	if err := announcing.Advance(t.Context(), c, domain.Settled, ""); err == nil {
		t.Fatal("Advance took a contract straight from accepted to settled")
	}

	select {
	case got := <-feed:
		t.Errorf("a refused transition was announced as %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}
