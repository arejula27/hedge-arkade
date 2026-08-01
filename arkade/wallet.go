package arkade

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
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
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/waddrmgr"
)

// The SDK insists on locking the wallet with something. The seed is supplied by
// the caller and the store is in memory, so this protects nothing that is not
// already in the process.
const walletPassword = "password"

// Wallet is one Arkade wallet: the go-sdk client, the key behind it, and the
// three transports that go with it.
type Wallet struct {
	sdk    arksdk.ArkClient
	wallet wallet.WalletService

	// privKey is the same key the wallet holds. Some paths cannot go through
	// the wallet — a contract leaf is not a leaf the wallet knows about — so
	// they sign with the raw key instead.
	privKey *btcec.PrivateKey
	pubKey  *btcec.PublicKey

	arkd     arkclient.TransportClient
	indexer  indexer.Indexer
	explorer explorer.Explorer

	stack *Stack
}

// NewWallet builds a wallet on top of an existing seed. Storage is in memory:
// the seed is what persists, and everything else is derived from it and from
// arkd on the way up.
func NewWallet(ctx context.Context, s *Stack, seed *btcec.PrivateKey) (*Wallet, error) {
	cfg := s.config

	appData, err := store.NewStore(store.Config{
		ConfigStoreType:  types.InMemoryStore,
		AppDataStoreType: types.KVStore,
	})
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	sdk, err := arksdk.NewArkClient(appData)
	if err != nil {
		return nil, fmt.Errorf("ark client: %w", err)
	}

	walletStore, err := inmemorystore.NewWalletStore()
	if err != nil {
		return nil, fmt.Errorf("wallet store: %w", err)
	}

	walletSvc, err := singlekeywallet.NewBitcoinWallet(appData.ConfigStore(), walletStore)
	if err != nil {
		return nil, fmt.Errorf("wallet: %w", err)
	}

	// ExplorerURL is not optional on regtest: left empty the SDK falls back to
	// mempool.space, which knows nothing about this chain.
	if err := sdk.InitWithWallet(ctx, arksdk.InitWithWalletArgs{
		Wallet:      walletSvc,
		ClientType:  arksdk.GrpcClient,
		ServerUrl:   cfg.ArkdURL,
		Password:    walletPassword,
		Seed:        hex.EncodeToString(seed.Serialize()),
		ExplorerURL: cfg.ExplorerURL,
	}); err != nil {
		return nil, fmt.Errorf("InitWithWallet: %w", err)
	}
	if err := sdk.Unlock(ctx, walletPassword); err != nil {
		return nil, fmt.Errorf("Unlock: %w", err)
	}

	arkd, err := grpcclient.NewClient(cfg.ArkdURL)
	if err != nil {
		return nil, fmt.Errorf("arkd transport: %w", err)
	}
	indexerSvc, err := grpcindexer.NewClient(cfg.ArkdURL)
	if err != nil {
		return nil, fmt.Errorf("indexer: %w", err)
	}
	network := cfg.Network
	if network.Name == "" {
		network = arklib.BitcoinRegTest
	}
	explorerSvc, err := mempoolexplorer.NewExplorer(cfg.ExplorerURL, network)
	if err != nil {
		return nil, fmt.Errorf("explorer: %w", err)
	}

	return &Wallet{
		sdk: sdk, wallet: walletSvc,
		privKey: seed, pubKey: seed.PubKey(),
		arkd: arkd, indexer: indexerSvc, explorer: explorerSvc,
		stack: s,
	}, nil
}

func (w *Wallet) PublicKey() *btcec.PublicKey     { return w.pubKey }
func (w *Wallet) PrivateKey() *btcec.PrivateKey   { return w.privKey }
func (w *Wallet) SDK() arksdk.ArkClient           { return w.sdk }
func (w *Wallet) Service() wallet.WalletService   { return w.wallet }
func (w *Wallet) Arkd() arkclient.TransportClient { return w.arkd }
func (w *Wallet) Indexer() indexer.Indexer        { return w.indexer }
func (w *Wallet) Explorer() explorer.Explorer     { return w.explorer }
func (w *Wallet) Stack() *Stack                   { return w.stack }

// Addresses returns where the wallet can be paid: offchain from inside Arkade,
// boarding from the chain.
func (w *Wallet) Addresses(ctx context.Context) (offchainAddr, boarding string, err error) {
	offchainAddr, _, boarding, err = w.sdk.Receive(ctx)
	return offchainAddr, boarding, err
}

func (w *Wallet) ListVtxos(ctx context.Context) ([]types.Vtxo, error) {
	spendable, _, err := w.sdk.ListVtxos(ctx)
	return spendable, err
}

// Balance is the sum of the wallet's spendable VTXOs.
func (w *Wallet) Balance(ctx context.Context) (int64, error) {
	spendable, err := w.ListVtxos(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, v := range spendable {
		total += int64(v.Amount)
	}
	return total, nil
}

func (w *Wallet) Settle(ctx context.Context) (string, error) {
	return w.sdk.Settle(ctx)
}

// Fund boards sats from the chain's faucet and settles them into a VTXO.
//
// Each settle attempt mines. On a stack with no automatic miner an input arkd
// is still calling unconfirmed would stay that way however long we waited, so
// retrying without producing blocks is retrying nothing. Where a miner does run
// the extra block is harmless.
func (w *Wallet) Fund(ctx context.Context, chain Chain, sats int64) error {
	_, boarding, err := w.Addresses(ctx)
	if err != nil {
		return fmt.Errorf("Receive: %w", err)
	}

	if err := chain.Faucet(ctx, boarding, sats); err != nil {
		return err
	}

	settling, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := Poll(settling, 2*time.Second, "Settle to succeed", func() error {
		if err := chain.Mine(ctx, 1); err != nil {
			return err
		}
		_, err := w.sdk.Settle(ctx)
		return err
	}); err != nil {
		return err
	}

	appearing, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return Poll(appearing, 2*time.Second, "a spendable VTXO to appear", func() error {
		spendable, err := w.ListVtxos(ctx)
		if err != nil {
			return err
		}
		if len(spendable) == 0 {
			return fmt.Errorf("no spendable VTXOs yet")
		}
		return nil
	})
}

// vtxoScript is the wallet's own default script: collaborative with the
// operator, unilateral after the exit delay.
func (w *Wallet) vtxoScript() *arkscript.TapscriptsVtxoScript {
	return arkscript.NewDefaultVtxoScript(w.pubKey, w.stack.ArkdSigner, w.stack.ExitDelay)
}

// VtxoPkScript is where the wallet is paid inside Arkade.
//
// This is the script a contract's payout has to name. A bare P2TR of the
// wallet's key would be a perfectly valid output that no Arkade wallet indexes,
// so the money would settle correctly and be unspendable.
func (w *Wallet) VtxoPkScript() ([]byte, error) {
	tapKey, _, err := w.vtxoScript().TapTree()
	if err != nil {
		return nil, fmt.Errorf("TapTree: %w", err)
	}
	return arkscript.P2TRScript(tapKey)
}

// SpendableVtxo finds a VTXO of the wallet's own worth at least min and builds
// the input that spends it through the collaborative path. It also returns the
// wallet's pkScript, which is where change goes.
func (w *Wallet) SpendableVtxo(ctx context.Context, min int64) (offchain.VtxoInput, []byte, error) {
	vtxoScript := w.vtxoScript()

	tapKey, tapTree, err := vtxoScript.TapTree()
	if err != nil {
		return offchain.VtxoInput{}, nil, fmt.Errorf("TapTree: %w", err)
	}
	pkScript, err := arkscript.P2TRScript(tapKey)
	if err != nil {
		return offchain.VtxoInput{}, nil, fmt.Errorf("P2TRScript: %w", err)
	}

	spendable, err := w.ListVtxos(ctx)
	if err != nil {
		return offchain.VtxoInput{}, nil, fmt.Errorf("ListVtxos: %w", err)
	}

	wanted := hex.EncodeToString(pkScript)
	for _, v := range spendable {
		if v.Script != wanted || int64(v.Amount) < min {
			continue
		}

		forfeits := vtxoScript.ForfeitClosures()
		if len(forfeits) == 0 {
			return offchain.VtxoInput{}, nil, fmt.Errorf("the default vtxo script has no collaborative path")
		}
		leaf, err := forfeits[0].Script()
		if err != nil {
			return offchain.VtxoInput{}, nil, fmt.Errorf("leaf script: %w", err)
		}

		proof, err := tapTree.GetTaprootMerkleProof(txscript.NewBaseTapLeaf(leaf).TapHash())
		if err != nil {
			return offchain.VtxoInput{}, nil, fmt.Errorf("merkle proof: %w", err)
		}
		control, err := txscript.ParseControlBlock(proof.ControlBlock)
		if err != nil {
			return offchain.VtxoInput{}, nil, fmt.Errorf("ParseControlBlock: %w", err)
		}
		revealed, err := vtxoScript.Encode()
		if err != nil {
			return offchain.VtxoInput{}, nil, fmt.Errorf("Encode: %w", err)
		}

		hash, err := ChainHash(v.Txid)
		if err != nil {
			return offchain.VtxoInput{}, nil, fmt.Errorf("vtxo txid %q: %w", v.Txid, err)
		}

		return offchain.VtxoInput{
			Outpoint: &wire.OutPoint{Hash: *hash, Index: v.VOut},
			Tapscript: &waddrmgr.Tapscript{
				ControlBlock:   control,
				RevealedScript: proof.Script,
			},
			Amount:             int64(v.Amount),
			RevealedTapscripts: revealed,
		}, pkScript, nil
	}

	return offchain.VtxoInput{}, nil, fmt.Errorf(
		"no spendable VTXO at %s worth %d or more; the wallet has %d", wanted, min, len(spendable))
}

// SignPacket signs the inputs of a packet whose leaf carries the wallet's key
// and leaves the rest alone, so a wallet cannot sign for its counterparty.
func (w *Wallet) SignPacket(ctx context.Context, packetB64 string) (string, error) {
	return w.wallet.SignTransaction(ctx, w.explorer, packetB64)
}

// Signer exposes the wallet as the signing half of a submission.
func (w *Wallet) Signer() Signer { return w.SignPacket }

// SignAll signs the ark transaction and every checkpoint.
//
// A leaf that carries no key of the wallet's gets nothing added, which is the
// case for a contract's settlement leaf.
func (w *Wallet) SignAll(
	ctx context.Context, arkTx *psbt.Packet, checkpoints []*psbt.Packet,
) (string, []string, error) {
	encoded, err := arkTx.B64Encode()
	if err != nil {
		return "", nil, fmt.Errorf("encoding the transaction: %w", err)
	}

	signed, err := w.SignPacket(ctx, encoded)
	if err != nil {
		return "", nil, fmt.Errorf("signing the transaction: %w", err)
	}

	encodedCheckpoints, err := Encode(checkpoints)
	if err != nil {
		return "", nil, err
	}

	signedCheckpoints := make([]string, 0, len(encodedCheckpoints))
	for _, checkpoint := range encodedCheckpoints {
		s, err := w.SignPacket(ctx, checkpoint)
		if err != nil {
			return "", nil, fmt.Errorf("signing a checkpoint: %w", err)
		}
		signedCheckpoints = append(signedCheckpoints, s)
	}

	return signed, signedCheckpoints, nil
}

func Encode(packets []*psbt.Packet) ([]string, error) {
	encoded := make([]string, 0, len(packets))
	for _, p := range packets {
		b64, err := p.B64Encode()
		if err != nil {
			return nil, fmt.Errorf("encoding a packet: %w", err)
		}
		encoded = append(encoded, b64)
	}
	return encoded, nil
}

func ChainHash(txid string) (*chainhash.Hash, error) {
	return chainhash.NewHashFromStr(strings.TrimSpace(txid))
}
