package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/apptest"
	"github.com/arejula27/hedge/service/internal/domain"
)

// exited runs a contract all the way out of Arkade.
func exited(t *testing.T, f *apptest.Fixture) *domain.Contract {
	t.Helper()

	c := f.Funded(t)
	if _, err := f.App.Exit(t.Context(), c.ID, f.Alice); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	app.NewWorker(f.App, app.WorkerOptions{}).Tick(t.Context())

	c, _ = f.App.Contract(t.Context(), c.ID)
	if c.State != domain.Exited {
		t.Fatalf("state %s, want exited", c.State)
	}
	return c
}

// Everything up to here assumed the operator answers. This is the path that
// does not.
func TestExitingTakesTheContractOutOfArkade(t *testing.T) {
	f := apptest.New(t)
	c := exited(t, f)

	if !f.StackStub.Exited {
		t.Fatal("nothing was unrolled or broadcast")
	}

	pkg, err := f.App.ExitPackage(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("ExitPackage: %v", err)
	}
	if !pkg.OnChain() {
		t.Fatal("the exit does not say where it landed")
	}
	if pkg.SweptSats != c.Terms.PayoutSats-2_000 {
		t.Errorf("the sweep holds %d, want the payout less the fee", pkg.SweptSats)
	}
}

func TestExitIsOnlyForThePartiesAndOnlyWhenActive(t *testing.T) {
	f := apptest.New(t)
	carol := f.AddUser(t, "carol", 0x25)

	c := f.Funded(t)

	if _, err := f.App.Exit(t.Context(), c.ID, carol); !errors.Is(err, app.ErrNotAllowed) {
		t.Errorf("a stranger exited the contract: %v", err)
	}

	if _, err := f.App.Exit(t.Context(), c.ID, f.Alice); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if _, err := f.App.Exit(t.Context(), c.ID, f.Bob); !domain.Lost(err) {
		t.Errorf("a second exit gave %v, want a conflict", err)
	}
}

// Where the exit landed is written the moment it is on the chain, because
// everything after that point has to be able to start again from the row.
func TestExitingIsSafeToRepeat(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	if _, err := f.App.Exit(t.Context(), c.ID, f.Alice); err != nil {
		t.Fatalf("Exit: %v", err)
	}

	// It lands on the chain and then the transition fails, which is what a
	// crash mid-step leaves behind.
	f.ContractStore.Stored[c.ID] = withState(f.ContractStore.Stored[c.ID], domain.Exiting)
	worker := app.NewWorker(f.App, app.WorkerOptions{})
	worker.Tick(t.Context())

	// Running it again must not unroll and broadcast a second time.
	f.StackStub.ExitErr = errors.New("this must not be reached")
	worker.Tick(t.Context())

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Exited {
		t.Errorf("state %s, want exited", got.State)
	}
}

func withState(c domain.Contract, state domain.State) domain.Contract {
	c.State = state
	return c
}

// The service decides the number and cannot move the money. Both halves of that
// matter, and this is the first.
func TestArbitratingUsesTheOraclesOwnPrice(t *testing.T) {
	f := apptest.New(t)
	c := exited(t, f)

	f.FeedStub.CurrentPrice = 5_000_000

	proposal, err := f.App.Arbitrate(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	if proposal.Price != 5_000_000 {
		t.Errorf("price %d", proposal.Price)
	}
	if len(proposal.Message) == 0 || len(proposal.Signature) == 0 {
		t.Error("the proposal carries no evidence")
	}

	// The service signs its own half at once: one signature short is one
	// nobody can use.
	if len(proposal.Signatures) != 1 {
		t.Errorf("%d signatures, want the service's", len(proposal.Signatures))
	}
	if proposal.Signed() {
		t.Error("the service alone was enough to move the money")
	}

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Arbitrating {
		t.Errorf("state %s, want arbitrating", got.State)
	}
}

// And the second half: the service cannot empty the 2-of-3 by itself. This is
// the property that makes it safe to let it decide the split at all.
func TestTheServiceCannotPayOutAlone(t *testing.T) {
	f := apptest.New(t)
	c := exited(t, f)

	if _, err := f.App.Arbitrate(t.Context(), c.ID); err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	worker := app.NewWorker(f.App, app.WorkerOptions{})
	worker.Tick(t.Context())

	if f.StackStub.PaidOut != nil {
		t.Fatal("the service paid out on its own signature")
	}

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Arbitrating {
		t.Errorf("state %s, want arbitrating", got.State)
	}
}

func TestOnePartyPlusTheServiceIsTwoOfThree(t *testing.T) {
	f := apptest.New(t)
	c := exited(t, f)

	f.FeedStub.CurrentPrice = 5_000_000
	if _, err := f.App.Arbitrate(t.Context(), c.ID); err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	proposal, err := f.App.SignArbitration(t.Context(), c.ID, f.Alice)
	if err != nil {
		t.Fatalf("SignArbitration: %v", err)
	}
	if !proposal.Signed() {
		t.Fatalf("%d signatures is not enough", len(proposal.Signatures))
	}

	app.NewWorker(f.App, app.WorkerOptions{}).Tick(t.Context())

	got, _ := f.App.Contract(t.Context(), c.ID)
	if got.State != domain.Arbitrated {
		t.Fatalf("state %s, want arbitrated", got.State)
	}
	if f.StackStub.PaidOut == nil {
		t.Fatal("nothing was paid out")
	}

	// And the split is the covenant's own formula at the clamped price, not a
	// number the service was free to choose.
	short, long, err := c.Split(5_000_000)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	// Both sides are scaled down by the miner's fee, so the ratio is what has
	// to survive, not the amounts.
	if f.StackStub.PaidOut.ShortSats <= f.StackStub.PaidOut.LongSats && short > long {
		t.Errorf("paid %d/%d, and the formula gives %d/%d",
			f.StackStub.PaidOut.ShortSats, f.StackStub.PaidOut.LongSats, short, long)
	}
}

func TestSigningAnArbitrationIsOnlyForTheParties(t *testing.T) {
	f := apptest.New(t)
	carol := f.AddUser(t, "carol", 0x25)
	c := exited(t, f)

	if _, err := f.App.Arbitrate(t.Context(), c.ID); err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	if _, err := f.App.SignArbitration(t.Context(), c.ID, carol); !errors.Is(err, app.ErrNotAllowed) {
		t.Errorf("a stranger signed: %v", err)
	}
}

// A party who signs without recomputing the number from the oracle's own bytes
// is trusting that the service decided honestly.
func TestSigningAnArbitrationVerifiesItFirst(t *testing.T) {
	f := apptest.New(t)
	c := exited(t, f)

	if _, err := f.App.Arbitrate(t.Context(), c.ID); err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	stored := f.ArbitrationStore.Stored[c.ID]
	stored.ShortSats += 1_000_000
	stored.LongSats -= 1_000_000
	f.ArbitrationStore.Stored[c.ID] = stored

	if _, err := f.App.SignArbitration(t.Context(), c.ID, f.Alice); err == nil {
		t.Error("alice signed a proposal that does not check out")
	}
}

// A step that will never finish gets written off, and each one goes back where
// it came from: an exit that could not be broadcast changed nothing, and an
// arbitration nobody signed can be proposed again.
func TestTheWorkerGivesUpOnAnExitAndOnAnArbitration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setUp func(*apptest.Fixture) *domain.Contract
		from  domain.State
		to    domain.State
	}{
		{
			name: "an exit that can never be broadcast",
			setUp: func(f *apptest.Fixture) *domain.Contract {
				c := f.Funded(t)
				f.StackStub.ExitErr = errors.New("the chain is not answering")
				if _, err := f.App.Exit(t.Context(), c.ID, f.Alice); err != nil {
					t.Fatalf("Exit: %v", err)
				}
				return c
			},
			from: domain.Exiting,
			to:   domain.Active,
		},
		{
			name: "an arbitration nobody signs",
			setUp: func(f *apptest.Fixture) *domain.Contract {
				c := exited(t, f)
				if _, err := f.App.Arbitrate(t.Context(), c.ID); err != nil {
					t.Fatalf("Arbitrate: %v", err)
				}
				return c
			},
			from: domain.Arbitrating,
			to:   domain.Exited,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := apptest.New(t)
			c := tc.setUp(f)

			worker := app.NewWorker(f.App, app.WorkerOptions{GiveUpAfter: 10 * time.Minute})
			worker.Tick(t.Context())

			got, _ := f.App.Contract(t.Context(), c.ID)
			if got.State != tc.from {
				t.Fatalf("state %s, want %s while it is still worth retrying", got.State, tc.from)
			}

			f.Now = f.Now.Add(11 * time.Minute)
			worker.Tick(t.Context())

			got, _ = f.App.Contract(t.Context(), c.ID)
			if got.State != tc.to {
				t.Errorf("state %s, want %s", got.State, tc.to)
			}
		})
	}
}
