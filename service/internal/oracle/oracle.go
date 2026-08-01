// Package oracle publishes signed prices on a fixed cadence.
//
// It knows nothing about any contract. One oracle serves every contract, and it
// can be entirely disconnected from Arkade: it signs a 24-byte message and
// stores it, and that is all.
//
// The message layout and the signing are `contract`'s, so the bytes a covenant
// verifies are built in exactly one place.
package oracle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/arejula27/hedge/contract"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// Publication is one signed price.
type Publication struct {
	Sequence  uint64
	Timestamp int64
	Price     int64
	Message   []byte
	Signature []byte
}

// Sign builds and signs the message for a sequence the store has just
// allocated. The sequence is inside the message, so it cannot be signed before
// the transaction that allocates it.
type Sign func(sequence uint64, timestamp int64, price int64) (message, signature []byte, err error)

// Store keeps every publication, forever.
//
// History is not an archive here, it is an input: settling requires the
// message *and its immediate predecessor*, so a publication that is thrown away
// takes a settlement with it.
type Store interface {
	// Append allocates the next sequence, signs the message with it, and
	// writes the result in one transaction.
	//
	// The sequence must have no gaps. A number that is allocated and then
	// rolled back can never be published, and every settlement that would have
	// needed it as a predecessor becomes impossible.
	Append(ctx context.Context, timestamp, price int64, sign Sign) (Publication, error)

	Latest(ctx context.Context) (Publication, error)
	At(ctx context.Context, sequence uint64) (Publication, error)
	History(ctx context.Context, limit int) ([]Publication, error)
}

// ErrNoPublications is what a store returns when it has none, or none at the
// sequence asked for.
var ErrNoPublications = fmt.Errorf("no publication")

// Publisher signs prices and hands them to the store.
type Publisher struct {
	store Store
	key   *btcec.PrivateKey
	now   func() time.Time

	mu    sync.Mutex
	price int64
}

func NewPublisher(store Store, key *btcec.PrivateKey, startPrice int64) *Publisher {
	return &Publisher{store: store, key: key, now: time.Now, price: startPrice}
}

// PublicKey is the 32-byte x-only key that goes into Terms.OraclePubKey.
func (p *Publisher) PublicKey() []byte {
	return schnorr.SerializePubKey(p.key.PubKey())
}

func (p *Publisher) Price() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.price
}

func (p *Publisher) SetPrice(price int64) error {
	if price < 1 {
		return fmt.Errorf("a price must be positive, got %d", price)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.price = price
	return nil
}

// Publish writes one publication at the current price.
func (p *Publisher) Publish(ctx context.Context) (Publication, error) {
	return p.store.Append(ctx, p.now().Unix(), p.Price(), p.sign)
}

func (p *Publisher) sign(sequence uint64, timestamp, price int64) ([]byte, []byte, error) {
	message := contract.OracleMessage(uint64(timestamp), sequence, uint64(price))
	signature, err := contract.SignOracleMessage(p.key, message)
	if err != nil {
		return nil, nil, fmt.Errorf("signing sequence %d: %w", sequence, err)
	}
	return message, signature, nil
}

// Run publishes once immediately and then on every tick.
//
// The first publication matters: a contract cannot settle until two messages
// exist, so a store that starts empty and waits a full interval is a store that
// cannot settle anything for that interval.
func (p *Publisher) Run(ctx context.Context, every time.Duration, onError func(error)) {
	publish := func() {
		if _, err := p.Publish(ctx); err != nil && onError != nil {
			onError(err)
		}
	}

	publish()

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish()
		}
	}
}

// Pair is a settlement message and its immediate predecessor, which is the
// witness the covenant needs.
type Pair struct {
	Settlement Publication
	Previous   Publication
}

// LatestPair is the most recent publication and the one before it.
func LatestPair(ctx context.Context, store Store) (Pair, error) {
	latest, err := store.Latest(ctx)
	if err != nil {
		return Pair{}, err
	}
	return PairAt(ctx, store, latest.Sequence)
}

// PairAt is the publication at sequence and the one before it.
//
// Asking the oracle for a pair rather than assembling one from two queries is
// what keeps the adjacency rule in the one place that can guarantee it.
func PairAt(ctx context.Context, store Store, sequence uint64) (Pair, error) {
	if sequence < 2 {
		return Pair{}, fmt.Errorf("sequence %d has no predecessor: %w", sequence, ErrNoPublications)
	}

	settlement, err := store.At(ctx, sequence)
	if err != nil {
		return Pair{}, err
	}
	previous, err := store.At(ctx, sequence-1)
	if err != nil {
		return Pair{}, err
	}
	return Pair{Settlement: settlement, Previous: previous}, nil
}
