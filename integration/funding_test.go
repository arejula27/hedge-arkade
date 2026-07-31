//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arejula27/hedge/covenant"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

// Funding is bilateral: each side posts its own collateral from its own VTXO,
// in one transaction, and neither hands money to the other or to us on the way
// in. A contract funded by one party is a different trust story than the one
// this protocol claims, so it is worth a test of its own.
//
// What each side puts in is what the covenant would pay it back at the opening
// price — settle immediately and nothing moves.

// signEveryone walks the transaction round the parties in turn. Each wallet
// signs the inputs whose leaf carries its key and leaves the rest alone, so the
// order does not matter and a party cannot sign for its counterparty.
func signEveryone(
	t *testing.T, parties []*party, arkTx *psbt.Packet, checkpoints []*psbt.Packet,
) (string, []string) {
	t.Helper()
	ctx := t.Context()

	signedArkTx, err := arkTx.B64Encode()
	if err != nil {
		t.Fatalf("encoding the transaction: %v", err)
	}
	signedCheckpoints := encode(t, checkpoints)

	for _, p := range parties {
		signedArkTx, err = p.wallet.SignTransaction(ctx, p.explorer, signedArkTx)
		if err != nil {
			t.Fatalf("signing the transaction: %v", err)
		}
		for i, checkpoint := range signedCheckpoints {
			signedCheckpoints[i], err = p.wallet.SignTransaction(ctx, p.explorer, checkpoint)
			if err != nil {
				t.Fatalf("signing a checkpoint: %v", err)
			}
		}
	}

	return signedArkTx, signedCheckpoints
}

// fundContractBilaterally builds one transaction with an input from each party
// and the contract as the first output, then submits it. Both parties' change
// comes back to them.
//
// stakes says what each side puts in; they have to sum to PayoutSats, which is
// what the covenant pins the input to.
func fundContractBilaterally(
	t *testing.T, short, long *party, c covenant.Contract, shortStake, longStake int64,
) wire.OutPoint {
	t.Helper()
	ctx := t.Context()

	if shortStake+longStake != c.Terms.PayoutSats {
		t.Fatalf("stakes %d + %d do not add up to the contract's %d",
			shortStake, longStake, c.Terms.PayoutSats)
	}

	contractPkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	shortInput, shortChangeScript := short.spendableVtxo(t)
	longInput, longChangeScript := long.spendableVtxo(t)

	outputs := []*wire.TxOut{{Value: c.Terms.PayoutSats, PkScript: contractPkScript}}
	for _, change := range []struct {
		amount   int64
		pkScript []byte
	}{
		{shortInput.Amount - shortStake, shortChangeScript},
		{longInput.Amount - longStake, longChangeScript},
	} {
		if change.amount <= int64(stack.dust) {
			t.Fatalf("change of %d is not above the operator's dust %d",
				change.amount, stack.dust)
		}
		outputs = append(outputs, &wire.TxOut{
			Value: change.amount, PkScript: change.pkScript,
		})
	}

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{shortInput, longInput}, outputs, checkpointTapscript(t),
	)
	if err != nil {
		t.Fatalf("building the funding transaction: %v", err)
	}

	parties := []*party{short, long}
	signedArkTx, signedCheckpoints := signEveryone(t, parties, arkTx, checkpoints)

	txid, _, returned, err := short.arkd.SubmitTx(ctx, signedArkTx, signedCheckpoints)
	if err != nil {
		t.Fatalf("arkd refused the bilateral funding transaction: %v", err)
	}

	final := make([]string, len(returned))
	copy(final, returned)
	for _, p := range parties {
		for i, checkpoint := range final {
			final[i], err = p.wallet.SignTransaction(ctx, p.explorer, checkpoint)
			if err != nil {
				t.Fatalf("signing a returned checkpoint: %v", err)
			}
		}
	}
	if err := short.arkd.FinalizeTx(ctx, txid, final); err != nil {
		t.Fatalf("arkd refused to finalize the bilateral funding: %v", err)
	}

	waitForVtxo(t, short, txid)

	hash, err := chainhashFrom(txid)
	if err != nil {
		t.Fatalf("funding txid %q: %v", txid, err)
	}
	return wire.OutPoint{Hash: *hash, Index: 0}
}

func waitForVtxo(t *testing.T, p *party, txid string) {
	t.Helper()

	waitFor(t, 30*time.Second, "the contract VTXO to be registered", func() error {
		spendable, _, err := p.sdk.ListVtxos(t.Context())
		if err != nil {
			return err
		}
		for _, v := range spendable {
			if v.Txid == txid {
				return nil
			}
		}
		return fmt.Errorf("tx %s not registered yet", txid)
	})
}

// Two parties, two VTXOs, one contract — and then it settles. At the opening
// price each side is paid back exactly what it put in, so this is the whole
// round trip: in from both sides, out to both sides, with the covenant deciding
// the split.
func TestTwoPartiesFundAndSettleTheContract(t *testing.T) {
	c := contract(t)

	short, long := newParty(t), newParty(t)
	short.fund(t, boardedSats)
	long.fund(t, boardedSats)

	// Each side posts what it is owed back at the opening price.
	outpoint := fundContractBilaterally(t, short, long, c, shortPayout, longPayout)

	arkTx, checkpoints := settlementSpending(t, c, outpoint, shortPayout, longPayout)
	if err := short.submitToEmulator(t, arkTx, checkpoints); err != nil {
		t.Fatalf("the stack refused to settle a bilaterally funded contract: %v", err)
	}
}

// The covenant pins the input to PayoutSats, so a contract funded with the
// wrong total is one that can never settle. Catching it at funding is the
// difference between a refused transaction and money nobody can move.
func TestTheStackRefusesAnOverfundedContract(t *testing.T) {
	c := contract(t)

	short, long := newParty(t), newParty(t)
	short.fund(t, boardedSats)
	long.fund(t, boardedSats)

	contractPkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	shortInput, shortChangeScript := short.spendableVtxo(t)
	longInput, longChangeScript := long.spendableVtxo(t)

	// One sat over what the covenant will accept.
	overfunded := c.Terms.PayoutSats + 1
	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{shortInput, longInput},
		[]*wire.TxOut{
			{Value: overfunded, PkScript: contractPkScript},
			{Value: shortInput.Amount - shortPayout, PkScript: shortChangeScript},
			{Value: longInput.Amount - longPayout - 1, PkScript: longChangeScript},
		},
		checkpointTapscript(t),
	)
	if err != nil {
		t.Fatalf("building the overfunded transaction: %v", err)
	}

	parties := []*party{short, long}
	signedArkTx, signedCheckpoints := signEveryone(t, parties, arkTx, checkpoints)

	ctx := t.Context()
	txid, _, returned, err := short.arkd.SubmitTx(ctx, signedArkTx, signedCheckpoints)
	if err != nil {
		t.Skipf("arkd refused the overfunded VTXO outright: %v", err)
	}

	final := make([]string, len(returned))
	copy(final, returned)
	for _, p := range parties {
		for i, checkpoint := range final {
			final[i], err = p.wallet.SignTransaction(ctx, p.explorer, checkpoint)
			if err != nil {
				t.Fatalf("signing a returned checkpoint: %v", err)
			}
		}
	}
	if err := short.arkd.FinalizeTx(ctx, txid, final); err != nil {
		t.Skipf("arkd refused to finalize the overfunded VTXO: %v", err)
	}
	waitForVtxo(t, short, txid)

	hash, err := chainhashFrom(txid)
	if err != nil {
		t.Fatalf("funding txid %q: %v", txid, err)
	}
	outpoint := wire.OutPoint{Hash: *hash, Index: 0}

	// Arkade conserves value exactly, so the extra sat has to go to one of the
	// two payouts — and the covenant checks both against the formula.
	for _, tc := range []struct {
		name        string
		short, long int64
	}{
		{"the extra sat given to the short", shortPayout + 1, longPayout},
		{"the extra sat given to the long", shortPayout, longPayout + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := contractInput(t, c, outpoint, overfunded)
			arkTx, checkpoints, err := offchain.BuildTxs(
				[]offchain.VtxoInput{input},
				[]*wire.TxOut{
					{Value: tc.short, PkScript: c.Terms.ShortLockScript},
					{Value: tc.long, PkScript: c.Terms.LongLockScript},
				},
				checkpointTapscript(t),
			)
			if err != nil {
				t.Fatalf("building the settlement: %v", err)
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

			err = short.submitToEmulator(t, arkTx, checkpoints)
			if err == nil {
				t.Fatal("the stack settled a contract funded with the wrong amount")
			}
			if !strings.Contains(err.Error(), "NUMEQUALVERIFY") {
				t.Fatalf("refused, but not by the covenant's input check: %v", err)
			}
			t.Logf("rejected with: %v", err)
		})
	}
}
