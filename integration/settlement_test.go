//go:build integration

package integration

import (
	"testing"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/contract"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	emulatorvm "github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

// The settlement the standard terms produce at an unchanged price: each side
// keeps what it put in. Written down, never computed — same rule as the unit
// tests.
const (
	settlementPrice = 10_000_000
	shortPayout     = 10_000_000
	longPayout      = 10_000_000
)

// boardedSats is what the party boards: enough to fund the contract and leave
// change well above dust.
const boardedSats = 50_000_000

// The whole path a settlement takes in production, on a contract VTXO that
// really exists.
//
// The client submits to the emulator, which parses the emulator packet, matches
// the tweaked key against the leaf, executes the covenant, signs, and — as the
// finalizer — forwards to arkd (`internal/application/tx.go:146`). So this one
// call exercises the covenant, arkd's value conservation, its output
// validation, and its signature checks.
//
// It is also the only tier that sees transaction shape. The transaction is
// built by offchain.BuildTxs, so it carries the checkpoint, the extension
// OP_RETURN and the P2A anchor — four outputs, not the two a covenant that
// counted them would demand.
func TestTheStackSettlesTheContract(t *testing.T) {
	c := liveContract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)
	arkTx, checkpoints := settlementSpending(t, c, outpoint, shortPayout, longPayout)

	if err := p.submitToEmulator(t, arkTx, checkpoints); err != nil {
		t.Fatalf("the stack refused a correct settlement: %v", err)
	}
}

// A sat moved to the short. The transaction still balances, so nothing arkd
// checks is violated — only the covenant is.
func TestTheStackRefusesAStolenSat(t *testing.T) {
	c := liveContract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)
	arkTx, checkpoints := settlementSpending(t, c, outpoint, shortPayout+1, longPayout-1)

	err := p.submitToEmulator(t, arkTx, checkpoints)
	if err == nil {
		t.Fatal("the stack settled a transaction that moved a sat to the short")
	}
	t.Logf("rejected with: %v", err)
}

// The amounts are right and the recipient is not. Value conservation cannot
// catch this; only the lock script check can.
func TestTheStackRefusesARedirectedPayout(t *testing.T) {
	c := liveContract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	thief := p2tr(key(0x99).PubKey())
	arkTx, checkpoints := settlementPaying(t, c, outpoint, []*wire.TxOut{
		{Value: shortPayout, PkScript: c.Terms.ShortLockScript},
		{Value: longPayout, PkScript: thief},
	})

	err := p.submitToEmulator(t, arkTx, checkpoints)
	if err == nil {
		t.Fatal("the stack settled a payout to the wrong recipient")
	}
	t.Logf("rejected with: %v", err)
}

// fundContract spends the party's VTXO into the contract address with exactly
// PayoutSats — which is what the covenant requires the input to hold — and
// sends the rest back as change. It returns the contract VTXO's outpoint.
//
// This transaction has no covenant on its input, so it goes straight to arkd.
func fundContract(t *testing.T, p *party, c contract.Contract) wire.OutPoint {
	t.Helper()

	contractPkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	input, changePkScript := p.spendableVtxo(t)

	change := input.Amount - c.Terms.PayoutSats
	if change <= int64(stack.Dust) {
		t.Fatalf("boarded %d sats: not enough to fund %d and leave change above dust %d",
			input.Amount, c.Terms.PayoutSats, stack.Dust)
	}

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{input},
		[]*wire.TxOut{
			{Value: c.Terms.PayoutSats, PkScript: contractPkScript},
			{Value: change, PkScript: changePkScript},
		},
		checkpointTapscript(t),
	)
	if err != nil {
		t.Fatalf("building the funding transaction: %v", err)
	}

	txid, err := p.submitToArkd(t, arkTx, checkpoints)
	if err != nil {
		t.Fatalf("arkd refused the funding transaction: %v", err)
	}

	hash, err := arkade.ChainHash(txid)
	if err != nil {
		t.Fatalf("funding txid %q: %v", txid, err)
	}
	return wire.OutPoint{Hash: *hash, Index: 0}
}

func settlementSpending(
	t *testing.T, c contract.Contract, outpoint wire.OutPoint, short, long int64,
) (*psbt.Packet, []*psbt.Packet) {
	t.Helper()

	return settlementPaying(t, c, outpoint, []*wire.TxOut{
		{Value: short, PkScript: c.Terms.ShortLockScript},
		{Value: long, PkScript: c.Terms.LongLockScript},
	})
}

// settlementPaying builds the transaction that spends the contract VTXO through
// the settlement leaf, with the covenant and both oracle messages in the
// emulator packet.
func settlementPaying(
	t *testing.T, c contract.Contract, outpoint wire.OutPoint, outputs []*wire.TxOut,
) (*psbt.Packet, []*psbt.Packet) {
	t.Helper()

	requireCheckpointTapscript(t)

	arkTx, checkpoints, err := arkade.BuildSettlementPaying(
		stack, c, outpoint, outputs, settlementWitness(t, settlementPrice),
	)
	if err != nil {
		t.Fatalf("building the settlement transaction: %v", err)
	}
	return arkTx, checkpoints
}

// contractInput points at the contract VTXO and reveals the settlement leaf.
func contractInput(
	t *testing.T, c contract.Contract, outpoint wire.OutPoint, amount int64,
) offchain.VtxoInput {
	t.Helper()

	input, err := arkade.ContractInput(c, contract.LeafSettlement, outpoint, amount)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

// checkpointTapscript is the operator's, read from its GetInfo rather than
// assumed.
func checkpointTapscript(t *testing.T) []byte {
	t.Helper()

	requireCheckpointTapscript(t)
	return stack.CheckpointTapscript
}

func requireCheckpointTapscript(t *testing.T) {
	t.Helper()

	if len(stack.CheckpointTapscript) == 0 {
		t.Skip("the operator reports no checkpoint tapscript")
	}
}

// addEmulatorPacket inserts the extension OP_RETURN before the P2A anchor, so
// the payout output indices the covenant inspects do not shift.
func addEmulatorPacket(t *testing.T, ptx *psbt.Packet, entries ...emulatorvm.EmulatorEntry) {
	t.Helper()

	if err := arkade.AddEmulatorPacket(ptx, entries...); err != nil {
		t.Fatal(err)
	}
}
