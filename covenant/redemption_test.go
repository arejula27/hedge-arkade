package covenant

import (
	"bytes"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
)

// The early close is built by one side and signed by both, so every test here
// is really the same question: can a party who verifies be given something
// other than what they agreed to?

// checkpointScript stands in for the operator's, which is a runtime value read
// from its GetInfo. All that matters offline is that it decodes as the CSV
// multisig closure the checkpoint is built around.
func checkpointScript(t *testing.T) []byte {
	t.Helper()

	closure := arkscript.CSVMultisigClosure{
		MultisigClosure: arkscript.MultisigClosure{
			PubKeys: []*btcec.PublicKey{arkdKey},
		},
		Locktime: arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: 512},
	}
	script, err := closure.Script()
	if err != nil {
		t.Fatalf("checkpoint closure: %v", err)
	}
	return script
}

func redemptionOutpoint() wire.OutPoint {
	return contractOutpoint()
}

// The ordinary case: an agreed split, paid to the two lock scripts, summing to
// what the contract holds.
func TestARedemptionPaysTheAgreedSplit(t *testing.T) {
	c := contract()
	short := c.Terms.PayoutSats / 4
	long := c.Terms.PayoutSats - short

	r, err := c.ProposeRedemption(redemptionOutpoint(), short, long, checkpointScript(t))
	if err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	if r.ShortSats != short || r.LongSats != long {
		t.Fatalf("proposed %d/%d, want %d/%d", r.ShortSats, r.LongSats, short, long)
	}
	if len(r.Checkpoints) != 1 {
		t.Fatalf("got %d checkpoints, want 1", len(r.Checkpoints))
	}

	outs := r.ArkTx.UnsignedTx.TxOut
	if outs[0].Value != short || !bytes.Equal(outs[0].PkScript, c.Terms.ShortLockScript) {
		t.Error("output 0 is not the short's payout")
	}
	if outs[1].Value != long || !bytes.Equal(outs[1].PkScript, c.Terms.LongLockScript) {
		t.Error("output 1 is not the long's payout")
	}

	if err := c.VerifyRedemption(r, redemptionOutpoint(), checkpointScript(t)); err != nil {
		t.Fatalf("a party would refuse a correct redemption: %v", err)
	}
}

// Leaf 2 has no covenant, so nothing onchain stops a lopsided split. That is
// the point of the leaf, and it has to keep working.
func TestARedemptionAllowsAnySplitThePartiesAgree(t *testing.T) {
	c := contract()

	for _, short := range []int64{
		Dust,
		c.Terms.PayoutSats / 2,
		c.Terms.PayoutSats - Dust,
	} {
		r, err := c.ProposeRedemption(
			redemptionOutpoint(), short, c.Terms.PayoutSats-short, checkpointScript(t),
		)
		if err != nil {
			t.Fatalf("ProposeRedemption(%d): %v", short, err)
		}
		if err := c.VerifyRedemption(r, redemptionOutpoint(), checkpointScript(t)); err != nil {
			t.Fatalf("a party would refuse the split %d: %v", short, err)
		}
	}
}

// Arkade conserves value exactly and will not create a dust output, so a
// proposal that breaks either is not a bad deal — it is a transaction that
// cannot be submitted. Refusing it here says so before anyone signs.
func TestARedemptionRefusesASplitArkdWouldNot(t *testing.T) {
	c := contract()
	payout := c.Terms.PayoutSats

	for _, tc := range []struct {
		name        string
		short, long int64
	}{
		{"a sat short", payout/2 - 1, payout / 2},
		{"a sat over", payout/2 + 1, payout / 2},
		{"the short gets nothing", 0, payout},
		{"the long gets nothing", payout, 0},
		{"the short is below dust", Dust - 1, payout - Dust + 1},
		{"the long is below dust", payout - Dust + 1, Dust - 1},
		{"both sides negative", -1, payout + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.ProposeRedemption(
				redemptionOutpoint(), tc.short, tc.long, checkpointScript(t),
			); err == nil {
				t.Fatal("built a redemption the operator would refuse")
			}
		})
	}
}

// A priced proposal is the convenience case, and it must land on the same split
// the covenant would compute at maturity — the formula has one home.
func TestAPricedRedemptionFollowsTheSameFormula(t *testing.T) {
	c := contract()

	for _, tc := range splitTable {
		msg, sig := oracleSigned(t, tc.price)

		r, err := c.ProposeRedemptionAt(
			redemptionOutpoint(), checkpointScript(t), msg, sig,
		)
		if err != nil {
			t.Fatalf("ProposeRedemptionAt(%d): %v", tc.price, err)
		}

		if r.ShortSats != tc.short || r.LongSats != tc.long {
			t.Errorf("at %d the redemption pays %d/%d, want %d/%d",
				tc.price, r.ShortSats, r.LongSats, tc.short, tc.long)
		}
		if err := c.VerifyRedemption(r, redemptionOutpoint(), checkpointScript(t)); err != nil {
			t.Errorf("a party would refuse the priced redemption at %d: %v", tc.price, err)
		}
	}
}

// Rewriting the proposal after it was built is what verification exists to
// catch. Each of these is a transaction that would still be perfectly valid to
// arkd — only the party it robs can tell.
func TestVerifyCatchesARewrittenRedemption(t *testing.T) {
	c := contract()
	half := c.Terms.PayoutSats / 2

	for _, tc := range []struct {
		name    string
		rewrite func(*Redemption)
	}{
		{"a sat moved to the short", func(r *Redemption) {
			r.ArkTx.UnsignedTx.TxOut[0].Value++
			r.ArkTx.UnsignedTx.TxOut[1].Value--
		}},
		{"the short's payout redirected", func(r *Redemption) {
			r.ArkTx.UnsignedTx.TxOut[0].PkScript = P2TR(
				[]byte(bytes.Repeat([]byte{0x11}, 32)),
			)
		}},
		{"the payouts swapped", func(r *Redemption) {
			r.ArkTx.UnsignedTx.TxOut[0], r.ArkTx.UnsignedTx.TxOut[1] =
				r.ArkTx.UnsignedTx.TxOut[1], r.ArkTx.UnsignedTx.TxOut[0]
		}},
		{"the claimed split raised", func(r *Redemption) {
			r.ShortSats++
			r.LongSats--
		}},
		{"a checkpoint replaced", func(r *Redemption) {
			r.Checkpoints = nil
		}},
		{"the ark transaction repointed", func(r *Redemption) {
			r.ArkTx.UnsignedTx.TxIn[0].PreviousOutPoint.Index++
		}},
		{"a different contract closed", func(r *Redemption) {
			r.Checkpoints[0].UnsignedTx.TxIn[0].PreviousOutPoint.Index++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := c.ProposeRedemption(
				redemptionOutpoint(), half, c.Terms.PayoutSats-half, checkpointScript(t),
			)
			if err != nil {
				t.Fatalf("ProposeRedemption: %v", err)
			}

			tc.rewrite(r)

			if err := c.VerifyRedemption(r, redemptionOutpoint(), checkpointScript(t)); err == nil {
				t.Fatal("a party would have signed a rewritten redemption")
			}
		})
	}
}

// A price the oracle never signed is the whole reason the message and the
// signature travel with the proposal instead of just the number.
func TestVerifyCatchesAPriceTheOracleDidNotSign(t *testing.T) {
	c := contract()

	for _, tc := range []struct {
		name    string
		rewrite func(*Redemption)
	}{
		{"the message edited after signing", func(r *Redemption) {
			r.Message[len(r.Message)-1] ^= 0x01
		}},
		{"the signature replaced", func(r *Redemption) {
			r.Signature[0] ^= 0x01
		}},
		{"signed by a different oracle", func(r *Redemption) {
			msg, sig := oracleSignedBy(t, privKey(0x77), midPrice)
			r.Message, r.Signature = msg, sig
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, sig := oracleSigned(t, midPrice)
			r, err := c.ProposeRedemptionAt(
				redemptionOutpoint(), checkpointScript(t), msg, sig,
			)
			if err != nil {
				t.Fatalf("ProposeRedemptionAt: %v", err)
			}

			tc.rewrite(r)

			if err := c.VerifyRedemption(r, redemptionOutpoint(), checkpointScript(t)); err == nil {
				t.Fatal("a party would have signed a price the oracle never gave")
			}
		})
	}
}

// A proposal claiming a split its own price does not produce is the subtler
// attack: the oracle's signature is genuine, the arithmetic is not.
func TestVerifyCatchesASplitTheAgreedPriceDoesNotProduce(t *testing.T) {
	c := contract()

	msg, sig := oracleSigned(t, midPrice)
	honest, err := c.ProposeRedemptionAt(redemptionOutpoint(), checkpointScript(t), msg, sig)
	if err != nil {
		t.Fatalf("ProposeRedemptionAt: %v", err)
	}

	// A different split, built honestly, then presented with the oracle's
	// message attached — so every transaction in it is internally consistent.
	lying, err := c.ProposeRedemption(
		redemptionOutpoint(), honest.ShortSats+1_000, c.Terms.PayoutSats-honest.ShortSats-1_000,
		checkpointScript(t),
	)
	if err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}
	lying.Message, lying.Signature, lying.Price = msg, sig, honest.Price

	if err := c.VerifyRedemption(lying, redemptionOutpoint(), checkpointScript(t)); err == nil {
		t.Fatal("a party would have signed a split the price does not produce")
	}
}

// Both transactions need both parties. The checkpoint spends the contract
// through leaf 2 and the ark transaction spends the checkpoint, whose output is
// built from that same closure.
func TestSigningARedemptionCoversEveryTransaction(t *testing.T) {
	c := contract()
	half := c.Terms.PayoutSats / 2

	r, err := c.ProposeRedemption(
		redemptionOutpoint(), half, c.Terms.PayoutSats-half, checkpointScript(t),
	)
	if err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	for _, key := range []*btcec.PrivateKey{shortPriv, longPriv} {
		if err := SignRedemption(key, r); err != nil {
			t.Fatalf("SignRedemption: %v", err)
		}
	}

	if got := len(r.ArkTx.Inputs[0].TaprootScriptSpendSig); got != 2 {
		t.Errorf("the ark transaction carries %d signatures, want 2", got)
	}
	for i, checkpoint := range r.Checkpoints {
		if got := len(checkpoint.Inputs[0].TaprootScriptSpendSig); got != 2 {
			t.Errorf("checkpoint %d carries %d signatures, want 2", i, got)
		}
	}
}

// A key that is in none of the revealed leaves signing nothing is not a
// success. Reporting it stops a caller from submitting a transaction it thinks
// is signed.
func TestSigningRefusesAKeyThatBelongsToNoLeaf(t *testing.T) {
	c := contract()
	half := c.Terms.PayoutSats / 2

	r, err := c.ProposeRedemption(
		redemptionOutpoint(), half, c.Terms.PayoutSats-half, checkpointScript(t),
	)
	if err != nil {
		t.Fatalf("ProposeRedemption: %v", err)
	}

	if err := SignRedemption(privKey(0x55), r); err == nil {
		t.Fatal("signing with a stranger's key reported success")
	}
}

// Without leaf 2 there is nothing to propose, and saying so beats building a
// transaction whose witness can never be completed.
func TestRedemptionNeedsTheLeafToExist(t *testing.T) {
	c := contract()
	c.EnableMutualRedemption = false

	half := c.Terms.PayoutSats / 2
	if _, err := c.ProposeRedemption(
		redemptionOutpoint(), half, c.Terms.PayoutSats-half, checkpointScript(t),
	); err == nil {
		t.Fatal("proposed a redemption through a leaf that is not in the tree")
	}
}
