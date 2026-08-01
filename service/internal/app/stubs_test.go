package app

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/contract"
	"github.com/arejula27/hedge/service/internal/domain"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
)

// The stubs are written by hand rather than generated: there are five of them,
// they are each a map and a few methods, and what a test needs to say about
// them is usually "this call failed" rather than "this call happened".

type stubUsers struct {
	byID map[uuid.UUID]domain.User
	fail error
}

func newStubUsers() *stubUsers { return &stubUsers{byID: map[uuid.UUID]domain.User{}} }

func (s *stubUsers) Create(_ context.Context, u domain.User, _ []byte) error {
	if s.fail != nil {
		return s.fail
	}
	for _, existing := range s.byID {
		if existing.Name == u.Name {
			return domain.ErrNameTaken
		}
	}
	s.byID[u.ID] = u
	return nil
}

func (s *stubUsers) Get(_ context.Context, id uuid.UUID) (domain.User, error) {
	u, ok := s.byID[id]
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

func (s *stubUsers) List(context.Context) ([]domain.User, error) {
	out := make([]domain.User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, u)
	}
	return out, nil
}

// stubContracts allocates state the way the real one does, including the
// compare-and-swap: Advance refuses a contract whose stored state has moved on.
type stubContracts struct {
	mu     sync.Mutex
	stored map[uuid.UUID]domain.Contract
	events map[uuid.UUID][]domain.Event
	now    func() time.Time
}

func newStubContracts(now func() time.Time) *stubContracts {
	return &stubContracts{
		stored: map[uuid.UUID]domain.Contract{},
		events: map[uuid.UUID][]domain.Event{},
		now:    now,
	}
}

func (s *stubContracts) Create(_ context.Context, c *domain.Contract) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c.UpdatedAt = s.now()
	s.stored[c.ID] = *c
	s.events[c.ID] = append(s.events[c.ID], domain.Event{ContractID: c.ID, To: c.State, Detail: "created"})
	return nil
}

func (s *stubContracts) Get(_ context.Context, id uuid.UUID) (*domain.Contract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.stored[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (s *stubContracts) List(_ context.Context, f domain.ContractFilter) ([]*domain.Contract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []*domain.Contract{}
	for _, c := range s.stored {
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

func (s *stubContracts) InState(_ context.Context, states ...domain.State) ([]*domain.Contract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wanted := map[domain.State]bool{}
	for _, state := range states {
		wanted[state] = true
	}

	out := []*domain.Contract{}
	for _, c := range s.stored {
		if wanted[c.State] {
			copied := c
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (s *stubContracts) Advance(_ context.Context, c *domain.Contract, to domain.State, detail string) error {
	from := c.State
	if err := from.Transition(to); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.stored[c.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.State != from {
		return domain.ErrConflict
	}

	c.State = to
	c.UpdatedAt = s.now()
	s.stored[c.ID] = *c
	s.events[c.ID] = append(s.events[c.ID],
		domain.Event{ContractID: c.ID, From: from, To: to, Detail: detail})
	return nil
}

func (s *stubContracts) Save(_ context.Context, c *domain.Contract) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.stored[c.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.State != c.State {
		return domain.ErrConflict
	}
	s.stored[c.ID] = *c
	return nil
}

func (s *stubContracts) Events(_ context.Context, id uuid.UUID) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[id], nil
}

type stubExits struct {
	stored map[uuid.UUID]domain.ExitPackage
	fail   error
}

func newStubExits() *stubExits { return &stubExits{stored: map[uuid.UUID]domain.ExitPackage{}} }

func (s *stubExits) Put(_ context.Context, e domain.ExitPackage) error {
	if s.fail != nil {
		return s.fail
	}
	s.stored[e.ContractID] = e
	return nil
}

func (s *stubExits) Get(_ context.Context, id uuid.UUID) (domain.ExitPackage, error) {
	e, ok := s.stored[id]
	if !ok {
		return domain.ExitPackage{}, domain.ErrNotFound
	}
	return e, nil
}

// stubSigner holds a real key per user, so signatures verify for real. The
// point of the port is that no use case ever sees one, and a stub that returned
// made-up bytes would not exercise that.
type stubSigner struct {
	keys      map[uuid.UUID]*btcec.PrivateKey
	failExit  error
	wrongKey  *btcec.PrivateKey
	signCalls int
}

func newStubSigner() *stubSigner {
	return &stubSigner{keys: map[uuid.UUID]*btcec.PrivateKey{}}
}

func (s *stubSigner) add(user uuid.UUID, key *btcec.PrivateKey) { s.keys[user] = key }

func (s *stubSigner) PublicKey(_ context.Context, user uuid.UUID) (*btcec.PublicKey, error) {
	key, ok := s.keys[user]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return key.PubKey(), nil
}

func (s *stubSigner) SignPacket(_ context.Context, _ uuid.UUID, packetB64 string) (string, error) {
	s.signCalls++
	return packetB64, nil
}

func (s *stubSigner) SignExit(
	_ context.Context, user uuid.UUID, c *domain.Contract, exit *domain.Exit,
) ([]byte, error) {
	if s.failExit != nil {
		return nil, s.failExit
	}

	key := s.keys[user]
	if s.wrongKey != nil {
		key = s.wrongKey
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

type stubArkade struct {
	stack    Stack
	balances map[uuid.UUID]int64
	scripts  map[uuid.UUID][]byte

	funded    *domain.Contract
	fundCalls int
	fundErr   error
	settled   *domain.Contract
	settleAt  [2]int64
	settleErr error
	topUps    map[uuid.UUID]int64
}

func newStubArkade() *stubArkade {
	arkd, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x23}, 32))
	emulator, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x24}, 32))

	return &stubArkade{
		stack: Stack{
			ArkdSigner:           arkd.PubKey(),
			EmulatorSigner:       emulator.PubKey(),
			ExitDelay:            arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 5},
			Dust:                 330,
			AllowsBlockTimelocks: true,
		},
		balances: map[uuid.UUID]int64{},
		scripts:  map[uuid.UUID][]byte{},
		topUps:   map[uuid.UUID]int64{},
	}
}

func (s *stubArkade) Stack() Stack { return s.stack }

func (s *stubArkade) Addresses(context.Context, uuid.UUID) (string, string, error) {
	return "ark1offchain", "bcrt1boarding", nil
}

func (s *stubArkade) Balance(_ context.Context, user uuid.UUID) (int64, error) {
	return s.balances[user], nil
}

func (s *stubArkade) VtxoPkScript(_ context.Context, user uuid.UUID) ([]byte, error) {
	script, ok := s.scripts[user]
	if !ok {
		return nil, fmt.Errorf("no script for %s", user)
	}
	return script, nil
}

func (s *stubArkade) TopUp(_ context.Context, user uuid.UUID, sats int64) error {
	s.topUps[user] += sats
	s.balances[user] += sats
	return nil
}

func (s *stubArkade) Fund(_ context.Context, c *domain.Contract) (domain.Outpoint, error) {
	if s.fundErr != nil {
		return domain.Outpoint{}, s.fundErr
	}
	s.fundCalls++
	s.funded = c
	return domain.Outpoint{
		Txid: "0000000000000000000000000000000000000000000000000000000000000001",
		Vout: 0,
	}, nil
}

func (s *stubArkade) Settle(_ context.Context, c *domain.Contract, short, long int64, _ Pair) error {
	if s.settleErr != nil {
		return s.settleErr
	}
	s.settled = c
	s.settleAt = [2]int64{short, long}
	return nil
}

// stubFeed is a real oracle key signing real messages, so a contract built
// against it can verify what comes back.
type stubFeed struct {
	key      *btcec.PrivateKey
	price    int64
	sequence uint64
	at       int64
	fail     error
	set      []int64

	// lies is what Latest reports while Pair keeps signing the real price, so
	// a test can check the split comes from the signed bytes and not from
	// whatever number arrived beside them.
	lies int64
}

func newStubFeed(price int64) *stubFeed {
	key, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x11}, 32))
	return &stubFeed{key: key, price: price, sequence: 10, at: fixedNow.Unix()}
}

func (s *stubFeed) PubKey(context.Context) ([]byte, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	return schnorrKey(s.key), nil
}

func (s *stubFeed) Latest(context.Context) (domain.Price, error) {
	if s.fail != nil {
		return domain.Price{}, s.fail
	}
	price := s.price
	if s.lies != 0 {
		price = s.lies
	}
	return domain.Price{Sequence: s.sequence, Timestamp: s.at, Price: price}, nil
}

func (s *stubFeed) Pair(context.Context) (Pair, error) {
	if s.fail != nil {
		return Pair{}, s.fail
	}
	return Pair{
		Settlement: s.sign(s.sequence, s.at, s.price),
		Previous:   s.sign(s.sequence-1, s.at-5, s.price),
	}, nil
}

func (s *stubFeed) sign(sequence uint64, at, price int64) arkade.SignedPrice {
	message := contract.OracleMessage(uint64(at), sequence, uint64(price))
	signature, err := contract.SignOracleMessage(s.key, message)
	if err != nil {
		panic(err)
	}
	return arkade.SignedPrice{Message: message, Signature: signature}
}

func (s *stubFeed) SetPrice(_ context.Context, price int64) error {
	if s.fail != nil {
		return s.fail
	}
	s.set = append(s.set, price)
	s.price = price
	s.sequence++
	return nil
}

func (s *stubFeed) History(_ context.Context, limit int) ([]domain.Price, error) {
	return []domain.Price{{Sequence: s.sequence, Timestamp: s.at, Price: s.price}}, nil
}

func schnorrKey(k *btcec.PrivateKey) []byte {
	serialized := k.PubKey().SerializeCompressed()
	return serialized[1:]
}

// fixedNow keeps every timestamp in the tests reproducible.
var fixedNow = time.Unix(1_800_000_000, 0)

// fixture is the whole app with stubs behind it, plus two users who have money.
type fixture struct {
	app       *App
	users     *stubUsers
	contracts *stubContracts
	exits     *stubExits
	signer    *stubSigner
	stack     *stubArkade
	feed      *stubFeed

	alice, bob uuid.UUID
	now        time.Time

	// contract is what accepted() left behind, so a test that only cares about
	// funding does not have to carry it around.
	contract *domain.Contract
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{now: fixedNow}
	clock := func() time.Time { return f.now }

	f.users = newStubUsers()
	f.contracts = newStubContracts(clock)
	f.exits = newStubExits()
	f.signer = newStubSigner()
	f.stack = newStubArkade()
	f.feed = newStubFeed(10_000_000)

	service, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x26}, 32))

	f.app = New(Options{
		Users:     f.users,
		Contracts: f.contracts,
		Exits:     f.exits,
		Signer:    f.signer,
		Stack:     f.stack,
		Feed:      f.feed,

		ServiceKey:  service.PubKey(),
		ExitFeeSats: 2_000,
	})
	f.app.now = clock

	f.alice = f.addUser(t, "alice", 0x21)
	f.bob = f.addUser(t, "bob", 0x22)
	return f
}

func (f *fixture) addUser(t *testing.T, name string, tag byte) uuid.UUID {
	t.Helper()

	key, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{tag}, 32))
	id := uuid.New()

	f.users.byID[id] = domain.User{ID: id, Name: name, PublicKey: key.PubKey().SerializeCompressed()}
	f.signer.add(id, key)
	f.stack.balances[id] = 50_000_000
	f.stack.scripts[id] = append([]byte{0x51, 0x20}, bytes.Repeat([]byte{tag}, 32)...)

	return id
}

// standard is the position the integration tests use: a $10,000 hedge against
// 0.2 BTC, liquidating at $50,000 and $200,000.
func (f *fixture) standard(creator uuid.UUID, side domain.Side) Proposal {
	return Proposal{
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
func (f *fixture) accepted(t *testing.T) *domain.Contract {
	t.Helper()

	c, err := f.app.Propose(t.Context(), f.standard(f.alice, domain.Short))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if c, err = f.app.Accept(t.Context(), c.ID, f.bob); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	f.contract = c
	return c
}

// funded runs a contract all the way to active.
func (f *fixture) funded(t *testing.T) *domain.Contract {
	t.Helper()

	c := f.accepted(t)
	if _, err := f.app.Fund(t.Context(), c.ID, f.alice); err != nil {
		t.Fatalf("Fund: %v", err)
	}

	NewWorker(f.app, WorkerOptions{}).Tick(t.Context())

	c, err := f.app.Contract(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if c.State != domain.Active {
		t.Fatalf("the contract is %s, want active", c.State)
	}
	return c
}
