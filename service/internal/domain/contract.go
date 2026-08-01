// Package domain is the service's own vocabulary: what a contract is, who the
// parties are, and what may follow what.
//
// It has no I/O. The payout formula is not here either — it lives in
// `contract`, and this package calls it rather than restating it.
package domain

import (
	"fmt"
	"time"

	"github.com/arejula27/hedge/contract"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
)

// Side is which half of a contract someone is on.
type Side string

const (
	// Short locks in a USD-denominated value.
	Short Side = "short"
	// Long posts the extra collateral and takes the opposite side.
	Long Side = "long"
)

func (s Side) Valid() bool { return s == Short || s == Long }

func (s Side) Other() Side {
	if s == Short {
		return Long
	}
	return Short
}

func (s Side) String() string { return string(s) }

// Outpoint is where a VTXO or a transaction output lives.
type Outpoint struct {
	Txid string
	Vout uint32
}

func (o Outpoint) String() string { return fmt.Sprintf("%s:%d", o.Txid, o.Vout) }

// Contract is one hedge position as the service tracks it.
//
// Terms and Keys are `contract`'s own types rather than copies: the taproot
// address is a pure function of them, so anything that re-declared them could
// drift from the address a party actually funded.
type Contract struct {
	ID    uuid.UUID
	State State

	// Creator is the side whoever proposed the contract took. The other side
	// is empty until someone accepts.
	Creator   Side
	ShortUser *uuid.UUID
	LongUser  *uuid.UUID

	Terms contract.Terms

	// The four keys the tree is built from. The operator's and the emulator's
	// are stored rather than re-read: they are baked into the address, and an
	// operator that rotates its key must not silently change where a funded
	// contract lives.
	ShortKey       []byte
	LongKey        []byte
	ArkdSigner     []byte
	EmulatorSigner []byte

	ExitDelay              arklib.RelativeLocktime
	EnableMutualRedemption bool

	// PkScript is derivable from everything above. It is stored so a mismatch
	// between the row and the address is detectable rather than assumed away.
	PkScript []byte

	// What each side puts in, decided at the opening price when the contract
	// is accepted. They sum to Terms.PayoutSats.
	ShortStake int64
	LongStake  int64

	// Funding is the contract VTXO, once there is one.
	Funding *Outpoint

	// UpdatedAt is when the contract last moved. The worker reads it to tell a
	// step that is slow from one that will never finish.
	UpdatedAt time.Time
}

// Covenant builds the contract `contract` understands from the row.
//
// This is the one place a stored contract becomes the thing whose address is
// derived, so a row that has drifted from the address it was funded at fails
// here rather than somewhere further along.
func (c *Contract) Covenant() (contract.Contract, error) {
	keys := contract.Keys{}
	for _, k := range []struct {
		name string
		raw  []byte
		into **btcec.PublicKey
	}{
		{"short", c.ShortKey, &keys.Short},
		{"long", c.LongKey, &keys.Long},
		{"operator", c.ArkdSigner, &keys.ArkdSigner},
		{"emulator", c.EmulatorSigner, &keys.EmulatorSigner},
	} {
		if len(k.raw) == 0 {
			return contract.Contract{}, fmt.Errorf("the %s key is not set yet", k.name)
		}
		parsed, err := btcec.ParsePubKey(k.raw)
		if err != nil {
			return contract.Contract{}, fmt.Errorf("the %s key: %w", k.name, err)
		}
		*k.into = parsed
	}

	return contract.Contract{
		Terms:                  c.Terms,
		Keys:                   keys,
		ExitDelay:              c.ExitDelay,
		EnableMutualRedemption: c.EnableMutualRedemption,
	}, nil
}

// Address renders the contract's taproot output key as an address on the given
// network. It is what a party checks before funding.
func (c *Contract) Address(params *chaincfg.Params) (string, error) {
	covenant, err := c.Covenant()
	if err != nil {
		return "", err
	}
	key, err := covenant.TaprootKey()
	if err != nil {
		return "", err
	}
	addr, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(key), params)
	if err != nil {
		return "", fmt.Errorf("rendering the contract address: %w", err)
	}
	return addr.EncodeAddress(), nil
}

// UserOn is whoever holds the given side, if anyone does yet.
func (c *Contract) UserOn(side Side) *uuid.UUID {
	if side == Short {
		return c.ShortUser
	}
	return c.LongUser
}

// SideOf says which side a user is on, and whether they are in this contract at
// all.
func (c *Contract) SideOf(user uuid.UUID) (Side, bool) {
	if c.ShortUser != nil && *c.ShortUser == user {
		return Short, true
	}
	if c.LongUser != nil && *c.LongUser == user {
		return Long, true
	}
	return "", false
}

// Open says whether the contract is still looking for a counterparty.
func (c *Contract) Open() bool {
	return c.State == Proposed && (c.ShortUser == nil || c.LongUser == nil)
}

// Split is what each side is owed at a price, straight from the covenant's own
// formula. The service never computes a payout any other way.
func (c *Contract) Split(price int64) (short, long int64, err error) {
	return contract.SettlementSplit(c.Terms, price)
}

// Liquidated says whether a price is at or beyond a boundary. The covenant
// clamps to the boundary and then settles, so touching it is the event.
func (c *Contract) Liquidated(price int64) bool {
	return price <= c.Terms.LowLiquidationPrice || price >= c.Terms.HighLiquidationPrice
}

// Matured says whether a timestamp is at or past maturity.
func (c *Contract) Matured(timestamp int64) bool {
	return timestamp >= c.Terms.MaturityTimestamp
}

// Settleable is the covenant's own trigger: out of bounds, or matured.
func (c *Contract) Settleable(price, timestamp int64) bool {
	return c.Liquidated(price) || c.Matured(timestamp)
}

// Event is one line of a contract's history, and what the UI's timeline is
// built from.
type Event struct {
	ContractID uuid.UUID
	From       State
	To         State
	Detail     string
}

// ContractFilter is what the lists the UI asks for come down to: what is on
// offer, and what is mine.
type ContractFilter struct {
	// State restricts to one state. Empty means any.
	State State
	// User restricts to contracts that user is a party to. Nil means anyone's.
	User *uuid.UUID
	// Open restricts to contracts still looking for a counterparty.
	Open bool
}
