package covenant

import (
	"crypto/sha256"
	"fmt"

	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/txscript"
)

// Dust is the floor each payout is raised to. AnyHedge pays max(DUST, …) and
// always emits exactly two outputs, rather than dropping a small one; a
// fixed-shape transaction is easier to introspect and leaves no value stranded
// as fee.
//
// 1332 is BCH's value. Bitcoin's relay rules differ and this needs setting for
// the output types we actually pay to.
const Dust = 1332

// Terms are the contract parameters baked into the script at funding time,
// mirroring AnyHedge v0.12's constructor.
type Terms struct {
	// NominalUnitsXSatsPerBtc is the nominal hedge value in units, scaled by
	// 1e8. It is the numerator of the payout division.
	NominalUnitsXSatsPerBtc int64

	// SatsForNominalUnitsAtHighLiquidation is the leverage term, subtracted
	// from the short's raw payout. Zero means a pure 1x hedge.
	SatsForNominalUnitsAtHighLiquidation int64

	// PayoutSats is the total the contract pays out, miner fee excluded.
	PayoutSats int64

	// LowLiquidationPrice and HighLiquidationPrice clamp the oracle price.
	//
	// The clamp is not a safety rail, it is load-bearing: because every price
	// beyond a boundary settles identically, it does not matter which
	// out-of-bounds oracle message a spender picks. That is half of why this
	// contract needs no clock.
	LowLiquidationPrice  int64
	HighLiquidationPrice int64

	// ShortLockScript and LongLockScript are where each side is paid, as full
	// scriptPubKeys. Checking payout amounts without checking these would leave
	// the amounts correct and the recipients up to whoever spends.
	ShortLockScript []byte
	LongLockScript  []byte

	// OraclePubKey is the 32-byte x-only key of the price feed. A different feed
	// means a different key, as in AnyHedge — there is no ticker field.
	OraclePubKey []byte

	// StartTimestamp is the earliest timestamp a liquidation may be redeemed at.
	// MaturityTimestamp is when the contract settles normally.
	StartTimestamp    int64
	MaturityTimestamp int64
}

func (t Terms) validate() error {
	switch {
	case t.NominalUnitsXSatsPerBtc <= 0:
		return fmt.Errorf("nominal units must be positive, got %d", t.NominalUnitsXSatsPerBtc)
	case t.PayoutSats <= 0:
		return fmt.Errorf("payout must be positive, got %d sats", t.PayoutSats)
	// Both sides are floored at Dust and the short is capped at PayoutSats-Dust.
	// Below 2*Dust the cap falls under the floor and no split satisfies both.
	case t.PayoutSats < 2*Dust:
		return fmt.Errorf("payout %d sats cannot cover a dust output on each side (%d)",
			t.PayoutSats, 2*Dust)
	case t.LowLiquidationPrice <= 0:
		return fmt.Errorf("low liquidation price must be positive, got %d", t.LowLiquidationPrice)
	case t.HighLiquidationPrice <= t.LowLiquidationPrice:
		return fmt.Errorf("high liquidation price %d is not above the low one %d",
			t.HighLiquidationPrice, t.LowLiquidationPrice)
	case t.SatsForNominalUnitsAtHighLiquidation < 0:
		return fmt.Errorf("leverage term is negative: %d", t.SatsForNominalUnitsAtHighLiquidation)
	case len(t.ShortLockScript) == 0:
		return fmt.Errorf("short lock script is empty")
	case len(t.LongLockScript) == 0:
		return fmt.Errorf("long lock script is empty")
	case len(t.OraclePubKey) != 32:
		return fmt.Errorf("oracle key must be 32 bytes x-only, got %d", len(t.OraclePubKey))
	case t.StartTimestamp <= 0:
		return fmt.Errorf("start timestamp must be positive, got %d", t.StartTimestamp)
	case t.MaturityTimestamp <= t.StartTimestamp:
		return fmt.Errorf("maturity %d is not after the start timestamp %d",
			t.MaturityTimestamp, t.StartTimestamp)
	}
	return nil
}

// SettlementScript emits the Arkade Script for AnyHedge's payout path.
//
// Witness, bottom to top: settlementSignature, settlementMessage,
// previousSignature, previousMessage.
//
// The spender must supply the settlement message *and its immediate
// predecessor*. That is what replaces a clock: if the predecessor is before
// maturity and the settlement message is the very next one in the oracle's
// sequence, the settlement message is the first one published on or after
// maturity, and there is exactly one of those. For a liquidation the price is
// clamped to the boundary it crossed, so every qualifying message pays the same
// and there is nothing to shop for either.
//
//	clampedPrice = max(min(price, high), low)
//	shortSats    = max(DUST, nominalUnits/clampedPrice - satsAtHighLiquidation)
//	longSats     = max(DUST, payoutSats - shortSats)
func (t Terms) SettlementScript() ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}

	consts := map[string][]byte{}
	for name, v := range map[string]int64{
		"nominal":  t.NominalUnitsXSatsPerBtc,
		"leverage": t.SatsForNominalUnitsAtHighLiquidation,
		"payout":   t.PayoutSats,
		"low":      t.LowLiquidationPrice,
		"high":     t.HighLiquidationPrice,
		"dust":     Dust,
		"shortCap": t.PayoutSats - Dust,
		"start":    t.StartTimestamp,
		"maturity": t.MaturityTimestamp,
	} {
		encoded, err := bigNumBytes(v)
		if err != nil {
			return nil, fmt.Errorf("encoding %s: %w", name, err)
		}
		consts[name] = encoded
	}

	b := txscript.NewScriptBuilder()

	// --- Transaction shape ---------------------------------------------------
	//
	// Exactly one input. A second would let a spender bring along value the
	// covenant never accounted for.
	//
	// The output count is deliberately *not* checked. AnyHedge requires exactly
	// two, which on BCH forces any surplus into the miner fee. An Arkade
	// transaction cannot satisfy that: it carries the emulator packet as an
	// extension OP_RETURN and a P2A anchor besides the two payouts.
	//
	// Arithmetic replaces the check, and is stronger. The input is pinned to
	// payoutSats below and the two payouts sum to exactly payoutSats, so every
	// remaining output must be worth zero — no transaction can pay out more
	// than it takes in. That holds by conservation of value, without relying on
	// arkd to reject the transaction.
	b.AddOp(arkade.OP_INSPECTNUMINPUTS)
	b.AddOp(arkade.OP_1)
	b.AddOp(arkade.OP_NUMEQUALVERIFY)

	// --- The VTXO holds exactly what the contract was funded with ------------
	//
	// Without this the covenant would settle a VTXO of any size, and the
	// difference between it and payoutSats would be value the transaction has
	// to place somewhere the covenant does not constrain. arkd would reject the
	// result for not balancing; failing here says why.
	b.AddOp(arkade.OP_PUSHCURRENTINPUTINDEX)
	b.AddOp(arkade.OP_INSPECTINPUTVALUE)
	b.AddData(consts["payout"])
	b.AddOp(arkade.OP_NUMEQUALVERIFY)

	// --- Recipients ----------------------------------------------------------
	//
	// Before any arithmetic: getting the amounts right is pointless if they can
	// be sent anywhere.
	if err := addLockScriptCheck(b, arkade.OP_0, t.ShortLockScript); err != nil {
		return nil, fmt.Errorf("short lock script: %w", err)
	}
	if err := addLockScriptCheck(b, arkade.OP_1, t.LongLockScript); err != nil {
		return nil, fmt.Errorf("long lock script: %w", err)
	}

	// --- Authenticate both oracle messages -----------------------------------
	//
	// CHECKSIGFROMSTACK verifies a signature over stack data and deliberately
	// does not hash for the caller, so the witness carries the raw message and
	// the script hashes it. Each message is stashed on the alt stack before its
	// digest is consumed.
	//
	// stack: settleSig settleMsg prevSig prevMsg
	b.AddOp(arkade.OP_DUP)
	b.AddOp(arkade.OP_TOALTSTACK)
	b.AddOp(arkade.OP_SHA256)
	b.AddData(t.OraclePubKey)
	b.AddOp(arkade.OP_CHECKSIGFROMSTACK)
	b.AddOp(arkade.OP_VERIFY)
	// stack: settleSig settleMsg
	b.AddOp(arkade.OP_DUP)
	b.AddOp(arkade.OP_TOALTSTACK)
	b.AddOp(arkade.OP_SHA256)
	b.AddData(t.OraclePubKey)
	b.AddOp(arkade.OP_CHECKSIGFROMSTACK)
	b.AddOp(arkade.OP_VERIFY)
	// stack: empty     alt: prevMsg settleMsg
	b.AddOp(arkade.OP_FROMALTSTACK) // settleMsg
	b.AddOp(arkade.OP_FROMALTSTACK) // settleMsg prevMsg

	// --- The predecessor is before maturity ----------------------------------
	b.AddOp(arkade.OP_DUP)
	addFieldExtract(b, oracleTimestampOffset)
	b.AddData(consts["maturity"])
	b.AddOp(arkade.OP_LESSTHAN)
	b.AddOp(arkade.OP_VERIFY)

	// --- The two messages are adjacent in the oracle's sequence --------------
	//
	// A negative content sequence is metadata rather than a price, which is why
	// AnyHedge rejects it explicitly.
	addFieldExtract(b, oracleSequenceOffset) // settleMsg prevSeq  (consumes prevMsg)
	b.AddOp(arkade.OP_DUP)
	b.AddOp(arkade.OP_0)
	b.AddOp(arkade.OP_GREATERTHAN)
	b.AddOp(arkade.OP_VERIFY)
	b.AddOp(arkade.OP_1)
	b.AddOp(arkade.OP_ADD) // settleMsg prevSeq+1
	b.AddOp(arkade.OP_OVER)
	addFieldExtract(b, oracleSequenceOffset) // settleMsg prevSeq+1 settleSeq
	b.AddOp(arkade.OP_NUMEQUALVERIFY)        // settleMsg

	// --- The settlement message is not before the earliest liquidation -------
	b.AddOp(arkade.OP_DUP)
	addFieldExtract(b, oracleTimestampOffset) // settleMsg settleTs
	b.AddOp(arkade.OP_DUP)
	b.AddData(consts["start"])
	b.AddOp(arkade.OP_GREATERTHANOREQUAL)
	b.AddOp(arkade.OP_VERIFY) // settleMsg settleTs

	// --- Price, rejected if not positive, then clamped -----------------------
	b.AddOp(arkade.OP_SWAP)               // settleTs settleMsg
	addFieldExtract(b, oraclePriceOffset) // settleTs price
	b.AddOp(arkade.OP_DUP)
	b.AddOp(arkade.OP_0)
	b.AddOp(arkade.OP_GREATERTHAN)
	b.AddOp(arkade.OP_VERIFY)
	b.AddData(consts["high"])
	b.AddOp(arkade.OP_MIN)
	b.AddData(consts["low"])
	b.AddOp(arkade.OP_MAX) // settleTs clampedPrice

	// --- Either matured, or the price crossed a boundary ---------------------
	//
	// The clamp keeps the price inside [low, high], so "out of bounds" is the
	// price having landed exactly on a boundary.
	b.AddOp(arkade.OP_DUP)
	b.AddOp(arkade.OP_TOALTSTACK) // alt: clampedPrice
	b.AddOp(arkade.OP_DUP)
	b.AddData(consts["low"])
	b.AddOp(arkade.OP_NUMEQUAL)
	b.AddOp(arkade.OP_SWAP)
	b.AddData(consts["high"])
	b.AddOp(arkade.OP_NUMEQUAL)
	b.AddOp(arkade.OP_BOOLOR) // settleTs outOfBounds
	b.AddOp(arkade.OP_SWAP)
	b.AddData(consts["maturity"])
	b.AddOp(arkade.OP_GREATERTHANOREQUAL) // outOfBounds matured
	b.AddOp(arkade.OP_BOOLOR)
	b.AddOp(arkade.OP_VERIFY)
	b.AddOp(arkade.OP_FROMALTSTACK) // clampedPrice

	// --- Payouts -------------------------------------------------------------
	//
	// shortSats = min(payoutSats - DUST, max(DUST, nominal/clampedPrice - leverage))
	// longSats  = payoutSats - shortSats
	//
	// AnyHedge floors both sides at DUST independently, which lets the two
	// payouts sum to more than payoutSats. Arkade cannot express that: arkd
	// rebuilds every offchain transaction with offchain.BuildTxs, which refuses
	// an input amount that differs from the output amount, and compares txids.
	// Capping the short at payoutSats - DUST leaves the long its dust output
	// while keeping the sum exactly payoutSats, so the settlement always
	// balances and no value is left over for a spender to claim.
	//
	// The division runs on the VM, and so must the multiplication that produces
	// `nominal`: it overflows int64 for a large position, and BigNum is the only
	// arithmetic here without a ceiling.
	b.AddData(consts["nominal"])
	b.AddOp(arkade.OP_SWAP)
	b.AddOp(arkade.OP_DIV)
	b.AddData(consts["leverage"])
	b.AddOp(arkade.OP_SUB)
	b.AddData(consts["dust"])
	b.AddOp(arkade.OP_MAX)
	b.AddData(consts["shortCap"])
	b.AddOp(arkade.OP_MIN) // shortSats

	// longSats is the remainder, never computed independently: two derivations
	// can disagree by a truncated sat and leave the contract unspendable.
	b.AddOp(arkade.OP_DUP)
	b.AddData(consts["payout"])
	b.AddOp(arkade.OP_SWAP)
	b.AddOp(arkade.OP_SUB) // shortSats longSats

	// Output 1 pays the long, exactly.
	b.AddOp(arkade.OP_1)
	b.AddOp(arkade.OP_INSPECTOUTPUTVALUE)
	b.AddOp(arkade.OP_NUMEQUALVERIFY) // shortSats

	// Output 0 pays the short, exactly.
	b.AddOp(arkade.OP_0)
	b.AddOp(arkade.OP_INSPECTOUTPUTVALUE)
	b.AddOp(arkade.OP_NUMEQUAL)

	return b.Script()
}

// addFieldExtract replaces the oracle message on top of the stack with one of
// its little-endian fields as a number.
func addFieldExtract(b *txscript.ScriptBuilder, offset int) {
	b.AddInt64(int64(offset))
	b.AddInt64(oracleFieldSize)
	b.AddOp(arkade.OP_SUBSTR)
	b.AddOp(arkade.OP_BIN2NUM)
}

// addLockScriptCheck emits a comparison against the scriptPubKey of one output.
//
// OP_INSPECTOUTPUTSCRIPTPUBKEY does not push the raw script. For a witness
// program it pushes the program and then the witness version; for anything else
// it pushes sha256(script) and then -1. Both shapes are handled so a contract
// can pay out to any valid output, as AnyHedge does.
func addLockScriptCheck(b *txscript.ScriptBuilder, indexOp byte, lockScript []byte) error {
	digest, version, err := lockScriptCommitment(lockScript)
	if err != nil {
		return err
	}
	encodedVersion, err := bigNumBytes(version)
	if err != nil {
		return err
	}

	b.AddOp(indexOp)
	b.AddOp(arkade.OP_INSPECTOUTPUTSCRIPTPUBKEY) // digest version
	b.AddData(encodedVersion)
	b.AddOp(arkade.OP_NUMEQUALVERIFY) // digest
	b.AddData(digest)
	b.AddOp(arkade.OP_EQUALVERIFY)

	return nil
}

// lockScriptCommitment returns what the VM will push for a given scriptPubKey:
// the witness program and its version, or sha256(script) and -1.
func lockScriptCommitment(lockScript []byte) ([]byte, int64, error) {
	if !txscript.IsWitnessProgram(lockScript) {
		hash := sha256.Sum256(lockScript)
		return hash[:], -1, nil
	}

	version, program, err := txscript.ExtractWitnessProgramInfo(lockScript)
	if err != nil {
		return nil, 0, fmt.Errorf("extracting witness program: %w", err)
	}
	return program, int64(version), nil
}

// bigNumBytes encodes an integer the way the VM's BigNum stack expects it.
func bigNumBytes(v int64) ([]byte, error) {
	return arkade.BigNumFromInt64(v).Bytes()
}
