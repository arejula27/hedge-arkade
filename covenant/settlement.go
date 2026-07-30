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
}

func (t Terms) validate() error {
	switch {
	case t.NominalUnitsXSatsPerBtc <= 0:
		return fmt.Errorf("nominal units must be positive, got %d", t.NominalUnitsXSatsPerBtc)
	case t.PayoutSats <= 0:
		return fmt.Errorf("payout must be positive, got %d sats", t.PayoutSats)
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
	}
	return nil
}

// SettlementScript emits the Arkade Script for AnyHedge's payout path.
//
// Witness: the oracle price, as the only stack item.
//
//	clampedPrice = max(min(price, high), low)
//	shortSats    = max(DUST, nominalUnits/clampedPrice - satsAtHighLiquidation)
//	longSats     = max(DUST, payoutSats - shortSats)
//
// Both outputs are then checked exactly — value and recipient — with the
// transaction pinned to one input and two outputs.
//
// Not yet built: oracle signature verification and the sequence-adjacency check
// that pins which oracle message may be used. Until those land the price on the
// witness is unauthenticated, so this is not deployable.
func (t Terms) SettlementScript() ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}

	nominal, err := bigNumBytes(t.NominalUnitsXSatsPerBtc)
	if err != nil {
		return nil, err
	}
	leverage, err := bigNumBytes(t.SatsForNominalUnitsAtHighLiquidation)
	if err != nil {
		return nil, err
	}
	payout, err := bigNumBytes(t.PayoutSats)
	if err != nil {
		return nil, err
	}
	low, err := bigNumBytes(t.LowLiquidationPrice)
	if err != nil {
		return nil, err
	}
	high, err := bigNumBytes(t.HighLiquidationPrice)
	if err != nil {
		return nil, err
	}
	dust, err := bigNumBytes(Dust)
	if err != nil {
		return nil, err
	}

	b := txscript.NewScriptBuilder()

	// Exactly one input and two outputs. A second input would let a spender
	// bring along value the covenant never accounted for.
	b.AddOp(arkade.OP_INSPECTNUMINPUTS)
	b.AddOp(arkade.OP_1)
	b.AddOp(arkade.OP_NUMEQUALVERIFY)
	b.AddOp(arkade.OP_INSPECTNUMOUTPUTS)
	b.AddOp(arkade.OP_2)
	b.AddOp(arkade.OP_NUMEQUALVERIFY)

	// Recipients, before any arithmetic: getting the amounts right is pointless
	// if they can be sent anywhere.
	if err := addLockScriptCheck(b, arkade.OP_0, t.ShortLockScript); err != nil {
		return nil, fmt.Errorf("short lock script: %w", err)
	}
	if err := addLockScriptCheck(b, arkade.OP_1, t.LongLockScript); err != nil {
		return nil, fmt.Errorf("long lock script: %w", err)
	}

	// clampedPrice = max(min(price, high), low)
	b.AddData(high)
	b.AddOp(arkade.OP_MIN)
	b.AddData(low)
	b.AddOp(arkade.OP_MAX) // clampedPrice

	// shortSats = max(DUST, nominal/clampedPrice - leverage)
	//
	// The division runs on the VM, and so must the multiplication that produces
	// `nominal`: it overflows int64 for a large position, and BigNum is the only
	// arithmetic here without a ceiling.
	b.AddData(nominal)
	b.AddOp(arkade.OP_SWAP)
	b.AddOp(arkade.OP_DIV)
	b.AddData(leverage)
	b.AddOp(arkade.OP_SUB)
	b.AddData(dust)
	b.AddOp(arkade.OP_MAX) // shortSats

	// longSats = max(DUST, payoutSats - shortSats)
	b.AddOp(arkade.OP_DUP)
	b.AddData(payout)
	b.AddOp(arkade.OP_SWAP)
	b.AddOp(arkade.OP_SUB)
	b.AddData(dust)
	b.AddOp(arkade.OP_MAX) // shortSats longSats

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
