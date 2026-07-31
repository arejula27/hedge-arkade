// Package covenant builds the contract's Arkade Script and runs it against the
// real Arkade VM.
//
// No contract logic is reimplemented here. The settlement formula exists in
// exactly one place — the script this package emits — and the tests assert on
// what the VM does with it, not on what a parallel Go model would compute.
package covenant

import (
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// Payout is one settlement output: an amount and who it pays.
type Payout struct {
	Sats       int64
	LockScript []byte
}

// Spend is the settlement a spender proposes. The covenant's job is to accept
// it or reject it.
type Spend struct {
	Outputs []Payout

	// ExtraInputs adds inputs beyond the contract VTXO, for checking that the
	// covenant pins the transaction's shape.
	ExtraInputs int

	// InputSats is what the contract VTXO holds. Zero means defaultInputSats.
	InputSats int64
}

func (s Spend) inputSats() int64 {
	if s.InputSats == 0 {
		return defaultInputSats
	}
	return s.InputSats
}

// Run executes an Arkade Script against the VM with the given witness stack and
// proposed spend, and reports whether it succeeded.
//
// The stack is bottom element first.
func Run(script []byte, stack [][]byte, spend Spend) error {
	vm, err := arkade.NewEngine(
		script, spendingTx(spend), 0, nil, nil, spend.inputSats(),
		newPrevOutFetcher(spend.inputSats()),
	)
	if err != nil {
		return err
	}

	// The emulator service replaces the limit table wholesale before executing
	// (internal/application/tx.go:67). Its config starts from these defaults and
	// applies COMPUTE_LIMITS overrides, so with no overrides this is the
	// deployed table. Declaring it here means a change to the defaults shows up
	// as a test failure rather than as a surprise in production.
	arkade.WithExactComputeLimits(arkade.DefaultComputeLimits())(vm)

	if len(stack) > 0 {
		vm.SetStack(stack)
	}

	return vm.Execute()
}

// defaultInputSats is what the contract VTXO holds when a Spend does not say.
// The covenant requires the input to equal PayoutSats exactly, so this matches
// the standard terms; a case with other terms sets Spend.InputSats.
const defaultInputSats = 20_000_000

var fundingOutpoint = wire.OutPoint{Hash: chainhash.Hash{}, Index: 0}

func spendingTx(spend Spend) *wire.MsgTx {
	tx := &wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{PreviousOutPoint: fundingOutpoint}},
	}

	for i := range spend.ExtraInputs {
		tx.TxIn = append(tx.TxIn, &wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{}, Index: uint32(i + 1)},
		})
	}

	for _, out := range spend.Outputs {
		tx.TxOut = append(tx.TxOut, &wire.TxOut{Value: out.Sats, PkScript: out.LockScript})
	}

	return tx
}

// P2TR builds a taproot scriptPubKey from a 32-byte x-only key. Payouts land in
// VTXOs, so this is the output type the contract actually pays to.
func P2TR(xOnlyKey []byte) []byte {
	script := make([]byte, 0, 34)
	script = append(script, txscript.OP_1, txscript.OP_DATA_32)
	return append(script, xOnlyKey...)
}

// prevOutFetcher satisfies arkade.ArkPrevOutFetcher for the synthetic funding
// outpoint. Extra inputs resolve to nil, which is what a spender bringing along
// an unrelated input would look like.
type prevOutFetcher struct {
	txscript.PrevOutputFetcher
	arkTx *wire.MsgTx
}

func newPrevOutFetcher(inputSats int64) *prevOutFetcher {
	prevOut := &wire.TxOut{Value: inputSats, PkScript: P2TR(make([]byte, 32))}

	return &prevOutFetcher{
		PrevOutputFetcher: txscript.NewMultiPrevOutFetcher(
			map[wire.OutPoint]*wire.TxOut{fundingOutpoint: prevOut},
		),
		arkTx: &wire.MsgTx{Version: 2, TxOut: []*wire.TxOut{prevOut}},
	}
}

func (f *prevOutFetcher) FetchPrevOutArkTx(op wire.OutPoint) *wire.MsgTx {
	if op != fundingOutpoint {
		return nil
	}
	return f.arkTx
}

func (f *prevOutFetcher) FetchVtxoPrevOutPkScript(op wire.OutPoint) []byte {
	if op != fundingOutpoint {
		return nil
	}
	return f.arkTx.TxOut[0].PkScript
}
