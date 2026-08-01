package arkade

import (
	"context"
	"fmt"

	"github.com/arejula27/hedge/contract"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/wire"
)

// Stake is what one side puts into a contract, and the wallet it comes from.
type Stake struct {
	Wallet *Wallet
	Sats   int64
}

// BuildBilateralFunding builds one transaction with an input from each party
// and the contract as the first output. Both parties' change comes back to them.
//
// The stakes have to sum to PayoutSats, which is what the covenant pins the
// input to: a contract funded with the wrong total can never settle.
func BuildBilateralFunding(
	ctx context.Context, s *Stack, c contract.Contract, short, long Stake,
) (*psbt.Packet, []*psbt.Packet, error) {
	if short.Sats+long.Sats != c.Terms.PayoutSats {
		return nil, nil, fmt.Errorf("stakes %d + %d do not add up to the contract's %d",
			short.Sats, long.Sats, c.Terms.PayoutSats)
	}

	contractPkScript, err := c.PkScript()
	if err != nil {
		return nil, nil, fmt.Errorf("PkScript: %w", err)
	}

	shortInput, shortChangeScript, err := short.Wallet.SpendableVtxo(ctx, short.Sats)
	if err != nil {
		return nil, nil, fmt.Errorf("the short's collateral: %w", err)
	}
	longInput, longChangeScript, err := long.Wallet.SpendableVtxo(ctx, long.Sats)
	if err != nil {
		return nil, nil, fmt.Errorf("the long's collateral: %w", err)
	}

	outputs := []*wire.TxOut{{Value: c.Terms.PayoutSats, PkScript: contractPkScript}}
	for _, change := range []struct {
		amount   int64
		pkScript []byte
	}{
		{shortInput.Amount - short.Sats, shortChangeScript},
		{longInput.Amount - long.Sats, longChangeScript},
	} {
		if change.amount <= int64(s.Dust) {
			return nil, nil, fmt.Errorf("change of %d is not above the operator's dust %d",
				change.amount, s.Dust)
		}
		outputs = append(outputs, &wire.TxOut{Value: change.amount, PkScript: change.pkScript})
	}

	arkTx, checkpoints, err := offchain.BuildTxs(
		[]offchain.VtxoInput{shortInput, longInput}, outputs, s.CheckpointTapscript,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("building the funding transaction: %w", err)
	}
	return arkTx, checkpoints, nil
}

// FundBilaterally builds the funding transaction, has both parties sign it, and
// submits it. It returns the contract VTXO's outpoint, which is always output 0.
//
// This transaction has no covenant on its input, so it goes straight to arkd.
func FundBilaterally(
	ctx context.Context, s *Stack, c contract.Contract, short, long Stake,
) (wire.OutPoint, error) {
	arkTx, checkpoints, err := BuildBilateralFunding(ctx, s, c, short, long)
	if err != nil {
		return wire.OutPoint{}, err
	}

	signers := []Signer{short.Wallet.Signer(), long.Wallet.Signer()}
	txid, err := SubmitToArkd(ctx, short.Wallet.Arkd(), arkTx, checkpoints, signers)
	if err != nil {
		return wire.OutPoint{}, fmt.Errorf("arkd refused the funding transaction: %w", err)
	}

	if err := WaitForVtxo(ctx, short.Wallet, txid); err != nil {
		return wire.OutPoint{}, err
	}

	hash, err := ChainHash(txid)
	if err != nil {
		return wire.OutPoint{}, fmt.Errorf("funding txid %q: %w", txid, err)
	}
	return wire.OutPoint{Hash: *hash, Index: 0}, nil
}
