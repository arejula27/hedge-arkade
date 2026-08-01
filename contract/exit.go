package contract

import (
	"bytes"
	"fmt"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// Sweep is the destination the unilateral exit pays to: a 2-of-3 between the
// two parties and the service.
//
// It is a plain Bitcoin output, not an Arkade closure, so the threshold is
// allowed. Every closure arkd can decode is strictly N-of-N, which is why the
// 2-of-3 lives here rather than in a leaf: the leaf is what Arkade constrains,
// the destination is what Bitcoin validates.
//
// The 2-of-3 is what makes the exit safe for two parties. A single-signature
// exit — what every example contract in the compiler uses — would let whoever
// broadcast it walk away with the counterparty's collateral. The service is the
// third key so that a party who loses their counterparty is not stuck.
type Sweep struct {
	// Keys in script order. Witnesses are built against this order.
	Keys []*btcec.PublicKey

	// Leaf is the 2-of-3 tapscript, ControlBlock proves it is the tree's only
	// leaf, and PkScript is what the exit transaction pays to.
	Leaf         []byte
	ControlBlock []byte
	PkScript     []byte
}

// SweepThreshold is how many of the three have to sign to move the money once
// it has left the contract.
const SweepThreshold = 2

// NewSweep builds the 2-of-3 destination. Everything needed to spend it later
// comes back with it: an exit that lands somewhere the parties cannot reopen is
// the same as no exit at all.
func NewSweep(short, long, service *btcec.PublicKey) (*Sweep, error) {
	keys := []*btcec.PublicKey{short, long, service}
	for i, name := range []string{"short", "long", "service"} {
		if keys[i] == nil {
			return nil, fmt.Errorf("%s public key is nil", name)
		}
	}

	// BIP342 k-of-n: CHECKSIG on the first key, CHECKSIGADD on the rest, and
	// the running total compared against the threshold.
	builder := txscript.NewScriptBuilder().
		AddData(schnorr.SerializePubKey(short)).
		AddOp(txscript.OP_CHECKSIG)
	for _, key := range keys[1:] {
		builder.AddData(schnorr.SerializePubKey(key)).AddOp(txscript.OP_CHECKSIGADD)
	}
	leaf, err := builder.AddInt64(SweepThreshold).AddOp(txscript.OP_NUMEQUAL).Script()
	if err != nil {
		return nil, fmt.Errorf("building the 2-of-3 leaf: %w", err)
	}

	// The same unspendable internal key the contract uses, so there is no
	// key-path spend around the threshold.
	tapLeaf := txscript.NewBaseTapLeaf(leaf)
	tree := txscript.AssembleTaprootScriptTree(tapLeaf)
	root := tree.RootNode.TapHash()
	internal := arkscript.UnspendableKey()

	pkScript, err := arkscript.P2TRScript(txscript.ComputeTaprootOutputKey(internal, root[:]))
	if err != nil {
		return nil, fmt.Errorf("building the sweep script: %w", err)
	}

	proof := tree.LeafMerkleProofs[0]
	control := proof.ToControlBlock(internal)
	controlBytes, err := control.ToBytes()
	if err != nil {
		return nil, fmt.Errorf("serialising the sweep control block: %w", err)
	}

	return &Sweep{
		Keys: keys, Leaf: leaf, ControlBlock: controlBytes, PkScript: pkScript,
	}, nil
}

// sighash is the digest the sweep's signers sign: a taproot script-path
// signature hash over the 2-of-3 leaf. amount is what the sweep output holds,
// since taproot signs over input values.
func (s *Sweep) sighash(tx *wire.MsgTx, amount int64) ([]byte, error) {
	if tx == nil || len(tx.TxIn) != 1 {
		return nil, fmt.Errorf("a sweep spend must have exactly one input")
	}

	prevOuts := txscript.NewCannedPrevOutputFetcher(s.PkScript, amount)

	return txscript.CalcTapscriptSignaturehash(
		txscript.NewTxSigHashes(tx, prevOuts),
		txscript.SigHashDefault,
		tx, 0, prevOuts,
		txscript.NewBaseTapLeaf(s.Leaf),
	)
}

// Witness assembles the witness that spends the 2-of-3, given signatures keyed
// by x-only pubkey. A key with no signature contributes an empty element, which
// CHECKSIGADD reads as a zero rather than a failure — that is how a threshold
// below n is satisfied at all.
//
// Order is the reverse of the script's: the first key the script checks is the
// last thing pushed, so it sits on top of the stack.
//
// Exactly SweepThreshold signatures go in, even when more are supplied. The
// script ends in NUMEQUAL, the canonical BIP342 threshold, so a third valid
// signature makes the running total 3 and the spend fails. Choosing here rather
// than making the caller choose keeps that from being a way to lose money.
func (s *Sweep) Witness(sigs map[string][]byte) (wire.TxWitness, error) {
	// In script order, so which signatures are dropped does not depend on map
	// iteration and two callers build the same witness.
	use := make(map[int][]byte, SweepThreshold)
	for i, key := range s.Keys {
		if len(use) == SweepThreshold {
			break
		}
		if sig := sigs[XOnlyHex(key)]; len(sig) > 0 {
			use[i] = sig
		}
	}
	if len(use) < SweepThreshold {
		return nil, fmt.Errorf(
			"the sweep needs %d signatures, got %d", SweepThreshold, len(use),
		)
	}

	witness := make(wire.TxWitness, 0, len(s.Keys)+2)
	for i := len(s.Keys) - 1; i >= 0; i-- {
		witness = append(witness, use[i])
	}

	return append(witness, s.Leaf, s.ControlBlock), nil
}

// ExitPackage is the pre-signed unilateral exit: one transaction, both parties'
// signatures, collected at funding time.
//
// Pre-signing is what makes the exit unilateral. From the moment it exists,
// either party can broadcast it alone once the CSV matures, with nothing left
// to negotiate. It is also what prevents theft: exactly one signed transaction
// exists and its destination is fixed, so redirecting it would need the
// counterparty to sign a different one.
type ExitPackage struct {
	Tx       *wire.MsgTx
	ShortSig []byte
	LongSig  []byte

	// Amount is what the contract VTXO held. Taproot signs over input values,
	// so verifying or re-finalising needs it.
	Amount int64
}

// ExitTx builds the unsigned sweep. outpoint is the contract VTXO once it is
// onchain, and feeSats is what is left for the miner — the exit is a real
// Bitcoin transaction and has to pay for itself.
func (c Contract) ExitTx(
	outpoint wire.OutPoint, amount, feeSats int64, sweepPkScript []byte,
) (*wire.MsgTx, error) {
	switch {
	case amount <= 0:
		return nil, fmt.Errorf("contract amount must be positive, got %d", amount)
	case feeSats < 0:
		return nil, fmt.Errorf("fee cannot be negative, got %d", feeSats)
	case amount-feeSats < Dust:
		return nil, fmt.Errorf("sweeping %d sats with a fee of %d leaves less than dust (%d)",
			amount, feeSats, Dust)
	case len(sweepPkScript) == 0:
		return nil, fmt.Errorf("sweep script is empty")
	}

	sequence, err := arklib.BIP68Sequence(c.ExitDelay)
	if err != nil {
		return nil, fmt.Errorf("encoding the exit delay: %w", err)
	}

	tx := wire.NewMsgTx(2)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: outpoint, Sequence: sequence})
	tx.AddTxOut(&wire.TxOut{Value: amount - feeSats, PkScript: sweepPkScript})

	return tx, nil
}

// exitLeaf returns the exit tapscript and the control block proving it belongs
// to the contract's tree.
func (c Contract) exitLeaf() (*arklib.TaprootMerkleProof, error) {
	return c.Tapscript(LeafExit)
}

// ExitSighash is the digest both parties sign: a taproot script-path signature
// hash over the exit leaf.
func (c Contract) ExitSighash(tx *wire.MsgTx, amount int64) ([]byte, error) {
	if tx == nil || len(tx.TxIn) != 1 {
		return nil, fmt.Errorf("the exit transaction must have exactly one input")
	}

	proof, err := c.exitLeaf()
	if err != nil {
		return nil, err
	}

	pkScript, err := c.PkScript()
	if err != nil {
		return nil, err
	}

	prevOuts := txscript.NewCannedPrevOutputFetcher(pkScript, amount)

	return txscript.CalcTapscriptSignaturehash(
		txscript.NewTxSigHashes(tx, prevOuts),
		txscript.SigHashDefault,
		tx, 0, prevOuts,
		txscript.NewBaseTapLeaf(proof.Script),
	)
}

// SignExit produces one party's signature over the exit transaction.
func (c Contract) SignExit(key *btcec.PrivateKey, tx *wire.MsgTx, amount int64) ([]byte, error) {
	if key == nil {
		return nil, fmt.Errorf("signing key is nil")
	}

	digest, err := c.ExitSighash(tx, amount)
	if err != nil {
		return nil, err
	}

	sig, err := schnorr.Sign(key, digest)
	if err != nil {
		return nil, fmt.Errorf("signing the exit: %w", err)
	}
	return sig.Serialize(), nil
}

// PreSignExit builds the exit transaction and has both parties sign it. This is
// what happens at funding: after it returns, neither party depends on the other
// or on the operator to get out.
func (c Contract) PreSignExit(
	short, long *btcec.PrivateKey,
	outpoint wire.OutPoint, amount, feeSats int64, sweepPkScript []byte,
) (*ExitPackage, error) {
	tx, err := c.ExitTx(outpoint, amount, feeSats, sweepPkScript)
	if err != nil {
		return nil, err
	}

	if err := c.signerMatches("short", short, c.Keys.Short); err != nil {
		return nil, err
	}
	if err := c.signerMatches("long", long, c.Keys.Long); err != nil {
		return nil, err
	}

	shortSig, err := c.SignExit(short, tx, amount)
	if err != nil {
		return nil, fmt.Errorf("short: %w", err)
	}
	longSig, err := c.SignExit(long, tx, amount)
	if err != nil {
		return nil, fmt.Errorf("long: %w", err)
	}

	return &ExitPackage{Tx: tx, ShortSig: shortSig, LongSig: longSig, Amount: amount}, nil
}

// signerMatches refuses a key that is not the one the contract was built
// around. A signature from the wrong key produces a package that looks complete
// and fails only when it is finally needed.
func (c Contract) signerMatches(name string, key *btcec.PrivateKey, expected *btcec.PublicKey) error {
	if key == nil {
		return fmt.Errorf("%s signing key is nil", name)
	}
	if expected == nil {
		return fmt.Errorf("%s public key is nil", name)
	}
	if !bytes.Equal(
		schnorr.SerializePubKey(key.PubKey()), schnorr.SerializePubKey(expected),
	) {
		return fmt.Errorf("%s signing key does not match the contract's %s key", name, name)
	}
	return nil
}

// Finalize attaches the witness, leaving a transaction that can be broadcast as
// it stands.
func (c Contract) Finalize(pkg *ExitPackage) (*wire.MsgTx, error) {
	if pkg == nil || pkg.Tx == nil {
		return nil, fmt.Errorf("no exit package")
	}

	proof, err := c.exitLeaf()
	if err != nil {
		return nil, err
	}

	index, err := c.leafIndex(LeafExit)
	if err != nil {
		return nil, err
	}
	closures, err := c.Closures()
	if err != nil {
		return nil, err
	}

	exit, ok := closures[index].(*arkscript.CSVMultisigClosure)
	if !ok {
		return nil, fmt.Errorf("the exit leaf is a %T, not a CSV multisig", closures[index])
	}

	// ark-lib orders the signatures for us — reverse of the pubkey order, which
	// is what the script's CHECKSIGVERIFY chain consumes.
	witness, err := exit.Witness(proof.ControlBlock, map[string][]byte{
		XOnlyHex(c.Keys.Short): pkg.ShortSig,
		XOnlyHex(c.Keys.Long):  pkg.LongSig,
	})
	if err != nil {
		return nil, fmt.Errorf("building the exit witness: %w", err)
	}

	signed := pkg.Tx.Copy()
	signed.TxIn[0].Witness = witness
	return signed, nil
}

// XOnlyHex is how a public key is named in the signature maps Witness and
// FinalizeArbitration take. Exported because anything assembling those
// signatures lives outside this package.
func XOnlyHex(key *btcec.PublicKey) string {
	return fmt.Sprintf("%x", schnorr.SerializePubKey(key))
}
