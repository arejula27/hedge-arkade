package covenant

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/wire"
)

// Arbitration is how the money leaves the 2-of-3 after a unilateral exit.
//
// The exit drops the covenant — that is inherent to exiting — so something has
// to decide the split. The service does, and it does so with no discretion: it
// takes an oracle-signed price, applies the contract's own formula, and builds
// the transaction. The parties only sign. Either of them plus the service is
// two of three, so neither can be held hostage by the other.
//
// The oracle's message and signature travel with the proposal, so a party can
// check the split against the price the whole world saw before signing, and
// anyone can audit it afterwards. A service that signed a different number
// would be caught by arithmetic, not trusted not to.
//
// This is a separate mechanism from the covenant, not a continuation of it.
// There is no script and no VM in this path — the 2-of-3 is plain Bitcoin — and
// there is no timing gate either: the covenant only settles at maturity or at a
// liquidation boundary, whereas an exit can happen at any moment, so the
// arbitration settles at whatever the price is when it runs. Exiting trades the
// covenant's guarantees for these.
type Arbitration struct {
	Tx *wire.MsgTx

	// ShortSats and LongSats are what the transaction pays, after fees.
	ShortSats int64
	LongSats  int64

	// Price is the clamped price the split came from, and Message/Signature are
	// the oracle's own bytes for it — the evidence.
	Price     int64
	Message   []byte
	Signature []byte
}

// SettlementSplit is the payout rule the arbitration applies: clamp the price
// to the liquidation boundaries, divide, floor each side at dust.
//
// It is ordinary Go arithmetic on the contract's parameters, and it belongs to
// the arbitration rather than to the covenant. Nothing here is executed by a
// VM, and nothing here can make the covenant pay differently — by the time this
// runs the covenant is behind us.
func SettlementSplit(t Terms, price int64) (short, long int64, err error) {
	if err := t.validate(); err != nil {
		return 0, 0, err
	}
	if price <= 0 {
		return 0, 0, fmt.Errorf("price must be positive, got %d", price)
	}

	// clampedPrice = max(min(price, high), low)
	clamped := min(max(price, t.LowLiquidationPrice), t.HighLiquidationPrice)

	// shortSats = min(payoutSats - DUST, max(DUST, nominal/clamped - leverage))
	short = t.NominalUnitsXSatsPerBtc/clamped - t.SatsForNominalUnitsAtHighLiquidation
	short = max(short, Dust)
	short = min(short, t.PayoutSats-Dust)

	return short, t.PayoutSats - short, nil
}

// PriceFrom reads the price out of a signed oracle message, after checking the
// signature against the contract's own oracle key.
//
// Checking the signature is what makes arbitration mechanical: the service can
// only propose a split it can produce evidence for.
func (c Contract) PriceFrom(message, signature []byte) (int64, error) {
	if len(message) != OracleMessageLength {
		return 0, fmt.Errorf("oracle message is %d bytes, want %d",
			len(message), OracleMessageLength)
	}

	key, err := schnorr.ParsePubKey(c.Terms.OraclePubKey)
	if err != nil {
		return 0, fmt.Errorf("parsing the contract's oracle key: %w", err)
	}
	sig, err := schnorr.ParseSignature(signature)
	if err != nil {
		return 0, fmt.Errorf("parsing the oracle signature: %w", err)
	}

	digest := sha256.Sum256(message)
	if !sig.Verify(digest[:], key) {
		return 0, fmt.Errorf("the oracle did not sign this price")
	}

	price := binary.LittleEndian.Uint64(message[oraclePriceOffset:])
	if price == 0 || price > 1<<62 {
		return 0, fmt.Errorf("oracle price %d is out of range", price)
	}
	return int64(price), nil
}

// Arbitrate builds the transaction that empties the 2-of-3 after an exit.
//
// available is what the sweep output actually holds — already short of
// PayoutSats by whatever the exit cost — and feeSats is what this transaction
// leaves for the miner. The shortfall is taken from both sides in proportion to
// what each was owed, so neither is charged for the other's decision to exit.
func (c Contract) Arbitrate(
	sweep *Sweep, outpoint wire.OutPoint, available, feeSats int64,
	message, signature []byte,
) (*Arbitration, error) {
	if sweep == nil {
		return nil, fmt.Errorf("no sweep to spend")
	}
	if feeSats < 0 {
		return nil, fmt.Errorf("fee cannot be negative, got %d", feeSats)
	}

	price, err := c.PriceFrom(message, signature)
	if err != nil {
		return nil, err
	}

	short, long, err := SettlementSplit(c.Terms, price)
	if err != nil {
		return nil, err
	}

	payable := available - feeSats
	shortFinal, longFinal, err := scaleToAvailable(short, long, c.Terms.PayoutSats, payable)
	if err != nil {
		return nil, err
	}

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: outpoint, Sequence: wire.MaxTxInSequenceNum})
	tx.AddTxOut(&wire.TxOut{Value: shortFinal, PkScript: c.Terms.ShortLockScript})
	tx.AddTxOut(&wire.TxOut{Value: longFinal, PkScript: c.Terms.LongLockScript})

	clamped := min(max(price, c.Terms.LowLiquidationPrice), c.Terms.HighLiquidationPrice)

	return &Arbitration{
		Tx: tx, ShortSats: shortFinal, LongSats: longFinal,
		Price: clamped, Message: message, Signature: signature,
	}, nil
}

// scaleToAvailable shares out what is actually left in proportion to what each
// side was owed. The rounding remainder goes to the long, deterministically, so
// two people computing this get the same answer to the sat.
func scaleToAvailable(short, long, owed, payable int64) (int64, int64, error) {
	if payable < 2*Dust {
		return 0, 0, fmt.Errorf(
			"%d sats left after fees cannot cover a dust output on each side (%d)",
			payable, 2*Dust)
	}

	scaledShort := short * payable / owed
	scaledShort = max(scaledShort, Dust)
	scaledShort = min(scaledShort, payable-Dust)

	return scaledShort, payable - scaledShort, nil
}

// VerifyArbitration is what a party runs before signing: recompute the whole proposal from
// the oracle's own bytes and require the transaction to match to the sat.
//
// The point of the 2-of-3 is that the service cannot move the money alone. That
// is only worth anything if the party's signature means something, and it only
// means something if the party checks first.
func (c Contract) VerifyArbitration(a *Arbitration, sweep *Sweep, available, feeSats int64) error {
	if a == nil || a.Tx == nil {
		return fmt.Errorf("no arbitration to verify")
	}

	expected, err := c.Arbitrate(sweep, a.Tx.TxIn[0].PreviousOutPoint,
		available, feeSats, a.Message, a.Signature)
	if err != nil {
		return fmt.Errorf("recomputing the arbitration: %w", err)
	}

	if len(a.Tx.TxOut) != 2 {
		return fmt.Errorf("the arbitration pays %d outputs, want 2", len(a.Tx.TxOut))
	}
	for i, want := range expected.Tx.TxOut {
		got := a.Tx.TxOut[i]
		if got.Value != want.Value {
			return fmt.Errorf("output %d pays %d sats, the price says %d",
				i, got.Value, want.Value)
		}
		if string(got.PkScript) != string(want.PkScript) {
			return fmt.Errorf("output %d pays the wrong recipient", i)
		}
	}
	if len(a.Tx.TxIn) != 1 {
		return fmt.Errorf("the arbitration spends %d inputs, want 1", len(a.Tx.TxIn))
	}

	return nil
}

// SignArbitration signs the proposal as one of the three keys on the sweep.
func (c Contract) SignArbitration(
	key *btcec.PrivateKey, a *Arbitration, sweep *Sweep, available int64,
) ([]byte, error) {
	if key == nil {
		return nil, fmt.Errorf("signing key is nil")
	}
	if a == nil || a.Tx == nil {
		return nil, fmt.Errorf("no arbitration to sign")
	}

	digest, err := sweep.sighash(a.Tx, available)
	if err != nil {
		return nil, err
	}

	sig, err := schnorr.Sign(key, digest)
	if err != nil {
		return nil, fmt.Errorf("signing the arbitration: %w", err)
	}
	return sig.Serialize(), nil
}

// FinalizeArbitration attaches the witness, given signatures keyed by x-only
// pubkey. Exactly two are used; see Sweep.Witness.
func FinalizeArbitration(
	a *Arbitration, sweep *Sweep, sigs map[string][]byte,
) (*wire.MsgTx, error) {
	if a == nil || a.Tx == nil {
		return nil, fmt.Errorf("no arbitration to finalize")
	}

	witness, err := sweep.Witness(sigs)
	if err != nil {
		return nil, err
	}

	signed := a.Tx.Copy()
	signed.TxIn[0].Witness = witness
	return signed, nil
}
