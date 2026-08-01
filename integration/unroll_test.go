//go:build integration

package integration

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arejula27/hedge/contract"
	"github.com/arkade-os/go-sdk/explorer"
	"github.com/arkade-os/go-sdk/redemption"
	"github.com/arkade-os/go-sdk/types"
	"github.com/btcsuite/btcd/wire"
)

// Unrolling is the step the earlier exit tests skipped. The contract lives
// offchain as a VTXO, built on a chain of transactions descending from a batch
// commitment. Getting at it unilaterally means putting that whole chain on the
// chain, and only then does the pre-signed exit have an output to spend.
//
// Without this, "either party can leave alone" was only proven from the point
// where the contract was already onchain.

// unroll walks the contract VTXO's chain down to the chain, one transaction per
// block. It returns the number of transactions it had to publish.
//
// The SDK's own Unroll only works on the client's own VTXOs (`client.go:933`),
// and the contract belongs to no wallet, so the branch is driven directly —
// which is exactly what a party with nothing but the contract details would
// have to do.
func unroll(t *testing.T, p *party, outpoint wire.OutPoint) int {
	t.Helper()

	branch, err := redemption.NewRedeemBranch(t.Context(), p.Explorer(), p.Indexer(), types.Vtxo{
		Outpoint: types.Outpoint{Txid: outpoint.Hash.String(), VOut: outpoint.Index},
	})
	if err != nil {
		t.Fatalf("reading the contract's chain: %v", err)
	}

	// The batch commitment the chain hangs from is itself only in the mempool:
	// nothing mines on this stack unless a test asks. Unrolling cannot start
	// until it is in a block, because the tree transaction spends its output.
	mine(t, 1)

	published := 0
	for step := 0; step < 40; step++ {
		next, err := branch.NextRedeemTx()
		if err != nil {
			// The branch reports this once every transaction is onchain.
			if strings.Contains(err.Error(), "already redeemed") {
				return published
			}

			// Something in the branch is in the mempool waiting for a block.
			// Waiting is the whole answer — it is what a party unrolling for
			// real would do, and here waiting means mining.
			var pending redemption.ErrPendingConfirmation
			if errors.As(err, &pending) {
				mine(t, 1)
				continue
			}

			t.Fatalf("next transaction to unroll: %v", err)
		}

		if err := regtestChain.MineTx(t.Context(), next); err != nil {
			t.Fatalf("mining an unroll transaction: %v", err)
		}
		published++
	}

	t.Fatal("the chain did not bottom out")
	return 0
}

// The whole unilateral exit with nothing skipped: fund the contract as a real
// VTXO through arkd, pre-sign the exit, then walk away from the operator
// entirely — unroll the chain onto Bitcoin, wait out the CSV, and sweep.
//
// This is the claim the protocol rests on, end to end.
func TestAPartyCanUnrollAndExitWithoutTheOperator(t *testing.T) {
	requireBlockDelay(t)

	c, parties, sweep := exitContract(t)

	p := newParty(t)
	p.fund(t, boardedSats)

	// A real contract VTXO, created through arkd like any other.
	outpoint := fundContract(t, p, c)

	// Pre-signed at funding, while the operator is still cooperating.
	pkg, err := c.PreSignExit(
		parties.short, parties.long, outpoint, c.Terms.PayoutSats, exitFeeSats, sweep.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit: %v", err)
	}
	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// From here on the operator is not asked for anything.
	published := unroll(t, p, outpoint)
	t.Logf("unrolled the contract in %d transactions", published)
	if published == 0 {
		t.Fatal("nothing was unrolled, so the contract was already onchain")
	}

	e := onchain(t)
	waitFor(t, 60*time.Second, "the contract output to be indexed onchain", func() error {
		return outputIsOnchain(t, e, c, outpoint)
	})

	// The CSV runs from the confirmation of the transaction that put the
	// contract output on the chain, so it starts now, not at funding.
	refuses(t, signed, reasonTooEarly)
	mine(t, int(stack.ExitDelay.Value)+1)

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

// outputIsOnchain reports whether the contract's own output is a spendable UTXO
// on the chain, which is what unrolling was for.
func outputIsOnchain(
	t *testing.T, e explorer.Explorer, c contract.Contract, outpoint wire.OutPoint,
) error {
	t.Helper()

	key, err := c.TaprootKey()
	if err != nil {
		t.Fatalf("TaprootKey: %v", err)
	}

	utxos, err := e.GetUtxos(taprootAddress(t, key))
	if err != nil {
		return err
	}
	for _, u := range utxos {
		if u.Txid == outpoint.Hash.String() && u.Vout == outpoint.Index {
			return nil
		}
	}
	return fmt.Errorf("the contract output %s:%d is not onchain",
		outpoint.Hash.String(), outpoint.Index)
}
