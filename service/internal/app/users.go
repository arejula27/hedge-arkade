package app

import (
	"context"
	"fmt"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/google/uuid"
)

// CreateUser makes a demo participant: a name, a key, and a wallet.
//
// The service generates the key because the demo holds the wallets. In the
// version that ships, the user arrives with a public key their own wallet
// already has and this call never sees a private one.
func (a *App) CreateUser(ctx context.Context, name string) (domain.User, error) {
	clean, err := domain.ValidateName(name)
	if err != nil {
		return domain.User{}, err
	}

	seed, err := btcec.NewPrivateKey()
	if err != nil {
		return domain.User{}, fmt.Errorf("generating a key: %w", err)
	}

	u := domain.User{
		ID:        uuid.New(),
		Name:      clean,
		PublicKey: seed.PubKey().SerializeCompressed(),
	}
	if err := a.users.Create(ctx, u, seed.Serialize()); err != nil {
		return domain.User{}, err
	}
	return u, nil
}

func (a *App) User(ctx context.Context, id uuid.UUID) (domain.User, error) {
	return a.users.Get(ctx, id)
}

func (a *App) Users(ctx context.Context) ([]domain.User, error) {
	return a.users.List(ctx)
}

// Wallet is what a user can see about their own money.
type Wallet struct {
	OffchainAddress string
	BoardingAddress string
	SpendableSats   int64
}

func (a *App) Wallet(ctx context.Context, user uuid.UUID) (Wallet, error) {
	offchain, boarding, err := a.stack.Addresses(ctx, user)
	if err != nil {
		return Wallet{}, err
	}
	balance, err := a.stack.Balance(ctx, user)
	if err != nil {
		return Wallet{}, err
	}
	return Wallet{
		OffchainAddress: offchain,
		BoardingAddress: boarding,
		SpendableSats:   balance,
	}, nil
}

// TopUp boards sats from the regtest faucet.
//
// It takes tens of seconds: the faucet pays the boarding address, the operator
// has to see the payment confirmed, and a batch has to close before the money
// is a spendable VTXO.
func (a *App) TopUp(ctx context.Context, user uuid.UUID, sats int64) error {
	if sats <= 0 {
		return fmt.Errorf("an amount must be positive, got %d", sats)
	}
	return a.stack.TopUp(ctx, user, sats)
}
