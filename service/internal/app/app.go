package app

import (
	"errors"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
)

// ErrNotAllowed is a request that is well formed but that this user may not
// make: accepting their own proposal, funding someone else's contract.
var ErrNotAllowed = errors.New("not allowed")

// ErrNotYet is a step the contract is not ready for — settling one whose price
// is neither out of bounds nor matured. It is not a failure, it is a "come
// back later", and the reason travels with it.
type ErrNotYet struct{ Reason string }

func (e ErrNotYet) Error() string { return e.Reason }

// App is the use cases. It is built once, at startup, from the adapters.
type App struct {
	users     Users
	contracts Contracts
	exits     Exits
	signer    Signer
	stack     Arkade
	feed      Feed

	// serviceKey is the third key of the 2-of-3 a unilateral exit sweeps into.
	// It is the service's own and legitimately lives here: the coordinator
	// holds this and the oracle's key, and never a party's.
	serviceKey *btcec.PublicKey

	// exitFeeSats is what the unilateral exit pays a miner. It is generous:
	// the transaction is ~180 vbytes and there is no second chance to fee-bump
	// something both parties signed months earlier.
	exitFeeSats int64

	now func() time.Time
}

type Options struct {
	Users     Users
	Contracts Contracts
	Exits     Exits
	Signer    Signer
	Stack     Arkade
	Feed      Feed

	ServiceKey  *btcec.PublicKey
	ExitFeeSats int64
}

func New(o Options) *App {
	fee := o.ExitFeeSats
	if fee <= 0 {
		fee = 2_000
	}

	return &App{
		users:     o.Users,
		contracts: o.Contracts,
		exits:     o.Exits,
		signer:    o.Signer,
		stack:     o.Stack,
		feed:      o.Feed,

		serviceKey: o.ServiceKey,

		exitFeeSats: fee,
		now:         time.Now,
	}
}

// Stack is what the operator reported at startup, for the banner that says this
// is a real stack rather than a mock.
func (a *App) Stack() Stack { return a.stack.Stack() }

func notYet(format string, args ...any) error {
	return ErrNotYet{Reason: fmt.Sprintf(format, args...)}
}
