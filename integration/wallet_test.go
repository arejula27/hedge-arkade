//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/arkade/regtest"
	"github.com/arkade-os/arkd/pkg/ark-lib/offchain"
	"github.com/btcsuite/btcd/btcutil/psbt"
)

// regtestChain is arkade-regtest's own faucet and miner, reached through the
// script that starts the stack.
var regtestChain = regtest.New("../scripts/regtest.sh")

// party is one side of the contract with a real Arkade wallet behind it.
//
// The wallet and everything it does live in the `arkade` module; what is left
// here is the test-shaped wrapping — a *testing.T instead of an error.
type party struct {
	*arkade.Wallet
}

func newParty(t *testing.T) *party {
	t.Helper()

	w, err := arkade.NewWallet(t.Context(), stack, freshKey(t))
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	return &party{Wallet: w}
}

// fund boards sats from the regtest faucet and settles them into a VTXO. The
// faucet is arkade-regtest's own, so nothing outside the stack is needed.
func (p *party) fund(t *testing.T, sats int64) {
	t.Helper()

	if err := p.Fund(t.Context(), regtestChain, sats); err != nil {
		t.Fatalf("funding the party: %v", err)
	}
}

// mine advances the chain. AUTOMINE_INTERVAL is 0, so height only moves when a
// test asks it to — which is what makes a relative timelock testable.
func mine(t *testing.T, blocks int) {
	t.Helper()

	if err := regtestChain.Mine(t.Context(), blocks); err != nil {
		t.Fatal(err)
	}
}

// waitFor polls until attempt succeeds or the budget runs out, reporting the
// last error rather than only that time ran out. arkd's view of the chain lags
// the faucet, and settling is a batch that has to close.
func waitFor(t *testing.T, budget time.Duration, what string, attempt func() error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	if err := arkade.Poll(ctx, 2*time.Second, what, attempt); err != nil {
		t.Fatal(err)
	}
}

// sign signs the ark transaction and every checkpoint with the party's wallet.
// A leaf that carries no party key gets nothing added, which is the case for
// the settlement leaf — whether the rest of the stack is happy with that is
// exactly what these tests are here to find out.
func (p *party) sign(t *testing.T, arkTx *psbt.Packet, checkpoints []*psbt.Packet) (string, []string) {
	t.Helper()

	signed, signedCheckpoints, err := p.SignAll(t.Context(), arkTx, checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	return signed, signedCheckpoints
}

// submitToArkd is the path for a transaction with no covenant on its input:
// straight to the operator, sign the checkpoints it returns, finalise.
func (p *party) submitToArkd(t *testing.T, arkTx *psbt.Packet, checkpoints []*psbt.Packet) (string, error) {
	t.Helper()

	txid, err := arkade.SubmitToArkd(
		t.Context(), p.Arkd(), arkTx, checkpoints, []arkade.Signer{p.Signer()},
	)
	if err != nil {
		return "", err
	}

	// arkd has to have registered the new VTXOs before anything can spend them.
	if err := arkade.WaitForVtxo(t.Context(), p.Wallet, txid); err != nil {
		t.Fatal(err)
	}
	return txid, nil
}

// submitToEmulator is the path for a covenant input. The emulator runs the
// script, signs, and forwards to arkd itself when it holds the last signature
// (`internal/application/tx.go:146`), so one call covers both services.
func (p *party) submitToEmulator(t *testing.T, arkTx *psbt.Packet, checkpoints []*psbt.Packet) error {
	t.Helper()

	return arkade.SubmitToEmulator(
		t.Context(), stack, arkTx, checkpoints, []arkade.Signer{p.Signer()},
	)
}

// spendableVtxo finds the party's own VTXO and builds the input that spends it
// through the collaborative path of the default script.
func (p *party) spendableVtxo(t *testing.T) (offchain.VtxoInput, []byte) {
	t.Helper()

	input, pkScript, err := p.SpendableVtxo(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return input, pkScript
}
