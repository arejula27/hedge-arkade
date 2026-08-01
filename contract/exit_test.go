package contract

import (
	"bytes"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// The exit leaf is the one path with no Arkade opcodes in it: a CSV and a
// 2-of-2, which is plain Bitcoin. So unlike the settlement covenant, it can be
// run through btcd's own consensus engine here, and these tests do — a witness
// that passes below is a witness a node accepts.

func privKey(b byte) *btcec.PrivateKey {
	k, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{b}, 32))
	return k
}

var (
	shortPriv   = privKey(0x21)
	longPriv    = privKey(0x22)
	servicePriv = privKey(0x26)
)

const (
	exitAmount = 20_000_000
	exitFee    = 500
)

func sweep(t *testing.T) *Sweep {
	t.Helper()
	s, err := NewSweep(shortKey, longKey, servicePriv.PubKey())
	if err != nil {
		t.Fatalf("NewSweep: %v", err)
	}
	return s
}

// contractOutpoint is the contract VTXO once it is on chain. The value is
// arbitrary; what matters is that signing and verifying agree on it.
func contractOutpoint() wire.OutPoint {
	var hash chainhash.Hash
	copy(hash[:], bytes.Repeat([]byte{0x11}, 32))
	return wire.OutPoint{Hash: hash, Index: 0}
}

func presign(t *testing.T, c Contract) (*ExitPackage, *Sweep) {
	t.Helper()

	s := sweep(t)
	pkg, err := c.PreSignExit(
		shortPriv, longPriv, contractOutpoint(), exitAmount, exitFee, s.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit: %v", err)
	}
	return pkg, s
}

// runEngine executes a spend against btcd under consensus rules. prevPkScript
// and amount describe the output being spent.
func runEngine(tx *wire.MsgTx, prevPkScript []byte, amount int64) error {
	prevOuts := txscript.NewCannedPrevOutputFetcher(prevPkScript, amount)

	engine, err := txscript.NewEngine(
		prevPkScript, tx, 0, txscript.StandardVerifyFlags, nil,
		txscript.NewTxSigHashes(tx, prevOuts), amount, prevOuts,
	)
	if err != nil {
		return err
	}
	return engine.Execute()
}

// exitPkScript is the contract output the exit spends.
func exitPkScript(t *testing.T, c Contract) []byte {
	t.Helper()
	pkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	return pkScript
}

func TestTheExitWitnessSatisfiesTheContract(t *testing.T) {
	c := contract()
	pkg, _ := presign(t, c)

	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if err := runEngine(signed, exitPkScript(t, c), exitAmount); err != nil {
		t.Fatalf("the pre-signed exit does not spend the contract: %v", err)
	}
}

// The exit works without the operator, so it must also work on a contract that
// never had a mutual-redemption leaf — where the exit sits at a different index
// and hangs off a different branch of the tree.
func TestTheExitWorksWithoutMutualRedemption(t *testing.T) {
	c := contract()
	c.EnableMutualRedemption = false

	pkg, _ := presign(t, c)
	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if err := runEngine(signed, exitPkScript(t, c), exitAmount); err != nil {
		t.Fatalf("the exit does not spend a contract without mutual redemption: %v", err)
	}
}

func TestTheExitCarriesTheDelayAsASequence(t *testing.T) {
	c := contract()
	pkg, _ := presign(t, c)

	want, err := arklib.BIP68Sequence(c.ExitDelay)
	if err != nil {
		t.Fatalf("BIP68Sequence: %v", err)
	}
	if got := pkg.Tx.TxIn[0].Sequence; got != want {
		t.Fatalf("sequence = %#x, want %#x", got, want)
	}
	// CSV is only enforced from version 2 onwards.
	if pkg.Tx.Version < 2 {
		t.Fatalf("transaction version = %d, want at least 2", pkg.Tx.Version)
	}
}

// The CSV is the whole reason the exit is safe to hand out at funding time:
// broadcasting it early has to fail. A sequence one step below the delay is the
// closest a party can get.
func TestTheExitCannotBeBroadcastEarly(t *testing.T) {
	c := contract()
	pkg, _ := presign(t, c)

	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	full := signed.TxIn[0].Sequence
	for _, tc := range []struct {
		name     string
		sequence uint32
	}{
		{"one step short of the delay", full - 1},
		{"no delay at all", 0},
		{"the delay disabled", wire.MaxTxInSequenceNum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			early := signed.Copy()
			early.TxIn[0].Sequence = tc.sequence

			if err := runEngine(early, exitPkScript(t, c), exitAmount); err == nil {
				t.Fatal("the exit was accepted before its delay had run")
			}
		})
	}
}

// The signature commits to the destination and the amount. Both are what stop
// the party holding the package from rewriting it in their favour.
func TestTheExitCannotBeRewritten(t *testing.T) {
	c := contract()
	pkg, _ := presign(t, c)

	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	thief, err := NewSweep(strangerKey, longKey, servicePriv.PubKey())
	if err != nil {
		t.Fatalf("NewSweep: %v", err)
	}

	for _, tc := range []struct {
		name   string
		tamper func(*wire.MsgTx)
	}{
		{"the destination is redirected", func(tx *wire.MsgTx) {
			tx.TxOut[0].PkScript = thief.PkScript
		}},
		{"the fee is raised, shrinking the payout", func(tx *wire.MsgTx) {
			tx.TxOut[0].Value -= 1_000
		}},
		{"a second output is added", func(tx *wire.MsgTx) {
			tx.TxOut[0].Value -= 10_000
			tx.AddTxOut(&wire.TxOut{Value: 10_000, PkScript: thief.PkScript})
		}},
		{"the input is repointed at another VTXO", func(tx *wire.MsgTx) {
			tx.TxIn[0].PreviousOutPoint.Index = 1
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := signed.Copy()
			tc.tamper(tampered)

			if err := runEngine(tampered, exitPkScript(t, c), exitAmount); err == nil {
				t.Fatal("a rewritten exit was accepted")
			}
		})
	}

	// The untampered original still passes, so the rejections above are the
	// edits and not something wrong with the package.
	if err := runEngine(signed, exitPkScript(t, c), exitAmount); err != nil {
		t.Fatalf("the original exit stopped working: %v", err)
	}
}

// Signing over the wrong input value produces a package that looks complete and
// fails only when it is finally needed.
func TestTheExitIsSignedOverTheRealAmount(t *testing.T) {
	c := contract()
	s := sweep(t)

	pkg, err := c.PreSignExit(
		shortPriv, longPriv, contractOutpoint(), exitAmount+1, exitFee, s.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit: %v", err)
	}
	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if err := runEngine(signed, exitPkScript(t, c), exitAmount); err == nil {
		t.Fatal("an exit signed over the wrong amount was accepted")
	}
}

func TestBothPartiesMustSignTheExit(t *testing.T) {
	c := contract()
	pkg, _ := presign(t, c)

	for _, tc := range []struct {
		name   string
		damage func(*ExitPackage)
	}{
		{"the short is missing", func(p *ExitPackage) { p.ShortSig = nil }},
		{"the long is missing", func(p *ExitPackage) { p.LongSig = nil }},
		{"the signatures are swapped", func(p *ExitPackage) {
			p.ShortSig, p.LongSig = p.LongSig, p.ShortSig
		}},
		{"a signature is garbage", func(p *ExitPackage) {
			p.LongSig = bytes.Repeat([]byte{0xff}, 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := &ExitPackage{
				Tx:       pkg.Tx.Copy(),
				ShortSig: append([]byte(nil), pkg.ShortSig...),
				LongSig:  append([]byte(nil), pkg.LongSig...),
				Amount:   pkg.Amount,
			}
			tc.damage(broken)

			signed, err := c.Finalize(broken)
			if err != nil {
				return // refused before it could be broadcast, which is better
			}
			if err := runEngine(signed, exitPkScript(t, c), exitAmount); err == nil {
				t.Fatal("an incomplete exit was accepted")
			}
		})
	}
}

// A signature from a third party is not a signature from the counterparty, even
// though the transaction it produces is otherwise identical.
func TestTheExitRejectsAStranger(t *testing.T) {
	c := contract()
	s := sweep(t)
	stranger := privKey(0x25) // strangerKey's private half

	if _, err := c.PreSignExit(
		stranger, longPriv, contractOutpoint(), exitAmount, exitFee, s.PkScript,
	); err == nil {
		t.Fatal("PreSignExit accepted a key that is not the contract's short")
	}
	if _, err := c.PreSignExit(
		shortPriv, stranger, contractOutpoint(), exitAmount, exitFee, s.PkScript,
	); err == nil {
		t.Fatal("PreSignExit accepted a key that is not the contract's long")
	}

	// And if the check were removed, the signature itself would still not verify.
	tx, err := c.ExitTx(contractOutpoint(), exitAmount, exitFee, s.PkScript)
	if err != nil {
		t.Fatalf("ExitTx: %v", err)
	}
	strangerSig, err := c.SignExit(stranger, tx, exitAmount)
	if err != nil {
		t.Fatalf("SignExit: %v", err)
	}
	longSig, err := c.SignExit(longPriv, tx, exitAmount)
	if err != nil {
		t.Fatalf("SignExit: %v", err)
	}

	signed, err := c.Finalize(&ExitPackage{
		Tx: tx, ShortSig: strangerSig, LongSig: longSig, Amount: exitAmount,
	})
	if err != nil {
		return
	}
	if err := runEngine(signed, exitPkScript(t, c), exitAmount); err == nil {
		t.Fatal("a stranger's signature satisfied the exit")
	}
}

func TestExitTxRefusesAnUnbroadcastableSweep(t *testing.T) {
	c := contract()
	s := sweep(t)

	for _, tc := range []struct {
		name            string
		amount, feeSats int64
		pkScript        []byte
	}{
		{"no amount", 0, exitFee, s.PkScript},
		{"a negative amount", -1, exitFee, s.PkScript},
		{"a negative fee", exitAmount, -1, s.PkScript},
		{"a fee that eats the whole output", exitAmount, exitAmount, s.PkScript},
		{"a fee that leaves dust", exitAmount, exitAmount - Dust + 1, s.PkScript},
		{"no destination", exitAmount, exitFee, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.ExitTx(
				contractOutpoint(), tc.amount, tc.feeSats, tc.pkScript,
			); err == nil {
				t.Fatal("ExitTx built a transaction that cannot be broadcast")
			}
		})
	}

	// Exactly dust is the boundary and is allowed.
	if _, err := c.ExitTx(
		contractOutpoint(), exitAmount, exitAmount-Dust, s.PkScript,
	); err != nil {
		t.Fatalf("ExitTx refused an output of exactly dust: %v", err)
	}
}

func TestExitTxPaysEverythingButTheFee(t *testing.T) {
	c := contract()
	s := sweep(t)

	tx, err := c.ExitTx(contractOutpoint(), exitAmount, exitFee, s.PkScript)
	if err != nil {
		t.Fatalf("ExitTx: %v", err)
	}

	if len(tx.TxIn) != 1 {
		t.Fatalf("the exit has %d inputs, want 1", len(tx.TxIn))
	}
	if len(tx.TxOut) != 1 {
		t.Fatalf("the exit has %d outputs, want 1", len(tx.TxOut))
	}
	if got := tx.TxOut[0].Value; got != exitAmount-exitFee {
		t.Fatalf("the exit pays %d, want %d", got, exitAmount-exitFee)
	}
	if !bytes.Equal(tx.TxOut[0].PkScript, s.PkScript) {
		t.Fatal("the exit does not pay the sweep")
	}
	if tx.TxIn[0].PreviousOutPoint != contractOutpoint() {
		t.Fatal("the exit does not spend the contract outpoint")
	}
}

// The exit lands in a 2-of-3, so the money is only really out if two of the
// three can move it again. This runs each pair through the engine.
func TestAnyTwoOfTheThreeCanSpendTheSweep(t *testing.T) {
	s := sweep(t)

	spend := func(t *testing.T, signers ...*btcec.PrivateKey) error {
		t.Helper()

		var hash chainhash.Hash
		copy(hash[:], bytes.Repeat([]byte{0x33}, 32))

		tx := wire.NewMsgTx(2)
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: hash}})
		tx.AddTxOut(&wire.TxOut{Value: exitAmount - 1_000, PkScript: s.PkScript})

		prevOuts := txscript.NewCannedPrevOutputFetcher(s.PkScript, exitAmount)
		digest, err := txscript.CalcTapscriptSignaturehash(
			txscript.NewTxSigHashes(tx, prevOuts), txscript.SigHashDefault,
			tx, 0, prevOuts, txscript.NewBaseTapLeaf(s.Leaf),
		)
		if err != nil {
			t.Fatalf("sighash: %v", err)
		}

		sigs := map[string][]byte{}
		for _, key := range signers {
			sig, err := schnorr.Sign(key, digest)
			if err != nil {
				t.Fatalf("signing: %v", err)
			}
			sigs[XOnlyHex(key.PubKey())] = sig.Serialize()
		}

		witness, err := s.Witness(sigs)
		if err != nil {
			return err
		}
		tx.TxIn[0].Witness = witness

		return runEngine(tx, s.PkScript, exitAmount)
	}

	for _, tc := range []struct {
		name    string
		signers []*btcec.PrivateKey
	}{
		{"the two parties", []*btcec.PrivateKey{shortPriv, longPriv}},
		{"the short and the service", []*btcec.PrivateKey{shortPriv, servicePriv}},
		{"the long and the service", []*btcec.PrivateKey{longPriv, servicePriv}},
		// The script ends in NUMEQUAL, so a third signature would make the
		// count 3 and fail. Witness drops the extra rather than handing back a
		// transaction that cannot be spent.
		{"all three", []*btcec.PrivateKey{shortPriv, longPriv, servicePriv}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := spend(t, tc.signers...); err != nil {
				t.Fatalf("%s could not spend the sweep: %v", tc.name, err)
			}
		})
	}

	for _, tc := range []struct {
		name    string
		signers []*btcec.PrivateKey
	}{
		{"the short alone", []*btcec.PrivateKey{shortPriv}},
		{"the long alone", []*btcec.PrivateKey{longPriv}},
		{"the service alone", []*btcec.PrivateKey{servicePriv}},
		{"nobody", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := spend(t, tc.signers...); err == nil {
				t.Fatalf("%s spent the sweep alone", tc.name)
			}
		})
	}
}

func TestSweepRejectsMissingKeys(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		short, long, service *btcec.PublicKey
	}{
		{"no short", nil, longKey, servicePriv.PubKey()},
		{"no long", shortKey, nil, servicePriv.PubKey()},
		{"no service", shortKey, longKey, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSweep(tc.short, tc.long, tc.service); err == nil {
				t.Fatal("NewSweep built a destination with a missing key")
			}
		})
	}
}

// Every key in the sweep changes the address, so the address is proof of who
// the three signers are.
func TestEveryKeyChangesTheSweepAddress(t *testing.T) {
	base := sweep(t)

	for _, tc := range []struct {
		name                 string
		short, long, service *btcec.PublicKey
	}{
		{"another short", strangerKey, longKey, servicePriv.PubKey()},
		{"another long", shortKey, strangerKey, servicePriv.PubKey()},
		{"another service", shortKey, longKey, strangerKey},
		{"the parties swapped", longKey, shortKey, servicePriv.PubKey()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other, err := NewSweep(tc.short, tc.long, tc.service)
			if err != nil {
				t.Fatalf("NewSweep: %v", err)
			}
			if bytes.Equal(base.PkScript, other.PkScript) {
				t.Fatal("the sweep address did not change")
			}
		})
	}
}

func TestFinalizeRejectsAnEmptyPackage(t *testing.T) {
	c := contract()

	if _, err := c.Finalize(nil); err == nil {
		t.Fatal("Finalize accepted no package at all")
	}
	if _, err := c.Finalize(&ExitPackage{}); err == nil {
		t.Fatal("Finalize accepted a package with no transaction")
	}
}

// The exit spends the exit leaf and nothing else. If the sighash were taken
// over another leaf the signatures would be over the wrong tapleaf hash, which
// the engine catches — but so does comparing the revealed script directly.
func TestTheExitRevealsTheExitLeaf(t *testing.T) {
	c := contract()
	pkg, _ := presign(t, c)

	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	proof, err := c.Tapscript(LeafExit)
	if err != nil {
		t.Fatalf("Tapscript: %v", err)
	}

	witness := signed.TxIn[0].Witness
	if len(witness) < 2 {
		t.Fatalf("the witness has %d elements", len(witness))
	}
	if !bytes.Equal(witness[len(witness)-2], proof.Script) {
		t.Fatal("the witness does not reveal the exit leaf")
	}
	if !bytes.Equal(witness[len(witness)-1], proof.ControlBlock) {
		t.Fatal("the witness does not carry the exit leaf's control block")
	}

	// The other leaves are not what got revealed.
	for _, leaf := range []Leaf{LeafSettlement, LeafMutualRedemption} {
		other, err := c.Tapscript(leaf)
		if err != nil {
			t.Fatalf("Tapscript(%s): %v", leaf, err)
		}
		if bytes.Equal(witness[len(witness)-2], other.Script) {
			t.Fatalf("the exit revealed the %s leaf", leaf)
		}
	}
}
