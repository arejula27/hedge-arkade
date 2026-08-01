package domain

import (
	"errors"
	"fmt"
)

// ErrTransition is a move the lifecycle does not allow. It is what a caller
// gets when someone else got to the contract first, so it belongs with the
// conflicts rather than with the failures.
var ErrTransition = errors.New("illegal transition")

// ErrUnknownState is a state the domain has never heard of. That is a bug, not
// a race.
var ErrUnknownState = errors.New("unknown state")

// State is where a contract is in its life.
//
// The transient states — funding, settling, redeeming, exiting, arbitrating —
// exist because each of those steps takes tens of seconds to minutes against a
// live stack, and the process can die in the middle of one. A contract sitting
// in a transient state is one a worker has to pick up and finish, so every step
// has to be resumable from the row alone.
type State string

const (
	// Proposed: one side exists and is looking for the other.
	Proposed State = "proposed"
	// Accepted: both sides agreed. The address is derivable from here on.
	Accepted State = "accepted"
	// Funding: the collateral is on its way into one Arkade transaction.
	Funding State = "funding"
	// Active: the contract VTXO holds exactly PayoutSats.
	Active State = "active"

	// Settling: the covenant is being spent through leaf 1.
	Settling State = "settling"
	Settled  State = "settled"

	// RedemptionProposed: an early close is waiting for the second signature.
	RedemptionProposed State = "redemption_proposed"
	Redeeming          State = "redeeming"
	Redeemed           State = "redeemed"

	// Exiting: the pre-signed exit is being unrolled and broadcast.
	Exiting State = "exiting"
	// Exited: the money is in the 2-of-3 on chain and the covenant is gone.
	Exited State = "exited"
	// Arbitrating: the service has proposed a split from an oracle price.
	Arbitrating State = "arbitrating"
	Arbitrated  State = "arbitrated"

	// Cancelled: withdrawn before any money moved.
	Cancelled State = "cancelled"
	// Failed: funding did not complete. No contract VTXO exists.
	Failed State = "failed"
)

// transitions is the whole of what may follow what.
//
// A step that fails goes back where it came from rather than to a state of its
// own: a settlement arkd refuses leaves the contract exactly as it was, still
// active and still settleable.
var transitions = map[State][]State{
	Proposed: {Accepted, Cancelled},
	Accepted: {Funding, Cancelled},
	Funding:  {Active, Failed},
	Active:   {Settling, RedemptionProposed, Exiting},

	Settling: {Settled, Active},

	RedemptionProposed: {Redeeming, Active},
	Redeeming:          {Redeemed, Active},

	Exiting:     {Exited, Active},
	Exited:      {Arbitrating},
	Arbitrating: {Arbitrated, Exited},

	Settled:    nil,
	Redeemed:   nil,
	Arbitrated: nil,
	Cancelled:  nil,
	Failed:     nil,
}

// States is every state, in lifecycle order. The database's CHECK constraint
// lists the same set, so a bug cannot write one the domain does not know.
func States() []State {
	return []State{
		Proposed, Accepted, Funding, Active,
		Settling, Settled,
		RedemptionProposed, Redeeming, Redeemed,
		Exiting, Exited, Arbitrating, Arbitrated,
		Cancelled, Failed,
	}
}

func (s State) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Terminal says whether anything can follow. A terminal contract is one the
// worker never has to look at again.
func (s State) Terminal() bool {
	return len(transitions[s]) == 0
}

func (s State) CanGoTo(next State) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Transition checks a move and reports what was refused, so the error can be
// handed to a caller as a 409 without the handler restating the rule.
func (s State) Transition(next State) error {
	if !s.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownState, s)
	}
	if !next.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownState, next)
	}
	if !s.CanGoTo(next) {
		return fmt.Errorf("%w: a contract cannot go from %s to %s", ErrTransition, s, next)
	}
	return nil
}

func (s State) String() string { return string(s) }
