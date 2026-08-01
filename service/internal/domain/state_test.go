package domain

import (
	"errors"
	"strings"
	"testing"
)

// The whole matrix, every pair, allowed and denied. A typo in the transition
// table is otherwise invisible until a contract gets stuck in production, and
// the table is small enough that asserting all of it is cheaper than choosing
// which parts to trust.
func TestTheTransitionMatrix(t *testing.T) {
	allowed := map[State]map[State]bool{
		Proposed:           {Accepted: true, Cancelled: true},
		Accepted:           {Funding: true, Cancelled: true},
		Funding:            {Active: true, Failed: true},
		Active:             {Settling: true, RedemptionProposed: true, Exiting: true},
		Settling:           {Settled: true, Active: true},
		RedemptionProposed: {Redeeming: true, Active: true},
		Redeeming:          {Redeemed: true, Active: true},
		Exiting:            {Exited: true, Active: true},
		Exited:             {Arbitrating: true},
		Arbitrating:        {Arbitrated: true, Exited: true},
		Settled:            {},
		Redeemed:           {},
		Arbitrated:         {},
		Cancelled:          {},
		Failed:             {},
	}

	if len(allowed) != len(States()) {
		t.Fatalf("the table covers %d states, there are %d", len(allowed), len(States()))
	}

	for _, from := range States() {
		for _, to := range States() {
			want := allowed[from][to]
			if got := from.CanGoTo(to); got != want {
				t.Errorf("%s -> %s is %v, want %v", from, to, got, want)
			}
		}
	}
}

// A contract cannot go back to being unfunded, and money that has moved cannot
// unmove. These are the ones worth naming separately from the matrix.
func TestTheMovesThatMustNeverBeAllowed(t *testing.T) {
	for _, tc := range []struct{ from, to State }{
		{Active, Proposed},
		{Active, Accepted},
		{Active, Cancelled},
		{Settled, Active},
		{Settled, Settling},
		{Redeemed, Active},
		{Arbitrated, Exited},
		{Cancelled, Accepted},
		{Failed, Active},
		{Proposed, Active},
		{Proposed, Funding},
		{Accepted, Active},
		{Exited, Active},
	} {
		t.Run(string(tc.from)+" to "+string(tc.to), func(t *testing.T) {
			if tc.from.CanGoTo(tc.to) {
				t.Errorf("%s -> %s is allowed", tc.from, tc.to)
			}
		})
	}
}

func TestTerminalStates(t *testing.T) {
	terminal := map[State]bool{
		Settled: true, Redeemed: true, Arbitrated: true, Cancelled: true, Failed: true,
	}

	for _, s := range States() {
		if got := s.Terminal(); got != terminal[s] {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, terminal[s])
		}
	}
}

// Every state has to be reachable from proposed, or it is a state nothing can
// ever be in.
func TestEveryStateIsReachableFromProposed(t *testing.T) {
	seen := map[State]bool{Proposed: true}
	frontier := []State{Proposed}

	for len(frontier) > 0 {
		from := frontier[0]
		frontier = frontier[1:]
		for _, to := range States() {
			if from.CanGoTo(to) && !seen[to] {
				seen[to] = true
				frontier = append(frontier, to)
			}
		}
	}

	for _, s := range States() {
		if !seen[s] {
			t.Errorf("%s is unreachable from proposed", s)
		}
	}
}

// And every non-terminal state has to lead somewhere terminal, or a contract
// that reaches it can never be closed.
func TestEveryStateReachesATerminalOne(t *testing.T) {
	for _, start := range States() {
		seen := map[State]bool{start: true}
		frontier := []State{start}
		reached := false

		for len(frontier) > 0 && !reached {
			from := frontier[0]
			frontier = frontier[1:]
			if from.Terminal() {
				reached = true
				break
			}
			for _, to := range States() {
				if from.CanGoTo(to) && !seen[to] {
					seen[to] = true
					frontier = append(frontier, to)
				}
			}
		}

		if !reached {
			t.Errorf("a contract in %s can never be closed", start)
		}
	}
}

func TestTransitionSaysWhatItRefused(t *testing.T) {
	err := Active.Transition(Proposed)
	if err == nil {
		t.Fatal("Transition allowed active -> proposed")
	}
	if !strings.Contains(err.Error(), "active") || !strings.Contains(err.Error(), "proposed") {
		t.Errorf("the error does not name both states: %v", err)
	}

	if err := Active.Transition(Settling); err != nil {
		t.Errorf("Transition refused active -> settling: %v", err)
	}
}

// A refused transition means somebody else got there first, which is a
// conflict. An invented state means a bug. A caller has to be able to tell them
// apart without reading the message.
func TestARefusedTransitionIsDistinguishableFromABug(t *testing.T) {
	const nonsense State = "pending-vibes"

	if nonsense.Valid() {
		t.Error("an invented state reports itself valid")
	}

	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"a move the lifecycle forbids", Active.Transition(Proposed), ErrTransition},
		{"an invented source", nonsense.Transition(Active), ErrUnknownState},
		{"an invented target", Active.Transition(nonsense), ErrUnknownState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("Transition accepted it")
			}
			if !errors.Is(tc.err, tc.want) {
				t.Errorf("%v is not a %v", tc.err, tc.want)
			}
		})
	}

	if errors.Is(Active.Transition(Proposed), ErrUnknownState) {
		t.Error("a legal state reported as unknown")
	}
	if errors.Is(Active.Transition(nonsense), ErrTransition) {
		t.Error("an invented state reported as a losable race")
	}
}
