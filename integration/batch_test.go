//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/arejula27/hedge/covenant"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	"github.com/arkade-os/arkd/pkg/ark-lib/tree"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	arksdk "github.com/arkade-os/go-sdk"
	arkclient "github.com/arkade-os/go-sdk/client"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// Joining a batch swap on behalf of the contract.
//
// The SDK's own batch participation is `arkClient.Settle`, which only swaps
// VTXOs its wallet owns and signs everything with the wallet's key. The event
// loop underneath it is not: `arksdk.JoinBatchSession` takes a
// `BatchEventsHandler` interface, so the protocol is reusable and only the
// signing has to be replaced.
//
// That replacement is the whole of this file. The contract's VTXO is forfeited
// through leaf 2, which needs both parties; the coin that pays arkd's fee is
// forfeited through its owner's own path.

// renewalSession is the contract's side of one batch swap.
type renewalSession struct {
	t        *testing.T
	party    *party
	contract covenant.Contract
	// coins are forfeited in the order they were registered, which is the order
	// arkd hands back connectors.
	coins    []forfeitable
	intentID string
	signer   tree.SignerSession

	sessionID string
	expiry    arklib.RelativeLocktime
	signed    int
}

// forfeitable is a coin the batch takes in exchange for a fresh one, and what
// it takes to hand it over.
type forfeitable struct {
	outpoint   wire.OutPoint
	amount     int64
	pkScript   []byte
	leaf       *arklib.TaprootMerkleProof
	locktime   arklib.AbsoluteLocktime
	signedWith []*btcec.PrivateKey
}

// contractForfeit hands the contract over through leaf 2.
//
// Which leaf matters. `ForfeitClosures()` returns leaves 1 and 2 and the SDK
// takes the first, which here is the settlement leaf — its second key is the
// tweaked emulator key, so that forfeit could only be signed by running the
// covenant. Leaf 2 is short + long + arkd, which is a forfeit closure in the
// ordinary sense: the two owners hand the money over and the operator co-signs.
func contractForfeit(
	t *testing.T, c covenant.Contract, outpoint wire.OutPoint, short, long *btcec.PrivateKey,
) forfeitable {
	t.Helper()

	merkle, err := c.Tapscript(covenant.LeafMutualRedemption)
	if err != nil {
		t.Fatalf("Tapscript: %v", err)
	}

	return forfeitable{
		outpoint:   outpoint,
		amount:     c.Terms.PayoutSats,
		pkScript:   mustPkScript(t, c),
		leaf:       merkle,
		signedWith: []*btcec.PrivateKey{short, long},
	}
}

// feeForfeit hands over the coin that paid arkd's fee.
func feeForfeit(t *testing.T, p *party, coin intentCoin) forfeitable {
	t.Helper()

	return forfeitable{
		outpoint:   *coin.input.OutPoint,
		amount:     coin.input.WitnessUtxo.Value,
		pkScript:   coin.input.WitnessUtxo.PkScript,
		leaf:       coin.leaf,
		signedWith: []*btcec.PrivateKey{p.privKey},
	}
}

// joinBatch drives the swap to its commitment transaction.
//
// The topics decide which batch the operator tells us about: our outpoints, and
// the cosigner key whose nonces and signatures it will ask for.
func (s *renewalSession) joinBatch(ctx context.Context) (string, error) {
	topics := make([]string, 0, len(s.coins)+1)
	for _, coin := range s.coins {
		topics = append(topics, coin.outpoint.String())
	}
	topics = append(topics, s.signer.GetPublicKey())

	events, closeStream, err := s.party.arkd.GetEventStream(ctx, topics)
	if err != nil {
		return "", fmt.Errorf("event stream: %w", err)
	}
	defer closeStream()

	return arksdk.JoinBatchSession(ctx, events, s)
}

// OnBatchStarted confirms our registration if this batch is carrying our
// intent, and otherwise waits for the next one. arkd names intents by hash, so
// nobody else in the batch learns which one is ours.
func (s *renewalSession) OnBatchStarted(
	ctx context.Context, event arkclient.BatchStartedEvent,
) (bool, error) {
	hashed := sha256.Sum256([]byte(s.intentID))
	wanted := hex.EncodeToString(hashed[:])

	for _, hash := range event.HashedIntentIds {
		if hash != wanted {
			continue
		}
		if err := s.party.arkd.ConfirmRegistration(ctx, s.intentID); err != nil {
			return false, fmt.Errorf("confirming registration: %w", err)
		}
		s.sessionID = event.Id
		s.expiry = locktime(event.BatchExpiry)
		return false, nil
	}

	return true, nil
}

// OnTreeSigningStarted contributes the cosigner's nonces for every branch of
// the tree that will hold the renewed contract.
//
// The batch output is swept by the operator after the expiry, so the tree is
// signed against that sweep closure as its taproot root — reconstructed here
// from the operator's own forfeit key and the expiry it just announced.
func (s *renewalSession) OnTreeSigningStarted(
	ctx context.Context, event arkclient.TreeSigningStartedEvent, vtxoTree *tree.TxTree,
) (bool, error) {
	mine := s.signer.GetPublicKey()
	if !contains(event.CosignersPubkeys, mine) {
		return true, nil
	}

	sweep := arkscript.CSVMultisigClosure{
		MultisigClosure: arkscript.MultisigClosure{
			PubKeys: []*btcec.PublicKey{stack.forfeitPubKey},
		},
		Locktime: s.expiry,
	}
	sweepScript, err := sweep.Script()
	if err != nil {
		return false, fmt.Errorf("sweep closure: %w", err)
	}

	commitment, err := psbt.NewFromRawBytes(strings.NewReader(event.UnsignedCommitmentTx), true)
	if err != nil {
		return false, fmt.Errorf("commitment tx: %w", err)
	}

	root := txscript.AssembleTaprootScriptTree(
		txscript.NewBaseTapLeaf(sweepScript),
	).RootNode.TapHash()

	if err := s.signer.Init(
		root.CloneBytes(), commitment.UnsignedTx.TxOut[0].Value, vtxoTree,
	); err != nil {
		return false, fmt.Errorf("signer session: %w", err)
	}

	nonces, err := s.signer.GetNonces()
	if err != nil {
		return false, fmt.Errorf("nonces: %w", err)
	}

	return false, s.party.arkd.SubmitTreeNonces(ctx, event.Id, mine, nonces)
}

// OnTreeNonces signs once the operator has aggregated everyone's nonces for a
// branch. Reporting done early would leave the tree unsigned.
func (s *renewalSession) OnTreeNonces(
	ctx context.Context, event arkclient.TreeNoncesEvent,
) (bool, error) {
	complete, err := s.signer.AggregateNonces(event.Txid, event.Nonces)
	if err != nil {
		return false, fmt.Errorf("aggregating nonces: %w", err)
	}
	if !complete {
		return false, nil
	}

	sigs, err := s.signer.Sign()
	if err != nil {
		return false, fmt.Errorf("signing the tree: %w", err)
	}
	if err := s.party.arkd.SubmitTreeSignatures(
		ctx, event.Id, s.signer.GetPublicKey(), sigs,
	); err != nil {
		return false, fmt.Errorf("submitting tree signatures: %w", err)
	}

	s.signed++
	return true, nil
}

// OnBatchFinalization hands over the old coins. The tree is signed by now, so
// the fresh contract exists as soon as the commitment confirms.
func (s *renewalSession) OnBatchFinalization(
	ctx context.Context, event arkclient.BatchFinalizationEvent, vtxoTree, connectors *tree.TxTree,
) error {
	if connectors == nil {
		return fmt.Errorf("no connector tree to forfeit against")
	}

	leaves := connectors.Leaves()
	if len(leaves) != len(s.coins) {
		return fmt.Errorf("got %d connectors for %d coins", len(leaves), len(s.coins))
	}

	forfeits := make([]string, 0, len(s.coins))
	for i, coin := range s.coins {
		forfeits = append(forfeits, s.forfeit(coin, leaves[i]))
	}

	return s.party.arkd.SubmitSignedForfeitTxs(ctx, forfeits, "")
}

// forfeit builds and signs the transaction that pays one coin to the operator's
// forfeit address, against the connector that pins it to this batch.
func (s *renewalSession) forfeit(coin forfeitable, connectorTx *psbt.Packet) string {
	s.t.Helper()

	connector, outpoint := connectorOutput(s.t, connectorTx)

	sequence := uint32(wire.MaxTxInSequenceNum)
	if coin.locktime != 0 {
		sequence = wire.MaxTxInSequenceNum - 1
	}

	forfeitTx, err := tree.BuildForfeitTx(
		[]*wire.OutPoint{&coin.outpoint, outpoint},
		[]uint32{sequence, wire.MaxTxInSequenceNum},
		[]*wire.TxOut{
			{Value: coin.amount, PkScript: coin.pkScript},
			connector,
		},
		forfeitPkScript(s.t),
		uint32(coin.locktime),
	)
	if err != nil {
		s.t.Fatalf("building the forfeit: %v", err)
	}

	forfeitTx.Inputs[0].TaprootLeafScript = []*psbt.TaprootTapLeafScript{{
		ControlBlock: coin.leaf.ControlBlock,
		Script:       coin.leaf.Script,
		LeafVersion:  txscript.BaseLeafVersion,
	}}

	encoded := ""
	for _, k := range coin.signedWith {
		encoded = signWithKey(s.t, forfeitTx, k)
	}
	return encoded
}

// connectorOutput picks the spendable output of a connector transaction. The
// other one is the P2A anchor that pays for it onchain.
func connectorOutput(t *testing.T, connectorTx *psbt.Packet) (*wire.TxOut, *wire.OutPoint) {
	t.Helper()

	for i, out := range connectorTx.UnsignedTx.TxOut {
		if string(out.PkScript) == string(txutils.ANCHOR_PKSCRIPT) {
			continue
		}
		return out, &wire.OutPoint{
			Hash:  connectorTx.UnsignedTx.TxHash(),
			Index: uint32(i),
		}
	}

	t.Fatal("a connector transaction with nothing but an anchor")
	return nil, nil
}

func forfeitPkScript(t *testing.T) []byte {
	t.Helper()

	address, err := btcutil.DecodeAddress(stack.forfeitAddress, nil)
	if err != nil {
		t.Fatalf("decoding the forfeit address: %v", err)
	}
	pkScript, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatalf("forfeit script: %v", err)
	}
	return pkScript
}

// A batch that is not ours failing is not our problem: the operator runs one
// batch at a time for everybody, and the event stream carries other people's.
func (s *renewalSession) OnBatchFailed(_ context.Context, event arkclient.BatchFailedEvent) error {
	if s.sessionID != "" && event.Id != s.sessionID {
		return nil
	}
	return fmt.Errorf("batch failed: %s", event.Reason)
}

func (s *renewalSession) OnBatchFinalized(context.Context, arkclient.BatchFinalizedEvent) error {
	return nil
}

func (s *renewalSession) OnStreamStarted(context.Context, arkclient.StreamStartedEvent) error {
	return nil
}

func (s *renewalSession) OnTreeTxEvent(context.Context, arkclient.TreeTxEvent) error {
	return nil
}

func (s *renewalSession) OnTreeSignatureEvent(context.Context, arkclient.TreeSignatureEvent) error {
	return nil
}

// Superseded by OnTreeNonces, which arkd sends per branch.
func (s *renewalSession) OnTreeNoncesAggregated(
	context.Context, arkclient.TreeNoncesAggregatedEvent,
) (bool, error) {
	return false, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
