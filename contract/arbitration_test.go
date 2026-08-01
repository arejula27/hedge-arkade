package contract

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/wire"
)

// Arbitration is not the covenant. It runs after the exit, on a plain Bitcoin
// 2-of-3, with no script and no VM anywhere in the path — so it is tested on
// its own terms here, against values written down rather than derived from
// anything the covenant does.

// What the split is for the standard contract at each price. Written down, not
// computed: a test that recomputes the rule it is checking checks nothing.
var splitTable = []struct {
	price       int64
	short, long int64
}{
	{1, 19_998_668, 1_332},             // far below the low boundary, clamped
	{4_999_999, 19_998_668, 1_332},     // just below
	{5_000_000, 19_998_668, 1_332},     // at the low boundary
	{5_000_001, 19_998_668, 1_332},     // just inside, still capped by dust
	{7_500_000, 13_333_333, 6_666_667}, // mid range, truncating down
	{10_000_000, 10_000_000, 10_000_000},
	{19_999_999, 5_000_000, 15_000_000},    // just below the high boundary
	{20_000_000, 5_000_000, 15_000_000},    // at it
	{20_000_001, 5_000_000, 15_000_000},    // just above, clamped
	{1_000_000_000, 5_000_000, 15_000_000}, // far above
}

func TestTheSplitFollowsThePrice(t *testing.T) {
	for _, tc := range splitTable {
		t.Run(fmt.Sprintf("price %d", tc.price), func(t *testing.T) {
			short, long, err := SettlementSplit(standard, tc.price)
			if err != nil {
				t.Fatalf("SettlementSplit: %v", err)
			}

			if short != tc.short || long != tc.long {
				t.Fatalf("split is %d/%d, want %d/%d", short, long, tc.short, tc.long)
			}
			if short+long != standard.PayoutSats {
				t.Fatalf("the split pays %d, not the contract's %d",
					short+long, standard.PayoutSats)
			}
		})
	}
}

// Both sides keep a spendable output at every price, including the ones where
// one of them is owed almost nothing.
func TestNeitherSideIsEverPaidBelowDust(t *testing.T) {
	for _, tc := range splitTable {
		t.Run(fmt.Sprintf("price %d", tc.price), func(t *testing.T) {
			short, long, err := SettlementSplit(standard, tc.price)
			if err != nil {
				t.Fatalf("SettlementSplit: %v", err)
			}
			if short < Dust || long < Dust {
				t.Fatalf("split %d/%d puts a side below dust (%d)", short, long, Dust)
			}
		})
	}
}

// A falling price pays the short more, monotonically. This is the shape of the
// hedge, and it holds whatever the exact numbers are.
func TestTheShortGainsAsThePriceFalls(t *testing.T) {
	previous := int64(-1)
	for i := len(splitTable) - 1; i >= 0; i-- {
		short, _, err := SettlementSplit(standard, splitTable[i].price)
		if err != nil {
			t.Fatalf("SettlementSplit: %v", err)
		}
		if short < previous {
			t.Fatalf("at price %d the short gets %d, less than %d at a higher price",
				splitTable[i].price, short, previous)
		}
		previous = short
	}
}

func TestSplitRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		terms Terms
		price int64
	}{
		{"a zero price", standard, 0},
		{"a negative price", standard, -1},
		{"terms that do not validate", Terms{}, midPrice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := SettlementSplit(tc.terms, tc.price); err == nil {
				t.Fatal("SettlementSplit accepted input it cannot serve")
			}
		})
	}
}

func oracleSigned(t *testing.T, price int64) (msg, sig []byte) {
	t.Helper()
	return oracleSignedBy(t, oracleKey, price)
}

func oracleSignedBy(t *testing.T, key *btcec.PrivateKey, price int64) (msg, sig []byte) {
	t.Helper()

	msg = OracleMessage(maturityTime, baseSequence+1, uint64(price))
	sig, err := SignOracleMessage(key, msg)
	if err != nil {
		t.Fatalf("signing the oracle message: %v", err)
	}
	return msg, sig
}

func arbitrationContract(t *testing.T) (Contract, *Sweep) {
	t.Helper()
	return contract(), sweep(t)
}

// The service can only propose a price the oracle actually published. Without
// this the 2-of-3 would just be the service's word.
func TestArbitrationNeedsTheOraclesSignature(t *testing.T) {
	c, s := arbitrationContract(t)
	msg, sig := oracleSigned(t, midPrice)

	if _, err := c.Arbitrate(
		s, contractOutpoint(), exitAmount, exitFee, msg, sig,
	); err != nil {
		t.Fatalf("a properly signed price was refused: %v", err)
	}

	_, strangerSig := oracleSignedBy(t, privKey(0x77), midPrice)

	for _, tc := range []struct {
		name     string
		msg, sig []byte
	}{
		{"no signature", msg, nil},
		{"a garbage signature", msg, bytes.Repeat([]byte{0xff}, 64)},
		{"a signature from another oracle", msg, strangerSig},
		{"the price edited after signing", editedPrice(t, msg), sig},
		{"a truncated message", msg[:len(msg)-1], sig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Arbitrate(
				s, contractOutpoint(), exitAmount, exitFee, tc.msg, tc.sig,
			); err == nil {
				t.Fatal("the service arbitrated on a price the oracle did not sign")
			}
		})
	}
}

func editedPrice(t *testing.T, msg []byte) []byte {
	t.Helper()

	edited := append([]byte(nil), msg...)
	edited[oraclePriceOffset]++
	return edited
}

// The exit cost sats, so there is less to share out than the contract promised.
// Neither side should carry the whole shortfall.
func TestTheShortfallIsSharedInProportion(t *testing.T) {
	c, s := arbitrationContract(t)
	msg, sig := oracleSigned(t, midPrice)

	const available = 19_990_000 // the exit cost 10,000 sats
	const fee = 2_000

	a, err := c.Arbitrate(s, contractOutpoint(), available, fee, msg, sig)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	if got := a.ShortSats + a.LongSats; got != available-fee {
		t.Fatalf("the arbitration pays out %d, but %d is available", got, available-fee)
	}

	// The standard contract at its opening price owes both sides the same, so
	// an even shortfall means an even split.
	if diff := a.ShortSats - a.LongSats; diff > 1 || diff < -1 {
		t.Fatalf("an even contract was arbitrated %d/%d", a.ShortSats, a.LongSats)
	}
}

// A party signs only what it has checked. Verify is that check, so it has to
// catch every way the proposal could be wrong.
func TestAPartyVerifiesBeforeSigning(t *testing.T) {
	c, s := arbitrationContract(t)
	msg, sig := oracleSigned(t, midPrice)

	const available = 19_990_000
	const fee = 2_000

	honest, err := c.Arbitrate(s, contractOutpoint(), available, fee, msg, sig)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if err := c.VerifyArbitration(honest, s, available, fee); err != nil {
		t.Fatalf("an honest arbitration did not verify: %v", err)
	}

	for _, tc := range []struct {
		name   string
		tamper func(*Arbitration)
	}{
		{"a sat moved to the short", func(a *Arbitration) {
			a.Tx.TxOut[0].Value++
			a.Tx.TxOut[1].Value--
		}},
		{"the short's payout redirected", func(a *Arbitration) {
			a.Tx.TxOut[0].PkScript = P2TR(schnorr.SerializePubKey(strangerKey))
		}},
		{"the long's payout redirected", func(a *Arbitration) {
			a.Tx.TxOut[1].PkScript = P2TR(schnorr.SerializePubKey(strangerKey))
		}},
		{"a third output added", func(a *Arbitration) {
			a.Tx.TxOut[1].Value -= 10_000
			a.Tx.AddTxOut(&wire.TxOut{
				Value: 10_000, PkScript: P2TR(schnorr.SerializePubKey(strangerKey)),
			})
		}},
		{"the whole amount taken by the short", func(a *Arbitration) {
			a.Tx.TxOut[0].Value = available - fee - Dust
			a.Tx.TxOut[1].Value = Dust
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered, err := c.Arbitrate(s, contractOutpoint(), available, fee, msg, sig)
			if err != nil {
				t.Fatalf("Arbitrate: %v", err)
			}
			tc.tamper(tampered)

			if err := c.VerifyArbitration(tampered, s, available, fee); err == nil {
				t.Fatal("a party would have signed a doctored arbitration")
			}
		})
	}
}

// A service that pays at one price and shows another as evidence is caught,
// because the party recomputes from the message it was handed.
func TestVerifyCatchesADifferentPrice(t *testing.T) {
	c, s := arbitrationContract(t)

	const available = 19_990_000
	const fee = 2_000

	honestMsg, honestSig := oracleSigned(t, midPrice)
	honest, err := c.Arbitrate(s, contractOutpoint(), available, fee, honestMsg, honestSig)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	favourableMsg, favourableSig := oracleSigned(t, 5_000_000)
	doctored, err := c.Arbitrate(s, contractOutpoint(), available, fee, favourableMsg, favourableSig)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if doctored.ShortSats == honest.ShortSats {
		t.Fatal("this test needs two prices that pay differently")
	}

	doctored.Message, doctored.Signature = honestMsg, honestSig
	if err := c.VerifyArbitration(doctored, s, available, fee); err == nil {
		t.Fatal("a party would have signed amounts the quoted price does not produce")
	}
}

// The service plus either party is two of three, and the service alone is not.
// This runs the finished witness through btcd, so it is what a node would do.
func TestTheServiceAndEitherPartyCanArbitrate(t *testing.T) {
	c, s := arbitrationContract(t)
	msg, sig := oracleSigned(t, midPrice)

	const available = 19_990_000
	const fee = 2_000

	a, err := c.Arbitrate(s, contractOutpoint(), available, fee, msg, sig)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}

	spend := func(t *testing.T, keys ...*btcec.PrivateKey) error {
		t.Helper()

		sigs := map[string][]byte{}
		for _, key := range keys {
			signature, err := c.SignArbitration(key, a, s, available)
			if err != nil {
				t.Fatalf("SignArbitration: %v", err)
			}
			sigs[XOnlyHex(key.PubKey())] = signature
		}

		signed, err := FinalizeArbitration(a, s, sigs)
		if err != nil {
			return err
		}
		return runEngine(signed, s.PkScript, available)
	}

	for _, tc := range []struct {
		name string
		keys []*btcec.PrivateKey
	}{
		{"the service and the short", []*btcec.PrivateKey{servicePriv, shortPriv}},
		{"the service and the long", []*btcec.PrivateKey{servicePriv, longPriv}},
		{"the two parties without the service", []*btcec.PrivateKey{shortPriv, longPriv}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := spend(t, tc.keys...); err != nil {
				t.Fatalf("%s could not move the money: %v", tc.name, err)
			}
		})
	}

	t.Run("the service alone", func(t *testing.T) {
		if err := spend(t, servicePriv); err == nil {
			t.Fatal("the service moved the money on its own")
		}
	})
}

func TestArbitrationRefusesWhatItCannotPay(t *testing.T) {
	c, s := arbitrationContract(t)
	msg, sig := oracleSigned(t, midPrice)

	for _, tc := range []struct {
		name           string
		available, fee int64
	}{
		{"a fee larger than the balance", 20_000, 30_000},
		{"nothing left after the fee", 20_000, 20_000},
		{"less than a dust output each", 2*Dust - 1, 0},
		{"a negative fee", 20_000, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Arbitrate(
				s, contractOutpoint(), tc.available, tc.fee, msg, sig,
			); err == nil {
				t.Fatal("the service built an arbitration it cannot pay")
			}
		})
	}
}
