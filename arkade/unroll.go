package arkade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arkade-os/go-sdk/redemption"
	"github.com/arkade-os/go-sdk/types"
	"github.com/btcsuite/btcd/wire"
)

// unrollLimit is how many transactions deep a chain may be before we conclude
// something is wrong. A batch tree is a handful.
const unrollLimit = 40

// Unroll puts a VTXO's whole chain of transactions on the chain, which is what
// has to happen before anything can spend it without the operator.
//
// The SDK's own Unroll only handles VTXOs its wallet owns (`client.go:933`), and
// a contract belongs to no wallet — so the branch is driven directly, which is
// exactly what a party holding nothing but the contract's details would have to
// do.
//
// It returns how many transactions it published. Zero means the chain was
// already down.
func Unroll(ctx context.Context, w *Wallet, chain Chain, outpoint wire.OutPoint) (int, error) {
	branch, err := redemption.NewRedeemBranch(ctx, w.Explorer(), w.Indexer(), types.Vtxo{
		Outpoint: types.Outpoint{Txid: outpoint.Hash.String(), VOut: outpoint.Index},
	})
	if err != nil {
		return 0, fmt.Errorf("reading the contract's chain: %w", err)
	}

	// The batch commitment the chain hangs from may itself only be in the
	// mempool. Unrolling cannot start until it is in a block, because the first
	// tree transaction spends its output.
	if err := chain.Mine(ctx, 1); err != nil {
		return 0, err
	}

	published := 0
	for range unrollLimit {
		if ctx.Err() != nil {
			return published, ctx.Err()
		}

		next, err := branch.NextRedeemTx()
		if err == nil {
			if err := mineTx(ctx, chain, next); err != nil {
				return published, err
			}
			published++
			continue
		}

		// The branch says this once every transaction is on the chain.
		if strings.Contains(err.Error(), "already redeemed") {
			return published, nil
		}

		// Something in the branch is in the mempool waiting for a block.
		// Waiting is the whole answer — it is what a party unrolling for real
		// would do, and here waiting means producing one.
		var pending redemption.ErrPendingConfirmation
		if errors.As(err, &pending) {
			if err := chain.Mine(ctx, 1); err != nil {
				return published, err
			}
			time.Sleep(time.Second)
			continue
		}

		return published, fmt.Errorf("unrolling: %w", err)
	}

	return published, fmt.Errorf("the chain did not bottom out in %d transactions", unrollLimit)
}

// txMiner is a chain that can put a specific transaction in a block.
//
// Unrolling broadcasts zero-fee v3 transactions carrying a P2A anchor that a
// CPFP child is meant to pay for. Building that child is SDK plumbing, and on
// regtest there is a shortcut: put the transaction straight in a block, where
// consensus rules still apply but mempool policy does not.
type txMiner interface {
	MineTx(ctx context.Context, rawTxHex string) error
}

func mineTx(ctx context.Context, chain Chain, rawTxHex string) error {
	miner, ok := chain.(txMiner)
	if !ok {
		return fmt.Errorf("this chain cannot mine a transaction directly")
	}
	return miner.MineTx(ctx, rawTxHex)
}
