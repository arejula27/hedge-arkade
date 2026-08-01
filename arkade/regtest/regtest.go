// Package regtest drives arkade-regtest through scripts/regtest.sh.
//
// It implements arkade.Chain, plus the two things only bitcoind can answer:
// mining a specific transaction, and saying why it would refuse one.
package regtest

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/btcsuite/btcd/btcutil"
)

// Script runs scripts/regtest.sh. Callers pass the path rather than let the
// package guess it: the tests run from integration/ and the service from
// service/.
type Script struct {
	// Path to the script, absolute.
	Path string
	// Root is the repo root, which the script resolves REGTEST_DIR against.
	Root string
}

// New takes a path to scripts/regtest.sh, relative to the caller's working
// directory or absolute.
//
// The repo root is derived from it rather than taken separately. The script
// looks for .regtest in its own working directory, so running it from anywhere
// else finds no stack at all — and the failure surfaces as a faucet that
// refuses, several layers away from the cause.
func New(path string) *Script {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return &Script{Path: abs, Root: filepath.Dir(filepath.Dir(abs))}
}

func (s *Script) Faucet(ctx context.Context, address string, sats int64) error {
	amount := strings.TrimSuffix(btcutil.Amount(sats).Format(btcutil.AmountBTC), " BTC")
	out, err := s.run(ctx, "faucet", address, amount)
	if err != nil {
		return fmt.Errorf("faucet %s %s: %w\n%s", address, amount, err, out)
	}
	return nil
}

func (s *Script) Mine(ctx context.Context, blocks int) error {
	out, err := s.run(ctx, "mine", strconv.Itoa(blocks))
	if err != nil {
		return fmt.Errorf("mining %d blocks: %w\n%s", blocks, err, out)
	}
	return nil
}

// MineTx puts a transaction straight in a block, skipping mempool policy.
// Unrolling broadcasts zero-fee v3 transactions carrying a P2A anchor that a
// CPFP child is meant to pay for; consensus rules still apply.
func (s *Script) MineTx(ctx context.Context, rawTxHex string) error {
	out, err := s.run(ctx, "minetx", rawTxHex)
	if err != nil {
		return fmt.Errorf("mining a transaction: %w\n%s", err, out)
	}
	return nil
}

// TestAccept asks bitcoind why it would refuse a transaction. Broadcasting only
// reports RPC error -26, which cannot tell a covenant's refusal from a typo in
// the setup.
func (s *Script) TestAccept(ctx context.Context, rawTxHex string) (string, error) {
	return s.run(ctx, "testaccept", rawTxHex)
}

func (s *Script) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, s.Path, args...)
	cmd.Dir = s.Root
	out, err := cmd.CombinedOutput()
	return string(out), err
}
