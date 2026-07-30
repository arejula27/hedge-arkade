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

// Outputs is the settlement a spender proposes: what each side would receive.
// The covenant's job is to accept it or reject it.
type Outputs struct {
	Hedge int64
	Long  int64
}

// Run executes an Arkade Script against the VM with the given initial stack and
// proposed outputs, and reports whether it succeeded.
//
// The stack is the script's witness, bottom element first. Outputs become the
// spending transaction's outputs in order, so output 0 is the hedge side.
func Run(script []byte, stack [][]byte, out Outputs) error {
	tx := spendingTx(out)

	vm, err := arkade.NewEngine(
		script, tx, 0, nil, nil, inputAmount, newPrevOutFetcher(),
	)
	if err != nil {
		return err
	}

	if len(stack) > 0 {
		vm.SetStack(stack)
	}

	return vm.Execute()
}

// RunWithOutputCount executes a script against a transaction with n outputs
// instead of the usual two, for checking that the covenant pins the shape.
func RunWithOutputCount(script []byte, stack [][]byte, n int) error {
	tx := &wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{PreviousOutPoint: fundingOutpoint}},
	}
	for range n {
		tx.TxOut = append(tx.TxOut, &wire.TxOut{Value: 1, PkScript: placeholderScript})
	}

	vm, err := arkade.NewEngine(script, tx, 0, nil, nil, inputAmount, newPrevOutFetcher())
	if err != nil {
		return err
	}
	if len(stack) > 0 {
		vm.SetStack(stack)
	}
	return vm.Execute()
}

// inputAmount is the value of the VTXO being spent. It is deliberately larger
// than any payout the tests exercise, so a failure is never an accident of the
// input being too small to fund the outputs.
const inputAmount = 10_000_000_000

var fundingOutpoint = wire.OutPoint{Hash: chainhash.Hash{}, Index: 0}

// spendingTx is the transaction the covenant introspects: one input spending the
// contract VTXO, one output per side.
func spendingTx(out Outputs) *wire.MsgTx {
	tx := &wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{PreviousOutPoint: fundingOutpoint}},
	}

	for _, v := range []int64{out.Hedge, out.Long} {
		tx.TxOut = append(tx.TxOut, &wire.TxOut{Value: v, PkScript: placeholderScript})
	}

	return tx
}

// placeholderScript stands in for a real destination. The settlement covenant
// constrains output *values*; which script each payout lands in is checked
// separately, so the tests here leave it constant.
var placeholderScript = append([]byte{txscript.OP_1, txscript.OP_DATA_32}, make([]byte, 32)...)

// prevOutFetcher satisfies arkade.ArkPrevOutFetcher for a single synthetic
// funding outpoint.
type prevOutFetcher struct {
	txscript.PrevOutputFetcher
	arkTx *wire.MsgTx
}

func newPrevOutFetcher() *prevOutFetcher {
	prevOut := &wire.TxOut{Value: inputAmount, PkScript: placeholderScript}

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
