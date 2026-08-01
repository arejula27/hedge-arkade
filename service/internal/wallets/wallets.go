// Package wallets keeps one Arkade wallet per demo user.
//
// It exists because the demo holds the wallets, and it is the only package that
// ever sees a party's private key. When wallets move to the user's own device
// this package goes with them: everything above it already talks to a port.
package wallets

import (
	"context"
	"fmt"
	"sync"

	"github.com/arejula27/hedge/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/google/uuid"
)

// Seeds is where a user's wallet key comes from.
type Seeds interface {
	Seed(ctx context.Context, user uuid.UUID) ([]byte, error)
}

// Registry builds a wallet the first time a user needs one and keeps it.
//
// Building one is not cheap — it initialises the SDK and opens three
// connections — and the SDK's stores are in memory, so a wallet rebuilt on
// every request would lose whatever it had learned about the chain. The seed is
// what persists; everything else is derived from it and from the operator.
type Registry struct {
	stack *arkade.Stack
	seeds Seeds

	mu    sync.Mutex
	built map[uuid.UUID]*arkade.Wallet
}

func New(stack *arkade.Stack, seeds Seeds) *Registry {
	return &Registry{stack: stack, seeds: seeds, built: map[uuid.UUID]*arkade.Wallet{}}
}

func (r *Registry) Wallet(ctx context.Context, user uuid.UUID) (*arkade.Wallet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if w, ok := r.built[user]; ok {
		return w, nil
	}

	key, err := r.key(ctx, user)
	if err != nil {
		return nil, err
	}

	w, err := arkade.NewWallet(ctx, r.stack, key)
	if err != nil {
		return nil, fmt.Errorf("building %s's wallet: %w", user, err)
	}
	r.built[user] = w
	return w, nil
}

// Key is the user's private key, for the paths that cannot go through the
// wallet: a contract leaf is not a leaf the wallet knows about.
func (r *Registry) Key(ctx context.Context, user uuid.UUID) (*btcec.PrivateKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.key(ctx, user)
}

func (r *Registry) key(ctx context.Context, user uuid.UUID) (*btcec.PrivateKey, error) {
	seed, err := r.seeds.Seed(ctx, user)
	if err != nil {
		return nil, err
	}
	if len(seed) != 32 {
		return nil, fmt.Errorf("%s's seed is %d bytes, want 32", user, len(seed))
	}
	key, _ := btcec.PrivKeyFromBytes(seed)
	return key, nil
}
