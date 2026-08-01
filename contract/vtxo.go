package contract

import (
	"fmt"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/emulator/pkg/arkade"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
)

// Leaf names the three spending paths, in tree order.
type Leaf int

const (
	// LeafSettlement pays out at maturity or liquidation. Its tapscript is a
	// plain multisig; the covenant itself is not in the tree.
	LeafSettlement Leaf = iota

	// LeafMutualRedemption lets both parties close at any split they agree on.
	// Optional — see Contract.EnableMutualRedemption.
	LeafMutualRedemption

	// LeafExit is the unilateral path: no operator, no covenant, a CSV delay,
	// and the only one that ever reaches Bitcoin.
	LeafExit
)

func (l Leaf) String() string {
	switch l {
	case LeafSettlement:
		return "settlement"
	case LeafMutualRedemption:
		return "mutual redemption"
	case LeafExit:
		return "unilateral exit"
	}
	return fmt.Sprintf("leaf(%d)", int(l))
}

// Keys are the public keys the VTXO binds. Party keys authorise the paths they
// appear in; the two service keys are structural.
type Keys struct {
	// Short and Long are the two counterparties. They sign leaves 2 and 3, and
	// nothing on leaf 1 — settlement is permissionless, as in AnyHedge.
	Short *btcec.PublicKey
	Long  *btcec.PublicKey

	// ArkdSigner is the operator's key, from the Arkade address the VTXO is
	// created under. Every forfeit closure must carry it or arkd rejects the
	// script (script/vtxo_script.go:97).
	ArkdSigner *btcec.PublicKey

	// EmulatorSigner is the covenant co-signer's key, from the emulator's
	// GetInfo. It never appears untweaked: leaf 1 carries
	// ComputeArkadeScriptPublicKey(EmulatorSigner, hash(settlementScript)), so
	// the leaf commits to the covenant without containing it.
	EmulatorSigner *btcec.PublicKey
}

func (k Keys) validate() error {
	for name, key := range map[string]*btcec.PublicKey{
		"short":           k.Short,
		"long":            k.Long,
		"arkd signer":     k.ArkdSigner,
		"emulator signer": k.EmulatorSigner,
	} {
		if key == nil {
			return fmt.Errorf("%s public key is nil", name)
		}
	}
	return nil
}

// Contract is a funded position: the settlement terms, the keys, and the exit
// delay. It produces the VTXO script the parties fund and the witness material
// each path needs.
type Contract struct {
	Terms Terms
	Keys  Keys

	// ExitDelay is the CSV on leaf 3. Seconds, a multiple of 512, and at or
	// above the operator's getInfo().exitDelay — the lower bound is theirs, the
	// value above it is ours.
	ExitDelay arklib.RelativeLocktime

	// EnableMutualRedemption builds leaf 2. AnyHedge makes the same path
	// optional; without it the only early close is a liquidation.
	EnableMutualRedemption bool
}

// SettlementScript is the Arkade Script leaf 1 commits to. It is not part of
// the tapscript: it travels in the emulator packet of the spending transaction,
// and the emulator recomputes the tweak to decide whether the leaf is asking
// for this script or another one.
func (c Contract) SettlementScript() ([]byte, error) {
	return c.Terms.SettlementScript()
}

// SettlementKey is the tweaked emulator key that stands for the covenant in
// leaf 1. Editing a single opcode of the settlement script changes this key,
// which no longer matches the leaf, and the emulator declines to sign.
func (c Contract) SettlementKey() (*btcec.PublicKey, error) {
	script, err := c.SettlementScript()
	if err != nil {
		return nil, err
	}
	return arkade.ComputeArkadeScriptPublicKey(
		c.Keys.EmulatorSigner, arkade.ArkadeScriptHash(script),
	), nil
}

// Closures returns the tapscript leaves in tree order. Order is fixed so the
// taproot key is reproducible: the client derives the same address from the
// same parameters or refuses to fund.
func (c Contract) Closures() ([]arkscript.Closure, error) {
	if err := c.Keys.validate(); err != nil {
		return nil, err
	}
	if err := c.Terms.validate(); err != nil {
		return nil, err
	}
	// Whether a block-based delay is acceptable is the operator's policy, not
	// ours — arkd takes it as a parameter. All that has to hold here is that
	// BIP68 can encode the value at all.
	if _, err := arklib.BIP68Sequence(c.ExitDelay); err != nil {
		return nil, fmt.Errorf("invalid exit delay: %w", err)
	}

	settlementKey, err := c.SettlementKey()
	if err != nil {
		return nil, err
	}

	// Leaf 1. No party key: whoever holds two adjacent oracle messages can
	// settle, and the covenant leaves them no freedom in what the transaction
	// pays. AnyHedge's payout path is permissionless for the same reason.
	closures := []arkscript.Closure{
		&arkscript.MultisigClosure{
			PubKeys: []*btcec.PublicKey{c.Keys.ArkdSigner, settlementKey},
		},
	}

	// Leaf 2. Both owners of the money, no oracle and no covenant.
	if c.EnableMutualRedemption {
		closures = append(closures, &arkscript.MultisigClosure{
			PubKeys: []*btcec.PublicKey{c.Keys.Short, c.Keys.Long, c.Keys.ArkdSigner},
		})
	}

	// Leaf 3. No operator key, so it survives the operator; the CSV is what
	// makes arkd classify it as an exit rather than reject it as a forfeit
	// closure missing its signer.
	closures = append(closures, &arkscript.CSVMultisigClosure{
		MultisigClosure: arkscript.MultisigClosure{
			PubKeys: []*btcec.PublicKey{c.Keys.Short, c.Keys.Long},
		},
		Locktime: c.ExitDelay,
	})

	return closures, nil
}

// VtxoScript assembles the closures into the structure arkd parses and
// validates.
func (c Contract) VtxoScript() (*arkscript.TapscriptsVtxoScript, error) {
	closures, err := c.Closures()
	if err != nil {
		return nil, err
	}
	return &arkscript.TapscriptsVtxoScript{Closures: closures}, nil
}

// TaprootKey returns the output key the parties fund. The internal key is
// ark-lib's unspendable NUMS point, so the only way to spend is through a leaf.
func (c Contract) TaprootKey() (*btcec.PublicKey, error) {
	vtxo, err := c.VtxoScript()
	if err != nil {
		return nil, err
	}
	key, _, err := vtxo.TapTree()
	return key, err
}

// PkScript is the P2TR scriptPubKey of the contract VTXO.
func (c Contract) PkScript() ([]byte, error) {
	key, err := c.TaprootKey()
	if err != nil {
		return nil, err
	}
	return arkscript.P2TRScript(key)
}

// Tapscript returns one leaf's script together with the control block proving
// it belongs to the tree. Both go in the witness of a spend along that path.
func (c Contract) Tapscript(leaf Leaf) (*arklib.TaprootMerkleProof, error) {
	vtxo, err := c.VtxoScript()
	if err != nil {
		return nil, err
	}

	index, err := c.leafIndex(leaf)
	if err != nil {
		return nil, err
	}

	leafScript, err := vtxo.Closures[index].Script()
	if err != nil {
		return nil, err
	}

	_, tree, err := vtxo.TapTree()
	if err != nil {
		return nil, err
	}
	return tree.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(leafScript).TapHash())
}

// leafIndex maps a Leaf to its position, which shifts when mutual redemption is
// disabled.
func (c Contract) leafIndex(leaf Leaf) (int, error) {
	switch leaf {
	case LeafSettlement:
		return 0, nil
	case LeafMutualRedemption:
		if !c.EnableMutualRedemption {
			return 0, fmt.Errorf("mutual redemption is disabled on this contract")
		}
		return 1, nil
	case LeafExit:
		if c.EnableMutualRedemption {
			return 2, nil
		}
		return 1, nil
	}
	return 0, fmt.Errorf("unknown leaf %d", int(leaf))
}

// Validate runs arkd's own acceptance check. minExitDelay is the operator's
// getInfo().exitDelay, and allowBlockTimelocks is its policy on block-based
// ones — false for a production operator, true for the regtest stacks that use
// block delays so timelocks fire on mining instead of on the wall clock.
//
// Calling this before funding is the point: a script arkd would reject is a
// contract nobody can exit.
func (c Contract) Validate(minExitDelay arklib.RelativeLocktime, allowBlockTimelocks bool) error {
	vtxo, err := c.VtxoScript()
	if err != nil {
		return err
	}
	return vtxo.Validate(c.Keys.ArkdSigner, minExitDelay, allowBlockTimelocks)
}
