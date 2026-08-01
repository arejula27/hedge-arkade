//go:build integration

package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/arejula27/hedge/contract"
	"github.com/arkade-os/go-sdk/explorer"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
)

// What happens to the money after a unilateral exit, on a real chain.
//
// The covenant is gone by this point, so bitcoind is the only thing left
// enforcing anything. That is what these tests put the arbitration in front of:
// no emulator, no arkd, no VM — just a 2-of-3 and a node.

// arbitrationFeeSats is what the arbitration leaves for the miner. The
// transaction is ~200 vbytes and regtest relays at 1 sat/vB.
const arbitrationFeeSats = 2_000

// exitTo runs a contract all the way into the 2-of-3 and returns the sweep
// output waiting there. This is the earlier exit test, reused as a starting
// position rather than as the thing under test.
func exitTo(
	t *testing.T, e explorer.Explorer, c contract.Contract,
	parties exitParties, sweep *contract.Sweep,
) (wire.OutPoint, int64) {
	t.Helper()

	taprootKey, err := c.TaprootKey()
	if err != nil {
		t.Fatalf("TaprootKey: %v", err)
	}
	address := taprootAddress(t, taprootKey)
	t.Logf("contract address %s", address)

	utxo := fundOnchain(t, e, address, exitFundedSats)
	outpoint, err := outpointOf(utxo)
	if err != nil {
		t.Fatalf("funding outpoint: %v", err)
	}

	pkg, err := c.PreSignExit(
		parties.short, parties.long, *outpoint,
		exitFundedSats, exitFeeSats, sweep.PkScript,
	)
	if err != nil {
		t.Fatalf("PreSignExit: %v", err)
	}
	signed, err := c.Finalize(pkg)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	mine(t, int(stack.ExitDelay.Value)+1)
	waitFor(t, 60*time.Second, "the chain to accept the exit", func() error {
		return broadcast(t, e, signed)
	})
	mine(t, 1)

	swept := int64(exitFundedSats - exitFeeSats)
	sweepAddress := taprootAddress(t, sweepKey(t, sweep))

	var found explorer.Utxo
	waitFor(t, 60*time.Second, "the swept output to appear", func() error {
		utxos, err := e.GetUtxos(sweepAddress)
		if err != nil {
			return err
		}
		for _, u := range utxos {
			if int64(u.Amount) == swept {
				found = u
				return nil
			}
		}
		return fmt.Errorf("no output of %d sats at the sweep yet", swept)
	})

	sweepOutpoint, err := outpointOf(found)
	if err != nil {
		t.Fatalf("sweep outpoint: %v", err)
	}
	return *sweepOutpoint, swept
}

// signedPrice is the oracle publication the service arbitrates on.
func signedPrice(t *testing.T, price uint64) (msg, sig []byte) {
	t.Helper()

	msg = contract.OracleMessage(maturityTime, baseSequence+1, price)
	sig, err := contract.SignOracleMessage(oracleKey, msg)
	if err != nil {
		t.Fatalf("signing the oracle message: %v", err)
	}
	return msg, sig
}

// paidOnchain waits for an address to hold an output of exactly sats.
func paidOnchain(t *testing.T, e explorer.Explorer, key *btcec.PublicKey, sats int64) {
	t.Helper()

	address := taprootAddress(t, key)
	waitFor(t, 60*time.Second, "the payout to appear onchain", func() error {
		utxos, err := e.GetUtxos(address)
		if err != nil {
			return err
		}
		for _, u := range utxos {
			if int64(u.Amount) == sats {
				return nil
			}
		}
		return fmt.Errorf("no output of %d sats at %s yet", sats, address)
	})
}

// The whole aftermath: a contract exits onto the chain, the service arbitrates
// on an oracle-signed price, a party checks the proposal and signs it, and both
// sides are paid.
//
// The service never holds the money and never moves it alone. It only decides
// the number, and the number is one anybody can recompute.
func TestTheServiceArbitratesAfterAnExit(t *testing.T) {
	requireBlockDelay(t)

	c, parties, sweep := exitContract(t)
	e := onchain(t)

	sweepOutpoint, available := exitTo(t, e, c, parties, sweep)

	// The service builds the proposal. It cannot invent a price: without the
	// oracle's signature Arbitrate refuses outright.
	msg, sig := signedPrice(t, settlementPrice)
	proposal, err := c.Arbitrate(
		sweep, sweepOutpoint, available, arbitrationFeeSats, msg, sig,
	)
	if err != nil {
		t.Fatalf("the service could not arbitrate: %v", err)
	}
	t.Logf("arbitrated %d/%d at price %d",
		proposal.ShortSats, proposal.LongSats, proposal.Price)

	// The party checks it before putting its key anywhere near it.
	if err := c.VerifyArbitration(proposal, sweep, available, arbitrationFeeSats); err != nil {
		t.Fatalf("the party refused a proposal it should have accepted: %v", err)
	}

	// Service plus one party is two of three.
	final := signArbitration(t, c, proposal, sweep, available,
		parties.service, parties.short)

	waitFor(t, 60*time.Second, "the chain to accept the arbitration", func() error {
		return broadcast(t, e, final)
	})
	mine(t, 1)

	paidOnchain(t, e, parties.short.PubKey(), proposal.ShortSats)
	paidOnchain(t, e, parties.long.PubKey(), proposal.LongSats)
}

// The service cannot empty the 2-of-3 by itself. This is the property that
// makes it safe to let it decide the split at all.
func TestTheServiceCannotArbitrateAlone(t *testing.T) {
	requireBlockDelay(t)

	c, parties, sweep := exitContract(t)
	e := onchain(t)

	sweepOutpoint, available := exitTo(t, e, c, parties, sweep)

	msg, sig := signedPrice(t, settlementPrice)
	proposal, err := c.Arbitrate(
		sweep, sweepOutpoint, available, arbitrationFeeSats, msg, sig,
	)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	// One signature is not enough to build a witness at all.
	signature, err := c.SignArbitration(parties.service, proposal, sweep, available)
	if err != nil {
		t.Fatalf("SignArbitration: %v", err)
	}
	if _, err := contract.FinalizeArbitration(proposal, sweep, map[string][]byte{
		contract.XOnlyHex(parties.service.PubKey()): signature,
	}); err == nil {
		t.Fatal("the service built a witness on its own signature")
	}

	// And a proposal the service pays itself, signed by the service and a
	// counterparty key it does not have, cannot reach the chain either: it can
	// only ever produce one signature.
	stolen := proposal.Tx.Copy()
	stolen.TxOut[0].PkScript = p2tr(parties.service.PubKey())
	stolen.TxOut[0].Value = available - arbitrationFeeSats - int64(stack.Dust)
	stolen.TxOut[1].Value = int64(stack.Dust)

	theft := &contract.Arbitration{
		Tx: stolen, ShortSats: stolen.TxOut[0].Value, LongSats: stolen.TxOut[1].Value,
		Message: msg, Signature: sig,
	}
	if err := c.VerifyArbitration(theft, sweep, available, arbitrationFeeSats); err == nil {
		t.Fatal("a party would have signed the service paying itself")
	}
}

// signArbitration collects signatures and finalises, failing the test rather
// than returning an error — a signing problem here is a broken test, not a
// result worth asserting on.
func signArbitration(
	t *testing.T, c contract.Contract, a *contract.Arbitration,
	sweep *contract.Sweep, available int64, signers ...*btcec.PrivateKey,
) *wire.MsgTx {
	t.Helper()

	sigs := make(map[string][]byte, len(signers))
	for _, key := range signers {
		sig, err := c.SignArbitration(key, a, sweep, available)
		if err != nil {
			t.Fatalf("SignArbitration: %v", err)
		}
		sigs[contract.XOnlyHex(key.PubKey())] = sig
	}

	final, err := contract.FinalizeArbitration(a, sweep, sigs)
	if err != nil {
		t.Fatalf("FinalizeArbitration: %v", err)
	}
	return final
}
