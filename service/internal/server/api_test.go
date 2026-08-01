package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/google/uuid"
)

// --- Identity ---------------------------------------------------------------

// Identity is a header and not a cookie, because a cookie is shared by every
// tab of an origin and the demo is two people in two tabs.
func TestEndpointsThatNeedAUserSayWhenThereIsNone(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/me"},
		{http.MethodGet, "/api/wallet"},
		{http.MethodPost, "/api/wallet/fund"},
		{http.MethodPost, "/api/contracts"},
		{http.MethodPost, "/api/contracts/" + uuid.New().String() + "/accept"},
		{http.MethodPost, "/api/contracts/" + uuid.New().String() + "/cancel"},
		{http.MethodPost, "/api/contracts/" + uuid.New().String() + "/fund"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := h.do(t, tc.method, tc.path, "{}", uuid.Nil)
			requireStatus(t, rec, http.StatusUnauthorized)
		})
	}
}

func TestAUserHeaderThatIsNotAUserIdIsRefused(t *testing.T) {
	h := newHarness(t)

	req := h.do(t, http.MethodGet, "/api/me", "", uuid.Nil)
	requireStatus(t, req, http.StatusUnauthorized)

	rec := h.doRaw(t, http.MethodGet, "/api/me", "", "not-a-uuid")
	requireStatus(t, rec, http.StatusUnauthorized)
}

// --- Users ------------------------------------------------------------------

func TestCreateUser(t *testing.T) {
	h := newHarness(t)

	rec := h.post(t, "/api/users", `{"name":"carol"}`, uuid.Nil)
	requireStatus(t, rec, http.StatusCreated)

	var body userResponse
	decode(t, rec, &body)

	if body.Name != "carol" {
		t.Errorf("name %q", body.Name)
	}
	if _, err := uuid.Parse(body.ID); err != nil {
		t.Errorf("id %q is not a user id", body.ID)
	}
	// A compressed key is 33 bytes.
	if len(body.PubKey) != 66 {
		t.Errorf("pubkey is %d hex chars, want 66", len(body.PubKey))
	}
}

func TestCreateUserRefusesWhatIsNotAName(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"empty", `{"name":""}`, http.StatusBadRequest},
		{"only spaces", `{"name":"   "}`, http.StatusBadRequest},
		{"missing", `{}`, http.StatusBadRequest},
		{"too long", `{"name":"` + strings.Repeat("a", 33) + `"}`, http.StatusBadRequest},
		{"not json", `nonsense`, http.StatusBadRequest},
		{"already taken", `{"name":"alice"}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.post(t, "/api/users", tc.body, uuid.Nil)
			requireStatus(t, rec, tc.want)
		})
	}
}

func TestListUsersAndMe(t *testing.T) {
	h := newHarness(t)

	rec := h.get(t, "/api/users", uuid.Nil)
	requireStatus(t, rec, http.StatusOK)

	var users []userResponse
	decode(t, rec, &users)
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}

	rec = h.get(t, "/api/me", h.Alice)
	requireStatus(t, rec, http.StatusOK)

	var me userResponse
	decode(t, rec, &me)
	if me.Name != "alice" {
		t.Errorf("me is %q", me.Name)
	}
}

func TestMeOfAUserWhoIsNotThere(t *testing.T) {
	h := newHarness(t)

	rec := h.get(t, "/api/me", uuid.New())
	requireStatus(t, rec, http.StatusNotFound)
}

// --- Wallet -----------------------------------------------------------------

func TestWallet(t *testing.T) {
	h := newHarness(t)

	rec := h.get(t, "/api/wallet", h.Alice)
	requireStatus(t, rec, http.StatusOK)

	var body walletResponse
	decode(t, rec, &body)
	if body.OffchainAddress == "" || body.BoardingAddress == "" {
		t.Error("the wallet has no addresses")
	}
	if body.SpendableSats != 50_000_000 {
		t.Errorf("balance %d", body.SpendableSats)
	}
}

// Boarding takes tens of seconds — a faucet payment to confirm, then a batch to
// close — so it answers 202 and the caller watches their balance.
func TestFundWalletIsAccepted(t *testing.T) {
	h := newHarness(t)

	rec := h.post(t, "/api/wallet/fund", `{"sats":100000}`, h.Alice)
	requireStatus(t, rec, http.StatusAccepted)

	if h.StackStub.TopUps[h.Alice] != 100_000 {
		t.Errorf("topped up %d", h.StackStub.TopUps[h.Alice])
	}
}

// Money that is there but unspendable has to read as its own thing, or a user
// sees a balance and a refusal and no way to connect the two.
func TestWalletSeparatesWhatCannotBeSpentYet(t *testing.T) {
	h := newHarness(t)
	h.StackStub.Balances[h.Alice] = 1_000
	h.StackStub.Recoverable[h.Alice] = 40_000

	rec := h.get(t, "/api/wallet", h.Alice)
	requireStatus(t, rec, http.StatusOK)

	var body walletResponse
	decode(t, rec, &body)
	if body.SpendableSats != 1_000 || body.RecoverableSats != 40_000 {
		t.Errorf("wallet reports %d spendable and %d recoverable",
			body.SpendableSats, body.RecoverableSats)
	}

	rec = h.post(t, "/api/wallet/recover", "", h.Alice)
	requireStatus(t, rec, http.StatusAccepted)

	rec = h.get(t, "/api/wallet", h.Alice)
	decode(t, rec, &body)
	if body.SpendableSats != 41_000 || body.RecoverableSats != 0 {
		t.Errorf("after recovering: %d spendable, %d recoverable",
			body.SpendableSats, body.RecoverableSats)
	}
}

// And funding says which of the two the shortfall is, because the answer to
// "top up" and the answer to "recover" are different actions.
func TestFundSaysWhenTheMoneyIsThereButNotSpendable(t *testing.T) {
	h := newHarness(t)
	c := h.accepted(t)

	h.StackStub.Balances[h.Bob] = 0
	h.StackStub.Recoverable[h.Bob] = 50_000_000

	rec := h.post(t, "/api/contracts/"+c.ID+"/fund", "", h.Alice)
	requireStatus(t, rec, http.StatusConflict)

	if !strings.Contains(rec.Body.String(), "recover") {
		t.Errorf("the reason does not mention recovering: %s", rec.Body.String())
	}
}

func TestFundWalletRefusesWhatIsNotAnAmount(t *testing.T) {
	h := newHarness(t)

	for _, body := range []string{`{"sats":0}`, `{"sats":-1}`, `{}`, `nonsense`} {
		rec := h.post(t, "/api/wallet/fund", body, h.Alice)
		requireStatus(t, rec, http.StatusBadRequest)
	}
}

// --- Contracts --------------------------------------------------------------

func TestProposeCarriesEverythingTheAddressIsAFunctionOf(t *testing.T) {
	h := newHarness(t)

	c := h.propose(t)

	if c.State != string(domain.Proposed) {
		t.Errorf("state %q", c.State)
	}
	if c.Short == nil || c.Short.Name != "alice" {
		t.Error("alice is not on the short side")
	}
	if c.Long != nil {
		t.Error("the long side is taken already")
	}
	if c.Terms.HedgeValueCents != 1_000_000 {
		t.Errorf("hedge value %d", c.Terms.HedgeValueCents)
	}
	if c.Terms.PayoutSats != 20_000_000 {
		t.Errorf("payout %d", c.Terms.PayoutSats)
	}
	if c.Terms.OraclePubKey == "" || c.Terms.ArkdSigner == "" || c.Terms.EmulatorSigner == "" {
		t.Error("the keys the tree is built from are not in the response")
	}
	// A client recognises the contract it funds rather than taking our word for
	// it, and there is nothing to recognise until both sides are known.
	if c.Address != "" {
		t.Error("there is an address before both sides are known")
	}
}

func TestProposeRefusesTermsThatCannotWork(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"no side", `{"side":"","payout_sats":20000000,"hedge_value_cents":1,"low_liquidation_cents":1,"high_liquidation_cents":2,"maturity_in_seconds":60}`, http.StatusBadRequest},
		{"a payout below two dust", `{"side":"short","payout_sats":100,"hedge_value_cents":1,"low_liquidation_cents":1,"high_liquidation_cents":2,"maturity_in_seconds":60}`, http.StatusBadRequest},
		{"boundaries the wrong way round", `{"side":"short","payout_sats":20000000,"hedge_value_cents":1000000,"low_liquidation_cents":20000000,"high_liquidation_cents":5000000,"maturity_in_seconds":60}`, http.StatusBadRequest},
		{"no maturity", `{"side":"short","payout_sats":20000000,"hedge_value_cents":1000000,"low_liquidation_cents":5000000,"high_liquidation_cents":20000000,"maturity_in_seconds":0}`, http.StatusBadRequest},
		{"not json", `nonsense`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.post(t, "/api/contracts", tc.body, h.Alice)
			requireStatus(t, rec, tc.want)
		})
	}
}

// A position already outside its boundaries would liquidate the instant it was
// funded. That is a "come back later", not a malformed request.
func TestProposeIs409WhenThePriceIsAlreadyPastABoundary(t *testing.T) {
	h := newHarness(t)
	h.FeedStub.CurrentPrice = 4_000_000

	rec := h.post(t, "/api/contracts", standardProposal, h.Alice)
	requireStatus(t, rec, http.StatusConflict)
}

func TestAcceptFillsInTheAddress(t *testing.T) {
	h := newHarness(t)

	c := h.accepted(t)

	if c.State != string(domain.Accepted) {
		t.Errorf("state %q", c.State)
	}
	if c.Long == nil || c.Long.Name != "bob" {
		t.Error("bob is not on the long side")
	}
	if c.Address == "" || c.PkScript == "" {
		t.Fatal("the contract has no address")
	}
	if !strings.HasPrefix(c.Address, "bcrt1p") {
		t.Errorf("address %q is not a regtest taproot address", c.Address)
	}
	if c.ShortStake+c.LongStake != c.Terms.PayoutSats {
		t.Errorf("the stakes %d/%d do not add up to %d",
			c.ShortStake, c.LongStake, c.Terms.PayoutSats)
	}
}

func TestAcceptRefusals(t *testing.T) {
	h := newHarness(t)
	carol := h.AddUser(t, "carol", 0x25)

	c := h.propose(t)

	t.Run("the creator cannot take both sides", func(t *testing.T) {
		rec := h.post(t, "/api/contracts/"+c.ID+"/accept", "", h.Alice)
		requireStatus(t, rec, http.StatusForbidden)
	})

	t.Run("a contract that is not there", func(t *testing.T) {
		rec := h.post(t, "/api/contracts/"+uuid.New().String()+"/accept", "", h.Bob)
		requireStatus(t, rec, http.StatusNotFound)
	})

	t.Run("an id that is not one", func(t *testing.T) {
		rec := h.post(t, "/api/contracts/soon/accept", "", h.Bob)
		requireStatus(t, rec, http.StatusBadRequest)
	})

	t.Run("one that has already been taken", func(t *testing.T) {
		rec := h.post(t, "/api/contracts/"+c.ID+"/accept", "", h.Bob)
		requireStatus(t, rec, http.StatusOK)

		rec = h.post(t, "/api/contracts/"+c.ID+"/accept", "", carol)
		requireStatus(t, rec, http.StatusConflict)
	})
}

func TestCancelIsOnlyForTheParties(t *testing.T) {
	h := newHarness(t)
	carol := h.AddUser(t, "carol", 0x25)

	c := h.propose(t)

	rec := h.post(t, "/api/contracts/"+c.ID+"/cancel", "", carol)
	requireStatus(t, rec, http.StatusForbidden)

	rec = h.post(t, "/api/contracts/"+c.ID+"/cancel", "", h.Alice)
	requireStatus(t, rec, http.StatusOK)

	var cancelled contractResponse
	decode(t, rec, &cancelled)
	if cancelled.State != string(domain.Cancelled) {
		t.Errorf("state %q", cancelled.State)
	}
}

func TestListContracts(t *testing.T) {
	h := newHarness(t)

	open := h.propose(t)
	taken := h.accepted(t)

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"everything", "", 2},
		{"by state", "?state=accepted", 1},
		{"still on offer", "?open=true", 1},
		{"by user", "?user=" + h.Bob.String(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.get(t, "/api/contracts"+tc.query, uuid.Nil)
			requireStatus(t, rec, http.StatusOK)

			var contracts []contractResponse
			decode(t, rec, &contracts)
			if len(contracts) != tc.want {
				t.Fatalf("got %d contracts, want %d", len(contracts), tc.want)
			}
		})
	}

	_ = open
	_ = taken
}

func TestListContractsRefusesAFilterThatMakesNoSense(t *testing.T) {
	h := newHarness(t)

	for _, query := range []string{"?state=pending-vibes", "?user=nobody"} {
		rec := h.get(t, "/api/contracts"+query, uuid.Nil)
		requireStatus(t, rec, http.StatusBadRequest)
	}
}

// The contract page reads one endpoint: the contract, its history, what it
// would pay right now, and whether the exit is in place.
func TestShowContractCarriesTheWholePage(t *testing.T) {
	h := newHarness(t)

	c := h.funded(t)

	if len(c.Events) != 4 {
		t.Fatalf("got %d events, want 4", len(c.Events))
	}
	if c.Events[0].To != string(domain.Proposed) {
		t.Errorf("the history does not start at proposed: %+v", c.Events[0])
	}
	if c.Funding == nil || c.Funding.Txid == "" {
		t.Error("there is no funding outpoint")
	}
	if !c.ExitReady {
		t.Error("the exit is not reported as pre-signed")
	}
	if c.Projection == nil {
		t.Fatal("there is no projection")
	}
	if c.Projection.ShortSats+c.Projection.LongSats != c.Terms.PayoutSats {
		t.Errorf("the projection %d/%d does not add up to %d",
			c.Projection.ShortSats, c.Projection.LongSats, c.Terms.PayoutSats)
	}
	if c.Projection.Liquidated {
		t.Error("a contract at its opening price is reported liquidated")
	}
}

func TestShowContractThatIsNotThere(t *testing.T) {
	h := newHarness(t)

	requireStatus(t, h.get(t, "/api/contracts/"+uuid.New().String(), uuid.Nil), http.StatusNotFound)
	requireStatus(t, h.get(t, "/api/contracts/soon", uuid.Nil), http.StatusBadRequest)
}

func TestFundContract(t *testing.T) {
	h := newHarness(t)
	carol := h.AddUser(t, "carol", 0x25)

	c := h.accepted(t)

	t.Run("a stranger cannot fund it", func(t *testing.T) {
		rec := h.post(t, "/api/contracts/"+c.ID+"/fund", "", carol)
		requireStatus(t, rec, http.StatusForbidden)
	})

	t.Run("a side that cannot pay is a 409 and nothing moves", func(t *testing.T) {
		h.StackStub.Balances[h.Bob] = 0
		rec := h.post(t, "/api/contracts/"+c.ID+"/fund", "", h.Alice)
		requireStatus(t, rec, http.StatusConflict)

		h.StackStub.Balances[h.Bob] = 50_000_000
	})

	t.Run("and then it funds", func(t *testing.T) {
		rec := h.post(t, "/api/contracts/"+c.ID+"/fund", "", h.Alice)
		requireStatus(t, rec, http.StatusOK)

		var funding contractResponse
		decode(t, rec, &funding)
		if funding.State != string(domain.Funding) {
			t.Errorf("state %q, want funding", funding.State)
		}
	})
}

// --- Settlement -------------------------------------------------------------

// Settling needs no identity: the settlement leaf carries no party key, so a
// contract that has liquidated must not need the losing side to cooperate.
func TestSettleNeedsNoIdentity(t *testing.T) {
	h := newHarness(t)
	c := h.funded(t)

	h.FeedStub.CurrentPrice = 5_000_000

	rec := h.post(t, "/api/contracts/"+c.ID+"/settle", "", uuid.Nil)
	requireStatus(t, rec, http.StatusOK)

	var settling contractResponse
	decode(t, rec, &settling)
	if settling.State != string(domain.Settling) {
		t.Errorf("state %q, want settling", settling.State)
	}
}

func TestSettleIs409WithNothingToSettle(t *testing.T) {
	h := newHarness(t)
	c := h.funded(t)

	rec := h.post(t, "/api/contracts/"+c.ID+"/settle", "", uuid.Nil)
	requireStatus(t, rec, http.StatusConflict)

	if !strings.Contains(rec.Body.String(), "nothing to settle") {
		t.Errorf("the reason is missing from %q", rec.Body.String())
	}
}

// --- Oracle -----------------------------------------------------------------

func TestOracle(t *testing.T) {
	h := newHarness(t)

	rec := h.get(t, "/api/oracle", uuid.Nil)
	requireStatus(t, rec, http.StatusOK)

	var price priceResponse
	decode(t, rec, &price)
	if price.Price != 10_000_000 {
		t.Errorf("price %d", price.Price)
	}
}

func TestOracleHistory(t *testing.T) {
	h := newHarness(t)

	rec := h.get(t, "/api/oracle/history?limit=5", uuid.Nil)
	requireStatus(t, rec, http.StatusOK)

	var history []priceResponse
	decode(t, rec, &history)
	if len(history) == 0 {
		t.Error("the history is empty")
	}

	for _, limit := range []string{"0", "-1", "soon"} {
		rec := h.get(t, "/api/oracle/history?limit="+limit, uuid.Nil)
		requireStatus(t, rec, http.StatusBadRequest)
	}
}

func TestSetPrice(t *testing.T) {
	h := newHarness(t)

	rec := h.post(t, "/api/oracle/price", `{"price":5000000}`, uuid.Nil)
	requireStatus(t, rec, http.StatusNoContent)

	if len(h.FeedStub.Set) != 1 || h.FeedStub.Set[0] != 5_000_000 {
		t.Errorf("the oracle was told %v", h.FeedStub.Set)
	}

	for _, body := range []string{`{"price":0}`, `{"price":-1}`, `nonsense`} {
		rec := h.post(t, "/api/oracle/price", body, uuid.Nil)
		requireStatus(t, rec, http.StatusBadRequest)
	}
}

// The banner that says this is a real stack: none of these numbers is one we
// chose.
func TestDemoStack(t *testing.T) {
	h := newHarness(t)

	rec := h.get(t, "/api/demo/stack", uuid.Nil)
	requireStatus(t, rec, http.StatusOK)

	var stack stackResponse
	decode(t, rec, &stack)

	if len(stack.ArkdSigner) != 66 || len(stack.EmulatorSigner) != 66 {
		t.Errorf("the signer keys are %q / %q", stack.ArkdSigner, stack.EmulatorSigner)
	}
	if stack.ExitDelay == 0 {
		t.Error("the exit delay is zero")
	}
}
