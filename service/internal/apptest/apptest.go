// Package apptest is the stubs behind the use cases.
//
// They live in a package of their own rather than in one suite's test files
// because two suites need them: the use cases' own, and the HTTP layer's, which
// drives requests through a real app.App to check the mapping from an outcome to a
// status code.
//
// They are written by hand. There are five, they are each a map and a few
// methods, and what a test needs to say about them is usually "this call
// failed" rather than "this call happened".
package apptest

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/app"
	"github.com/arejula27/hedge/service/internal/domain"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
)

type Users struct {
	ByID map[uuid.UUID]domain.User
	Fail error
}

func NewUsers() *Users { return &Users{ByID: map[uuid.UUID]domain.User{}} }

func (s *Users) Create(_ context.Context, u domain.User, _ []byte) error {
	if s.Fail != nil {
		return s.Fail
	}
	for _, existing := range s.ByID {
		if existing.Name == u.Name {
			return domain.ErrNameTaken
		}
	}
	s.ByID[u.ID] = u
	return nil
}

func (s *Users) Get(_ context.Context, id uuid.UUID) (domain.User, error) {
	u, ok := s.ByID[id]
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

func (s *Users) List(context.Context) ([]domain.User, error) {
	out := make([]domain.User, 0, len(s.ByID))
	for _, u := range s.ByID {
		out = append(out, u)
	}
	return out, nil
}

// Contracts allocates state the way the real one does, including the
// compare-and-swap: Advance refuses a contract whose stored state has moved on.
type Contracts struct {
	mu       sync.Mutex
	Stored   map[uuid.UUID]domain.Contract
	Recorded map[uuid.UUID][]domain.Event
	now      func() time.Time
}

func NewContracts(now func() time.Time) *Contracts {
	return &Contracts{
		Stored:   map[uuid.UUID]domain.Contract{},
		Recorded: map[uuid.UUID][]domain.Event{},
		now:      now,
	}
}

func (s *Contracts) Create(_ context.Context, c *domain.Contract) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c.UpdatedAt = s.now()
	s.Stored[c.ID] = *c
	s.Recorded[c.ID] = append(s.Recorded[c.ID], domain.Event{ContractID: c.ID, To: c.State, Detail: "created"})
	return nil
}

func (s *Contracts) Get(_ context.Context, id uuid.UUID) (*domain.Contract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.Stored[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (s *Contracts) List(_ context.Context, f domain.ContractFilter) ([]*domain.Contract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []*domain.Contract{}
	for _, c := range s.Stored {
		if f.State != "" && c.State != f.State {
			continue
		}
		if f.User != nil {
			if _, party := c.SideOf(*f.User); !party {
				continue
			}
		}
		if f.Open && !c.Open() {
			continue
		}
		copied := c
		out = append(out, &copied)
	}
	return out, nil
}

func (s *Contracts) InState(_ context.Context, states ...domain.State) ([]*domain.Contract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wanted := map[domain.State]bool{}
	for _, state := range states {
		wanted[state] = true
	}

	out := []*domain.Contract{}
	for _, c := range s.Stored {
		if wanted[c.State] {
			copied := c
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (s *Contracts) Advance(_ context.Context, c *domain.Contract, to domain.State, detail string) error {
	from := c.State
	if err := from.Transition(to); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.Stored[c.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.State != from {
		return domain.ErrConflict
	}

	c.State = to
	c.UpdatedAt = s.now()
	s.Stored[c.ID] = *c
	s.Recorded[c.ID] = append(s.Recorded[c.ID],
		domain.Event{ContractID: c.ID, From: from, To: to, Detail: detail})
	return nil
}

func (s *Contracts) Save(_ context.Context, c *domain.Contract) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.Stored[c.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.State != c.State {
		return domain.ErrConflict
	}
	s.Stored[c.ID] = *c
	return nil
}

func (s *Contracts) Events(_ context.Context, id uuid.UUID) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Recorded[id], nil
}

type Exits struct {
	Stored map[uuid.UUID]domain.ExitPackage
	Fail   error
}

func NewExits() *Exits { return &Exits{Stored: map[uuid.UUID]domain.ExitPackage{}} }

func (s *Exits) Put(_ context.Context, e domain.ExitPackage) error {
	if s.Fail != nil {
		return s.Fail
	}
	s.Stored[e.ContractID] = e
	return nil
}

func (s *Exits) Get(_ context.Context, id uuid.UUID) (domain.ExitPackage, error) {
	e, ok := s.Stored[id]
	if !ok {
		return domain.ExitPackage{}, domain.ErrNotFound
	}
	return e, nil
}

// Signer holds a real key per user, so signatures verify for real. The
// point of the port is that no use case ever sees one, and a stub that returned
// made-up bytes would not exercise that.
type Signer struct {
	Keys      map[uuid.UUID]*btcec.PrivateKey
	FailExit  error
	WrongKey  *btcec.PrivateKey
	SignCalls int
}

func NewSigner() *Signer {
	return &Signer{Keys: map[uuid.UUID]*btcec.PrivateKey{}}
}

func (s *Signer) Add(user uuid.UUID, key *btcec.PrivateKey) { s.Keys[user] = key }

func (s *Signer) PublicKey(_ context.Context, user uuid.UUID) (*btcec.PublicKey, error) {
	key, ok := s.Keys[user]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return key.PubKey(), nil
}

func (s *Signer) SignPacket(_ context.Context, _ uuid.UUID, packetB64 string) (string, error) {
	s.SignCalls++
	return packetB64, nil
}

func (s *Signer) SignExit(
	_ context.Context, user uuid.UUID, c *domain.Contract, exit *domain.Exit,
) ([]byte, error) {
	if s.FailExit != nil {
		return nil, s.FailExit
	}

	key := s.Keys[user]
	if s.WrongKey != nil {
		key = s.WrongKey
	}
	if key == nil {
		return nil, domain.ErrNotFound
	}

	covenant, err := c.Covenant()
	if err != nil {
		return nil, err
	}

	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(exit.RawTx)); err != nil {
		return nil, err
	}
	return covenant.SignExit(key, &tx, exit.Amount)
}

type Arkade struct {
	StackInfo app.Stack
	Balances  map[uuid.UUID]int64
	// Recoverable is money a wallet holds that it cannot spend offchain until
	// it has been back through a batch.
	Recoverable map[uuid.UUID]int64
	Scripts     map[uuid.UUID][]byte

	Funded    *domain.Contract
	FundCalls int
	FundErr   error
	Settled   *domain.Contract
	SettleAt  [2]int64
	SettleErr error
	TopUps    map[uuid.UUID]int64
}

func NewArkade() *Arkade {
	arkd, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x23}, 32))
	emulator, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x24}, 32))

	return &Arkade{
		StackInfo: app.Stack{
			ArkdSigner:           arkd.PubKey(),
			EmulatorSigner:       emulator.PubKey(),
			ExitDelay:            arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 5},
			Dust:                 330,
			AllowsBlockTimelocks: true,
		},
		Balances:    map[uuid.UUID]int64{},
		Recoverable: map[uuid.UUID]int64{},
		Scripts:     map[uuid.UUID][]byte{},
		TopUps:      map[uuid.UUID]int64{},
	}
}

func (s *Arkade) Stack() app.Stack { return s.StackInfo }

func (s *Arkade) Addresses(context.Context, uuid.UUID) (string, string, error) {
	return "ark1offchain", "bcrt1boarding", nil
}

func (s *Arkade) Balance(_ context.Context, user uuid.UUID) (int64, int64, error) {
	return s.Balances[user], s.Recoverable[user], nil
}

func (s *Arkade) Recover(_ context.Context, user uuid.UUID) error {
	s.Balances[user] += s.Recoverable[user]
	s.Recoverable[user] = 0
	return nil
}

func (s *Arkade) VtxoPkScript(_ context.Context, user uuid.UUID) ([]byte, error) {
	script, ok := s.Scripts[user]
	if !ok {
		return nil, fmt.Errorf("no script for %s", user)
	}
	return script, nil
}

func (s *Arkade) TopUp(_ context.Context, user uuid.UUID, sats int64) error {
	s.TopUps[user] += sats
	s.Balances[user] += sats
	return nil
}

func (s *Arkade) Fund(_ context.Context, c *domain.Contract) (domain.Outpoint, error) {
	if s.FundErr != nil {
		return domain.Outpoint{}, s.FundErr
	}
	s.FundCalls++
	s.Funded = c
	return domain.Outpoint{
		Txid: "0000000000000000000000000000000000000000000000000000000000000001",
		Vout: 0,
	}, nil
}

func (s *Arkade) Settle(_ context.Context, c *domain.Contract, short, long int64, _ app.Pair) error {
	if s.SettleErr != nil {
		return s.SettleErr
	}
	s.Settled = c
	s.SettleAt = [2]int64{short, long}
	return nil
}

// Feed is a real oracle key signing real messages, so a contract built
// against it can verify what comes back.
type Feed struct {
	SigningKey   *btcec.PrivateKey
	CurrentPrice int64
	Sequence     uint64
	At           int64
	Fail         error
	Set          []int64

	// lies is what Latest reports while app.Pair keeps signing the real price, so
	// a test can check the split comes from the signed bytes and not from
	// whatever number arrived beside them.
	Lies int64
}

func NewFeed(price int64) *Feed {
	key, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x11}, 32))
	return &Feed{SigningKey: key, CurrentPrice: price, Sequence: 10, At: FixedNow.Unix()}
}

func (s *Feed) PubKey(context.Context) ([]byte, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	return schnorrKey(s.SigningKey), nil
}

func (s *Feed) Latest(context.Context) (domain.Price, error) {
	if s.Fail != nil {
		return domain.Price{}, s.Fail
	}
	price := s.CurrentPrice
	if s.Lies != 0 {
		price = s.Lies
	}
	return domain.Price{Sequence: s.Sequence, Timestamp: s.At, Price: price}, nil
}

func (s *Feed) Pair(context.Context) (app.Pair, error) {
	if s.Fail != nil {
		return app.Pair{}, s.Fail
	}
	return app.Pair{
		Settlement: s.sign(s.Sequence, s.At, s.CurrentPrice),
		Previous:   s.sign(s.Sequence-1, s.At-5, s.CurrentPrice),
	}, nil
}

func (s *Feed) sign(sequence uint64, at, price int64) arkade.SignedPrice {
	message := contract.OracleMessage(uint64(at), sequence, uint64(price))
	signature, err := contract.SignOracleMessage(s.SigningKey, message)
	if err != nil {
		panic(err)
	}
	return arkade.SignedPrice{Message: message, Signature: signature}
}

func (s *Feed) SetPrice(_ context.Context, price int64) error {
	if s.Fail != nil {
		return s.Fail
	}
	s.Set = append(s.Set, price)
	s.CurrentPrice = price
	s.Sequence++
	return nil
}

func (s *Feed) History(_ context.Context, limit int) ([]domain.Price, error) {
	return []domain.Price{{Sequence: s.Sequence, Timestamp: s.At, Price: s.CurrentPrice}}, nil
}

func schnorrKey(k *btcec.PrivateKey) []byte {
	serialized := k.PubKey().SerializeCompressed()
	return serialized[1:]
}

// FixedNow keeps every timestamp in the tests reproducible.
var FixedNow = time.Unix(1_800_000_000, 0)

// Fixture is the whole app with stubs behind it, plus two users who have money.
type Fixture struct {
	App           *app.App
	UserStore     *Users
	ContractStore *Contracts
	ExitStore     *Exits
	SignerStub    *Signer
	StackStub     *Arkade
	FeedStub      *Feed

	Alice, Bob uuid.UUID

	// ServiceKey is the third of the 2-of-3 a unilateral exit sweeps into.
	ServiceKey *btcec.PublicKey

	// Now is the clock every stub reads, so a test can move time forward and
	// watch the worker give up on a step that will never finish.
	Now time.Time

	clock func() time.Time

	// Contract is what Accepted left behind, so a test that only cares about
	// funding does not have to carry it around.
	Contract *domain.Contract
}

func New(t *testing.T) *Fixture {
	t.Helper()

	f := &Fixture{Now: FixedNow}
	clock := func() time.Time { return f.Now }

	f.UserStore = NewUsers()
	f.ContractStore = NewContracts(clock)
	f.ExitStore = NewExits()
	f.SignerStub = NewSigner()
	f.StackStub = NewArkade()
	f.FeedStub = NewFeed(10_000_000)

	service, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x26}, 32))
	f.ServiceKey = service.PubKey()
	f.clock = clock

	f.App = app.New(f.Options(f.ContractStore))

	f.Alice = f.AddUser(t, "alice", 0x21)
	f.Bob = f.AddUser(t, "bob", 0x22)
	return f
}

// Options is how the app is wired, with the contract store left open.
//
// Rebuilding it with a different store is what lets the HTTP tests slot the
// event broker in exactly where the composition root does — around the store,
// because Advance is the only way a contract moves.
func (f *Fixture) Options(contracts app.Contracts) app.Options {
	return app.Options{
		Users:     f.UserStore,
		Contracts: contracts,
		Exits:     f.ExitStore,
		Signer:    f.SignerStub,
		Stack:     f.StackStub,
		Feed:      f.FeedStub,

		ServiceKey:  f.ServiceKey,
		ExitFeeSats: 2_000,
		Now:         f.clock,
	}
}

func (f *Fixture) AddUser(t *testing.T, name string, tag byte) uuid.UUID {
	t.Helper()

	key, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{tag}, 32))
	id := uuid.New()

	f.UserStore.ByID[id] = domain.User{ID: id, Name: name, PublicKey: key.PubKey().SerializeCompressed()}
	f.SignerStub.Add(id, key)
	f.StackStub.Balances[id] = 50_000_000
	f.StackStub.Scripts[id] = append([]byte{0x51, 0x20}, bytes.Repeat([]byte{tag}, 32)...)

	return id
}

// standard is the position the integration tests use: a $10,000 hedge against
// 0.2 BTC, liquidating at $50,000 and $200,000.
func (f *Fixture) Standard(creator uuid.UUID, side domain.Side) app.Proposal {
	return app.Proposal{
		Creator:                creator,
		Side:                   side,
		HedgeValueCents:        1_000_000,
		PayoutSats:             20_000_000,
		LowLiquidationCents:    5_000_000,
		HighLiquidationCents:   20_000_000,
		MaturityIn:             24 * time.Hour,
		EnableMutualRedemption: true,
	}
}

// accepted runs a contract as far as both sides being known.
func (f *Fixture) Accepted(t *testing.T) *domain.Contract {
	t.Helper()

	c, err := f.App.Propose(t.Context(), f.Standard(f.Alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if c, err = f.App.Accept(t.Context(), c.ID, f.Bob); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	f.Contract = c
	return c
}

// funded runs a contract all the way to active.
func (f *Fixture) Funded(t *testing.T) *domain.Contract {
	t.Helper()

	c := f.Accepted(t)
	if _, err := f.App.Fund(t.Context(), c.ID, f.Alice); err != nil {
		t.Fatalf("Fund: %v", err)
	}

	app.NewWorker(f.App, app.WorkerOptions{}).Tick(t.Context())

	c, err := f.App.Contract(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if c.State != domain.Active {
		t.Fatalf("the contract is %s, want active", c.State)
	}
	return c
}
