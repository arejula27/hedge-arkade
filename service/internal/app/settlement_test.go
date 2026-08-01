package app

import (
	"errors"
	"testing"
	"time"

	"github.com/arejula27/hedge/service/internal/domain"
)

// A contract whose price is inside its boundaries and whose maturity has not
// come has nothing to settle, and saying so here is what turns a script failure
// nobody can read into a sentence.
func TestSettleRefusesAContractWithNothingToSettle(t *testing.T) {
	f := newFixture(t)
	c := f.funded(t)

	_, err := f.app.Settle(t.Context(), c.ID)

	var notReady ErrNotYet
	if !errors.As(err, &notReady) {
		t.Fatalf("Settle gave %v, want a not-yet", err)
	}

	c, _ = f.app.Contract(t.Context(), c.ID)
	if c.State != domain.Active {
		t.Errorf("the contract moved to %s anyway", c.State)
	}
	if f.stack.settled != nil {
		t.Error("something was submitted")
	}
}

// Touching a boundary is the event: the covenant clamps to it and settles, so a
// price at the boundary and one far past it pay identically.
func TestSettleTriggersOnEitherBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price int64
	}{
		{"exactly on the low boundary", 5_000_000},
		{"well below it", 1_000_000},
		{"exactly on the high boundary", 20_000_000},
		{"well above it", 90_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			c := f.funded(t)

			f.feed.price = tc.price

			if _, err := f.app.Settle(t.Context(), c.ID); err != nil {
				t.Fatalf("Settle: %v", err)
			}

			c, _ = f.app.Contract(t.Context(), c.ID)
			if c.State != domain.Settling {
				t.Fatalf("state %s, want settling", c.State)
			}
		})
	}
}

// The other trigger: maturity, with the price still inside the boundaries.
func TestSettleTriggersAtMaturity(t *testing.T) {
	f := newFixture(t)
	c := f.funded(t)

	f.feed.at = c.Terms.MaturityTimestamp

	if _, err := f.app.Settle(t.Context(), c.ID); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	c, _ = f.app.Contract(t.Context(), c.ID)
	if c.State != domain.Settling {
		t.Errorf("state %s, want settling", c.State)
	}
}

// The whole base flow: two parties fund, the price crosses a boundary, and the
// contract settles into the split the covenant's own formula gives.
func TestTheWorkerSettlesAtTheClampedPrice(t *testing.T) {
	f := newFixture(t)
	c := f.funded(t)

	// A crash to $50,000, the low boundary: the short is made whole and the
	// long takes the loss.
	f.feed.price = 5_000_000

	if _, err := f.app.Settle(t.Context(), c.ID); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	NewWorker(f.app, WorkerOptions{}).Tick(t.Context())

	c, _ = f.app.Contract(t.Context(), c.ID)
	if c.State != domain.Settled {
		t.Fatalf("state %s, want settled", c.State)
	}
	if f.stack.settled == nil {
		t.Fatal("nothing was submitted")
	}

	short, long, err := c.Split(5_000_000)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if f.stack.settleAt != [2]int64{short, long} {
		t.Errorf("settled at %v, want the formula's %d/%d", f.stack.settleAt, short, long)
	}
	if short+long != c.Terms.PayoutSats {
		t.Errorf("the payouts do not add up to %d", c.Terms.PayoutSats)
	}
	// At the low boundary the short gets more than it put in, and the long less.
	if short <= c.ShortStake {
		t.Errorf("the short was paid %d and staked %d: a crash should pay the hedge",
			short, c.ShortStake)
	}
}

// A price past the boundary and a price on it settle identically, because the
// covenant clamps. That is half of why the contract needs no clock.
func TestSettlingIsTheSameOnAndPastTheBoundary(t *testing.T) {
	var paid [2][2]int64

	for i, price := range []int64{5_000_000, 1_000_000} {
		f := newFixture(t)
		c := f.funded(t)
		f.feed.price = price

		if _, err := f.app.Settle(t.Context(), c.ID); err != nil {
			t.Fatalf("Settle: %v", err)
		}
		NewWorker(f.app, WorkerOptions{}).Tick(t.Context())
		paid[i] = f.stack.settleAt
	}

	if paid[0] != paid[1] {
		t.Errorf("on the boundary paid %v, past it paid %v", paid[0], paid[1])
	}
}

// The split is derived from the signed bytes the covenant will check, not from
// whatever number the feed happened to report alongside them.
func TestSettlingReadsThePriceOutOfTheSignedMessage(t *testing.T) {
	f := newFixture(t)
	c := f.funded(t)

	f.feed.price = 5_000_000
	if _, err := f.app.Settle(t.Context(), c.ID); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	// The feed now claims a different price than the one it will sign for.
	// What matters is the signed message, so the payouts must not move.
	signedPrice := f.feed.price
	f.feed.lies = 19_000_000

	NewWorker(f.app, WorkerOptions{}).Tick(t.Context())

	short, long, err := c.Split(signedPrice)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if f.stack.settleAt != [2]int64{short, long} {
		t.Errorf("settled at %v, want %d/%d from the signed message",
			f.stack.settleAt, short, long)
	}
}

// A settlement the emulator refuses leaves the contract exactly as it was:
// still funded, still settleable, and still worth retrying.
func TestASettlementThatFailsIsRetried(t *testing.T) {
	f := newFixture(t)
	c := f.funded(t)

	f.feed.price = 5_000_000
	if _, err := f.app.Settle(t.Context(), c.ID); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	f.stack.settleErr = errors.New("the emulator is not answering")
	worker := NewWorker(f.app, WorkerOptions{})
	worker.Tick(t.Context())

	c, _ = f.app.Contract(t.Context(), c.ID)
	if c.State != domain.Settling {
		t.Fatalf("state %s, want settling", c.State)
	}

	f.stack.settleErr = nil
	worker.Tick(t.Context())

	c, _ = f.app.Contract(t.Context(), c.ID)
	if c.State != domain.Settled {
		t.Fatalf("state %s, want settled after the retry", c.State)
	}
}

// A contract stuck mid-step forever reports nothing and never closes, so the
// worker writes it off — settling back to active, because a settlement that
// failed changed nothing.
func TestTheWorkerGivesUpOnAStepThatWillNeverFinish(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(*fixture, *domain.Contract)
		from  domain.State
		to    domain.State
	}{
		{
			name: "funding that can never succeed",
			start: func(f *fixture, c *domain.Contract) {
				f.stack.fundErr = errors.New("the operator refuses")
			},
			from: domain.Funding,
			to:   domain.Failed,
		},
		{
			name: "settling that can never succeed",
			start: func(f *fixture, c *domain.Contract) {
				f.stack.settleErr = errors.New("the emulator refuses")
			},
			from: domain.Settling,
			to:   domain.Active,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)

			var c *domain.Contract
			if tc.from == domain.Funding {
				c = f.accepted(t)
				tc.start(f, c)
				if _, err := f.app.Fund(t.Context(), c.ID, f.alice); err != nil {
					t.Fatalf("Fund: %v", err)
				}
			} else {
				c = f.funded(t)
				tc.start(f, c)
				f.feed.price = 5_000_000
				if _, err := f.app.Settle(t.Context(), c.ID); err != nil {
					t.Fatalf("Settle: %v", err)
				}
			}

			worker := NewWorker(f.app, WorkerOptions{GiveUpAfter: 10 * time.Minute})
			worker.Tick(t.Context())

			got, _ := f.app.Contract(t.Context(), c.ID)
			if got.State != tc.from {
				t.Fatalf("state %s, want %s while it is still worth retrying", got.State, tc.from)
			}

			// Long enough that retrying is no longer worth it.
			f.now = f.now.Add(11 * time.Minute)
			worker.Tick(t.Context())

			got, _ = f.app.Contract(t.Context(), c.ID)
			if got.State != tc.to {
				t.Errorf("state %s, want %s", got.State, tc.to)
			}
		})
	}
}

// Projection is what the UI shows while a contract is alive, and it comes from
// the covenant's own formula rather than from a second implementation of it.
func TestProjectionMatchesTheFormula(t *testing.T) {
	f := newFixture(t)
	c := f.funded(t)

	for _, price := range []int64{5_000_000, 8_000_000, 10_000_000, 15_000_000, 20_000_000} {
		f.feed.price = price

		got, err := f.app.Project(t.Context(), c)
		if err != nil {
			t.Fatalf("Project at %d: %v", price, err)
		}

		short, long, err := c.Split(price)
		if err != nil {
			t.Fatalf("Split: %v", err)
		}
		if got.ShortSats != short || got.LongSats != long {
			t.Errorf("at %d the projection is %d/%d, the formula says %d/%d",
				price, got.ShortSats, got.LongSats, short, long)
		}
		if got.Liquidated != c.Liquidated(price) {
			t.Errorf("at %d liquidated is %v", price, got.Liquidated)
		}
	}
}

func TestSetPriceRefusesWhatCannotBeAPrice(t *testing.T) {
	f := newFixture(t)

	for _, price := range []int64{0, -1} {
		if err := f.app.SetPrice(t.Context(), price); err == nil {
			t.Errorf("SetPrice(%d) was accepted", price)
		}
	}
	if len(f.feed.set) != 0 {
		t.Error("a refused price reached the oracle")
	}
}
