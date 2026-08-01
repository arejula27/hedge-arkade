package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/apptest"
	"github.com/arejula27/hedge/service/internal/domain"
)

// The split on leaf 2 is whatever the two of them say it is — that is the point
// of it, and the reason it needs both signatures.
func TestProposeAnAgreedSplit(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	lopsided := app.Split{ShortSats: 19_999_670, LongSats: 330}

	proposal, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, lopsided)
	if err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	if proposal.ShortSats != lopsided.ShortSats || proposal.LongSats != lopsided.LongSats {
		t.Errorf("proposed %d/%d", proposal.ShortSats, proposal.LongSats)
	}
	if proposal.FromOracle() {
		t.Error("an agreed split carries oracle evidence")
	}

	// The proposer signs on the way out. Nobody proposes a close they are not
	// willing to sign.
	if !proposal.HasSigned(domain.Short) {
		t.Error("alice proposed without signing")
	}
	if proposal.Signed() {
		t.Error("one signature was enough")
	}

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.RedemptionProposed {
		t.Errorf("state %s, want redemption_proposed", got.State)
	}
}

// A close at the oracle's price carries the signed message with it, so the
// other party can check the numbers against the same bytes rather than against
// a promise.
func TestProposeAtTheOraclePriceCarriesItsEvidence(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	f.FeedStub.CurrentPrice = 8_000_000

	proposal, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, app.Split{})
	if err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	if !proposal.FromOracle() {
		t.Fatal("the proposal has no evidence")
	}
	if proposal.Price != 8_000_000 {
		t.Errorf("price %d", proposal.Price)
	}

	short, long, err := c.Split(8_000_000)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if proposal.ShortSats != short || proposal.LongSats != long {
		t.Errorf("proposed %d/%d, the formula says %d/%d",
			proposal.ShortSats, proposal.LongSats, short, long)
	}
}

func TestProposeRefusesASplitThatCannotWork(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	for _, tc := range []struct {
		name  string
		split app.Split
	}{
		{"one sat over", app.Split{ShortSats: 10_000_001, LongSats: 10_000_000}},
		{"one sat under", app.Split{ShortSats: 9_999_999, LongSats: 10_000_000}},
		{"the short below dust", app.Split{ShortSats: 100, LongSats: 19_999_900}},
		{"the long below dust", app.Split{ShortSats: 19_999_900, LongSats: 100}},
		{"a negative side", app.Split{ShortSats: -1, LongSats: 20_000_001}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, tc.split)
			if !errors.Is(err, app.ErrInvalid) {
				t.Errorf("ProposeRedemption gave %v, want invalid", err)
			}
		})
	}
}

func TestProposeIsOnlyForThePartiesAndOnlyWhenActive(t *testing.T) {
	f := apptest.New(t)
	carol := f.AddUser(t, "carol", 0x25)

	c := f.Funded(t)
	split := app.Split{ShortSats: 10_000_000, LongSats: 10_000_000}

	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, carol, split); !errors.Is(err, app.ErrNotAllowed) {
		t.Errorf("a stranger proposed a close: %v", err)
	}

	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, split); err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}
	// A second proposal on a contract that is already closing.
	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Bob, split); !domain.Lost(err) {
		t.Errorf("a second proposal gave %v, want a conflict", err)
	}
}

// Both signatures, and then the worker submits.
func TestTheSecondSignatureClosesIt(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, app.Split{}); err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	proposal, err := f.App.SignRedemption(t.Context(), c.ID, f.Bob)
	if err != nil {
		t.Fatalf("SignRedemption: %v", err)
	}
	if !proposal.Signed() {
		t.Fatal("both parties signed and it is not signed")
	}

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Redeeming {
		t.Fatalf("state %s, want redeeming", got.State)
	}

	app.NewWorker(f.App, app.WorkerOptions{}).Tick(t.Context())

	got, _ = f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Redeemed {
		t.Fatalf("state %s, want redeemed", got.State)
	}
	if f.StackStub.Redeemed == nil {
		t.Fatal("nothing was submitted")
	}

	// The operator re-verifies every key in the revealed leaf on the
	// checkpoints it hands back, and no wallet holds those keys — so both
	// parties have to sign that round too.
	for _, checkpoint := range f.StackStub.RedeemedFinal {
		for _, party := range []string{f.Alice.String(), f.Bob.String()} {
			if !strings.Contains(checkpoint, party) {
				t.Errorf("the returned checkpoint was not signed by %s", party)
			}
		}
	}
}

// Signing twice is a refresh, not a second signature.
func TestSigningTwiceIsNotTwoSignatures(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, app.Split{}); err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	proposal, err := f.App.SignRedemption(t.Context(), c.ID, f.Alice)
	if err != nil {
		t.Fatalf("SignRedemption: %v", err)
	}
	if proposal.Signed() {
		t.Error("the proposer signing again counted as the second signature")
	}

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.RedemptionProposed {
		t.Errorf("state %s, want redemption_proposed", got.State)
	}
}

// The check a party must run before signing. Without it, "the service says this
// is the right number" is the whole of the guarantee.
func TestSigningVerifiesTheProposalFirst(t *testing.T) {
	f := apptest.New(t)

	for _, tc := range []struct {
		name   string
		tamper func(*domain.Redemption)
	}{
		{"the numbers were moved", func(r *domain.Redemption) {
			r.ShortSats += 1_000_000
			r.LongSats -= 1_000_000
		}},
		{"the price was moved", func(r *domain.Redemption) {
			r.Price = 19_000_000
		}},
		{"the evidence was replaced", func(r *domain.Redemption) {
			r.Signature = []byte("not a signature")
		}},
		{"the split no longer adds up", func(r *domain.Redemption) {
			r.ShortSats += 1
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f = apptest.New(t)
			c := f.Funded(t)

			if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, app.Split{}); err != nil {
				t.Fatalf("ProposeRedemption: %v", err)
			}

			// Somebody edits the row between the proposal and the signature.
			stored := f.RedemptionStore.Stored[c.ID]
			tc.tamper(&stored)
			f.RedemptionStore.Stored[c.ID] = stored

			if _, err := f.App.SignRedemption(t.Context(), c.ID, f.Bob); err == nil {
				t.Error("bob signed a proposal that does not check out")
			}

			got, _ := f.App.Contract(t.Context(), c.ID)
			if got.State == domain.Redeeming {
				t.Error("a tampered proposal was submitted")
			}
		})
	}
}

func TestRejectingPutsTheContractBack(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, app.Split{}); err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	got, err := f.App.RejectRedemption(t.Context(), c.ID, f.Bob)
	if err != nil {
		t.Fatalf("RejectRedemption: %v", err)
	}
	if got.State != domain.Active {
		t.Errorf("state %s, want active", got.State)
	}
	if _, err := f.App.Redemption(t.Context(), c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Error("the rejected proposal is still there")
	}

	// And the contract can have another.
	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Bob, app.Split{}); err != nil {
		t.Errorf("proposing again after a rejection: %v", err)
	}
}

// A close the operator refuses leaves the contract exactly as it was: still
// funded, still closeable, and still worth retrying.
func TestACloseThatFailsIsRetried(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	if _, err := f.App.ProposeRedemption(t.Context(), c.ID, f.Alice, app.Split{}); err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}
	if _, err := f.App.SignRedemption(t.Context(), c.ID, f.Bob); err != nil {
		t.Fatalf("SignRedemption: %v", err)
	}

	f.StackStub.SubmitRedeemErr = errors.New("the operator is not answering")
	worker := app.NewWorker(f.App, app.WorkerOptions{})
	worker.Tick(t.Context())

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Redeeming {
		t.Fatalf("state %s, want redeeming", got.State)
	}

	f.StackStub.SubmitRedeemErr = nil
	worker.Tick(t.Context())

	got, _ = f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Redeemed {
		t.Fatalf("state %s, want redeemed after the retry", got.State)
	}
}
