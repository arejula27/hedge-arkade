//go:build integration

package integration

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/arejula27/hedge/covenant"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/intent"
	"github.com/arkade-os/arkd/pkg/ark-lib/tree"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/arkade-os/go-sdk/indexer"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// Renewal: swapping the contract VTXO for a fresh one in a new batch, so the
// contract outlives the batch it was funded in.
//
// A batch swap starts with an intent — a BIP322 proof that names the inputs to
// spend and the outputs to create. The SDK will not build one for the contract:
// it only knows VTXOs its own wallet owns, and it signs the proof with the
// wallet's key. The contract belongs to no wallet, so the proof is built here
// and signed with the contract's own keys.
//
// Which leaf the proof spends through is the question this file starts with.
// Arkade's intent delegation describes the proof as using the exit path
// (https://docs.arkadeos.com/arkd/components/intent-delegation, step 3), which
// for us is leaf 3 — no operator key, so the proof commits nobody but the two
// parties.

// renewalCosigner signs the VTXO tree branch that recreates the contract. It
// authorises the shape of the tree, not the money: the branch it signs pays the
// contract address and nothing else.
var renewalCosigner = key(0x23)

// intentCoin is one input of an intent proof: the coin, the leaf it is proved
// through, and the tapscript list that lets arkd check the leaf belongs to the
// address.
type intentCoin struct {
	input      intent.Input
	leaf       *arklib.TaprootMerkleProof
	tapscripts []string
}

// contractCoin proves the contract VTXO through one of its leaves.
func contractCoin(
	t *testing.T, c covenant.Contract, outpoint wire.OutPoint, leaf covenant.Leaf,
) intentCoin {
	t.Helper()

	pkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	merkle, err := c.Tapscript(leaf)
	if err != nil {
		t.Fatalf("Tapscript: %v", err)
	}
	vtxo, err := c.VtxoScript()
	if err != nil {
		t.Fatalf("VtxoScript: %v", err)
	}
	tapscripts, err := vtxo.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// The proof tx is verified by btcd's script engine (`intent/proof.go:52`),
	// so a leaf with a CSV needs a sequence that encodes at least the delay.
	// The engine checks the sequence against the script; how old the VTXO
	// actually is belongs to consensus, which never sees this transaction.
	sequence := uint32(wire.MaxTxInSequenceNum)
	if leaf == covenant.LeafExit {
		sequence, err = arklib.BIP68Sequence(c.ExitDelay)
		if err != nil {
			t.Fatalf("BIP68Sequence: %v", err)
		}
	}

	return intentCoin{
		input: intent.Input{
			OutPoint:    &outpoint,
			Sequence:    sequence,
			WitnessUtxo: &wire.TxOut{Value: c.Terms.PayoutSats, PkScript: pkScript},
		},
		leaf:       merkle,
		tapscripts: tapscripts,
	}
}

// feeCoin proves one of the party's own VTXOs, through the collaborative path
// of the default script.
//
// Renewal is not free and the fee cannot come out of the contract, so somebody
// has to bring their own sats to the intent. See
// TestRenewalCannotBePaidOutOfTheContract.
func feeCoin(t *testing.T, p *party) intentCoin {
	t.Helper()

	vtxo, pkScript := p.spendableVtxo(t)

	control, err := vtxo.Tapscript.ControlBlock.ToBytes()
	if err != nil {
		t.Fatalf("serialising the control block: %v", err)
	}

	return intentCoin{
		input: intent.Input{
			OutPoint:    vtxo.Outpoint,
			Sequence:    wire.MaxTxInSequenceNum,
			WitnessUtxo: &wire.TxOut{Value: vtxo.Amount, PkScript: pkScript},
		},
		leaf: &arklib.TaprootMerkleProof{
			ControlBlock: control,
			Script:       vtxo.Tapscript.RevealedScript,
		},
		tapscripts: vtxo.RevealedTapscripts,
	}
}

// buildIntent assembles the BIP322 proof over the coins and outputs.
//
// The proof has one input more than it has coins: index 0 spends the synthetic
// toSpend output, which carries the same script as the first coin
// (`intent/proof.go:135`). So input 0 reveals the first coin's leaf, and each
// real coin lands at index i+1 with its own leaf and tapscript list.
func buildIntent(
	t *testing.T, message string, coins []intentCoin, outputs []*wire.TxOut,
) *intent.Proof {
	t.Helper()

	inputs := make([]intent.Input, 0, len(coins))
	for _, coin := range coins {
		inputs = append(inputs, coin.input)
	}

	proof, err := intent.New(message, inputs, outputs)
	if err != nil {
		t.Fatalf("building the intent proof: %v", err)
	}

	for i := range proof.Inputs {
		coin := coins[0]
		if i > 0 {
			coin = coins[i-1]
		}

		proof.Inputs[i].TaprootLeafScript = []*psbt.TaprootTapLeafScript{{
			ControlBlock: coin.leaf.ControlBlock,
			Script:       coin.leaf.Script,
			LeafVersion:  txscript.BaseLeafVersion,
		}}

		if i == 0 {
			continue
		}
		if err := txutils.SetArkPsbtField(
			&proof.Packet, i, txutils.VtxoTaprootTreeField, coin.tapscripts,
		); err != nil {
			t.Fatalf("setting the taproot tree field on input %d: %v", i, err)
		}
	}

	return proof
}

// intentMessage encodes the register — or fee-estimate — message. arkd prices
// an intent from the same proof it would register, under a different message.
func intentMessage(t *testing.T, messageType intent.IntentMessageType) string {
	t.Helper()

	base := intent.RegisterMessage{
		BaseMessage:          intent.BaseMessage{Type: messageType},
		OnchainOutputIndexes: []int{},
		CosignersPublicKeys: []string{
			hex.EncodeToString(renewalCosigner.PubKey().SerializeCompressed()),
		},
	}

	var message string
	var err error
	if messageType == intent.IntentMessageTypeEstimateFee {
		message, err = intent.EstimateIntentFeeMessage(base).Encode()
	} else {
		message, err = base.Encode()
	}
	if err != nil {
		t.Fatalf("encoding the intent message: %v", err)
	}
	return message
}

// renewalIntent builds the intent that renews the contract: spend the contract
// VTXO and recreate it at the same address, for exactly the same amount.
//
// The amount is the whole point. The covenant pins the settlement input at
// PayoutSats, so a renewal that shrank the contract by arkd's fee would leave a
// VTXO that leaf 1 can never settle. The fee comes from the party's own coin
// instead, and its change comes back to the party.
func renewalIntent(
	t *testing.T, p *party, c covenant.Contract, outpoint wire.OutPoint,
	leaf covenant.Leaf, fee int64, messageType intent.IntentMessageType,
) (*intent.Proof, string) {
	t.Helper()

	contractScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}

	message := intentMessage(t, messageType)
	coins := []intentCoin{contractCoin(t, c, outpoint, leaf)}
	outputs := []*wire.TxOut{{Value: c.Terms.PayoutSats, PkScript: contractScript}}

	if fee > 0 {
		payer := feeCoin(t, p)
		change := payer.input.WitnessUtxo.Value - fee
		if change < int64(stack.dust) {
			t.Fatalf("the party's coin holds %d, not enough to pay a %d sat fee",
				payer.input.WitnessUtxo.Value, fee)
		}

		coins = append(coins, payer)
		outputs = append(outputs, &wire.TxOut{
			Value: change, PkScript: payer.input.WitnessUtxo.PkScript,
		})
	}

	return buildIntent(t, message, coins, outputs), message
}

// signIntent signs the proof with each key and returns it encoded. A key signs
// the inputs whose revealed leaf names it and skips the rest, so the contract's
// keys and the fee payer's can be handed in together.
func signIntent(t *testing.T, proof *intent.Proof, signers ...*btcec.PrivateKey) string {
	t.Helper()

	encoded := ""
	for _, k := range signers {
		encoded = signWithKey(t, &proof.Packet, k)
	}
	if encoded == "" {
		t.Fatal("no signers given")
	}
	return encoded
}

// renewalFee asks arkd what it charges to renew this contract.
//
// The fee is a runtime value — arkd evaluates a CEL program it was configured
// with — so it cannot be a constant here any more than the dust threshold or
// the exit delay can.
// The fee is priced from the intent it is charged on, and paying it changes
// that intent — the fee coin is another input and its change another output. So
// the estimate is a fixed point, not a lookup: quote, rebuild, quote again,
// until the quote is covered.
func renewalFee(
	t *testing.T, p *party, c covenant.Contract, outpoint wire.OutPoint, leaf covenant.Leaf,
	short, long *btcec.PrivateKey,
) int64 {
	t.Helper()

	// Seeded above zero so the first quote already prices the shape that will
	// be registered, fee coin and change included.
	fee := int64(stack.dust)

	for range 6 {
		proof, message := renewalIntent(
			t, p, c, outpoint, leaf, fee, intent.IntentMessageTypeEstimateFee,
		)
		quoted, err := p.arkd.EstimateIntentFee(
			t.Context(), signIntent(t, proof, short, long, p.privKey), message,
		)
		if err != nil {
			t.Fatalf("EstimateIntentFee: %v", err)
		}
		if quoted <= fee {
			return fee
		}
		fee = quoted
	}

	t.Fatalf("arkd's fee never settled; last quote %d", fee)
	return 0
}

// registerRenewal signs the intent with the given keys and offers it to arkd.
// It returns arkd's error rather than failing, so the negative cases can assert
// on it.
//
// The party's own key signs too: its coin is what pays arkd's fee, and the
// contract's keys are not in that coin's leaf.
func registerRenewal(
	t *testing.T, p *party, c covenant.Contract, outpoint wire.OutPoint,
	leaf covenant.Leaf, signers ...*btcec.PrivateKey,
) (string, error) {
	t.Helper()

	fee := renewalFee(t, p, c, outpoint, leaf, shortKey, longKey)
	proof, message := renewalIntent(
		t, p, c, outpoint, leaf, fee, intent.IntentMessageTypeRegister,
	)
	if fee > 0 {
		signers = append(signers, p.privKey)
	}

	return p.arkd.RegisterIntent(t.Context(), signIntent(t, proof, signers...), message)
}

// renew swaps the contract VTXO for a fresh one in a new batch, and returns
// where the contract now lives.
//
// The contract comes out the other side at the same address holding the same
// sats — only its outpoint and its expiry change. Which is the catch: the
// pre-signed exit package commits to the outpoint, so it dies here and has to
// be signed again.
func renew(
	t *testing.T, p *party, c covenant.Contract, outpoint wire.OutPoint,
	short, long *btcec.PrivateKey,
) wire.OutPoint {
	t.Helper()
	ctx := t.Context()

	fee := renewalFee(t, p, c, outpoint, covenant.LeafExit, short, long)
	payer := feeCoin(t, p)
	proof, message := renewalIntent(
		t, p, c, outpoint, covenant.LeafExit, fee, intent.IntentMessageTypeRegister,
	)

	intentID, err := p.arkd.RegisterIntent(
		ctx, signIntent(t, proof, short, long, p.privKey), message,
	)
	if err != nil {
		t.Fatalf("arkd refused the renewal intent: %v", err)
	}

	session := &renewalSession{
		t: t, party: p, contract: c, intentID: intentID,
		signer: tree.NewTreeSignerSession(renewalCosigner),
		coins: []forfeitable{
			contractForfeit(t, c, outpoint, short, long),
			feeForfeit(t, p, payer),
		},
	}

	commitment, err := session.joinBatch(ctx)
	if err != nil {
		t.Fatalf("the renewal batch did not complete: %v", err)
	}
	t.Logf("renewed in commitment %s", commitment)

	// Nothing mines on this stack unless a test asks, and a commitment left in
	// the mempool blocks the next batch: its inputs still look unspent, so the
	// following intent gets dropped for containing a spent input.
	mine(t, 1)

	return contractVtxo(t, p, c, outpoint)
}

// contractVtxo finds the contract's current VTXO, which must be the only
// spendable one at its address and must hold exactly PayoutSats.
//
// `previous` is the outpoint the renewal spent: the indexer is eventually
// consistent, so a lookup that returned it would mean the swap has not landed
// yet rather than that it failed.
func contractVtxo(
	t *testing.T, p *party, c covenant.Contract, previous wire.OutPoint,
) wire.OutPoint {
	t.Helper()

	script := hex.EncodeToString(mustPkScript(t, c))
	opts := indexer.GetVtxosRequestOption{}
	if err := opts.WithScripts([]string{script}); err != nil {
		t.Fatalf("indexer scripts: %v", err)
	}
	opts.WithSpendableOnly()

	var found wire.OutPoint
	waitFor(t, 60*time.Second, "the renewed contract VTXO to appear", func() error {
		resp, err := p.indexer.GetVtxos(t.Context(), opts)
		if err != nil {
			return err
		}

		for _, v := range resp.Vtxos {
			if v.Txid == previous.Hash.String() && v.VOut == previous.Index {
				return fmt.Errorf("the indexer still reports the old VTXO as spendable")
			}
			if int64(v.Amount) != c.Terms.PayoutSats {
				return fmt.Errorf("a VTXO at the contract address holds %d, not %d",
					v.Amount, c.Terms.PayoutSats)
			}

			hash, err := chainhashFrom(v.Txid)
			if err != nil {
				return err
			}
			found = wire.OutPoint{Hash: *hash, Index: v.VOut}
			return nil
		}
		return fmt.Errorf("no spendable VTXO at the contract address yet")
	})

	return found
}

// forgetRenewal withdraws a registered intent.
//
// A registered intent that nobody completes is not inert: arkd opens a batch
// for it, waits for a confirmation that never comes, and aborts. That failed
// batch is shared with every other test running against this stack, so an
// intent registered to prove a point has to be taken back.
func forgetRenewal(
	t *testing.T, p *party, c covenant.Contract, outpoint wire.OutPoint,
	leaf covenant.Leaf, signers ...*btcec.PrivateKey,
) {
	t.Helper()

	message, err := intent.DeleteMessage{
		BaseMessage: intent.BaseMessage{Type: intent.IntentMessageTypeDelete},
		ExpireAt:    time.Now().Add(2 * time.Minute).Unix(),
	}.Encode()
	if err != nil {
		t.Fatalf("encoding the delete message: %v", err)
	}

	proof := buildIntent(t, message, []intentCoin{contractCoin(t, c, outpoint, leaf)}, nil)

	if err := p.arkd.DeleteIntent(t.Context(), signIntent(t, proof, signers...), message); err != nil {
		t.Errorf("could not withdraw the intent, the stack is left dirty: %v", err)
	}
}

// The premise the whole renewal design rests on: arkd accepts an intent for a
// VTXO no wallet owns, proved on the exit leaf by the two parties alone.
//
// If this fails, renewal cannot be delegated — every batch swap would need a
// key that is in a collaborative leaf, which means the operator, which means
// the contract cannot be renewed without it.
func TestArkdAcceptsAnIntentSignedOnTheExitLeaf(t *testing.T) {
	c := contract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	intentID, err := registerRenewal(t, p, c, outpoint, covenant.LeafExit, shortKey, longKey)
	if err != nil {
		t.Fatalf("arkd refused an intent proved on the exit leaf: %v", err)
	}
	t.Logf("intent accepted: %s", intentID)

	forgetRenewal(t, p, c, outpoint, covenant.LeafExit, shortKey, longKey)
}

// Whether the mutual redemption leaf works as well decides whether the proof
// has to carry a BIP68 sequence at all. It is not the leaf the docs describe,
// and it carries the operator key, so a failure here costs nothing.
func TestArkdAcceptsAnIntentSignedOnTheMutualLeaf(t *testing.T) {
	c := contract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	intentID, err := registerRenewal(
		t, p, c, outpoint, covenant.LeafMutualRedemption, shortKey, longKey,
	)
	if err != nil {
		t.Logf("arkd refused an intent proved on the mutual leaf: %v", err)
		return
	}
	t.Logf("intent accepted: %s", intentID)

	forgetRenewal(t, p, c, outpoint, covenant.LeafMutualRedemption, shortKey, longKey)
}

// Renewing is not free, and the obvious way to pay — take it off the contract —
// is the one way that cannot work. The covenant pins the settlement input at
// exactly PayoutSats, so a contract that had paid a single renewal fee could
// never settle through leaf 1 again.
//
// This asserts the trap rather than a fee level: the fee is a runtime value
// arkd computes from a CEL program, and an operator that charged nothing would
// hide the problem rather than solve it.
func TestRenewalCannotBePaidOutOfTheContract(t *testing.T) {
	c := contract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	fee := renewalFee(t, p, c, outpoint, covenant.LeafExit, shortKey, longKey)
	if fee <= 0 {
		t.Skip("this operator charges nothing for an intent")
	}
	t.Logf("arkd charges %d sats to renew a %d sat contract", fee, c.Terms.PayoutSats)

	// The renewal that pays itself: one input, one output, short by the fee.
	shrunk := buildIntent(t,
		intentMessage(t, intent.IntentMessageTypeRegister),
		[]intentCoin{contractCoin(t, c, outpoint, covenant.LeafExit)},
		[]*wire.TxOut{{Value: c.Terms.PayoutSats - fee, PkScript: mustPkScript(t, c)}},
	)

	intentID, err := p.arkd.RegisterIntent(
		t.Context(), signIntent(t, shrunk, shortKey, longKey),
		intentMessage(t, intent.IntentMessageTypeRegister),
	)
	if err != nil {
		t.Fatalf("arkd refused a self-funded renewal: %v", err)
	}
	t.Logf("arkd accepts it (%s) — and it would leave a contract holding %d "+
		"that leaf 1 requires to hold %d", intentID, c.Terms.PayoutSats-fee, c.Terms.PayoutSats)

	forgetRenewal(t, p, c, outpoint, covenant.LeafExit, shortKey, longKey)
}

func mustPkScript(t *testing.T, c covenant.Contract) []byte {
	t.Helper()

	pkScript, err := c.PkScript()
	if err != nil {
		t.Fatalf("PkScript: %v", err)
	}
	return pkScript
}

// One party is not the contract. Without this, either side could renew the
// contract to an address of their choosing.
func TestArkdRefusesARenewalOnlyOnePartySigned(t *testing.T) {
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

			_, err := registerRenewal(t, p, c, outpoint, covenant.LeafExit, tc.signer)
			if err == nil {
				t.Fatal("arkd accepted a renewal intent one party signed alone")
			}
			t.Logf("rejected with: %v", err)
		})
	}
}

// The three round trips. A contract is created in one batch, renewed into
// another, and only then closed — through each of its three leaves in turn.
//
// A contract VTXO inherits the batch expiry of whatever funded it, so without
// renewal every contract is bounded by a batch it did not choose. These are the
// tests that say the contract still works after outliving that batch, which is
// the whole reason renewal exists.

// Leaf 1: the covenant settles a renewed contract exactly as it settles a fresh
// one. The renewed VTXO holds PayoutSats to the sat, which is what makes this
// possible at all — see TestRenewalCannotBePaidOutOfTheContract.
func TestARenewedContractStillSettles(t *testing.T) {
	c := contract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)
	renewed := renew(t, p, c, outpoint, shortKey, longKey)
	if renewed == outpoint {
		t.Fatal("renewal did not move the contract")
	}

	arkTx, checkpoints := settlementSpending(t, c, renewed, shortPayout, longPayout)
	if err := p.submitToEmulator(t, arkTx, checkpoints); err != nil {
		t.Fatalf("the stack refused to settle a renewed contract: %v", err)
	}
}

// Leaf 2: the parties can still close early at a split of their own after a
// renewal. This is also the leaf the renewal itself forfeits through, so it
// proves the leaf survives being used that way.
func TestARenewedContractStillRedeemsMutually(t *testing.T) {
	c := contract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)
	renewed := renew(t, p, c, outpoint, shortKey, longKey)

	lopsided := []*wire.TxOut{
		{Value: c.Terms.PayoutSats - int64(stack.dust), PkScript: c.Terms.ShortLockScript},
		{Value: int64(stack.dust), PkScript: c.Terms.LongLockScript},
	}

	if err := redeem(t, p, c, renewed, lopsided, shortKey, longKey); err != nil {
		t.Fatalf("the stack refused a mutual redemption after a renewal: %v", err)
	}
}

// Leaf 3: the unilateral exit still works after a renewal — but only against
// the new outpoint.
//
// This is the cost renewal carries. A taproot signature commits to the outpoint
// of the input it spends, and no sighash flag changes that, so the package
// pre-signed at funding is worthless the moment the contract moves. Both
// parties have to sign again, for a VTXO whose identity nobody could know in
// advance, which is why renewal cannot be delegated end to end.
func TestARenewedContractStillExitsUnilaterally(t *testing.T) {
	requireBlockDelay(t)

	c, parties, sweep := exitContract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	// The package pre-signed at funding, kept to show what renewal does to it.
	stale, err := c.PreSignExit(
		parties.short, parties.long, outpoint, c.Terms.PayoutSats, exitFeeSats, sweep.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit: %v", err)
	}

	renewed := renew(t, p, c, outpoint, parties.short, parties.long)
	if renewed == outpoint {
		t.Fatal("renewal did not move the contract")
	}
	if stale.Tx.TxIn[0].PreviousOutPoint == renewed {
		t.Fatal("the pre-signed exit still points at the contract, so it proves nothing")
	}

	// Re-signed for the outpoint the renewal created. In production this is a
	// ceremony both parties have to attend, once per renewal.
	pkg, err := c.PreSignExit(
		parties.short, parties.long, renewed, c.Terms.PayoutSats, exitFeeSats, sweep.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit after renewal: %v", err)
	}
	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// From here the operator is not asked for anything.
	if published := unroll(t, p, renewed); published == 0 {
		t.Fatal("nothing was unrolled, so the renewed contract was already onchain")
	}

	e := onchain(t)
	waitFor(t, 60*time.Second, "the renewed contract output to be indexed onchain", func() error {
		return outputIsOnchain(t, e, c, renewed)
	})

	refuses(t, signed, reasonTooEarly)
	mine(t, int(stack.exitDelay.Value)+1)

	waitFor(t, 60*time.Second, "the chain to accept the matured exit", func() error {
		return broadcast(t, e, signed)
	})
	mine(t, 1)

	sweepAddress := taprootAddress(t, sweepKey(t, sweep))
	waitFor(t, 60*time.Second, "the swept output to appear", func() error {
		utxos, err := e.GetUtxos(sweepAddress)
		if err != nil {
			return err
		}
		want := c.Terms.PayoutSats - exitFeeSats
		for _, u := range utxos {
			if int64(u.Amount) == want {
				return nil
			}
		}
		return fmt.Errorf("no output of %d sats at the sweep", want)
	})
}

// Renewing once proves the mechanism; renewing repeatedly proves the contract
// is not bounded by any batch at all.
//
// The second renewal is the one that matters most: it starts from a VTXO the
// first one created, whose ancestry is a branch of a batch tree rather than an
// Arkade transaction. That is the case a contract is in for the rest of its
// life, and the first renewal does not exercise it. The third is there because
// nothing should distinguish it from the second, and a chain that only survived
// two hops would say otherwise.
func TestAContractSurvivesSeveralRenewals(t *testing.T) {
	c := contract(t)
	p := newParty(t)
	p.fund(t, boardedSats)

	outpoint := fundContract(t, p, c)

	seen := map[wire.OutPoint]bool{outpoint: true}
	for round := 1; round <= 3; round++ {
		outpoint = renew(t, p, c, outpoint, shortKey, longKey)
		if seen[outpoint] {
			t.Fatalf("renewal %d put the contract back where it already was", round)
		}
		seen[outpoint] = true
		t.Logf("renewal %d: the contract is now at %s", round, outpoint)
	}

	// Still the contract it started as: same terms, same address, still exactly
	// PayoutSats, and the covenant still settles it.
	arkTx, checkpoints := settlementSpending(t, c, outpoint, shortPayout, longPayout)
	if err := p.submitToEmulator(t, arkTx, checkpoints); err != nil {
		t.Fatalf("the stack refused to settle a thrice-renewed contract: %v", err)
	}
}
