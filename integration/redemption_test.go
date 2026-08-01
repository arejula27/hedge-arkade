//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/arejula27/hedge/contract"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

// Leaf 2, mutual redemption: both parties agree to close early at whatever
// split they like, with no oracle and no covenant. It is the only leaf whose
// authority is the two signatures alone, so it is also the only one where
// getting the signing wrong is invisible until someone tries to use it.
//
// The party wallets cannot sign this leaf — it carries the contract's own keys,
// not theirs — so the signatures are added with the raw keys, the way the
// service will have to add them.

// signWithKey signs a packet with a key no wallet holds, and returns it
// encoded.
//
// The signing itself is `contract.SignTapscript`: it is what the service will
// run in production, so having the tests exercise anything else would leave the
// real thing untested.
func signWithKey(t *testing.T, packet *psbt.Packet, key *btcec.PrivateKey) string {
	t.Helper()

	if err := contract.SignTapscript(key, packet); err != nil {
		t.Fatalf("signing with %x: %v", schnorr.SerializePubKey(key.PubKey()), err)
	}

	encoded, err := packet.B64Encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return encoded
}

// redemptionInput points at the contract VTXO and reveals the mutual
// redemption leaf.
func redemptionInput(
	t *testing.T, c contract.Contract, outpoint wire.OutPoint, amount int64,
) offchain.VtxoInput {
	t.Helper()

	input, err := c.RedemptionInput(outpoint)
	if err != nil {
		t.Fatalf("RedemptionInput: %v", err)
	}
	input.Amount = amount
	return input
}

// redeem builds the mutual-redemption spend and submits it with the signatures
// of whoever is given. It returns arkd's error rather than failing, so the
// negative cases can assert on it.
//
// The leaf carries no tweaked emulator key, so this goes straight to arkd —
// the emulator has nothing to execute.
func redeem(
	t *testing.T, p *party, c contract.Contract, outpoint wire.OutPoint,
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
	c := liveContract(t)
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
			c := liveContract(t)
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

// The flow a user will actually see: the service builds the early close, each
// party verifies it against the contract, and both sign. Nothing here builds a
// transaction by hand.
//
// `redeem` above proves the leaf works; this proves the code that will be
// shipped works, which is not the same claim. The proposal is oracle-priced
// because that is the case with something to verify — a split the parties
// simply agreed on has nothing to check it against.
func TestThePartiesSignAnEarlyCloseTheyDidNotBuild(t *testing.T) {
	c := liveContract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	message, signature := signedPrice(t, settlementPrice)

	// The service proposes.
	r, err := c.ProposeRedemptionAt(outpoint, checkpointTapscript(t), message, signature)
	if err != nil {
		t.Fatalf("ProposeRedemptionAt: %v", err)
	}
	t.Logf("proposed %d/%d at price %d", r.ShortSats, r.LongSats, r.Price)

	// Each party checks before signing. A party that skips this is trusting
	// whoever proposed, which is the thing the design refuses to require.
	for _, side := range []string{"short", "long"} {
		if err := c.VerifyRedemption(r, outpoint, checkpointTapscript(t)); err != nil {
			t.Fatalf("the %s would refuse a correct proposal: %v", side, err)
		}
	}

	for _, key := range []*btcec.PrivateKey{shortKey, longKey} {
		if err := contract.SignRedemption(key, r); err != nil {
			t.Fatalf("SignRedemption: %v", err)
		}
	}

	if err := submitRedemption(t, p, r, shortKey, longKey); err != nil {
		t.Fatalf("the stack refused an early close both parties signed: %v", err)
	}
}

// submitRedemption hands a signed early close to arkd and finalises it.
//
// The wallet signs nothing on leaf 2 — it holds none of the contract's keys —
// but it is still the transport, and arkd re-verifies every key in the revealed
// leaf on the checkpoints it hands back (`service.go:1236`), so the contract's
// keys sign that round too.
func submitRedemption(
	t *testing.T, p *party, r *contract.Redemption, signers ...*btcec.PrivateKey,
) error {
	t.Helper()
	ctx := t.Context()

	signedArkTx, signedCheckpoints := p.sign(t, r.ArkTx, r.Checkpoints)

	txid, _, returned, err := p.arkd.SubmitTx(ctx, signedArkTx, signedCheckpoints)
	if err != nil {
		return err
	}

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
