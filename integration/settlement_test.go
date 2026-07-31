//go:build integration

package integration

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/arejula27/hedge/covenant"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

// The settlement the standard terms produce at an unchanged price: each side
// keeps what it put in. Written down, never computed — same rule as the unit
// tests.
const (
	settlementPrice = 10_000_000
	shortPayout     = 10_000_000
	longPayout      = 10_000_000
)

// This is the test the in-process VM cannot be: it hands a real settlement
// transaction to the real emulator, which parses the emulator packet, matches
// the tweaked key against the leaf, executes the covenant and only signs if it
// succeeds.
//
// The unit suite builds its own transaction. This one is built by
// offchain.BuildTxs, so it carries the checkpoint, the extension OP_RETURN and
// the P2A anchor a real settlement has — the shape a covenant that counted
// outputs would reject.
func TestTheEmulatorSignsARealSettlement(t *testing.T) {
	ctx := t.Context()
	c := contract(t)

	arkTx, checkpoints := buildSettlement(t, c, shortPayout, longPayout)

	encoded, err := arkTx.B64Encode()
	if err != nil {
		t.Fatalf("encoding the settlement: %v", err)
	}

	signed, signedCheckpoints, err := stack.emulator.SubmitTx(ctx, encoded, encode(t, checkpoints))
	if err != nil {
		t.Fatalf("the emulator refused a correct settlement: %v", err)
	}
	if signed == "" {
		t.Fatal("the emulator returned an empty transaction")
	}
	if len(signedCheckpoints) != len(checkpoints) {
		t.Fatalf("got %d signed checkpoints, want %d", len(signedCheckpoints), len(checkpoints))
	}
}

// The same transaction with a sat moved to the short. Everything else is
// identical, so a refusal can only come from the covenant.
func TestTheEmulatorRefusesAStolenSat(t *testing.T) {
	ctx := t.Context()
	c := contract(t)

	arkTx, checkpoints := buildSettlement(t, c, shortPayout+1, longPayout-1)

	encoded, err := arkTx.B64Encode()
	if err != nil {
		t.Fatalf("encoding the settlement: %v", err)
	}

	if _, _, err := stack.emulator.SubmitTx(ctx, encoded, encode(t, checkpoints)); err == nil {
		t.Fatal("the emulator signed a settlement that moved a sat to the short")
	}
}

// Redirecting a payout leaves the amounts correct and the recipient arbitrary.
func TestTheEmulatorRefusesARedirectedPayout(t *testing.T) {
	ctx := t.Context()
	c := contract(t)

	stolen := c
	stolen.Terms.LongLockScript = p2tr(key(0x99).PubKey())

	// The covenant is still the one the leaf commits to; only the transaction's
	// outputs are wrong.
	arkTx, checkpoints := buildSettlementPaying(t, c, []*wire.TxOut{
		{Value: shortPayout, PkScript: c.Terms.ShortLockScript},
		{Value: longPayout, PkScript: stolen.Terms.LongLockScript},
	})

	encoded, err := arkTx.B64Encode()
	if err != nil {
		t.Fatalf("encoding the settlement: %v", err)
	}

	if _, _, err := stack.emulator.SubmitTx(ctx, encoded, encode(t, checkpoints)); err == nil {
		t.Fatal("the emulator signed a settlement paying the wrong recipient")
	}
}

func buildSettlement(t *testing.T, c covenant.Contract, short, long int64) (*psbt.Packet, []*psbt.Packet) {
	t.Helper()

	return buildSettlementPaying(t, c, []*wire.TxOut{
		{Value: short, PkScript: c.Terms.ShortLockScript},
		{Value: long, PkScript: c.Terms.LongLockScript},
	})
}

// buildSettlementPaying assembles the transaction that spends the contract VTXO
// through the settlement leaf, with the emulator packet attached.
func buildSettlementPaying(
	t *testing.T, c covenant.Contract, outputs []*wire.TxOut,
) (*psbt.Packet, []*psbt.Packet) {
	t.Helper()

	funding := fundingTx(t, c)
	input := contractInput(t, c, funding)

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{input}, outputs, checkpointTapscript(t),
	)
	if err != nil {
		t.Fatalf("building the settlement transaction: %v", err)
	}

	// The emulator resolves the input's previous ark transaction from this
	// field, not from a chain it can query.
	if err := txutils.SetArkPsbtField(arkTx, 0, arkade.PrevArkTxField, *funding); err != nil {
		t.Fatalf("attaching the previous ark tx: %v", err)
	}

	settlementScript, err := c.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript: %v", err)
	}

	addEmulatorPacket(t, arkTx, arkade.EmulatorEntry{
		Vin:     0,
		Script:  settlementScript,
		Witness: settlementWitness(t, settlementPrice),
	})

	return arkTx, checkpoints
}

// fundingTx is the transaction that created the contract VTXO. The emulator
// only reads it to resolve the input being spent, so it does not have to exist
// on any chain — but it does have to hold exactly PayoutSats, which is what the
// covenant checks.
func fundingTx(t *testing.T, c covenant.Contract) *wire.MsgTx {
	t.Helper()

	pkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	return &wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{{PreviousOutPoint: wire.OutPoint{Index: 0}}},
		TxOut:   []*wire.TxOut{{Value: c.Terms.PayoutSats, PkScript: pkScript}},
	}
}

// contractInput points at the contract VTXO and reveals the settlement leaf.
func contractInput(t *testing.T, c covenant.Contract, funding *wire.MsgTx) offchain.VtxoInput {
	t.Helper()

	proof, err := c.Tapscript(covenant.LeafSettlement)
	if err != nil {
		t.Fatalf("Tapscript: %v", err)
	}

	control, err := txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		t.Fatalf("ParseControlBlock: %v", err)
	}

	vtxo, err := c.VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}
	revealed, err := vtxo.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	return offchain.VtxoInput{
		Outpoint: &wire.OutPoint{Hash: funding.TxHash(), Index: 0},
		Tapscript: &waddrmgr.Tapscript{
			ControlBlock:   control,
			RevealedScript: proof.Script,
		},
		Amount:             funding.TxOut[0].Value,
		RevealedTapscripts: revealed,
	}
}

// checkpointTapscript is the operator's, read from its GetInfo rather than
// assumed.
func checkpointTapscript(t *testing.T) []byte {
	t.Helper()

	if stack.checkpointTapscript == "" {
		t.Skip("the operator reports no checkpoint tapscript")
	}

	raw, err := hex.DecodeString(stack.checkpointTapscript)
	if err != nil {
		t.Fatalf("decoding the checkpoint tapscript: %v", err)
	}
	return raw
}

// addEmulatorPacket inserts the extension OP_RETURN before the P2A anchor, so
// the payout output indices the covenant inspects do not shift.
func addEmulatorPacket(t *testing.T, ptx *psbt.Packet, entries ...arkade.EmulatorEntry) {
	t.Helper()

	packet, err := arkade.NewPacket(entries...)
	if err != nil {
		t.Fatalf("building the emulator packet: %v", err)
	}

	txOut, err := extension.Extension{packet}.TxOut()
	if err != nil {
		t.Fatalf("encoding the extension output: %v", err)
	}

	last := len(ptx.UnsignedTx.TxOut) - 1
	if last >= 0 && bytes.Equal(ptx.UnsignedTx.TxOut[last].PkScript, txutils.ANCHOR_PKSCRIPT) {
		anchor := ptx.UnsignedTx.TxOut[last]
		ptx.UnsignedTx.TxOut[last] = txOut
		ptx.UnsignedTx.AddTxOut(anchor)
	} else {
		ptx.UnsignedTx.AddTxOut(txOut)
	}
	ptx.Outputs = append(ptx.Outputs, psbt.POutput{})
}

func encode(t *testing.T, packets []*psbt.Packet) []string {
	t.Helper()

	encoded := make([]string, 0, len(packets))
	for _, p := range packets {
		b64, err := p.B64Encode()
		if err != nil {
			t.Fatalf("encoding a checkpoint: %v", err)
		}
		encoded = append(encoded, b64)
	}
	return encoded
}
