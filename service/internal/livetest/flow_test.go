//go:build livestack

// Package livetest drives the whole demo against a live Arkade stack.
//
// It is the test that says the demo works, rather than that it worked once on
// somebody's laptop: real arkd, real emulator, real bitcoind, a real oracle, a
// real postgres, and requests going in through the same router a browser uses.
//
// It needs `just regtest-reset` to have run, and Docker for the database.
package livetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/boot"
	"github.com/arejula27/hedge/service/internal/config"
	"github.com/arejula27/hedge/service/internal/oracle"
	"github.com/arejula27/hedge/service/internal/oracleclient"
	"github.com/arejula27/hedge/service/internal/oracleserver"
	"github.com/arejula27/hedge/service/internal/postgres"
	"github.com/arejula27/hedge/service/internal/server"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The standard position, in the units the API takes: a $10,000 hedge against
// 0.2 BTC, liquidating at $50,000 and $200,000.
const (
	openingPrice = 10_000_000
	lowBoundary  = 5_000_000
	payoutSats   = 20_000_000
	boardedSats  = 50_000_000
)

// What the covenant pays at the low boundary. Written down, never computed:
// computing it here would recreate the parallel implementation the covenant
// exists to be the only copy of.
const (
	shortAtTheBoundary = 19_998_668
	longAtTheBoundary  = 1_332
)

var dsn string

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := pgcontainer.Run(ctx, "postgres:latest",
		pgcontainer.WithDatabase("hedge"),
		pgcontainer.WithUsername("hedge"),
		pgcontainer.WithPassword("hedge"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		log.Fatalf("starting postgres: %v", err)
	}

	if dsn, err = container.ConnectionString(ctx, "sslmode=disable"); err != nil {
		log.Fatalf("connection string: %v", err)
	}

	code := m.Run()

	if err := container.Terminate(ctx); err != nil {
		log.Printf("terminating postgres: %v", err)
	}
	os.Exit(code)
}

// demo is the whole service, wired the way the binaries wire it.
type demo struct {
	t      *testing.T
	url    string
	oracle *oracle.Publisher
}

func newDemo(t *testing.T) *demo {
	t.Helper()

	ctx := t.Context()

	cfg := config.Config{
		Database: dsn,
		// The demo's key, the same one .env.example ships.
		ServiceSeed: "2626262626262626262626262626262626262626262626262626262626262626",
		// The faucet, from this package's directory.
		RegtestScript: "../../../scripts/regtest.sh",
	}

	db, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.Pool().ExecContext(ctx,
		`TRUNCATE exit_packages, contract_events, contracts, wallets, users,
		          oracle_publications CASCADE`); err != nil {
		t.Fatalf("emptying the tables: %v", err)
	}

	// The oracle in process, but the real one: the real publisher over the real
	// store, behind the real HTTP handlers, reached through the real client.
	key, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x11}, 32))
	store := postgres.NewOracleStore(db)
	publisher := oracle.NewPublisher(store, key, openingPrice)

	// Two publications, because settling needs a message and the one
	// immediately before it.
	for range 2 {
		if _, err := publisher.Publish(ctx); err != nil {
			t.Fatalf("publishing: %v", err)
		}
	}

	oracleAPI := httptest.NewServer(oracleserver.NewServer(publisher, store, oracleserver.Options{
		Interval:    5 * time.Second,
		AllowManual: true,
	}).Routes())
	t.Cleanup(oracleAPI.Close)

	cfg.Oracle = oracleAPI.URL

	service, err := boot.Wire(ctx, cfg, boot.Options{Feed: oracleclient.New(oracleAPI.URL)})
	if err != nil {
		t.Fatalf("wiring the service: %v", err)
	}
	t.Cleanup(service.Close)

	go app.NewWorker(service.App, app.WorkerOptions{Log: t.Logf}).Run(ctx)

	api := httptest.NewServer(server.NewServer(server.Options{
		App:    service.App,
		DB:     db,
		Broker: service.Broker,
		Params: service.Params,
	}).Routes(""))
	t.Cleanup(api.Close)

	return &demo{t: t, url: api.URL, oracle: publisher}
}

// The whole base flow, end to end: two people with wallets fund a contract
// between them, the price crosses a boundary, and it settles into the split the
// covenant's own formula gives — with the sats landing in wallets that can
// spend them.
func TestTheDemo(t *testing.T) {
	d := newDemo(t)

	alice := d.user("alice")
	bob := d.user("bob")

	d.board(alice)
	d.board(bob)

	before := map[string]int64{
		"alice": d.wallet(alice).SpendableSats,
		"bob":   d.wallet(bob).SpendableSats,
	}

	contract := d.propose(alice)
	if contract.Address != "" {
		t.Error("there is an address before both sides are known")
	}

	contract = d.accept(bob, contract.ID)
	if contract.Address == "" {
		t.Fatal("accepting did not produce an address")
	}
	if contract.ShortStake+contract.LongStake != payoutSats {
		t.Fatalf("the stakes %d/%d do not add up to %d",
			contract.ShortStake, contract.LongStake, payoutSats)
	}

	d.fund(alice, contract.ID)
	d.waitFor(contract.ID, "active", 3*time.Minute)

	if got := d.contract(contract.ID); !got.ExitReady {
		t.Error("the contract went live without a pre-signed exit")
	}

	// A crash to the low boundary: the short's hedge is paid in full.
	d.setPrice(lowBoundary)
	d.settle(contract.ID)
	d.waitFor(contract.ID, "settled", 2*time.Minute)

	settled := d.contract(contract.ID)
	if settled.Projection == nil {
		t.Fatal("there is no projection on a settled contract")
	}
	if settled.Projection.ShortSats != shortAtTheBoundary ||
		settled.Projection.LongSats != longAtTheBoundary {
		t.Errorf("paid %d/%d, want %d/%d",
			settled.Projection.ShortSats, settled.Projection.LongSats,
			shortAtTheBoundary, longAtTheBoundary)
	}

	// The part that matters: the payouts are spendable VTXOs in the parties'
	// own wallets, not a number in our database. A bare P2TR of their key would
	// be a perfectly valid output that no Arkade wallet indexes.
	for _, side := range []struct {
		name  string
		user  string
		stake int64
		paid  int64
	}{
		{"alice", alice, settled.ShortStake, shortAtTheBoundary},
		{"bob", bob, settled.LongStake, longAtTheBoundary},
	} {
		want := before[side.name] - side.stake + side.paid
		got := d.settledBalance(side.user, want, time.Minute)
		if got != want {
			t.Errorf("%s has %d sats, want %d (started %d, staked %d, was paid %d)",
				side.name, got, want, before[side.name], side.stake, side.paid)
		}
	}
}

// Leaf 2: both parties agree to end it early, at a split of their own choosing,
// with no oracle and no covenant involved. It goes straight to the operator —
// the leaf carries no tweaked emulator key, so the emulator has nothing to run.
func TestClosingEarly(t *testing.T) {
	d := newDemo(t)

	alice, bob := d.user("alice"), d.user("bob")
	d.board(alice)
	d.board(bob)

	before := map[string]int64{
		"alice": d.wallet(alice).SpendableSats,
		"bob":   d.wallet(bob).SpendableSats,
	}

	contract := d.accept(bob, d.propose(alice).ID)
	d.fund(alice, contract.ID)
	d.waitFor(contract.ID, "active", 3*time.Minute)

	// A lopsided split, nothing like what the covenant would pay: the point of
	// this leaf is that the two of them decide, and the covenant is out of it.
	lopsided := struct{ short, long int64 }{19_000_000, 1_000_000}

	var proposal struct {
		ShortSats   int64 `json:"short_sats"`
		LongSats    int64 `json:"long_sats"`
		ShortSigned bool  `json:"short_signed"`
		LongSigned  bool  `json:"long_signed"`
	}
	d.expect(&alice, http.MethodPost, "/api/contracts/"+contract.ID+"/redemption",
		fmt.Sprintf(`{"short_sats":%d,"long_sats":%d}`, lopsided.short, lopsided.long),
		http.StatusCreated, &proposal)

	if !proposal.ShortSigned || proposal.LongSigned {
		t.Fatalf("after proposing: short %v, long %v", proposal.ShortSigned, proposal.LongSigned)
	}

	d.expect(&bob, http.MethodPost, "/api/contracts/"+contract.ID+"/redemption/sign", "",
		http.StatusOK, &proposal)
	if !proposal.ShortSigned || !proposal.LongSigned {
		t.Fatal("both signed and the proposal does not say so")
	}

	d.waitFor(contract.ID, "redeemed", 2*time.Minute)

	// And the money moved the way they agreed, not the way the formula would
	// have.
	for _, side := range []struct {
		name  string
		user  string
		stake int64
		paid  int64
	}{
		{"alice", alice, 10_000_000, lopsided.short},
		{"bob", bob, 10_000_000, lopsided.long},
	} {
		want := before[side.name] - side.stake + side.paid
		if got := d.settledBalance(side.user, want, time.Minute); got != want {
			t.Errorf("%s has %d sats, want %d", side.name, got, want)
		}
	}
}

// A contract whose price is inside its boundaries has nothing to settle, and
// the refusal has to say so rather than come back as a script failure.
func TestSettlingTooEarlyIsRefused(t *testing.T) {
	d := newDemo(t)

	alice, bob := d.user("alice"), d.user("bob")
	d.board(alice)
	d.board(bob)

	contract := d.accept(bob, d.propose(alice).ID)
	d.fund(alice, contract.ID)
	d.waitFor(contract.ID, "active", 3*time.Minute)

	status, body := d.post(nil, "/api/contracts/"+contract.ID+"/settle", "")
	if status != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", status, body)
	}
	if !bytes.Contains(body, []byte("nothing to settle")) {
		t.Errorf("the reason is missing from %s", body)
	}
}

// --- driving the API --------------------------------------------------------

func (d *demo) user(name string) string {
	d.t.Helper()

	var out struct{ ID string }
	d.expect(nil, http.MethodPost, "/api/users",
		fmt.Sprintf(`{"name":%q}`, name), http.StatusCreated, &out)
	return out.ID
}

func (d *demo) board(user string) {
	d.t.Helper()

	d.expect(&user, http.MethodPost, "/api/wallet/fund",
		fmt.Sprintf(`{"sats":%d}`, boardedSats), http.StatusAccepted, nil)

	if got := d.wallet(user).SpendableSats; got < payoutSats {
		d.t.Fatalf("boarding left %d sats, which cannot fund a contract", got)
	}
}

type walletView struct {
	SpendableSats   int64 `json:"spendable_sats"`
	RecoverableSats int64 `json:"recoverable_sats"`
}

func (d *demo) wallet(user string) walletView {
	d.t.Helper()

	var out walletView
	d.expect(&user, http.MethodGet, "/api/wallet", "", http.StatusOK, &out)
	return out
}

// settledBalance polls until the payout has landed, because arkd registers the
// new VTXOs a moment after the emulator forwards the transaction.
func (d *demo) settledBalance(user string, want int64, budget time.Duration) int64 {
	d.t.Helper()

	deadline := time.Now().Add(budget)
	var got int64
	for time.Now().Before(deadline) {
		got = d.wallet(user).SpendableSats
		if got == want {
			return got
		}
		time.Sleep(2 * time.Second)
	}
	return got
}

type contractView struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Address    string `json:"address"`
	ShortStake int64  `json:"short_stake"`
	LongStake  int64  `json:"long_stake"`
	ExitReady  bool   `json:"exit_ready"`
	Projection *struct {
		Price      int64 `json:"price"`
		ShortSats  int64 `json:"short_sats"`
		LongSats   int64 `json:"long_sats"`
		Liquidated bool  `json:"liquidated"`
	} `json:"projection"`
}

func (d *demo) propose(user string) contractView {
	d.t.Helper()

	var out contractView
	d.expect(&user, http.MethodPost, "/api/contracts", fmt.Sprintf(`{
		"side": "short",
		"hedge_value_cents": 1000000,
		"payout_sats": %d,
		"low_liquidation_cents": %d,
		"high_liquidation_cents": 20000000,
		"maturity_in_seconds": 86400,
		"enable_mutual_redemption": true
	}`, payoutSats, lowBoundary), http.StatusCreated, &out)
	return out
}

func (d *demo) accept(user, id string) contractView {
	d.t.Helper()

	var out contractView
	d.expect(&user, http.MethodPost, "/api/contracts/"+id+"/accept", "", http.StatusOK, &out)
	return out
}

func (d *demo) fund(user, id string) {
	d.t.Helper()
	d.expect(&user, http.MethodPost, "/api/contracts/"+id+"/fund", "", http.StatusOK, nil)
}

// settle needs no identity: the settlement leaf carries no party key.
func (d *demo) settle(id string) {
	d.t.Helper()
	d.expect(nil, http.MethodPost, "/api/contracts/"+id+"/settle", "", http.StatusOK, nil)
}

func (d *demo) setPrice(price int64) {
	d.t.Helper()
	d.expect(nil, http.MethodPost, "/api/oracle/price",
		fmt.Sprintf(`{"price":%d}`, price), http.StatusNoContent, nil)
}

func (d *demo) contract(id string) contractView {
	d.t.Helper()

	var out contractView
	d.expect(nil, http.MethodGet, "/api/contracts/"+id, "", http.StatusOK, &out)
	return out
}

// waitFor polls until the contract reaches a state, reporting where it actually
// got to. Funding and settling are the worker's job and take tens of seconds.
func (d *demo) waitFor(id, want string, budget time.Duration) {
	d.t.Helper()

	deadline := time.Now().Add(budget)
	var state string
	for time.Now().Before(deadline) {
		state = d.contract(id).State
		if state == want {
			return
		}
		if state == "failed" || state == "cancelled" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	d.t.Fatalf("waited %s for %s to be %s; it is %s", budget, id, want, state)
}

func (d *demo) expect(user *string, method, path, body string, want int, into any) {
	d.t.Helper()

	status, raw := d.do(user, method, path, body)
	if status != want {
		d.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			d.t.Fatalf("decoding %s: %v", raw, err)
		}
	}
}

func (d *demo) post(user *string, path, body string) (int, []byte) {
	d.t.Helper()
	return d.do(user, http.MethodPost, path, body)
}

func (d *demo) do(user *string, method, path, body string) (int, []byte) {
	d.t.Helper()

	req, err := http.NewRequestWithContext(d.t.Context(), method, d.url+path, bytes.NewBufferString(body))
	if err != nil {
		d.t.Fatalf("building the request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != nil {
		req.Header.Set("X-Hedge-User", *user)
	}

	// Boarding is minutes of faucet, blocks and a batch.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		d.t.Fatalf("reading the response: %v", err)
	}
	return resp.StatusCode, raw
}
