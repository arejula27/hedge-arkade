package covenant

import (
	"bytes"
	"encoding/hex"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
)

// Fixed keys, as with oracleKey: a failure has to reproduce exactly.
func key(b byte) *btcec.PublicKey {
	k, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{b}, 32))
	return k.PubKey()
}

var (
	shortKey    = key(0x21)
	longKey     = key(0x22)
	arkdKey     = key(0x23)
	emulatorKey = key(0x24)
	strangerKey = key(0x25)
)

// exitDelay is a day. Seconds, and a multiple of 512 as BIP68 requires.
var exitDelay = arklib.RelativeLocktime{
	Type:  arklib.LocktimeTypeSecond,
	Value: 86_528, // 169 × 512
}

func contract() Contract {
	return Contract{
		Terms: standard,
		Keys: Keys{
			Short:          shortKey,
			Long:           longKey,
			ArkdSigner:     arkdKey,
			EmulatorSigner: emulatorKey,
		},
		ExitDelay:              exitDelay,
		EnableMutualRedemption: true,
	}
}

func xonly(k *btcec.PublicKey) string {
	return hex.EncodeToString(schnorr.SerializePubKey(k))
}

// keysOf returns the closure's pubkeys, x-only and hex, in script order.
func keysOf(t *testing.T, c arkscript.Closure) []string {
	t.Helper()

	var pubkeys []*btcec.PublicKey
	switch v := c.(type) {
	case *arkscript.MultisigClosure:
		pubkeys = v.PubKeys
	case *arkscript.CSVMultisigClosure:
		pubkeys = v.PubKeys
	default:
		t.Fatalf("unexpected closure type %T", c)
	}

	out := make([]string, len(pubkeys))
	for i, k := range pubkeys {
		out[i] = xonly(k)
	}
	return out
}

func closures(t *testing.T, c Contract) []arkscript.Closure {
	t.Helper()
	cl, err := c.Closures()
	if err != nil {
		t.Fatalf("Closures: %v", err)
	}
	return cl
}

func TestLeavesCarryTheRightKeys(t *testing.T) {
	c := contract()
	cl := closures(t, c)

	if len(cl) != 3 {
		t.Fatalf("got %d leaves, want 3", len(cl))
	}

	settlementKey, err := c.SettlementKey()
	if err != nil {
		t.Fatalf("SettlementKey: %v", err)
	}

	for _, tc := range []struct {
		leaf Leaf
		want []string
	}{
		// No party key on settlement: whoever holds two adjacent oracle
		// messages may settle, and the covenant fixes what they may pay.
		{LeafSettlement, []string{xonly(arkdKey), xonly(settlementKey)}},
		{LeafMutualRedemption, []string{xonly(shortKey), xonly(longKey), xonly(arkdKey)}},
		// No operator on the exit, or it would not survive the operator.
		{LeafExit, []string{xonly(shortKey), xonly(longKey)}},
	} {
		t.Run(tc.leaf.String(), func(t *testing.T) {
			index, err := c.leafIndex(tc.leaf)
			if err != nil {
				t.Fatalf("leafIndex: %v", err)
			}

			got := keysOf(t, cl[index])
			if len(got) != len(tc.want) {
				t.Fatalf("got %d keys %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("key %d = %s, want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The emulator never appears untweaked. If it did, it would be a signer on a
// path with no covenant attached to it.
func TestSettlementLeafCarriesNoUntweakedEmulatorKey(t *testing.T) {
	c := contract()
	for _, k := range keysOf(t, closures(t, c)[0]) {
		if k == xonly(emulatorKey) {
			t.Fatal("settlement leaf carries the raw emulator key")
		}
	}
}

// The tweak is what binds the leaf to the covenant. Change any contract
// parameter and the key in the tapscript changes with it, so a leaf built for
// one set of terms cannot be reused for another.
func TestSettlementKeyCommitsToTheScript(t *testing.T) {
	c := contract()

	base, err := c.SettlementKey()
	if err != nil {
		t.Fatalf("SettlementKey: %v", err)
	}

	settlementScript, err := c.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript: %v", err)
	}

	// The same derivation the emulator performs in ReadArkadeScript.
	want := arkade.ComputeArkadeScriptPublicKey(
		emulatorKey, arkade.ArkadeScriptHash(settlementScript),
	)
	if xonly(base) != xonly(want) {
		t.Fatalf("SettlementKey = %s, want %s", xonly(base), xonly(want))
	}

	for name, mutate := range map[string]func(*Contract){
		"payout":          func(c *Contract) { c.Terms.PayoutSats++ },
		"maturity":        func(c *Contract) { c.Terms.MaturityTimestamp++ },
		"low liquidation": func(c *Contract) { c.Terms.LowLiquidationPrice++ },
		"short recipient": func(c *Contract) { c.Terms.ShortLockScript = thiefScript },
		"oracle key":      func(c *Contract) { c.Terms.OraclePubKey = bytes.Repeat([]byte{0x77}, 32) },
		"emulator key":    func(c *Contract) { c.Keys.EmulatorSigner = strangerKey },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := contract()
			mutate(&mutated)

			got, err := mutated.SettlementKey()
			if err != nil {
				t.Fatalf("SettlementKey: %v", err)
			}
			if xonly(got) == xonly(base) {
				t.Errorf("changing the %s left the tweaked key unchanged", name)
			}
		})
	}
}

// arkd's own acceptance check. A script it would reject is a contract nobody
// can exit, so this runs before anything is funded.
func TestArkdAcceptsTheVtxoScript(t *testing.T) {
	if err := contract().Validate(exitDelay, false); err != nil {
		t.Fatalf("arkd rejected the script: %v", err)
	}

	// The operator's minimum is a lower bound, so a longer delay passes too.
	longer := contract()
	longer.ExitDelay = arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: 173_056}
	if err := longer.Validate(exitDelay, false); err != nil {
		t.Fatalf("arkd rejected a longer exit delay: %v", err)
	}
}

// Whether a block-based delay is acceptable is the operator's policy. A
// production operator refuses; the regtest stacks use them so timelocks fire on
// mining rather than on the wall clock, and the contract has to work there too.
func TestBlockTimelocksFollowTheOperatorsPolicy(t *testing.T) {
	blocks := arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144}

	c := contract()
	c.ExitDelay = blocks

	if err := c.Validate(blocks, false); err == nil {
		t.Error("an operator that forbids block timelocks accepted one")
	}
	if err := c.Validate(blocks, true); err != nil {
		t.Errorf("an operator that allows block timelocks rejected one: %v", err)
	}
}

// A forfeit leaf built around somebody else's key. Would let a covenant path be
// spent without the operator, bypassing the offchain history it has already
// guaranteed — arkd calls it "invalid forfeit closure, signer pubkey not found".
func TestArkdRejectsAForfeitLeafWithoutItsSigner(t *testing.T) {
	c := contract()
	c.Keys.ArkdSigner = strangerKey

	vtxo, err := c.VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}

	// The tree was built for the stranger; the operator validates as itself.
	if err := vtxo.Validate(arkdKey, exitDelay, false); err == nil {
		t.Fatal("arkd accepted a forfeit leaf without its signer")
	}
}

// A short CSV would let a party unroll and spend before the operator can react
// to a double-spend.
func TestArkdRejectsAnExitDelayBelowItsMinimum(t *testing.T) {
	c := contract()
	c.ExitDelay = arklib.RelativeLocktime{Type: arklib.LocktimeTypeSecond, Value: 512}

	if err := c.Validate(exitDelay, false); err == nil {
		t.Fatal("arkd accepted an exit delay below its minimum")
	}
}

// Closures rejects what BIP68 cannot encode before arkd ever sees it.
func TestExitDelayMustBeEncodable(t *testing.T) {
	for name, delay := range map[string]arklib.RelativeLocktime{
		"seconds not a multiple of 512": {Type: arklib.LocktimeTypeSecond, Value: 86_400},
		"seconds past the BIP68 ceiling": {
			Type: arklib.LocktimeTypeSecond, Value: 1 << 30,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := contract()
			c.ExitDelay = delay
			if _, err := c.Closures(); err == nil {
				t.Fatal("accepted an exit delay BIP68 cannot encode")
			}
		})
	}
}

// Every leaf must decode back to one of arkd's five known closure shapes, or
// the VTXO is unparseable and the funds are stuck.
func TestEveryLeafRoundTripsThroughArkdsDecoder(t *testing.T) {
	c := contract()

	vtxo, err := c.VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}

	encoded, err := vtxo.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	parsed, err := arkscript.ParseVtxoScript(encoded)
	if err != nil {
		t.Fatalf("ParseVtxoScript: %v", err)
	}

	reencoded, err := parsed.Encode()
	if err != nil {
		t.Fatalf("Encode after parse: %v", err)
	}

	for i := range encoded {
		if encoded[i] != reencoded[i] {
			t.Errorf("leaf %d did not survive the round trip:\n got %s\nwant %s",
				i, reencoded[i], encoded[i])
		}
	}
}

// arkd sorts leaves into forfeit and exit by shape alone. Getting a leaf into
// the wrong class changes which rule it is held to.
func TestLeavesLandInTheRightClass(t *testing.T) {
	vtxo, err := contract().VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}

	if got := len(vtxo.ForfeitClosures()); got != 2 {
		t.Errorf("got %d forfeit closures, want 2 (settlement, mutual redemption)", got)
	}
	if got := len(vtxo.ExitClosures()); got != 1 {
		t.Errorf("got %d exit closures, want 1 (unilateral exit)", got)
	}

	smallest, err := vtxo.SmallestExitDelay()
	if err != nil {
		t.Fatalf("SmallestExitDelay: %v", err)
	}
	if smallest.Value != exitDelay.Value || smallest.Type != exitDelay.Type {
		t.Errorf("smallest exit delay = %+v, want %+v", *smallest, exitDelay)
	}
}

// The client derives the address from the parameters it was sent and compares
// it with the one it is about to fund. That only works if the derivation is
// reproducible.
func TestTaprootKeyIsDeterministic(t *testing.T) {
	first, err := contract().PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	second, err := contract().PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("two builds disagree:\n %x\n %x", first, second)
	}

	if len(first) != 34 || first[0] != txscript.OP_1 || first[1] != 32 {
		t.Fatalf("not a v1 witness program: %x", first)
	}
}

// A fourth leaf, a different key, different terms — anything that changes the
// tree has to change the address, or the address proves nothing about the tree.
func TestEveryParameterChangesTheAddress(t *testing.T) {
	base, err := contract().PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	for name, mutate := range map[string]func(*Contract){
		"short key":                 func(c *Contract) { c.Keys.Short = strangerKey },
		"long key":                  func(c *Contract) { c.Keys.Long = strangerKey },
		"arkd signer":               func(c *Contract) { c.Keys.ArkdSigner = strangerKey },
		"emulator signer":           func(c *Contract) { c.Keys.EmulatorSigner = strangerKey },
		"exit delay":                func(c *Contract) { c.ExitDelay.Value += 512 },
		"payout":                    func(c *Contract) { c.Terms.PayoutSats++ },
		"nominal":                   func(c *Contract) { c.Terms.NominalUnitsXSatsPerBtc++ },
		"leverage term":             func(c *Contract) { c.Terms.SatsForNominalUnitsAtHighLiquidation++ },
		"low liquidation":           func(c *Contract) { c.Terms.LowLiquidationPrice++ },
		"high liquidation":          func(c *Contract) { c.Terms.HighLiquidationPrice++ },
		"start":                     func(c *Contract) { c.Terms.StartTimestamp++ },
		"maturity":                  func(c *Contract) { c.Terms.MaturityTimestamp++ },
		"short recipient":           func(c *Contract) { c.Terms.ShortLockScript = thiefScript },
		"long recipient":            func(c *Contract) { c.Terms.LongLockScript = thiefScript },
		"oracle key":                func(c *Contract) { c.Terms.OraclePubKey = bytes.Repeat([]byte{0x77}, 32) },
		"mutual redemption dropped": func(c *Contract) { c.EnableMutualRedemption = false },
	} {
		t.Run(name, func(t *testing.T) {
			c := contract()
			mutate(&c)

			got, err := c.PkScript()
			if err != nil {
				t.Fatalf("PkScript: %v", err)
			}
			if bytes.Equal(got, base) {
				t.Errorf("changing the %s left the address unchanged", name)
			}
		})
	}
}

// Swapping short and long is a different contract: they are paid differently
// and the leaf key order differs. It must not produce the same address.
func TestSwappingThePartiesChangesTheAddress(t *testing.T) {
	base, err := contract().PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	swapped := contract()
	swapped.Keys.Short, swapped.Keys.Long = longKey, shortKey

	got, err := swapped.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	if bytes.Equal(got, base) {
		t.Fatal("swapping the parties left the address unchanged")
	}
}

// The control block is what proves a leaf belongs to the funded tree. A spend
// carries it, so it has to verify against the output key.
func TestEveryLeafProvesItBelongsToTheTree(t *testing.T) {
	c := contract()

	tapKey, err := c.TaprootKey()
	if err != nil {
		t.Fatalf("TaprootKey: %v", err)
	}

	for _, leaf := range []Leaf{LeafSettlement, LeafMutualRedemption, LeafExit} {
		t.Run(leaf.String(), func(t *testing.T) {
			proof, err := c.Tapscript(leaf)
			if err != nil {
				t.Fatalf("Tapscript: %v", err)
			}

			control, err := txscript.ParseControlBlock(proof.ControlBlock)
			if err != nil {
				t.Fatalf("ParseControlBlock: %v", err)
			}

			// The internal key is the NUMS point: there is no key-path spend.
			if !control.InternalKey.IsEqual(arkscript.UnspendableKey()) {
				t.Error("control block does not commit to the unspendable internal key")
			}

			root := control.RootHash(proof.Script)
			derived := txscript.ComputeTaprootOutputKey(control.InternalKey, root)
			if xonly(derived) != xonly(tapKey) {
				t.Fatalf("leaf does not belong to the funded tree:\n got %s\nwant %s",
					xonly(derived), xonly(tapKey))
			}
		})
	}
}

// The emulator locates the leaf being spent by looking for the tweaked key
// inside it (ReadArkadeScript). If it is not there, it declines with
// ErrTweakedArkadePubKeyNotFound and the covenant path is unspendable.
func TestTheEmulatorCanFindItsKeyInTheSettlementLeaf(t *testing.T) {
	c := contract()

	proof, err := c.Tapscript(LeafSettlement)
	if err != nil {
		t.Fatalf("Tapscript: %v", err)
	}

	closure, err := arkscript.DecodeClosure(proof.Script)
	if err != nil {
		t.Fatalf("DecodeClosure: %v", err)
	}

	multisig, ok := closure.(*arkscript.MultisigClosure)
	if !ok {
		t.Fatalf("settlement leaf decoded as %T, want a plain multisig", closure)
	}

	settlementScript, err := c.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript: %v", err)
	}
	expected := arkade.ComputeArkadeScriptPublicKey(
		emulatorKey, arkade.ArkadeScriptHash(settlementScript),
	)

	for _, k := range multisig.PubKeys {
		if xonly(k) == xonly(expected) {
			return
		}
	}
	t.Fatal("the emulator would not find its tweaked key in the leaf")
}

// Without mutual redemption the tree has two leaves and the exit moves up one
// position. Asking for the leaf that is not there is an error, not leaf 0.
func TestMutualRedemptionCanBeDisabled(t *testing.T) {
	c := contract()
	c.EnableMutualRedemption = false

	cl := closures(t, c)
	if len(cl) != 2 {
		t.Fatalf("got %d leaves, want 2", len(cl))
	}

	if _, err := c.Tapscript(LeafMutualRedemption); err == nil {
		t.Fatal("returned a tapscript for a leaf that is not in the tree")
	}

	proof, err := c.Tapscript(LeafExit)
	if err != nil {
		t.Fatalf("Tapscript(exit): %v", err)
	}
	closure, err := arkscript.DecodeClosure(proof.Script)
	if err != nil {
		t.Fatalf("DecodeClosure: %v", err)
	}
	if _, ok := closure.(*arkscript.CSVMultisigClosure); !ok {
		t.Fatalf("exit leaf decoded as %T, want a CSV multisig", closure)
	}

	if err := c.Validate(exitDelay, false); err != nil {
		t.Fatalf("arkd rejected a contract without mutual redemption: %v", err)
	}
}

func TestContractRejectsMissingKeys(t *testing.T) {
	for name, mutate := range map[string]func(*Contract){
		"short":    func(c *Contract) { c.Keys.Short = nil },
		"long":     func(c *Contract) { c.Keys.Long = nil },
		"arkd":     func(c *Contract) { c.Keys.ArkdSigner = nil },
		"emulator": func(c *Contract) { c.Keys.EmulatorSigner = nil },
	} {
		t.Run(name, func(t *testing.T) {
			c := contract()
			mutate(&c)
			if _, err := c.Closures(); err == nil {
				t.Fatalf("accepted a contract with no %s key", name)
			}
		})
	}
}

// Bad terms have to fail here too: the settlement script is built to derive the
// tweak, so an unbuildable script means an underivable address.
func TestContractRejectsBadTerms(t *testing.T) {
	c := contract()
	c.Terms.PayoutSats = 0

	if _, err := c.Closures(); err == nil {
		t.Fatal("accepted a contract with a zero payout")
	}
	if _, err := c.PkScript(); err == nil {
		t.Fatal("derived an address from unbuildable terms")
	}
}

// The TypeScript verifier has to derive the same address from the same
// parameters. This pins the Go side of that.
const standardPkScriptHex = "5120ee919fb3d1154184097c2e4de25bdfd6bd8e412fe819751d35a62d44110b38c0"

func TestPkScriptIsStable(t *testing.T) {
	got, err := contract().PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	if want := standardPkScriptHex; hex.EncodeToString(got) != want {
		t.Fatalf("scriptPubKey changed:\n got %x\nwant %s", got, want)
	}
}

// Renewing a VTXO means forfeiting it: handing it to the operator in exchange
// for a fresh one in a new batch. The forfeit is an ordinary transaction paying
// the operator's forfeit address, so it has to be signable on one of the tree's
// forfeit closures.
//
// Leaf 1 is a forfeit closure by shape and useless for this. Its second key is
// the emulator's, tweaked by the settlement script, and the emulator only signs
// once that script has run and passed — and the script demands the transaction
// pay ShortLockScript and LongLockScript exactly. A forfeit pays neither.
//
// So renewability rests entirely on leaf 2 being covenant-free, and this is the
// test that says so. If it ever failed, contracts would be silently bounded by
// the batch that funded them.
func TestOnlyMutualRedemptionCanSignAForfeit(t *testing.T) {
	c := contract()

	vtxo, err := c.VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}

	signable := 0
	for _, closure := range vtxo.ForfeitClosures() {
		multisig, ok := closure.(*arkscript.MultisigClosure)
		if !ok {
			continue
		}
		if namesBoth(multisig.PubKeys, c.Keys.Short, c.Keys.Long) {
			signable++
		}
	}

	if signable != 1 {
		t.Fatalf("got %d forfeit closures the parties can sign, want exactly 1 "+
			"(mutual redemption); the contract cannot be renewed without one", signable)
	}
}

// Disabling mutual redemption is not only a missing leaf: it removes the only
// path a forfeit can be signed on, so the contract cannot be renewed and dies
// with the batch that funded it. That is a real trade, and it should be a
// deliberate one rather than a surprise.
func TestWithoutMutualRedemptionNothingCanSignAForfeit(t *testing.T) {
	c := contract()
	c.EnableMutualRedemption = false

	vtxo, err := c.VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}

	for _, closure := range vtxo.ForfeitClosures() {
		multisig, ok := closure.(*arkscript.MultisigClosure)
		if !ok {
			continue
		}
		if namesBoth(multisig.PubKeys, c.Keys.Short, c.Keys.Long) {
			t.Fatal("a forfeit closure the parties can sign appeared without leaf 2")
		}
	}
}

func namesBoth(keys []*btcec.PublicKey, short, long *btcec.PublicKey) bool {
	found := 0
	for _, k := range keys {
		if xonly(k) == xonly(short) || xonly(k) == xonly(long) {
			found++
		}
	}
	return found == 2
}
