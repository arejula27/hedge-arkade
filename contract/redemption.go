package contract

import (
	"bytes"
	"fmt"

	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

// Closing the contract early, through leaf 2.
//
// Leaf 2 constrains nothing: the split is whatever the two parties sign, which
// is the whole point of it — an oracle-priced close cannot unwind a funding
// error. What that leaves is a usability problem rather than a protocol one.
// Someone has to turn "we agree to close at X" into a transaction, and neither
// party should have to build one to check the other's.
//
// So the proposer builds it and each party verifies it. The verification is
// what makes the proposer untrusted: it rebuilds the transaction from the
// contract and the agreed split and demands a match. A party that signs without
// verifying is trusting whoever proposed, and nothing here can help them.

// Redemption is an early close built and ready to sign.
type Redemption struct {
	ArkTx       *psbt.Packet
	Checkpoints []*psbt.Packet

	ShortSats int64
	LongSats  int64

	// Price is the clamped oracle price the split was computed from, and the
	// message it was read out of. All three are zero for a split the parties
	// simply agreed on, which leaf 2 exists to allow.
	Price     int64
	Message   []byte
	Signature []byte
}

// RedemptionInput points at the contract VTXO and reveals leaf 2.
func (c Contract) RedemptionInput(
	outpoint wire.OutPoint,
) (offchain.VtxoInput, error) {
	proof, err := c.Tapscript(LeafMutualRedemption)
	if err != nil {
		return offchain.VtxoInput{}, err
	}
	control, err := txscript.ParseControlBlock(proof.ControlBlock)
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("parsing the control block: %w", err)
	}
	vtxo, err := c.VtxoScript()
	if err != nil {
		return offchain.VtxoInput{}, err
	}
	revealed, err := vtxo.Encode()
	if err != nil {
		return offchain.VtxoInput{}, fmt.Errorf("encoding the vtxo script: %w", err)
	}

	return offchain.VtxoInput{
		Outpoint: &outpoint,
		Tapscript: &waddrmgr.Tapscript{
			ControlBlock:   control,
			RevealedScript: proof.Script,
		},
		Amount:             c.Terms.PayoutSats,
		RevealedTapscripts: revealed,
	}, nil
}

// ProposeRedemption builds the early close that pays the given split.
//
// checkpointTapscript is the operator's, from its GetInfo. It is a parameter
// rather than a constant because it is the operator's to choose and a contract
// knows nothing about it.
func (c Contract) ProposeRedemption(
	outpoint wire.OutPoint, short, long int64, checkpointTapscript []byte,
) (*Redemption, error) {
	if err := c.checkSplit(short, long); err != nil {
		return nil, err
	}

	input, err := c.RedemptionInput(outpoint)
	if err != nil {
		return nil, err
	}

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{input},
		[]*wire.TxOut{
			{Value: short, PkScript: c.Terms.ShortLockScript},
			{Value: long, PkScript: c.Terms.LongLockScript},
		},
		checkpointTapscript,
	)
	if err != nil {
		return nil, fmt.Errorf("building the redemption: %w", err)
	}

	return &Redemption{
		ArkTx: arkTx, Checkpoints: checkpoints,
		ShortSats: short, LongSats: long,
	}, nil
}

// ProposeRedemptionAt builds the early close at the oracle's price, using the
// same formula the covenant would apply at maturity.
//
// This is a convenience, not a rule. Nothing in leaf 2 requires the split to
// come from a price, and a party is free to refuse one that does.
func (c Contract) ProposeRedemptionAt(
	outpoint wire.OutPoint, checkpointTapscript []byte, message, signature []byte,
) (*Redemption, error) {
	price, err := c.PriceFrom(message, signature)
	if err != nil {
		return nil, err
	}

	short, long, err := SettlementSplit(c.Terms, price)
	if err != nil {
		return nil, err
	}

	r, err := c.ProposeRedemption(outpoint, short, long, checkpointTapscript)
	if err != nil {
		return nil, err
	}

	r.Price = min(max(price, c.Terms.LowLiquidationPrice), c.Terms.HighLiquidationPrice)
	r.Message, r.Signature = message, signature
	return r, nil
}

// checkSplit holds the two rules a redemption cannot break. Arkade conserves
// value exactly — arkd rebuilds the transaction and refuses an input amount
// that differs from the output amount — and an output below dust is one arkd
// will not create.
func (c Contract) checkSplit(short, long int64) error {
	if short < Dust || long < Dust {
		return fmt.Errorf("split %d/%d puts a side below the dust floor of %d",
			short, long, Dust)
	}
	if short+long != c.Terms.PayoutSats {
		return fmt.Errorf("split %d/%d sums to %d, and the contract holds %d",
			short, long, short+long, c.Terms.PayoutSats)
	}
	return nil
}

// VerifyRedemption is what a party runs before signing: rebuild the whole
// proposal and require a match.
//
// outpoint is the contract the party means to close, and it is a parameter
// rather than something read out of the proposal on purpose. Taking it from the
// proposal would verify that the transaction is internally consistent while
// saying nothing about which contract it closes.
//
// The split is checked first so a disagreement reads as "you are paying me the
// wrong amount" rather than as a txid mismatch, and the txid is checked last
// because it catches everything the field-by-field comparison does not.
func (c Contract) VerifyRedemption(
	r *Redemption, outpoint wire.OutPoint, checkpointTapscript []byte,
) error {
	if r == nil || r.ArkTx == nil {
		return fmt.Errorf("no redemption to verify")
	}
	if len(r.ArkTx.UnsignedTx.TxIn) != 1 {
		return fmt.Errorf("the redemption spends %d inputs, want 1",
			len(r.ArkTx.UnsignedTx.TxIn))
	}

	// The ark transaction spends the checkpoint; the checkpoint is what spends
	// the contract. So this is where the contract's outpoint has to be checked.
	if len(r.Checkpoints) != 1 {
		return fmt.Errorf("the redemption carries %d checkpoints, want 1", len(r.Checkpoints))
	}
	if len(r.Checkpoints[0].UnsignedTx.TxIn) != 1 {
		return fmt.Errorf("the checkpoint spends %d inputs, want 1",
			len(r.Checkpoints[0].UnsignedTx.TxIn))
	}
	if spent := r.Checkpoints[0].UnsignedTx.TxIn[0].PreviousOutPoint; spent != outpoint {
		return fmt.Errorf("the redemption closes %s, not the contract at %s", spent, outpoint)
	}

	short, long := r.ShortSats, r.LongSats

	// A priced proposal is recomputed from the oracle's own bytes, so the
	// proposer cannot claim a price the oracle never signed.
	if len(r.Signature) > 0 {
		price, err := c.PriceFrom(r.Message, r.Signature)
		if err != nil {
			return fmt.Errorf("checking the oracle's signature: %w", err)
		}
		short, long, err = SettlementSplit(c.Terms, price)
		if err != nil {
			return err
		}
		if short != r.ShortSats || long != r.LongSats {
			return fmt.Errorf("the proposal pays %d/%d, the oracle's price says %d/%d",
				r.ShortSats, r.LongSats, short, long)
		}
	}

	if err := c.checkSplit(short, long); err != nil {
		return err
	}

	for i, want := range []*wire.TxOut{
		{Value: short, PkScript: c.Terms.ShortLockScript},
		{Value: long, PkScript: c.Terms.LongLockScript},
	} {
		if i >= len(r.ArkTx.UnsignedTx.TxOut) {
			return fmt.Errorf("the redemption has no output %d", i)
		}
		got := r.ArkTx.UnsignedTx.TxOut[i]
		if got.Value != want.Value {
			return fmt.Errorf("output %d pays %d sats, want %d", i, got.Value, want.Value)
		}
		if !bytes.Equal(got.PkScript, want.PkScript) {
			return fmt.Errorf("output %d pays the wrong recipient", i)
		}
	}

	expected, err := c.ProposeRedemption(outpoint, short, long, checkpointTapscript)
	if err != nil {
		return fmt.Errorf("rebuilding the redemption: %w", err)
	}

	if expected.ArkTx.UnsignedTx.TxID() != r.ArkTx.UnsignedTx.TxID() {
		return fmt.Errorf("the redemption is not the transaction this split builds")
	}
	for i, want := range expected.Checkpoints {
		if want.UnsignedTx.TxID() != r.Checkpoints[i].UnsignedTx.TxID() {
			return fmt.Errorf("checkpoint %d is not the one this split builds", i)
		}
	}

	return nil
}

// SignRedemption adds one party's signature everywhere it is needed.
//
// Both transactions need it. The checkpoint spends the contract through leaf 2,
// and the checkpoint output is built from that same closure
// (`offchain/tx.go:184`), so the ark transaction that spends it reveals leaf 2
// as well.
func SignRedemption(key *btcec.PrivateKey, r *Redemption) error {
	if r == nil || r.ArkTx == nil {
		return fmt.Errorf("no redemption to sign")
	}

	if err := SignTapscript(key, r.ArkTx); err != nil {
		return fmt.Errorf("signing the ark transaction: %w", err)
	}
	for i, checkpoint := range r.Checkpoints {
		if err := SignTapscript(key, checkpoint); err != nil {
			return fmt.Errorf("signing checkpoint %d: %w", i, err)
		}
	}
	return nil
}

// SignTapscript adds key's signature to every input of the packet whose
// revealed leaf names it, and reports how many that was.
//
// Callers need this on its own for the checkpoints arkd hands back: FinalizeTx
// re-verifies every key in the revealed leaf (`service.go:1236`), and no wallet
// holds the contract's keys, so they have to sign that round too.
func SignTapscript(key *btcec.PrivateKey, packet *psbt.Packet) error {
	if key == nil {
		return fmt.Errorf("signing key is nil")
	}
	if packet == nil {
		return fmt.Errorf("no packet to sign")
	}

	mine := schnorr.SerializePubKey(key.PubKey())

	prevOuts := txscript.NewMultiPrevOutFetcher(nil)
	for i, in := range packet.Inputs {
		if in.WitnessUtxo == nil {
			return fmt.Errorf("input %d has no witness utxo", i)
		}
		prevOuts.AddPrevOut(packet.UnsignedTx.TxIn[i].PreviousOutPoint, in.WitnessUtxo)
	}
	sigHashes := txscript.NewTxSigHashes(packet.UnsignedTx, prevOuts)

	signed := 0
	for i, in := range packet.Inputs {
		for _, leaf := range in.TaprootLeafScript {
			if !leafNamesKey(leaf.Script, mine) {
				continue
			}

			digest, err := txscript.CalcTapscriptSignaturehash(
				sigHashes, in.SighashType, packet.UnsignedTx, i, prevOuts,
				txscript.NewBaseTapLeaf(leaf.Script),
			)
			if err != nil {
				return fmt.Errorf("sighash for input %d: %w", i, err)
			}

			sig, err := schnorr.Sign(key, digest)
			if err != nil {
				return fmt.Errorf("signing input %d: %w", i, err)
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
		return fmt.Errorf("key %x signs none of the revealed leaves", mine)
	}
	return nil
}

// leafNamesKey reports whether the closure names the key, using arkd's own
// decoder so a leaf it would not recognise is skipped rather than guessed at.
func leafNamesKey(leaf, xOnlyKey []byte) bool {
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
