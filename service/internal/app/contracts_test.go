package app_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/apptest"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

func TestProposeLeavesTheContractLookingForACounterparty(t *testing.T) {
	f := apptest.New(t)

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	if c.State != domain.Proposed {
		t.Errorf("state %s, want proposed", c.State)
	}
	if !c.Open() {
		t.Error("the contract is not open to a counterparty")
	}
	if c.ShortUser == nil || *c.ShortUser != f.Alice {
		t.Error("alice is not on the short side")
	}
	if c.LongUser != nil {
		t.Error("the long side is taken already")
	}
	if !bytes.Equal(c.Terms.ShortLockScript, f.StackStub.Scripts[f.Alice]) {
		t.Error("the payout does not go to alice's own vtxo script")
	}
	if len(c.Terms.LongLockScript) != 0 {
		t.Error("the long's payout script is set before there is a long")
	}
	if len(c.PkScript) != 0 {
		t.Error("there is an address before both sides are known")
	}
}

// The hedge value is in cents and Terms wants it scaled by 1e8. Confusing the
// two builds a contract for the wrong amount and nothing downstream notices.
func TestProposeScalesTheHedgeValue(t *testing.T) {
	f := apptest.New(t)

	p := f.Standard(f.Alice, domain.Short)
	p.HedgeValueCents = 1_000_000 // $10,000

	c, err := f.App.Propose(t.Context(), p)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	if c.Terms.NominalUnitsXSatsPerBtc != 100_000_000_000_000 {
		t.Errorf("nominal units %d, want 100000000000000", c.Terms.NominalUnitsXSatsPerBtc)
	}
	if got := domain.HedgeValueCents(c.Terms.NominalUnitsXSatsPerBtc); got != 1_000_000 {
		t.Errorf("the value does not come back: %d", got)
	}
}

func TestProposeRefusesTermsThatCannotWork(t *testing.T) {
	f := apptest.New(t)

	for _, tc := range []struct {
		name  string
		spoil func(*app.Proposal)
	}{
		{"no side", func(p *app.Proposal) { p.Side = "" }},
		{"a side that is not one", func(p *app.Proposal) { p.Side = "sideways" }},
		{"no hedge value", func(p *app.Proposal) { p.HedgeValueCents = 0 }},
		{"a negative hedge value", func(p *app.Proposal) { p.HedgeValueCents = -1 }},
		{"a payout below two dust", func(p *app.Proposal) { p.PayoutSats = 2*contract.Dust - 1 }},
		{"no low boundary", func(p *app.Proposal) { p.LowLiquidationCents = 0 }},
		{"boundaries the wrong way round", func(p *app.Proposal) {
			p.LowLiquidationCents, p.HighLiquidationCents = 20_000_000, 5_000_000
		}},
		{"boundaries that are the same", func(p *app.Proposal) {
			p.HighLiquidationCents = p.LowLiquidationCents
		}},
		{"maturity in the past", func(p *app.Proposal) { p.MaturityIn = -time.Hour }},
		{"no maturity at all", func(p *app.Proposal) { p.MaturityIn = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := f.Standard(f.Alice, domain.Short)
			tc.spoil(&p)

			if _, err := f.App.Propose(t.Context(), p); err == nil {
				t.Error("Propose accepted it")
			}
		})
	}
}

// A position that is already outside its boundaries liquidates the instant it
// is funded, which is a way to lose money to a typo rather than to the market.
func TestProposeRefusesAContractThatWouldLiquidateOnFunding(t *testing.T) {
	f := apptest.New(t)

	for _, tc := range []struct {
		name  string
		price int64
	}{
		{"below the low boundary", 4_000_000},
		{"exactly on the low boundary", 5_000_000},
		{"above the high boundary", 21_000_000},
		{"exactly on the high boundary", 20_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.FeedStub.CurrentPrice = tc.price

			_, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
			var notReady app.ErrNotYet
			if !errors.As(err, &notReady) {
				t.Fatalf("Propose gave %v, want a not-yet", err)
			}
		})
	}
}

// Accepting is where the contract becomes real: both payout scripts are known,
// so the address is, and so is what each side has to put in.
func TestAcceptFillsInTheOtherSideAndTheAddress(t *testing.T) {
	f := apptest.New(t)

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	c, err = f.App.Accept(t.Context(), c.ID, f.Bob)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	if c.State != domain.Accepted {
		t.Errorf("state %s, want accepted", c.State)
	}
	if c.LongUser == nil || *c.LongUser != f.Bob {
		t.Error("bob is not on the long side")
	}
	if !bytes.Equal(c.Terms.LongLockScript, f.StackStub.Scripts[f.Bob]) {
		t.Error("the long's payout does not go to bob's own vtxo script")
	}
	if len(c.PkScript) == 0 {
		t.Fatal("the contract has no address")
	}

	// The stored script has to be the one the leaves derive, or the parties
	// would fund an address the covenant does not live at.
	covenant, err := c.Covenant()
	if err != nil {
		t.Fatalf("Covenant: %v", err)
	}
	derived, err := covenant.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	if !bytes.Equal(c.PkScript, derived) {
		t.Error("the stored address is not the one the leaves derive")
	}
}

// Each side puts in exactly what the covenant would pay it back at today's
// price, so a contract that settled the moment it was funded would move
// nothing. The numbers come from the formula and from nowhere else.
func TestAcceptStakesWhatTheFormulaSays(t *testing.T) {
	f := apptest.New(t)
	f.FeedStub.CurrentPrice = 10_000_000

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	c, err = f.App.Accept(t.Context(), c.ID, f.Bob)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// $10,000 against 0.2 BTC at $100,000: each side holds half.
	if c.ShortStake != 10_000_000 || c.LongStake != 10_000_000 {
		t.Errorf("stakes %d/%d, want 10000000/10000000", c.ShortStake, c.LongStake)
	}
	if c.ShortStake+c.LongStake != c.Terms.PayoutSats {
		t.Errorf("the stakes do not add up to the payout")
	}

	short, long, err := c.Split(10_000_000)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if short != c.ShortStake || long != c.LongStake {
		t.Errorf("the stakes are not what the formula gives: %d/%d", short, long)
	}
}

func TestAcceptRefusesTheCreator(t *testing.T) {
	f := apptest.New(t)

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	_, err = f.App.Accept(t.Context(), c.ID, f.Alice)
	if !errors.Is(err, app.ErrNotAllowed) {
		t.Errorf("Accept gave %v, want not allowed", err)
	}
}

func TestAcceptRefusesAContractThatIsNoLongerOnOffer(t *testing.T) {
	f := apptest.New(t)
	carol := f.AddUser(t, "carol", 0x25)

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := f.App.Accept(t.Context(), c.ID, f.Bob); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	_, err = f.App.Accept(t.Context(), c.ID, carol)
	if !domain.Lost(err) {
		t.Errorf("the second accept gave %v, want a conflict", err)
	}
}

func TestAcceptOfAContractThatIsNotThere(t *testing.T) {
	f := apptest.New(t)

	if _, err := f.App.Accept(t.Context(), uuid.New(), f.Bob); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Accept gave %v", err)
	}
}

// The operator's own rules are run before anyone funds, so a contract it would
// refuse is refused here rather than discovered when the transaction bounces.
func TestAcceptRunsTheOperatorsAcceptanceRules(t *testing.T) {
	f := apptest.New(t)

	// An exit delay below the operator's own minimum is the check arkd makes.
	f.StackStub.StackInfo.ExitDelay.Value = 100

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	// The contract was built with the old delay; the operator now wants more.
	f.StackStub.StackInfo.ExitDelay.Value = 200

	if _, err := f.App.Accept(t.Context(), c.ID, f.Bob); err == nil {
		t.Error("Accept took a contract the operator would refuse")
	}
}

func TestCancelIsOnlyForTheParties(t *testing.T) {
	f := apptest.New(t)
	carol := f.AddUser(t, "carol", 0x25)

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	if _, err := f.App.Cancel(t.Context(), c.ID, carol); !errors.Is(err, app.ErrNotAllowed) {
		t.Errorf("a stranger cancelled the contract: %v", err)
	}

	c, err = f.App.Cancel(t.Context(), c.ID, f.Alice)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if c.State != domain.Cancelled {
		t.Errorf("state %s, want cancelled", c.State)
	}
}

// Once money has moved there is nothing to cancel: the way out is settling,
// closing early, or exiting.
func TestAFundedContractCannotBeCancelled(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	if _, err := f.App.Cancel(t.Context(), c.ID, f.Alice); !domain.Lost(err) {
		t.Errorf("Cancel gave %v, want a refused transition", err)
	}
}

func TestEveryTransitionIsRecorded(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	events, err := f.App.History(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	want := []struct{ from, to domain.State }{
		{"", domain.Proposed},
		{domain.Proposed, domain.Accepted},
		{domain.Accepted, domain.Funding},
		{domain.Funding, domain.Active},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for i, w := range want {
		if events[i].From != w.from || events[i].To != w.to {
			t.Errorf("event %d is %s -> %s, want %s -> %s",
				i, events[i].From, events[i].To, w.from, w.to)
		}
	}
}
