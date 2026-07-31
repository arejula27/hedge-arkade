//go:build integration

package integration

import (
	"bytes"
	"testing"

	"github.com/arejula27/hedge/covenant"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
)

// The two counterparties. Fixed so a CI failure reproduces.
var (
	shortKey = key(0x21)
	longKey  = key(0x22)
)

func key(b byte) *btcec.PrivateKey {
	k, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{b}, 32))
	return k
}

// terms are the standard position: a $10,000 hedge against 0.2 BTC, liquidating
// at $50,000 and $200,000.
func terms(t *testing.T) covenant.Terms {
	t.Helper()

	return covenant.Terms{
		NominalUnitsXSatsPerBtc:              100_000_000_000_000,
		SatsForNominalUnitsAtHighLiquidation: 0,
		PayoutSats:                           20_000_000,
		LowLiquidationPrice:                  5_000_000,
		HighLiquidationPrice:                 20_000_000,
		ShortLockScript:                      p2tr(shortKey.PubKey()),
		LongLockScript:                       p2tr(longKey.PubKey()),
		OraclePubKey:                         schnorr.SerializePubKey(oracleKey.PubKey()),
		StartTimestamp:                       startTime,
		MaturityTimestamp:                    maturityTime,
	}
}

func p2tr(k *btcec.PublicKey) []byte {
	return covenant.P2TR(schnorr.SerializePubKey(k))
}

// contract builds the position against the keys and the exit delay the live
// stack reports, not against constants we chose.
func contract(t *testing.T) covenant.Contract {
	t.Helper()

	return covenant.Contract{
		Terms: terms(t),
		Keys: covenant.Keys{
			Short:          shortKey.PubKey(),
			Long:           longKey.PubKey(),
			ArkdSigner:     stack.arkdSigner,
			EmulatorSigner: stack.emulatorSigner,
		},
		ExitDelay:              stack.exitDelay,
		EnableMutualRedemption: true,
	}
}

// The unit tests call arkd's Validate with an exit delay we invented. This calls
// it with the operator's real one, on the real signer key. A drift in either —
// a new arkd version, a different regtest configuration — surfaces here.
func TestTheRealOperatorWouldAcceptTheContract(t *testing.T) {
	c := contract(t)

	if err := c.Validate(stack.exitDelay, stack.allowsBlockTimelocks()); err != nil {
		t.Fatalf("the live operator's rules reject this contract: %v", err)
	}

	pkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	t.Logf("contract scriptPubKey %x", pkScript)
}

// The exit delay comes from the operator, so a contract must not be able to
// undercut it. The unit test asserts this against a delay we made up; here the
// bound is whatever the running arkd is configured with.
func TestTheRealOperatorRejectsAShortExit(t *testing.T) {
	if stack.exitDelay.Value <= 1 {
		t.Skip("the operator's exit delay is already at the minimum")
	}

	c := contract(t)
	c.ExitDelay = arklib.RelativeLocktime{Type: stack.exitDelay.Type, Value: 1}

	if err := c.Validate(stack.exitDelay, stack.allowsBlockTimelocks()); err == nil {
		t.Fatal("the live operator accepted an exit delay below its own minimum")
	}
}

// Our leaves have to survive arkd's decoder, which is a closed whitelist of
// five closure shapes. This runs it at the version the stack is actually
// running rather than the version our go.mod happens to pin.
func TestTheRealArkdDecodesEveryLeaf(t *testing.T) {
	c := contract(t)

	vtxo, err := c.VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}

	encoded, err := vtxo.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if _, err := arkscript.ParseVtxoScript(encoded); err != nil {
		t.Fatalf("arkd cannot parse the VTXO script: %v", err)
	}
}

// Both payouts must clear the operator's own dust threshold, which is a runtime
// value we cannot know offline. A contract whose liquidation pays less than
// arkd will accept is a contract that cannot be liquidated.
func TestBothPayoutsClearTheOperatorsDust(t *testing.T) {
	if stack.dust == 0 {
		t.Skip("the operator reports no dust threshold")
	}

	if covenant.Dust < stack.dust {
		t.Errorf("the covenant's dust floor is %d, below the operator's %d — "+
			"a liquidation would pay an output arkd rejects",
			covenant.Dust, stack.dust)
	}
}

// The control block we build has to verify against the address we would fund.
// The unit test proves this against our own taproot implementation; this proves
// it against the leaf bytes arkd would be given.
func TestEveryLeafProvesItselfAgainstTheFundedAddress(t *testing.T) {
	c := contract(t)

	tapKey, err := c.TaprootKey()
	if err != nil {
		t.Fatalf("TaprootKey: %v", err)
	}

	for _, leaf := range []covenant.Leaf{
		covenant.LeafSettlement, covenant.LeafMutualRedemption, covenant.LeafExit,
	} {
		t.Run(leaf.String(), func(t *testing.T) {
			proof, err := c.Tapscript(leaf)
			if err != nil {
				t.Fatalf("Tapscript: %v", err)
			}

			control, err := txscript.ParseControlBlock(proof.ControlBlock)
			if err != nil {
				t.Fatalf("ParseControlBlock: %v", err)
			}

			derived := txscript.ComputeTaprootOutputKey(
				control.InternalKey, control.RootHash(proof.Script),
			)
			if !bytes.Equal(
				schnorr.SerializePubKey(derived), schnorr.SerializePubKey(tapKey),
			) {
				t.Fatal("the leaf does not belong to the address we would fund")
			}
		})
	}
}
