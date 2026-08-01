package oracle

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arejula27/hedge/contract"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// memoryStore is a Store that keeps its publications in a slice. It allocates
// the sequence the same way postgres does — read the last, add one — so the
// density the covenant needs is visible here as well as in the database test.
type memoryStore struct {
	published []Publication
	failWith  error
}

func (m *memoryStore) Append(_ context.Context, timestamp, price int64, sign Sign) (Publication, error) {
	if m.failWith != nil {
		return Publication{}, m.failWith
	}

	sequence := uint64(len(m.published)) + 1
	message, signature, err := sign(sequence, timestamp, price)
	if err != nil {
		return Publication{}, err
	}

	p := Publication{
		Sequence: sequence, Timestamp: timestamp, Price: price,
		Message: message, Signature: signature,
	}
	m.published = append(m.published, p)
	return p, nil
}

func (m *memoryStore) Latest(context.Context) (Publication, error) {
	if len(m.published) == 0 {
		return Publication{}, ErrNoPublications
	}
	return m.published[len(m.published)-1], nil
}

func (m *memoryStore) At(_ context.Context, sequence uint64) (Publication, error) {
	if sequence == 0 || sequence > uint64(len(m.published)) {
		return Publication{}, ErrNoPublications
	}
	return m.published[sequence-1], nil
}

func (m *memoryStore) History(_ context.Context, limit int) ([]Publication, error) {
	if limit > len(m.published) {
		limit = len(m.published)
	}
	out := make([]Publication, 0, limit)
	for i := len(m.published) - 1; i >= len(m.published)-limit; i-- {
		out = append(out, m.published[i])
	}
	return out, nil
}

var testKey, _ = btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x11}, 32))

func newTestPublisher(store Store, price int64) *Publisher {
	p := NewPublisher(store, testKey, price)
	p.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return p
}

// The message the oracle signs has to be the one a covenant will verify, and
// there is exactly one place that layout is written down.
func TestPublishSignsTheMessageTheCovenantVerifies(t *testing.T) {
	store := &memoryStore{}
	p := newTestPublisher(store, 10_000_000)

	got, err := p.Publish(t.Context())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	want := contract.OracleMessage(1_800_000_000, 1, 10_000_000)
	if !bytes.Equal(got.Message, want) {
		t.Errorf("message %x, want %x", got.Message, want)
	}
	if len(got.Signature) != 64 {
		t.Errorf("signature is %d bytes, want 64", len(got.Signature))
	}

	// The same check the covenant runs: the signature is over sha256 of the
	// message, against the x-only key the contract was built with.
	c := contract.Contract{Terms: contract.Terms{OraclePubKey: p.PublicKey()}}
	price, err := c.PriceFrom(got.Message, got.Signature)
	if err != nil {
		t.Fatalf("a contract cannot verify what the oracle signed: %v", err)
	}
	if price != 10_000_000 {
		t.Errorf("the contract read a price of %d, want 10000000", price)
	}
}

func TestPublicKeyIsTheXOnlyKeyAContractCarries(t *testing.T) {
	p := newTestPublisher(&memoryStore{}, 1)

	got := p.PublicKey()
	if len(got) != 32 {
		t.Fatalf("PublicKey is %d bytes, want 32", len(got))
	}
	if want := schnorr.SerializePubKey(testKey.PubKey()); !bytes.Equal(got, want) {
		t.Errorf("PublicKey = %x, want %x", got, want)
	}
}

func TestSequencesAreDense(t *testing.T) {
	store := &memoryStore{}
	p := newTestPublisher(store, 10_000_000)

	for i := 1; i <= 5; i++ {
		got, err := p.Publish(t.Context())
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if got.Sequence != uint64(i) {
			t.Fatalf("publication %d has sequence %d", i, got.Sequence)
		}
	}
}

func TestSetPriceRefusesWhatCannotBeAPrice(t *testing.T) {
	p := newTestPublisher(&memoryStore{}, 10_000_000)

	for _, price := range []int64{0, -1, -10_000_000} {
		if err := p.SetPrice(price); err == nil {
			t.Errorf("SetPrice(%d) was accepted", price)
		}
	}
	if p.Price() != 10_000_000 {
		t.Errorf("a refused price changed the current one to %d", p.Price())
	}
}

func TestPublishUsesTheCurrentPrice(t *testing.T) {
	store := &memoryStore{}
	p := newTestPublisher(store, 10_000_000)

	if err := p.SetPrice(4_999_999); err != nil {
		t.Fatalf("SetPrice: %v", err)
	}
	got, err := p.Publish(t.Context())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got.Price != 4_999_999 {
		t.Errorf("published %d, want 4999999", got.Price)
	}
}

// A pair is the message and the one immediately before it. Anything else is a
// witness the covenant refuses.
func TestPairIsAdjacent(t *testing.T) {
	store := &memoryStore{}
	p := newTestPublisher(store, 10_000_000)
	for range 4 {
		if _, err := p.Publish(t.Context()); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	for _, tc := range []struct {
		name             string
		pair             func() (Pair, error)
		settle, previous uint64
	}{
		{"the latest", func() (Pair, error) { return LatestPair(t.Context(), store) }, 4, 3},
		{"an earlier one", func() (Pair, error) { return PairAt(t.Context(), store, 2) }, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, err := tc.pair()
			if err != nil {
				t.Fatalf("pair: %v", err)
			}
			if pair.Settlement.Sequence != tc.settle {
				t.Errorf("settlement sequence %d, want %d", pair.Settlement.Sequence, tc.settle)
			}
			if pair.Previous.Sequence != tc.previous {
				t.Errorf("previous sequence %d, want %d", pair.Previous.Sequence, tc.previous)
			}
			if pair.Settlement.Sequence != pair.Previous.Sequence+1 {
				t.Error("the pair is not adjacent")
			}
		})
	}
}

// A contract cannot settle until two messages exist, so asking for a pair too
// early has to say so rather than return half of one.
func TestPairNeedsTwoPublications(t *testing.T) {
	store := &memoryStore{}
	p := newTestPublisher(store, 10_000_000)

	if _, err := LatestPair(t.Context(), store); !errors.Is(err, ErrNoPublications) {
		t.Errorf("an empty oracle gave %v", err)
	}

	if _, err := p.Publish(t.Context()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := LatestPair(t.Context(), store); !errors.Is(err, ErrNoPublications) {
		t.Errorf("the very first publication has no predecessor, got %v", err)
	}

	if _, err := p.Publish(t.Context()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := LatestPair(t.Context(), store); err != nil {
		t.Errorf("two publications should make a pair: %v", err)
	}
}

func TestPairAtRefusesASequenceWithNoPredecessor(t *testing.T) {
	store := &memoryStore{}
	if _, err := PairAt(t.Context(), store, 1); !errors.Is(err, ErrNoPublications) {
		t.Errorf("PairAt(1) gave %v", err)
	}
}

// Waiting a full interval before the first publication is an oracle that
// nothing can settle against for that long.
func TestRunPublishesImmediately(t *testing.T) {
	store := &memoryStore{}
	p := newTestPublisher(store, 10_000_000)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, time.Hour, nil)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for len(store.published) == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("Run published nothing before its first tick")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	<-done
}

func TestRunReportsFailuresAndKeepsGoing(t *testing.T) {
	store := &memoryStore{failWith: errors.New("the database is down")}
	p := newTestPublisher(store, 10_000_000)

	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go p.Run(ctx, time.Hour, func(err error) {
		select {
		case reported <- err:
		default:
		}
	})

	select {
	case err := <-reported:
		if err == nil {
			t.Error("Run reported a nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run swallowed a publication failure")
	}
}
