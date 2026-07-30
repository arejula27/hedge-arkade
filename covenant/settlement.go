package covenant

import (
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
// Both output values are then checked exactly, not as a lower bound.
//
// Not yet built: oracle signature verification, the sequence-adjacency check
// that pins which message may be used, and the output lock scripts. Values
// without lock scripts means the amounts are right and the recipients are
// whoever the spender likes — this script is not safe to deploy as it stands.
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

	// clampedPrice = max(min(price, high), low)
	b.AddData(high)
	b.AddOp(arkade.OP_MIN)
	b.AddData(low)
	b.AddOp(arkade.OP_MAX) // clampedPrice

	// shortSats = max(DUST, nominal/clampedPrice - leverage)
	//
	// The division runs on the VM. Folding nominal/price into a build-time
	// constant is impossible anyway, but note the multiplication that produces
	// `nominal` must not be done in Go either: it overflows int64 for a large
	// position, and BigNum is the only arithmetic here without a ceiling.
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

// bigNumBytes encodes an integer the way the VM's BigNum stack expects it.
func bigNumBytes(v int64) ([]byte, error) {
	return arkade.BigNumFromInt64(v).Bytes()
}
