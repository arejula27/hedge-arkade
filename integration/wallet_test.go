//go:build integration

package integration

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	arkscript "github.com/arkade-os/arkd/pkg/ark-lib/script"
	arksdk "github.com/arkade-os/go-sdk"
	arkclient "github.com/arkade-os/go-sdk/client"
	grpcclient "github.com/arkade-os/go-sdk/client/grpc"
	"github.com/arkade-os/go-sdk/explorer"
	mempoolexplorer "github.com/arkade-os/go-sdk/explorer/mempool"
	"github.com/arkade-os/go-sdk/indexer"
	grpcindexer "github.com/arkade-os/go-sdk/indexer/grpc"
	"github.com/arkade-os/go-sdk/store"
	"github.com/arkade-os/go-sdk/types"
	"github.com/arkade-os/go-sdk/wallet"
	singlekeywallet "github.com/arkade-os/go-sdk/wallet/singlekey"
	inmemorystore "github.com/arkade-os/go-sdk/wallet/singlekey/store/inmemory"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

const walletPassword = "password"

// party is one side of the contract with a real Arkade wallet behind it.
type party struct {
	sdk      arksdk.ArkClient
	wallet   wallet.WalletService
	pubKey   *btcec.PublicKey
	arkd     arkclient.TransportClient
	indexer  indexer.Indexer
	explorer explorer.Explorer
}

func newParty(t *testing.T) *party {
	t.Helper()
	ctx := t.Context()

	appData, err := store.NewStore(store.Config{
		ConfigStoreType:  types.InMemoryStore,
		AppDataStoreType: types.KVStore,
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	sdk, err := arksdk.NewArkClient(appData)
	if err != nil {
		t.Fatalf("ark client: %v", err)
	}

	walletStore, err := inmemorystore.NewWalletStore()
	if err != nil {
		t.Fatalf("wallet store: %v", err)
	}

	walletSvc, err := singlekeywallet.NewBitcoinWallet(appData.ConfigStore(), walletStore)
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}

	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	// ExplorerURL is not optional on regtest: left empty the SDK falls back to
	// mempool.space, which knows nothing about this chain.
	if err := sdk.InitWithWallet(ctx, arksdk.InitWithWalletArgs{
		Wallet:      walletSvc,
		ClientType:  arksdk.GrpcClient,
		ServerUrl:   ArkdURL,
		Password:    walletPassword,
		Seed:        hex.EncodeToString(privKey.Serialize()),
		ExplorerURL: ExplorerURL,
	}); err != nil {
		t.Fatalf("InitWithWallet: %v", err)
	}
	if err := sdk.Unlock(ctx, walletPassword); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	arkd, err := grpcclient.NewClient(ArkdURL)
	if err != nil {
		t.Fatalf("arkd transport: %v", err)
	}
	indexerSvc, err := grpcindexer.NewClient(ArkdURL)
	if err != nil {
		t.Fatalf("indexer: %v", err)
	}
	explorerSvc, err := mempoolexplorer.NewExplorer(ExplorerURL, arklib.BitcoinRegTest)
	if err != nil {
		t.Fatalf("explorer: %v", err)
	}

	return &party{
		sdk: sdk, wallet: walletSvc, pubKey: privKey.PubKey(),
		arkd: arkd, indexer: indexerSvc, explorer: explorerSvc,
	}
}

// fund boards sats from the regtest faucet and settles them into a VTXO. The
// faucet is arkade-regtest's own, so nothing outside the stack is needed.
func (p *party) fund(t *testing.T, sats int64) {
	t.Helper()
	ctx := t.Context()

	_, _, boarding, err := p.sdk.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	amount := strings.TrimSuffix(btcutil.Amount(sats).Format(btcutil.AmountBTC), " BTC")
	if out, err := faucet(boarding, amount); err != nil {
		t.Fatalf("faucet %s %s: %v\n%s", boarding, amount, err, out)
	}

	// The faucet confirms its own payment, but arkd's view of the chain lags it,
	// and Settle has to wait for a batch to close. Retrying covers both.
	var settleErr error
	waitFor(t, 2*time.Minute, "Settle to succeed", func() error {
		_, settleErr = p.sdk.Settle(ctx)
		return settleErr
	})

	waitFor(t, 60*time.Second, "a spendable VTXO to appear", func() error {
		spendable, _, err := p.sdk.ListVtxos(ctx)
		if err != nil {
			return err
		}
		if len(spendable) == 0 {
			return fmt.Errorf("no spendable VTXOs yet")
		}
		return nil
	})
}

func faucet(address, amountBtc string) (string, error) {
	return regtest("faucet", address, amountBtc)
}

// mine advances the chain. AUTOMINE_INTERVAL is 0, so height only moves when a
// test asks it to — which is what makes a relative timelock testable.
func mine(t *testing.T, blocks int) {
	t.Helper()
	if out, err := regtest("mine", strconv.Itoa(blocks)); err != nil {
		t.Fatalf("mining %d blocks: %v\n%s", blocks, err, out)
	}
}

func regtest(args ...string) (string, error) {
	cmd := exec.Command("./scripts/regtest.sh", args...)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// waitFor polls until attempt succeeds or the budget runs out, reporting the
// last error rather than only that time ran out. arkd's view of the chain lags
// the faucet, and settling is a batch that has to close.
func waitFor(t *testing.T, budget time.Duration, what string, attempt func() error) {
	t.Helper()

	deadline := time.Now().Add(budget)
	var last error
	for time.Now().Before(deadline) {
		last = attempt()
		if last == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("waited %s for %s; last error: %v", budget, what, last)
}

// sign signs the ark transaction and every checkpoint with the party's wallet.
// A leaf that carries no party key gets nothing added, which is the case for
// the settlement leaf — whether the rest of the stack is happy with that is
// exactly what these tests are here to find out.
func (p *party) sign(t *testing.T, arkTx *psbt.Packet, checkpoints []*psbt.Packet) (string, []string) {
	t.Helper()
	ctx := t.Context()

	encoded, err := arkTx.B64Encode()
	if err != nil {
		t.Fatalf("encoding the transaction: %v", err)
	}

	signed, err := p.wallet.SignTransaction(ctx, p.explorer, encoded)
	if err != nil {
		t.Fatalf("signing the transaction: %v", err)
	}

	signedCheckpoints := make([]string, 0, len(checkpoints))
	for _, checkpoint := range encode(t, checkpoints) {
		s, err := p.wallet.SignTransaction(ctx, p.explorer, checkpoint)
		if err != nil {
			t.Fatalf("signing a checkpoint: %v", err)
		}
		signedCheckpoints = append(signedCheckpoints, s)
	}

	return signed, signedCheckpoints
}

// submitToArkd is the path for a transaction with no covenant on its input:
// straight to the operator, sign the checkpoints it returns, finalise.
func (p *party) submitToArkd(t *testing.T, arkTx *psbt.Packet, checkpoints []*psbt.Packet) (string, error) {
	t.Helper()
	ctx := t.Context()

	signed, signedCheckpoints := p.sign(t, arkTx, checkpoints)

	txid, _, returned, err := p.arkd.SubmitTx(ctx, signed, signedCheckpoints)
	if err != nil {
		return "", err
	}

	final := make([]string, 0, len(returned))
	for _, checkpoint := range returned {
		s, err := p.wallet.SignTransaction(ctx, p.explorer, checkpoint)
		if err != nil {
			t.Fatalf("signing a returned checkpoint: %v", err)
		}
		final = append(final, s)
	}

	if err := p.arkd.FinalizeTx(ctx, txid, final); err != nil {
		return "", err
	}

	// arkd has to have registered the new VTXOs before anything can spend them.
	waitFor(t, 30*time.Second, "the new VTXOs to be registered", func() error {
		spendable, _, err := p.sdk.ListVtxos(ctx)
		if err != nil {
			return err
		}
		for _, v := range spendable {
			if v.Txid == txid {
				return nil
			}
		}
		return fmt.Errorf("tx %s not among %d spendable VTXOs", txid, len(spendable))
	})

	return txid, nil
}

// submitToEmulator is the path for a covenant input. The emulator runs the
// script, signs, and forwards to arkd itself when it holds the last signature
// (`internal/application/tx.go:146`), so one call covers both services.
func (p *party) submitToEmulator(t *testing.T, arkTx *psbt.Packet, checkpoints []*psbt.Packet) error {
	t.Helper()

	signed, signedCheckpoints := p.sign(t, arkTx, checkpoints)

	_, _, err := stack.emulator.SubmitTx(t.Context(), signed, signedCheckpoints)
	return err
}

// spendableVtxo finds the party's own VTXO and builds the input that spends it
// through the collaborative path of the default script.
func (p *party) spendableVtxo(t *testing.T) (offchain.VtxoInput, []byte) {
	t.Helper()
	ctx := t.Context()

	vtxoScript := arkscript.NewDefaultVtxoScript(p.pubKey, stack.arkdSigner, stack.exitDelay)

	tapKey, tapTree, err := vtxoScript.TapTree()
	if err != nil {
		t.Fatalf("TapTree: %v", err)
	}
	pkScript, err := arkscript.P2TRScript(tapKey)
	if err != nil {
		t.Fatalf("P2TRScript: %v", err)
	}

	spendable, _, err := p.sdk.ListVtxos(ctx)
	if err != nil {
		t.Fatalf("ListVtxos: %v", err)
	}

	wanted := hex.EncodeToString(pkScript)
	for _, v := range spendable {
		if v.Script != wanted {
			continue
		}

		forfeits := vtxoScript.ForfeitClosures()
		if len(forfeits) == 0 {
			t.Fatal("the default vtxo script has no collaborative path")
		}
		leaf, err := forfeits[0].Script()
		if err != nil {
			t.Fatalf("leaf script: %v", err)
		}

		proof, err := tapTree.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(leaf).TapHash())
		if err != nil {
			t.Fatalf("merkle proof: %v", err)
		}
		control, err := txscript.ParseControlBlock(proof.ControlBlock)
		if err != nil {
			t.Fatalf("ParseControlBlock: %v", err)
		}
		revealed, err := vtxoScript.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		hash, err := chainhashFrom(v.Txid)
		if err != nil {
			t.Fatalf("vtxo txid %q: %v", v.Txid, err)
		}

		return offchain.VtxoInput{
			Outpoint: &wire.OutPoint{Hash: *hash, Index: v.VOut},
			Tapscript: &waddrmgr.Tapscript{
				ControlBlock:   control,
				RevealedScript: proof.Script,
			},
			Amount:             int64(v.Amount),
			RevealedTapscripts: revealed,
		}, pkScript
	}

	t.Fatalf("no spendable VTXO at %s; the party has %d", wanted, len(spendable))
	return offchain.VtxoInput{}, nil
}

func chainhashFrom(txid string) (*chainhash.Hash, error) {
	return chainhash.NewHashFromStr(strings.TrimSpace(txid))
}
