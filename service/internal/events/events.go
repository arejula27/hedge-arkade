// Package events announces contract transitions to whoever is watching.
//
// The demo is two browser tabs looking at the same contract, so the thing that
// matters is that a state change reaches the tab that did not cause it.
package events

import (
	"context"
	"sync"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

// Event is one transition, in the shape a browser wants it.
type Event struct {
	Contract uuid.UUID
	State    domain.State
	Detail   string
}

// Broker fans transitions out to every subscriber.
//
// Delivery is best effort: a subscriber whose buffer is full is a browser that
// has stopped reading, and blocking the transition that is trying to reach it
// would take the whole service down with one wedged tab.
type Broker struct {
	mu    sync.Mutex
	next  int
	feeds map[int]chan Event
}

func NewBroker() *Broker {
	return &Broker{feeds: map[int]chan Event{}}
}

const backlog = 16

// Subscribe returns a channel and the way to stop. The channel is closed on
// unsubscribe, so a reader ranging over it terminates.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.next
	b.next++

	feed := make(chan Event, backlog)
	b.feeds[id] = feed

	return feed, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if existing, ok := b.feeds[id]; ok {
			delete(b.feeds, id)
			close(existing)
		}
	}
}

func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, feed := range b.feeds {
		select {
		case feed <- e:
		default:
		}
	}
}

// Subscribers is how many are listening. It exists so a test can prove
// unsubscribing does not leak.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.feeds)
}

// Publishing wraps a contract store and announces every transition it makes.
//
// Advance is the only way a contract moves, so wrapping it is the only place
// this has to happen — no use case has to remember to publish, and none of them
// knows anyone is listening.
type Publishing struct {
	app.Contracts
	broker *Broker
}

func Announce(contracts app.Contracts, broker *Broker) *Publishing {
	return &Publishing{Contracts: contracts, broker: broker}
}

func (p *Publishing) Advance(
	ctx context.Context, c *domain.Contract, to domain.State, detail string,
) error {
	if err := p.Contracts.Advance(ctx, c, to, detail); err != nil {
		return err
	}
	p.broker.Publish(Event{Contract: c.ID, State: to, Detail: detail})
	return nil
}
