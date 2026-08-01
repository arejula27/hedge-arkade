//go:build integration

package postgres

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/domain"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/google/uuid"
)

// repos opens a connection, migrates, and empties every table so each test
// starts from nothing.
func repos(t *testing.T) (*UserRepo, *ContractRepo) {
	t.Helper()

	db, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.pool.ExecContext(t.Context(),
		`TRUNCATE contract_events, contracts, wallets, users CASCADE`); err != nil {
		t.Fatalf("emptying the tables: %v", err)
	}

	return NewUserRepo(db), NewContractRepo(db)
}

func newUser(t *testing.T, users *UserRepo, name string) domain.User {
	t.Helper()

	u := domain.User{ID: uuid.New(), Name: name, PublicKey: bytes.Repeat([]byte{0x02}, 33)}
	if err := users.Create(t.Context(), u, bytes.Repeat([]byte{0x11}, 32)); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	return u
}

// newContract is the standard position: a $10,000 hedge against 0.2 BTC,
// liquidating at $50,000 and $200,000.
func newContract(t *testing.T, short, long *uuid.UUID) *domain.Contract {
	t.Helper()

	return &domain.Contract{
		ID:        uuid.New(),
		State:     domain.Proposed,
		Creator:   domain.Short,
		ShortUser: short, LongUser: long,
		Terms: contract.Terms{
			NominalUnitsXSatsPerBtc:              100_000_000_000_000,
			SatsForNominalUnitsAtHighLiquidation: 0,
			PayoutSats:                           20_000_000,
			LowLiquidationPrice:                  5_000_000,
			HighLiquidationPrice:                 20_000_000,
			ShortLockScript:                      []byte{0x51, 0x20, 0xaa},
			LongLockScript:                       []byte{0x51, 0x20, 0xbb},
			OraclePubKey:                         bytes.Repeat([]byte{0x11}, 32),
			StartTimestamp:                       1_800_000_000,
			MaturityTimestamp:                    1_800_086_400,
		},
		ShortKey:               bytes.Repeat([]byte{0x21}, 33),
		LongKey:                bytes.Repeat([]byte{0x22}, 33),
		ArkdSigner:             bytes.Repeat([]byte{0x23}, 33),
		EmulatorSigner:         bytes.Repeat([]byte{0x24}, 33),
		ExitDelay:              arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 5},
		EnableMutualRedemption: true,
		PkScript:               []byte{0x51, 0x20, 0xcc},
		ShortStake:             10_000_000,
		LongStake:              10_000_000,
	}
}

func TestAUserIsWrittenWithTheirWallet(t *testing.T) {
	users, _ := repos(t)

	alice := newUser(t, users, "alice")

	got, err := users.Get(t.Context(), alice.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "alice" || !bytes.Equal(got.PublicKey, alice.PublicKey) {
		t.Errorf("read back %+v", got)
	}

	seed, err := users.Seed(t.Context(), alice.ID)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if !bytes.Equal(seed, bytes.Repeat([]byte{0x11}, 32)) {
		t.Errorf("the seed came back as %x", seed)
	}
}

func TestTwoUsersCannotShareAName(t *testing.T) {
	users, _ := repos(t)
	newUser(t, users, "alice")

	second := domain.User{ID: uuid.New(), Name: "alice", PublicKey: []byte{0x02}}
	err := users.Create(t.Context(), second, []byte{0x11})
	if !errors.Is(err, ErrNameTaken) {
		t.Errorf("Create gave %v, want ErrNameTaken", err)
	}
}

func TestAMissingUserIsNotFound(t *testing.T) {
	users, _ := repos(t)

	if _, err := users.Get(t.Context(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get gave %v", err)
	}
	if _, err := users.ByName(t.Context(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByName gave %v", err)
	}
	if _, err := users.Seed(t.Context(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Seed gave %v", err)
	}
}

// Every field has to survive the round trip, because the taproot address is a
// pure function of most of them: a column that comes back wrong is a contract
// funded at an address nobody can spend.
func TestAContractRoundTrips(t *testing.T) {
	users, contracts := repos(t)
	alice := newUser(t, users, "alice")

	want := newContract(t, &alice.ID, nil)
	want.Funding = &domain.Outpoint{Txid: "abc123", Vout: 0}

	if err := contracts.Create(t.Context(), want); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := contracts.Get(t.Context(), want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.State != domain.Proposed || got.Creator != domain.Short {
		t.Errorf("state %s, creator %s", got.State, got.Creator)
	}
	if got.ShortUser == nil || *got.ShortUser != alice.ID {
		t.Errorf("short user %v", got.ShortUser)
	}
	if got.LongUser != nil {
		t.Errorf("long user is %v, want nil", got.LongUser)
	}
	if !reflect.DeepEqual(got.Terms, want.Terms) {
		t.Errorf("terms came back as %+v", got.Terms)
	}
	if got.ExitDelay != want.ExitDelay {
		t.Errorf("exit delay %+v, want %+v", got.ExitDelay, want.ExitDelay)
	}
	if !bytes.Equal(got.PkScript, want.PkScript) || !bytes.Equal(got.ArkdSigner, want.ArkdSigner) {
		t.Error("the scripts or keys did not survive")
	}
	if got.Funding == nil || *got.Funding != *want.Funding {
		t.Errorf("funding %+v", got.Funding)
	}
}

// A seconds-based delay and a block-based one are stored in the same column
// with a flag, and reading the flag backwards builds a contract the operator
// refuses.
func TestTheExitDelayKeepsItsUnit(t *testing.T) {
	users, contracts := repos(t)
	alice := newUser(t, users, "alice")

	for _, tc := range []struct {
		name string
		want arklib.RelativeLocktime
	}{
		{"blocks", arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 5}},
		{"seconds", arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: 1024}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			c := newContract(t, &alice.ID, nil)
			c.ExitDelay = want
			if err := contracts.Create(t.Context(), c); err != nil {
				t.Fatalf("Create: %v", err)
			}

			got, err := contracts.Get(t.Context(), c.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.ExitDelay != want {
				t.Errorf("exit delay %+v, want %+v", got.ExitDelay, want)
			}
		})
	}
}

func TestAdvanceRecordsEveryTransition(t *testing.T) {
	users, contracts := repos(t)
	alice, bob := newUser(t, users, "alice"), newUser(t, users, "bob")

	c := newContract(t, &alice.ID, nil)
	if err := contracts.Create(t.Context(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	c.LongUser = &bob.ID
	if err := contracts.Advance(t.Context(), c, domain.Accepted, "bob took the long"); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if err := contracts.Advance(t.Context(), c, domain.Funding, ""); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	got, err := contracts.Get(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != domain.Funding {
		t.Errorf("state %s, want funding", got.State)
	}
	if got.LongUser == nil || *got.LongUser != bob.ID {
		t.Error("the counterparty was not written with the transition")
	}

	events, err := contracts.Events(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i, want := range []struct{ from, to domain.State }{
		{"", domain.Proposed},
		{domain.Proposed, domain.Accepted},
		{domain.Accepted, domain.Funding},
	} {
		if events[i].From != want.from || events[i].To != want.to {
			t.Errorf("event %d is %s -> %s, want %s -> %s",
				i, events[i].From, events[i].To, want.from, want.to)
		}
	}
	if events[1].Detail != "bob took the long" {
		t.Errorf("the detail came back as %q", events[1].Detail)
	}
}

func TestAdvanceRefusesAMoveTheDomainForbids(t *testing.T) {
	users, contracts := repos(t)
	alice := newUser(t, users, "alice")

	c := newContract(t, &alice.ID, nil)
	if err := contracts.Create(t.Context(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := contracts.Advance(t.Context(), c, domain.Settled, ""); err == nil {
		t.Fatal("Advance took a contract straight from proposed to settled")
	}
	if c.State != domain.Proposed {
		t.Errorf("a refused transition left the contract in %s", c.State)
	}

	got, _ := contracts.Get(t.Context(), c.ID)
	if got.State != domain.Proposed {
		t.Errorf("the row moved to %s", got.State)
	}
}

// Two workers picking up the same contract is expected. One of them losing is
// the mechanism working, and it has to lose loudly rather than write over the
// other's result.
func TestAdvanceRefusesAContractThatMovedOn(t *testing.T) {
	users, contracts := repos(t)
	alice, bob := newUser(t, users, "alice"), newUser(t, users, "bob")

	c := newContract(t, &alice.ID, nil)
	if err := contracts.Create(t.Context(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Two readers, both holding the contract as `proposed`.
	first, err := contracts.Get(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	second, err := contracts.Get(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	first.LongUser = &bob.ID
	if err := contracts.Advance(t.Context(), first, domain.Accepted, ""); err != nil {
		t.Fatalf("the first Advance failed: %v", err)
	}

	second.LongUser = &alice.ID
	err = contracts.Advance(t.Context(), second, domain.Accepted, "")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("the second Advance gave %v, want ErrConflict", err)
	}

	got, _ := contracts.Get(t.Context(), c.ID)
	if got.LongUser == nil || *got.LongUser != bob.ID {
		t.Error("the loser overwrote the winner's counterparty")
	}
}

// The same race, run for real: only one of many concurrent accepts may win,
// and every loser has to lose in a way the caller can recognise as a conflict
// rather than as the database being down.
func TestOnlyOneAcceptWins(t *testing.T) {
	users, contracts := repos(t)
	alice := newUser(t, users, "alice")

	c := newContract(t, &alice.ID, nil)
	if err := contracts.Create(t.Context(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	const racers = 10
	var wg sync.WaitGroup
	results := make([]error, racers)

	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			read, err := contracts.Get(t.Context(), c.ID)
			if err != nil {
				results[i] = err
				return
			}
			results[i] = contracts.Advance(t.Context(), read, domain.Accepted, "")
		}()
	}
	wg.Wait()

	won := 0
	for i, err := range results {
		switch {
		case err == nil:
			won++
		case Lost(err):
		default:
			t.Errorf("racer %d failed with something other than a conflict: %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d racers accepted the same contract, want 1", won)
	}
}

func TestListFilters(t *testing.T) {
	users, contracts := repos(t)
	alice, bob := newUser(t, users, "alice"), newUser(t, users, "bob")

	open := newContract(t, &alice.ID, nil)
	taken := newContract(t, &alice.ID, &bob.ID)
	other := newContract(t, &bob.ID, nil)

	for _, c := range []*domain.Contract{open, taken, other} {
		if err := contracts.Create(t.Context(), c); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if err := contracts.Advance(t.Context(), taken, domain.Accepted, ""); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	for _, tc := range []struct {
		name   string
		filter Filter
		want   []uuid.UUID
	}{
		{"everything", Filter{}, []uuid.UUID{open.ID, taken.ID, other.ID}},
		{"by state", Filter{State: domain.Accepted}, []uuid.UUID{taken.ID}},
		{"by user", Filter{User: &bob.ID}, []uuid.UUID{taken.ID, other.ID}},
		{"still on offer", Filter{Open: true}, []uuid.UUID{open.ID, other.ID}},
		{"one user's offers", Filter{Open: true, User: &alice.ID}, []uuid.UUID{open.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := contracts.List(t.Context(), tc.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}

			seen := map[uuid.UUID]bool{}
			for _, c := range got {
				seen[c.ID] = true
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d contracts, want %d", len(got), len(tc.want))
			}
			for _, id := range tc.want {
				if !seen[id] {
					t.Errorf("%s is missing", id)
				}
			}
		})
	}
}

// After a restart the worker has to find whatever was left mid-step.
func TestInStateFindsWhatIsStuck(t *testing.T) {
	users, contracts := repos(t)
	alice, bob := newUser(t, users, "alice"), newUser(t, users, "bob")

	stuck := newContract(t, &alice.ID, &bob.ID)
	idle := newContract(t, &alice.ID, nil)
	for _, c := range []*domain.Contract{stuck, idle} {
		if err := contracts.Create(t.Context(), c); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	for _, to := range []domain.State{domain.Accepted, domain.Funding} {
		if err := contracts.Advance(t.Context(), stuck, to, ""); err != nil {
			t.Fatalf("Advance: %v", err)
		}
	}

	got, err := contracts.InState(t.Context(), domain.Funding, domain.Settling)
	if err != nil {
		t.Fatalf("InState: %v", err)
	}
	if len(got) != 1 || got[0].ID != stuck.ID {
		t.Fatalf("InState returned %d contracts", len(got))
	}
}

func TestAMissingContractIsNotFound(t *testing.T) {
	_, contracts := repos(t)

	if _, err := contracts.Get(t.Context(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get gave %v", err)
	}
}

// The stakes are what the parties actually put in, and the covenant pins the
// input to their sum. A row where they do not add up is a contract that can
// never settle, so the schema refuses it.
func TestTheSchemaRefusesStakesThatDoNotAddUp(t *testing.T) {
	users, contracts := repos(t)
	alice := newUser(t, users, "alice")

	c := newContract(t, &alice.ID, nil)
	c.ShortStake = 10_000_001

	if err := contracts.Create(t.Context(), c); err == nil {
		t.Error("the database accepted stakes that do not sum to the payout")
	}
}

func TestTheSchemaRefusesAStateItDoesNotKnow(t *testing.T) {
	users, contracts := repos(t)
	alice := newUser(t, users, "alice")

	c := newContract(t, &alice.ID, nil)
	c.State = "pending-vibes"

	if err := contracts.Create(t.Context(), c); err == nil {
		t.Error("the database accepted a state the domain does not have")
	}
}

// Half a funding outpoint is a row every reader would have to defend against.
func TestTheSchemaRefusesHalfAnOutpoint(t *testing.T) {
	users, contracts := repos(t)
	alice := newUser(t, users, "alice")

	c := newContract(t, &alice.ID, nil)
	if err := contracts.Create(t.Context(), c); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := contracts.db.pool.ExecContext(t.Context(),
		`UPDATE contracts SET funding_txid = 'abc' WHERE id = $1`, c.ID); err == nil {
		t.Error("the database accepted a funding txid with no vout")
	}
}

// Both ways of losing the race have to be recognisable as one, and a database
// that is down must not look like either.
func TestLostRecognisesBothWaysOfLosing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the row moved under us", ErrConflict, true},
		{"it had already moved before we read it", domain.Active.Transition(domain.Proposed), true},
		{"wrapped", fmt.Errorf("accepting: %w", ErrConflict), true},
		{"a state that does not exist", domain.State("nonsense").Transition(domain.Active), false},
		{"the row is not there", ErrNotFound, false},
		{"something else entirely", errors.New("the database is down"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Lost(tc.err); got != tc.want {
				t.Errorf("Lost(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
