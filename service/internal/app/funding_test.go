package app_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/apptest"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
)

// Funding takes tens of seconds against a live stack, so the request only moves
// the contract and the worker does the work.
func TestFundOnlyMovesTheContract(t *testing.T) {
	f := apptest.New(t)
	f.Accepted(t)

	c, err := f.App.Fund(t.Context(), f.Contract.ID, f.Alice)
	if err != nil {
		t.Fatalf("Fund: %v", err)
	}

	if c.State != domain.Funding {
		t.Errorf("state %s, want funding", c.State)
	}
	if f.StackStub.Funded != nil {
		t.Error("Fund built the transaction inside the request")
	}
	if c.Funding != nil {
		t.Error("there is a funding outpoint before anything was submitted")
	}
}

func TestFundIsOnlyForTheParties(t *testing.T) {
	f := apptest.New(t)
	f.Accepted(t)
	carol := f.AddUser(t, "carol", 0x25)

	if _, err := f.App.Fund(t.Context(), f.Contract.ID, carol); !errors.Is(err, app.ErrNotAllowed) {
		t.Errorf("a stranger funded the contract: %v", err)
	}
}

func TestFundRefusesAContractThatIsNotAccepted(t *testing.T) {
	f := apptest.New(t)

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	if _, err := f.App.Fund(t.Context(), c.ID, f.Alice); !domain.Lost(err) {
		t.Errorf("Fund gave %v, want a refused transition", err)
	}
}

// Each party's VTXO has to cover their stake and still leave change above dust,
// because the funding transaction pays that change back and an output below
// dust is one the operator refuses. Finding out after the contract has moved
// would leave it stuck.
func TestFundRefusesBeforeMovingWhenASideCannotPay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		balance int64
	}{
		{"nothing at all", 0},
		{"exactly the stake, no room for change", 10_000_000},
		{"the stake plus dust exactly", 10_000_330},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := apptest.New(t)
			f.Accepted(t)
			f.StackStub.Balances[f.Bob] = tc.balance

			_, err := f.App.Fund(t.Context(), f.Contract.ID, f.Alice)
			var notReady app.ErrNotYet
			if !errors.As(err, &notReady) {
				t.Fatalf("Fund gave %v, want a not-yet", err)
			}

			c, _ := f.App.Contract(t.Context(), f.Contract.ID)
			if c.State != domain.Accepted {
				t.Errorf("the contract moved to %s anyway", c.State)
			}
		})
	}
}

func TestTheWorkerFinishesFunding(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	if c.State != domain.Active {
		t.Fatalf("state %s, want active", c.State)
	}
	if c.Funding == nil {
		t.Fatal("the contract has no funding outpoint")
	}
	if f.StackStub.Funded == nil {
		t.Fatal("the worker never funded anything")
	}
}

// The exit is signed at funding, before either party needs it. From that moment
// neither of them depends on the other or on the operator to get out.
func TestFundingPreSignsTheExit(t *testing.T) {
	f := apptest.New(t)
	c := f.Funded(t)

	pkg, err := f.App.ExitPackage(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if !pkg.Complete() {
		t.Fatal("the exit is not signed by both parties")
	}
	if pkg.Amount != c.Terms.PayoutSats {
		t.Errorf("the exit covers %d, want the contract's %d", pkg.Amount, c.Terms.PayoutSats)
	}
	if len(pkg.Sweep.PkScript) == 0 || len(pkg.Sweep.Leaf) == 0 || len(pkg.Sweep.ControlBlock) == 0 {
		t.Error("the sweep is not stored whole")
	}

	// Both signatures have to verify against the transaction they cover, which
	// is what makes the package usable months from now.
	covenant, err := c.Covenant()
	if err != nil {
		t.Fatalf("Covenant: %v", err)
	}
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(pkg.RawTx)); err != nil {
		t.Fatalf("reading the exit: %v", err)
	}
	if err := covenant.VerifyExitSignature(covenant.Keys.Short, pkg.ShortSig, &tx, pkg.Amount); err != nil {
		t.Errorf("the short's signature does not cover the exit: %v", err)
	}
	if err := covenant.VerifyExitSignature(covenant.Keys.Long, pkg.LongSig, &tx, pkg.Amount); err != nil {
		t.Errorf("the long's signature does not cover the exit: %v", err)
	}

	// And it has to be the exit for this contract's own funding outpoint.
	if len(tx.TxIn) != 1 {
		t.Fatalf("the exit has %d inputs, want 1", len(tx.TxIn))
	}
	if got := tx.TxIn[0].PreviousOutPoint.Hash.String(); got != c.Funding.Txid {
		t.Errorf("the exit spends %s, want the contract's %s", got, c.Funding.Txid)
	}
}

// Composing the exit from two separate SignExit calls loses the check
// PreSignExit does for free — that the key signing is the contract's. A wrong
// signature stored now would look complete and fail only when the operator is
// already gone, so it is verified before it is written.
func TestFundingRefusesASignatureFromTheWrongKey(t *testing.T) {
	f := apptest.New(t)
	f.Accepted(t)

	stranger, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x77}, 32))
	f.SignerStub.WrongKey = stranger

	if _, err := f.App.Fund(t.Context(), f.Contract.ID, f.Alice); err != nil {
		t.Fatalf("Fund: %v", err)
	}
	app.NewWorker(f.App, app.WorkerOptions{}).Tick(t.Context())

	c, _ := f.App.Contract(t.Context(), f.Contract.ID)
	if c.State == domain.Active {
		t.Fatal("the contract went live with an exit neither party could use")
	}
	if _, err := f.App.ExitPackage(t.Context(), c.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Error("a package signed by the wrong key was stored")
	}
}

// The worker runs again after a restart, so funding has to be safe to repeat.
func TestFundingIsSafeToRepeat(t *testing.T) {
	f := apptest.New(t)
	f.Accepted(t)

	if _, err := f.App.Fund(t.Context(), f.Contract.ID, f.Alice); err != nil {
		t.Fatalf("Fund: %v", err)
	}

	// The exit fails once, so the contract stays in funding with its outpoint
	// already written — exactly the state a crash mid-step leaves behind.
	f.ExitStore.Fail = errors.New("the database went away")
	worker := app.NewWorker(f.App, app.WorkerOptions{})
	worker.Tick(t.Context())

	c, _ := f.App.Contract(t.Context(), f.Contract.ID)
	if c.State != domain.Funding {
		t.Fatalf("state %s, want funding", c.State)
	}

	f.ExitStore.Fail = nil
	worker.Tick(t.Context())

	c, _ = f.App.Contract(t.Context(), f.Contract.ID)
	if c.State != domain.Active {
		t.Fatalf("state %s, want active after the retry", c.State)
	}
	if f.StackStub.FundCalls != 1 {
		t.Errorf("the funding transaction was submitted %d times, want 1", f.StackStub.FundCalls)
	}
}
