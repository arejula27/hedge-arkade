package covenant

import (
	"fmt"

	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/txscript"
)

// SatsPerBtc is the scale that converts a USD-cent price per BTC into sats.
const SatsPerBtc = 100_000_000

// DustLimit is the payout below which no output is created. A payout of exactly
// DustLimit is dropped: stability_vault.ark:294 tests `> 330`.
const DustLimit = 330

// Terms are the contract parameters baked into the script at funding time. They
// are constants inside the covenant, which is what makes the taproot address
// commit to them.
type Terms struct {
	// HedgeValueCents is the USD value the hedge side locks in, in cents.
	HedgeValueCents int64
	// TotalCollateral is the sum of both contributions, in sats.
	TotalCollateral int64
}

func (t Terms) validate() error {
	if t.HedgeValueCents < 0 {
		return fmt.Errorf("hedge value is negative: %d cents", t.HedgeValueCents)
	}
	if t.TotalCollateral <= 0 {
		return fmt.Errorf("total collateral must be positive, got %d sats", t.TotalCollateral)
	}
	return nil
}

// SettlementScript emits the Arkade Script that settles the contract at an
// oracle-signed price.
//
// Witness: the oracle price in USD cents per BTC, as the only stack item.
//
// The script computes the hedge payout, clamps it to the collateral, and checks
// both outputs cover what each side is owed. It never computes the long's share
// independently — that share is the remainder, so the two cannot disagree by a
// truncated sat and leave the transaction unspendable.
func (t Terms) SettlementScript() ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}

	hedgeValue, err := bigNumBytes(t.HedgeValueCents)
	if err != nil {
		return nil, err
	}
	scale, err := bigNumBytes(SatsPerBtc)
	if err != nil {
		return nil, err
	}
	collateral, err := bigNumBytes(t.TotalCollateral)
	if err != nil {
		return nil, err
	}
	dust, err := bigNumBytes(DustLimit)
	if err != nil {
		return nil, err
	}

	b := txscript.NewScriptBuilder()

	// stack: price
	//
	// The multiplication runs on the VM rather than being folded into a
	// constant here: hedgeValueCents * 1e8 overflows int64 for a large enough
	// position, and BigNum is the only arithmetic in this system without a
	// ceiling.
	b.AddData(hedgeValue)                 // price hedgeValue
	b.AddData(scale)                      // price hedgeValue 1e8
	b.AddOp(arkade.OP_MUL)                // price numerator
	b.AddOp(arkade.OP_SWAP)               // numerator price
	b.AddOp(arkade.OP_DIV)                // raw
	b.AddOp(arkade.OP_DUP)                // raw raw
	b.AddData(collateral)                 // raw raw collateral
	b.AddOp(arkade.OP_GREATERTHANOREQUAL) // raw (raw>=collateral)

	// Upper clamp: once the raw payout reaches the collateral the long is wiped
	// out and the hedge takes everything. There is no lower clamp to write —
	// validate() rules out a negative hedge value, so the quotient cannot go
	// below zero.
	b.AddOp(arkade.OP_IF)
	b.AddOp(arkade.OP_DROP)
	b.AddData(collateral)
	b.AddOp(arkade.OP_ENDIF) // hedgePayout

	// Output 0 must cover the hedge payout. Overpaying is allowed: the covenant
	// stops a side being short-changed, it does not stop a spender being
	// generous with their own share.
	b.AddOp(arkade.OP_DUP)                // hedgePayout hedgePayout
	b.AddOp(arkade.OP_0)                  // hedgePayout hedgePayout 0
	b.AddOp(arkade.OP_INSPECTOUTPUTVALUE) // hedgePayout hedgePayout out0
	b.AddOp(arkade.OP_SWAP)               // hedgePayout out0 hedgePayout
	b.AddOp(arkade.OP_GREATERTHANOREQUAL)
	b.AddOp(arkade.OP_VERIFY) // hedgePayout

	// The long gets the remainder, by subtraction rather than recomputation.
	b.AddData(collateral)   // hedgePayout collateral
	b.AddOp(arkade.OP_SWAP) // collateral hedgePayout
	b.AddOp(arkade.OP_SUB)  // longPayout

	// A dust-sized remainder gets no output at all, so the script must not go
	// looking for output 1 — the transaction does not have one.
	b.AddOp(arkade.OP_DUP)
	b.AddData(dust)
	b.AddOp(arkade.OP_GREATERTHAN)
	b.AddOp(arkade.OP_IF)
	b.AddOp(arkade.OP_1)
	b.AddOp(arkade.OP_INSPECTOUTPUTVALUE) // longPayout out1
	b.AddOp(arkade.OP_SWAP)               // out1 longPayout
	b.AddOp(arkade.OP_GREATERTHANOREQUAL)
	b.AddOp(arkade.OP_ELSE)
	b.AddOp(arkade.OP_DROP)
	b.AddOp(arkade.OP_1)
	b.AddOp(arkade.OP_ENDIF)

	return b.Script()
}

// bigNumBytes encodes an integer the way the VM's BigNum stack expects it.
func bigNumBytes(v int64) ([]byte, error) {
	return arkade.BigNumFromInt64(v).Bytes()
}
