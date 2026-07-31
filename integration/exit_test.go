//go:build integration

package integration

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arejula27/hedge/covenant"
	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/arkade-os/go-sdk/explorer"
	mempoolexplorer "github.com/arkade-os/go-sdk/explorer/mempool"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/wire"
)

// The unilateral exit is the one path that leaves Arkade entirely: no arkd, no
// emulator, no covenant — a taproot script spend that Bitcoin Core validates
// like any other. So these tests use neither service. They fund the contract
// address straight from the regtest faucet and broadcast through the explorer,
// which is exactly the position a party is in when the operator is gone.

// exitContract is a contract nothing else has funded: fresh party keys, so the
// taproot address is one this chain has never seen.
//
// The other suites use fixed keys so a failure reproduces, but these tests
// settle on a chain that survives between runs, and a leftover output at a
// shared address makes them read each other's state. The address is logged, so
// a failure is still traceable.
type exitParties struct {
	short, long, service *btcec.PrivateKey
}

func exitContract(t *testing.T) (covenant.Contract, exitParties, *covenant.Sweep) {
	t.Helper()

	parties := exitParties{freshKey(t), freshKey(t), freshKey(t)}

	c := contract(t)
	c.Keys.Short = parties.short.PubKey()
	c.Keys.Long = parties.long.PubKey()

	// Pay the parties to the same fresh keys that sign for them. Leaving the
	// shared payout addresses in place would have two runs settling into the
	// same place and reading each other's outputs.
	c.Terms.ShortLockScript = p2tr(parties.short.PubKey())
	c.Terms.LongLockScript = p2tr(parties.long.PubKey())

	sweep, err := covenant.NewSweep(
		parties.short.PubKey(), parties.long.PubKey(), parties.service.PubKey(),
	)
	if err != nil {
		t.Fatalf("NewSweep: %v", err)
	}

	return c, parties, sweep
}

func freshKey(t *testing.T) *btcec.PrivateKey {
	t.Helper()

	k, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return k
}

// exitFundedSats is what the contract holds on chain. exitFeeSats is generous:
// the exit is ~180 vbytes and regtest relays at 1 sat/vB.
const (
	exitFundedSats = 1_000_000
	exitFeeSats    = 2_000
)

func onchain(t *testing.T) explorer.Explorer {
	t.Helper()

	e, err := mempoolexplorer.NewExplorer(ExplorerURL, arklib.BitcoinRegTest)
	if err != nil {
		t.Fatalf("explorer: %v", err)
	}
	return e
}

// taprootAddress renders an output key as the regtest bech32m address the
// faucet pays to.
func taprootAddress(t *testing.T, key *btcec.PublicKey) string {
	t.Helper()

	addr, err := btcutil.NewAddressTaproot(
		schnorr.SerializePubKey(key), &chaincfg.RegressionNetParams,
	)
	if err != nil {
		t.Fatalf("taproot address: %v", err)
	}
	return addr.EncodeAddress()
}

// fundOnchain pays sats to an address and waits for the explorer to index the
// output. The contract VTXO is funded directly here rather than through arkd:
// an exit is what a party falls back on when arkd is not answering, so a test
// that needs arkd to set it up is testing the wrong thing.
func fundOnchain(t *testing.T, e explorer.Explorer, address string, sats int64) explorer.Utxo {
	t.Helper()

	amount := strings.TrimSuffix(btcutil.Amount(sats).Format(btcutil.AmountBTC), " BTC")
	if out, err := faucet(address, amount); err != nil {
		t.Fatalf("faucet %s %s: %v\n%s", address, amount, err, out)
	}

	var found explorer.Utxo
	waitFor(t, 60*time.Second, "the funding output to be indexed", func() error {
		utxos, err := e.GetUtxos(address)
		if err != nil {
			return err
		}
		for _, u := range utxos {
			if int64(u.Amount) == sats {
				found = u
				return nil
			}
		}
		return fmt.Errorf("no output of %d sats at %s yet", sats, address)
	})

	return found
}

func broadcast(t *testing.T, e explorer.Explorer, tx *wire.MsgTx) error {
	t.Helper()

	_, err := e.Broadcast(rawHex(t, tx))
	return err
}

func rawHex(t *testing.T, tx *wire.MsgTx) string {
	t.Helper()

	var raw bytes.Buffer
	if err := tx.Serialize(&raw); err != nil {
		t.Fatalf("serialising the transaction: %v", err)
	}
	return hex.EncodeToString(raw.Bytes())
}

// refuses asserts that bitcoind rejects the transaction *for the stated reason*.
// Broadcasting only surfaces RPC error -26, which a mistyped outpoint produces
// just as readily as a covenant doing its job, so the reason is the assertion.
func refuses(t *testing.T, tx *wire.MsgTx, because string) {
	t.Helper()

	out, err := regtest("testaccept", rawHex(t, tx))
	if err != nil {
		t.Fatalf("testmempoolaccept: %v\n%s", err, out)
	}

	var results []struct {
		Allowed      bool   `json:"allowed"`
		RejectReason string `json:"reject-reason"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("parsing testmempoolaccept output %q: %v", out, err)
	}
	if len(results) != 1 {
		t.Fatalf("testmempoolaccept returned %d results", len(results))
	}

	if results[0].Allowed {
		t.Fatalf("the chain accepted a transaction it had to refuse (%s)", because)
	}
	if !strings.Contains(results[0].RejectReason, because) {
		t.Fatalf("refused with %q, want %q", results[0].RejectReason, because)
	}
	t.Logf("refused: %s", results[0].RejectReason)
}

// Why bitcoind refuses each thing the exit is meant to prevent. Both are
// substrings of the reject reason rather than the whole of it: Core has
// reworded the surrounding text between versions, but not these.
const (
	// The relative timelock has not matured.
	reasonTooEarly = "non-BIP68-final"
	// The signatures are over the transaction that was signed, so a rewritten
	// one no longer verifies.
	reasonBadSignature = "Invalid Schnorr signature"
)

// requireBlockDelay skips when the operator's exit delay is seconds-based.
// Mining moves height, not the median time past, so a seconds delay cannot be
// cleared in a test — the contract is still correct, it just is not observable
// here. The regtest stacks configure blocks precisely so it is.
func requireBlockDelay(t *testing.T) {
	t.Helper()
	if !stack.allowsBlockTimelocks() {
		t.Skipf("the operator's exit delay is %+v; mining cannot clear it", stack.exitDelay)
	}
}

// The whole unilateral exit, against a real chain: fund the contract, pre-sign
// at funding time, watch the network refuse it while the delay is running, then
// broadcast it once the delay has passed and see the money land in the 2-of-3.
//
// This is the guarantee the whole design rests on. Everything else assumes the
// operator answers.
func TestTheChainAcceptsTheUnilateralExit(t *testing.T) {
	requireBlockDelay(t)

	c, parties, sweep := exitContract(t)
	e := onchain(t)

	taprootKey, err := c.TaprootKey()
	if err != nil {
		t.Fatalf("TaprootKey: %v", err)
	}
	address := taprootAddress(t, taprootKey)
	t.Logf("contract address %s", address)

	utxo := fundOnchain(t, e, address, exitFundedSats)

	// Pre-signing happens at funding, before either party needs it, and needs
	// nothing from the operator.
	outpoint, err := outpointOf(utxo)
	if err != nil {
		t.Fatalf("funding outpoint: %v", err)
	}
	pkg, err := c.PreSignExit(
		parties.short, parties.long, *outpoint, exitFundedSats, exitFeeSats, sweep.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit: %v", err)
	}
	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// The delay has not run, so the network must refuse it — and for the CSV,
	// not for anything else. Without this the test below would pass even if the
	// timelock were missing entirely.
	refuses(t, signed, reasonTooEarly)

	mine(t, int(stack.exitDelay.Value)+1)

	waitFor(t, 60*time.Second, "the chain to accept the matured exit", func() error {
		return broadcast(t, e, signed)
	})
	mine(t, 1)

	// The money is in the 2-of-3, and the contract output is gone.
	sweepAddress := taprootAddress(t, sweepKey(t, sweep))
	waitFor(t, 60*time.Second, "the swept output to appear", func() error {
		utxos, err := e.GetUtxos(sweepAddress)
		if err != nil {
			return err
		}
		for _, u := range utxos {
			if int64(u.Amount) == exitFundedSats-exitFeeSats {
				return nil
			}
		}
		return fmt.Errorf("no output of %d sats at the sweep yet", exitFundedSats-exitFeeSats)
	})

	remaining, err := e.GetUtxos(address)
	if err != nil {
		t.Fatalf("GetUtxos: %v", err)
	}
	for _, u := range remaining {
		if u.Txid == utxo.Txid && u.Vout == utxo.Vout {
			t.Fatal("the contract output was not spent")
		}
	}
}

// A party who rewrites the pre-signed exit in their own favour gets a
// transaction the chain refuses. The unit tests prove this against btcd's
// engine; this proves it against the node that would actually see it.
func TestTheChainRefusesARewrittenExit(t *testing.T) {
	requireBlockDelay(t)

	c, parties, sweep := exitContract(t)
	e := onchain(t)

	taprootKey, err := c.TaprootKey()
	if err != nil {
		t.Fatalf("TaprootKey: %v", err)
	}
	address := taprootAddress(t, taprootKey)
	t.Logf("contract address %s", address)

	// Where the short would rather the money went: a 2-of-3 the long is not in.
	thief, err := covenant.NewSweep(parties.short.PubKey(), parties.short.PubKey(), parties.short.PubKey())
	if err != nil {
		t.Fatalf("NewSweep: %v", err)
	}

	utxo := fundOnchain(t, e, address, exitFundedSats)
	outpoint, err := outpointOf(utxo)
	if err != nil {
		t.Fatalf("funding outpoint: %v", err)
	}

	pkg, err := c.PreSignExit(
		parties.short, parties.long, *outpoint, exitFundedSats, exitFeeSats, sweep.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit: %v", err)
	}
	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	mine(t, int(stack.exitDelay.Value)+1)

	for _, tc := range []struct {
		name   string
		tamper func(*wire.MsgTx)
	}{
		{"redirected to a destination the long is not in", func(tx *wire.MsgTx) {
			tx.TxOut[0].PkScript = thief.PkScript
		}},
		{"a second output skimmed off the top", func(tx *wire.MsgTx) {
			tx.TxOut[0].Value -= 100_000
			tx.AddTxOut(&wire.TxOut{Value: 100_000, PkScript: thief.PkScript})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := signed.Copy()
			tc.tamper(tampered)

			refuses(t, tampered, reasonBadSignature)
		})
	}

	// The untouched exit still goes through, so the refusals above are the
	// edits and not the setup.
	waitFor(t, 60*time.Second, "the chain to accept the untouched exit", func() error {
		return broadcast(t, e, signed)
	})

	// Confirm it rather than leaving it in the mempool. A later test reading
	// its own branch cannot tell someone else's pending transaction from one of
	// its own, and stalls waiting for it.
	mine(t, 1)
}

// sweepKey recovers the sweep's output key so it can be rendered as an address.
func sweepKey(t *testing.T, s *covenant.Sweep) *btcec.PublicKey {
	t.Helper()

	// P2TR: OP_1 <32-byte key>.
	if len(s.PkScript) != 34 {
		t.Fatalf("sweep script is %d bytes, want 34", len(s.PkScript))
	}
	key, err := schnorr.ParsePubKey(s.PkScript[2:])
	if err != nil {
		t.Fatalf("parsing the sweep output key: %v", err)
	}
	return key
}

func outpointOf(u explorer.Utxo) (*wire.OutPoint, error) {
	hash, err := chainhashFrom(u.Txid)
	if err != nil {
		return nil, err
	}
	return &wire.OutPoint{Hash: *hash, Index: u.Vout}, nil
}
