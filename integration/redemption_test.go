//go:build integration

package integration

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arejula27/hedge/covenant"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

// Leaf 2, mutual redemption: both parties agree to close early at whatever
// split they like, with no oracle and no covenant. It is the only leaf whose
// authority is the two signatures alone, so it is also the only one where
// getting the signing wrong is invisible until someone tries to use it.
//
// The party wallets cannot sign this leaf — it carries the contract's own keys,
// not theirs — so the signatures are added with the raw keys, the way the
// service will have to add them.

// signWithKey adds a tapscript signature to every input whose revealed leaf
// contains the key, and returns the packet base64-encoded.
//
// This is what `singlekeywallet` does internally (`bitcoin_wallet.go:255`),
// reproduced because the contract's keys do not live in an SDK wallet. Leaves
// the key is not part of are skipped, so the same call works on the ark
// transaction and on the checkpoints.
func signWithKey(t *testing.T, packet *psbt.Packet, key *btcec.PrivateKey) string {
	t.Helper()

	mine := schnorr.SerializePubKey(key.PubKey())

	prevOuts := txscript.NewMultiPrevOutFetcher(nil)
	for i, in := range packet.Inputs {
		if in.WitnessUtxo == nil {
			t.Fatalf("input %d has no witness utxo", i)
		}
		prevOuts.AddPrevOut(packet.UnsignedTx.TxIn[i].PreviousOutPoint, in.WitnessUtxo)
	}
	sigHashes := txscript.NewTxSigHashes(packet.UnsignedTx, prevOuts)

	signed := 0
	for i, in := range packet.Inputs {
		for _, leaf := range in.TaprootLeafScript {
			if !leafHasKey(leaf.Script, mine) {
				continue
			}

			digest, err := txscript.CalcTapscriptSignaturehash(
				sigHashes, in.SighashType, packet.UnsignedTx, i, prevOuts,
				txscript.NewBaseTapLeaf(leaf.Script),
			)
			if err != nil {
				t.Fatalf("sighash for input %d: %v", i, err)
			}

			sig, err := schnorr.Sign(key, digest)
			if err != nil {
				t.Fatalf("signing input %d: %v", i, err)
			}

			hash := txscript.NewTapLeaf(leaf.LeafVersion, leaf.Script).TapHash()
			packet.Inputs[i].TaprootScriptSpendSig = append(
				packet.Inputs[i].TaprootScriptSpendSig,
				&psbt.TaprootScriptSpendSig{
					XOnlyPubKey: mine,
					LeafHash:    hash.CloneBytes(),
					Signature:   sig.Serialize(),
					SigHash:     in.SighashType,
				},
			)
			signed++
		}
	}

	if signed == 0 {
		t.Fatalf("key %x signs none of the revealed leaves", mine)
	}

	encoded, err := packet.B64Encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return encoded
}

// leafHasKey reports whether the closure names the key, using arkd's own
// decoder so an unrecognisable leaf is skipped rather than guessed at.
func leafHasKey(leaf, xOnlyKey []byte) bool {
	closure, err := arkscript.DecodeClosure(leaf)
	if err != nil {
		return false
	}

	var keys []*btcec.PublicKey
	switch c := closure.(type) {
	case *arkscript.MultisigClosure:
		keys = c.PubKeys
	case *arkscript.CSVMultisigClosure:
		keys = c.PubKeys
	case *arkscript.CLTVMultisigClosure:
		keys = c.PubKeys
	default:
		return false
	}

	for _, key := range keys {
		if bytes.Equal(schnorr.SerializePubKey(key), xOnlyKey) {
			return true
		}
	}
	return false
}

// redemptionInput points at the contract VTXO and reveals the mutual
// redemption leaf.
func redemptionInput(
	t *testing.T, c covenant.Contract, outpoint wire.OutPoint, amount int64,
) offchain.VtxoInput {
	t.Helper()

	proof, err := c.Tapscript(covenant.LeafMutualRedemption)
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
		Outpoint: &outpoint,
		Tapscript: &waddrmgr.Tapscript{
			ControlBlock:   control,
			RevealedScript: proof.Script,
		},
		Amount:             amount,
		RevealedTapscripts: revealed,
	}
}

// redeem builds the mutual-redemption spend and submits it with the signatures
// of whoever is given. It returns arkd's error rather than failing, so the
// negative cases can assert on it.
//
// The leaf carries no tweaked emulator key, so this goes straight to arkd —
// the emulator has nothing to execute.
func redeem(
	t *testing.T, p *party, c covenant.Contract, outpoint wire.OutPoint,
	outputs []*wire.TxOut, signers ...*btcec.PrivateKey,
) error {
	t.Helper()
	ctx := t.Context()

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{redemptionInput(t, c, outpoint, c.Terms.PayoutSats)},
		outputs, checkpointTapscript(t),
	)
	if err != nil {
		t.Fatalf("building the redemption transaction: %v", err)
	}

	// Both transactions need the parties' signatures. The checkpoint spends the
	// contract VTXO through leaf 2, and the checkpoint output is built from
	// that same closure (`offchain/tx.go:184`), so the ark tx that spends it
	// reveals leaf 2 as well. A settlement never hits this: its leaf carries no
	// party key, so arkd and the emulator sign both.
	for _, key := range signers {
		signWithKey(t, arkTx, key)
		for _, checkpoint := range checkpoints {
			signWithKey(t, checkpoint, key)
		}
	}

	signedArkTx, signedCheckpoints := p.sign(t, arkTx, checkpoints)

	txid, _, returned, err := p.arkd.SubmitTx(ctx, signedArkTx, signedCheckpoints)
	if err != nil {
		return err
	}

	// arkd hands the checkpoints back with its own signature on them, and
	// FinalizeTx re-verifies every key in the revealed leaf
	// (`service.go:1236`). The party wallet signs nothing on leaf 2, so the
	// contract's keys have to sign this round too.
	final := make([]string, 0, len(returned))
	for _, checkpoint := range returned {
		packet, err := psbt.NewFromRawBytes(strings.NewReader(checkpoint), true)
		if err != nil {
			t.Fatalf("decoding a returned checkpoint: %v", err)
		}
		encoded := checkpoint
		for _, key := range signers {
			encoded = signWithKey(t, packet, key)
		}
		final = append(final, encoded)
	}
	return p.arkd.FinalizeTx(ctx, txid, final)
}

// The split on this leaf is whatever the two of them say it is — that is the
// point of it, and the reason it needs both signatures. This one is nothing
// like the covenant's, to prove the covenant is genuinely out of the way.
func TestTheStackRedeemsTheContractMutually(t *testing.T) {
	c := contract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	lopsided := []*wire.TxOut{
		{Value: c.Terms.PayoutSats - int64(stack.dust), PkScript: c.Terms.ShortLockScript},
		{Value: int64(stack.dust), PkScript: c.Terms.LongLockScript},
	}

	if err := redeem(t, p, c, outpoint, lopsided, shortKey, longKey); err != nil {
		t.Fatalf("the stack refused a mutual redemption both parties signed: %v", err)
	}
}

// One signature is not agreement. Without this the leaf would be a way for
// either party to take the whole contract whenever it suited them.
func TestTheStackRefusesAOneSidedRedemption(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signer *btcec.PrivateKey
	}{
		{"only the short signs", shortKey},
		{"only the long signs", longKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := contract(t)
			p := newParty(t)
			p.fund(t, boardedSats)

			outpoint := fundContract(t, p, c)

			// Everything to whoever is signing.
			grab := []*wire.TxOut{
				{Value: c.Terms.PayoutSats, PkScript: p2tr(tc.signer.PubKey())},
			}

			err := redeem(t, p, c, outpoint, grab, tc.signer)
			if err == nil {
				t.Fatal("the stack let one party redeem the contract alone")
			}
			t.Logf("rejected with: %v", err)
		})
	}
}
