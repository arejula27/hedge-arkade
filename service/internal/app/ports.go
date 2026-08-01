// Package app is the service's use cases: what happens when someone proposes a
// contract, accepts one, funds it, or settles it.
//
// Everything it touches from the outside is a port defined here, next to the
// code that needs it. Nothing in this package knows there is a database, an
// HTTP server, or a live Arkade stack.
package app

import (
	"context"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/service/internal/domain"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/google/uuid"
)

// Users is where people and their keys live.
type Users interface {
	Create(ctx context.Context, u domain.User, seed []byte) error
	Get(ctx context.Context, id uuid.UUID) (domain.User, error)
	List(ctx context.Context) ([]domain.User, error)
}

// Contracts is the contract store. Advance is the only way a contract moves,
// and it refuses if the row is no longer in the state it was read in.
type Contracts interface {
	Create(ctx context.Context, c *domain.Contract) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Contract, error)
	List(ctx context.Context, f domain.ContractFilter) ([]*domain.Contract, error)
	InState(ctx context.Context, states ...domain.State) ([]*domain.Contract, error)
	Advance(ctx context.Context, c *domain.Contract, to domain.State, detail string) error

	// Save writes a contract without moving it, for a step that has produced
	// something irreversible and has to record it before the next thing that
	// might fail.
	Save(ctx context.Context, c *domain.Contract) error
	Events(ctx context.Context, id uuid.UUID) ([]domain.Event, error)
}

// Exits stores the pre-signed unilateral exit. It is written once, at funding,
// and read only when a party gives up on the operator.
type Exits interface {
	Put(ctx context.Context, e domain.ExitPackage) error
	Get(ctx context.Context, contract uuid.UUID) (domain.ExitPackage, error)
}

// Signer is every operation that needs a party's own key.
//
// No method takes or returns a private key: each one takes a user and returns
// bytes. That is the whole acceptance criterion for the version where the key
// lives on the user's phone instead of in this process — the implementation
// changes and no use case does.
//
// It is also why the service never calls contract.PreSignExit, which wants both
// parties' keys in one call: the exit is composed from one deterministic
// ExitTx and two independent SignExit calls, which may arrive minutes apart.
type Signer interface {
	PublicKey(ctx context.Context, user uuid.UUID) (*btcec.PublicKey, error)

	// SignPacket signs every input of a PSBT that names the user's key, and
	// leaves the rest alone.
	SignPacket(ctx context.Context, user uuid.UUID, packetB64 string) (string, error)

	// SignExit signs one side of the pre-signed unilateral exit.
	SignExit(ctx context.Context, user uuid.UUID, c *domain.Contract, exit *domain.Exit) ([]byte, error)
}

// Stack is what the operator and the emulator told us about themselves, read at
// startup and never guessed.
type Stack struct {
	ArkdSigner     *btcec.PublicKey
	EmulatorSigner *btcec.PublicKey
	ExitDelay      arklib.RelativeLocktime
	Dust           uint64
	// AllowsBlockTimelocks is the operator's own policy, read off its exit
	// delay: regtest counts blocks, production counts seconds.
	AllowsBlockTimelocks bool
}

// Arkade is the live stack, in the shape the use cases need it.
type Arkade interface {
	Stack() Stack

	// Addresses is where a user can be paid: offchain from inside Arkade,
	// boarding from the chain.
	Addresses(ctx context.Context, user uuid.UUID) (offchain, boarding string, err error)
	Balance(ctx context.Context, user uuid.UUID) (int64, error)

	// VtxoPkScript is where a user is paid inside Arkade, and so what a
	// contract's payout has to name.
	VtxoPkScript(ctx context.Context, user uuid.UUID) ([]byte, error)

	// TopUp boards sats from the chain's faucet. It only exists on regtest.
	TopUp(ctx context.Context, user uuid.UUID, sats int64) error

	// Fund puts both parties' collateral into the contract in one Arkade
	// transaction, and returns the contract VTXO.
	Fund(ctx context.Context, c *domain.Contract) (domain.Outpoint, error)

	// Settle spends the contract through the covenant, paying each side what
	// the formula says at the oracle's price.
	Settle(ctx context.Context, c *domain.Contract, short, long int64, pair Pair) error
}

// Pair is a settlement message and its immediate predecessor. The covenant
// needs both, and only the oracle can promise they are adjacent.
type Pair struct {
	Settlement arkade.SignedPrice
	Previous   arkade.SignedPrice
}

// Feed is the oracle, over HTTP.
type Feed interface {
	// PubKey is the 32-byte x-only key a contract is built against.
	PubKey(ctx context.Context) ([]byte, error)
	Latest(ctx context.Context) (domain.Price, error)
	Pair(ctx context.Context) (Pair, error)
	SetPrice(ctx context.Context, price int64) error
	History(ctx context.Context, limit int) ([]domain.Price, error)
}
