package covenant

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// The mid-range price both parties would settle at if the contract had matured.
const midPrice = 10_000_000

func midSettlement() Spend { return settlement(10_000_000, 10_000_000) }

func TestOracleMessageLayout(t *testing.T) {
	msg := OracleMessage(0x1122334455667788, 0x99aabbccddeeff00, 0x0102030405060708)

	if len(msg) != OracleMessageLength {
		t.Fatalf("message is %d bytes, want %d", len(msg), OracleMessageLength)
	}

	for _, f := range []struct {
		name   string
		offset int
		want   uint64
	}{
		{"timestamp", oracleTimestampOffset, 0x1122334455667788},
		{"sequence", oracleSequenceOffset, 0x99aabbccddeeff00},
		{"price", oraclePriceOffset, 0x0102030405060708},
	} {
		got := binary.LittleEndian.Uint64(msg[f.offset : f.offset+oracleFieldSize])
		if got != f.want {
			t.Errorf("%s = %#x, want %#x", f.name, got, f.want)
		}
	}
}

// Every field must reach the signature. One that does not is a field an attacker
// can change after the oracle signed.
func TestOracleMessageCommitsToEveryField(t *testing.T) {
	base := OracleMessage(1, 2, 3)

	for _, tc := range []struct {
		name string
		msg  []byte
	}{
		{"timestamp", OracleMessage(9, 2, 3)},
		{"sequence", OracleMessage(1, 9, 3)},
		{"price", OracleMessage(1, 2, 9)},
	} {
		if bytes.Equal(tc.msg, base) {
			t.Errorf("changing the %s did not change the message", tc.name)
		}
	}
}

// Settlement is legitimate two ways: the contract matured, or the price crossed
// a liquidation boundary. Nothing else redeems it.
func TestSettlementAcceptsMaturityAndLiquidation(t *testing.T) {
	s := script(t, standard)

	tests := []struct {
		name    string
		settle  message
		prev    message
		outputs Spend
	}{
		{
			name:    "the first message on or after maturity",
			settle:  message{maturityTime, baseSequence + 1, midPrice},
			prev:    message{maturityTime - 60, baseSequence, midPrice},
			outputs: midSettlement(),
		},
		{
			name:    "a message well after maturity, as long as it is the first",
			settle:  message{maturityTime + 86_400, baseSequence + 1, midPrice},
			prev:    message{maturityTime - 1, baseSequence, midPrice},
			outputs: midSettlement(),
		},
		{
			// Liquidation before maturity: the price touched the low boundary,
			// which wipes the long out permanently, like a stop-out.
			name:    "the price crossed the low boundary before maturity",
			settle:  message{startTime + 3_600, baseSequence + 1, 4_000_000},
			prev:    message{startTime + 3_540, baseSequence, midPrice},
			outputs: settlement(20_000_000, Dust),
		},
		{
			name:    "the price crossed the high boundary before maturity",
			settle:  message{startTime + 3_600, baseSequence + 1, 50_000_000},
			prev:    message{startTime + 3_540, baseSequence, midPrice},
			outputs: settlement(5_000_000, 15_000_000),
		},
		{
			name:    "a liquidation exactly at the earliest allowed timestamp",
			settle:  message{startTime, baseSequence + 1, 4_000_000},
			prev:    message{startTime - 60, baseSequence, midPrice},
			outputs: settlement(20_000_000, Dust),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(s, signed(t, tc.settle, tc.prev), tc.outputs); err != nil {
				t.Errorf("VM rejected a legitimate settlement: %v", err)
			}
		})
	}
}

// The attack the sequence check exists to stop, and its neighbours.
func TestSettlementRejectsIllegitimateOracleMessages(t *testing.T) {
	s := script(t, standard)

	tests := []struct {
		name    string
		settle  message
		prev    message
		outputs Spend
	}{
		{
			// The whole point. A mid-range price from the middle of the
			// contract's life: not a liquidation, and not the first message
			// after maturity. Without the sequence check this is how you settle
			// at whichever historical price suits you.
			name:    "a mid-range price from before maturity",
			settle:  message{startTime + 3_600, baseSequence + 1, midPrice},
			prev:    message{startTime + 3_540, baseSequence, midPrice},
			outputs: midSettlement(),
		},
		{
			// The predecessor is also after maturity, so the settlement message
			// is not the first one — it is some later publication the spender
			// preferred.
			name:    "not the first message after maturity",
			settle:  message{maturityTime + 7_200, baseSequence + 2, midPrice},
			prev:    message{maturityTime + 60, baseSequence + 1, midPrice},
			outputs: midSettlement(),
		},
		{
			name:    "a gap in the sequence",
			settle:  message{maturityTime, baseSequence + 2, midPrice},
			prev:    message{maturityTime - 60, baseSequence, midPrice},
			outputs: midSettlement(),
		},
		{
			name:    "the predecessor is the later message",
			settle:  message{maturityTime, baseSequence, midPrice},
			prev:    message{maturityTime - 60, baseSequence + 1, midPrice},
			outputs: midSettlement(),
		},
		{
			name:    "the same message used twice",
			settle:  message{maturityTime, baseSequence, midPrice},
			prev:    message{maturityTime, baseSequence, midPrice},
			outputs: midSettlement(),
		},
		{
			// Sequences at or below zero are metadata rather than prices.
			name:    "a zero predecessor sequence",
			settle:  message{maturityTime, 1, midPrice},
			prev:    message{maturityTime - 60, 0, midPrice},
			outputs: midSettlement(),
		},
		{
			name:    "a liquidation before the earliest allowed timestamp",
			settle:  message{startTime - 1, baseSequence + 1, 4_000_000},
			prev:    message{startTime - 61, baseSequence, midPrice},
			outputs: settlement(20_000_000, Dust),
		},
		{
			name:    "a zero price",
			settle:  message{maturityTime, baseSequence + 1, 0},
			prev:    message{maturityTime - 60, baseSequence, midPrice},
			outputs: settlement(20_000_000, Dust),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(s, signed(t, tc.settle, tc.prev), tc.outputs); err == nil {
				t.Error("VM accepted an illegitimate settlement")
			}
		})
	}
}

// Without authentication the price on the witness is just a number the spender
// wrote down.
func TestSettlementRejectsUnauthenticatedPrices(t *testing.T) {
	s := script(t, standard)

	settle := message{maturityTime, baseSequence + 1, midPrice}
	prev := message{maturityTime - 60, baseSequence, midPrice}
	valid := signed(t, settle, prev)

	otherKey, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x22}, 32))

	tamper := func(mutate func(w [][]byte)) [][]byte {
		w := make([][]byte, len(valid))
		for i, item := range valid {
			w[i] = bytes.Clone(item)
		}
		mutate(w)
		return w
	}

	tests := []struct {
		name    string
		witness [][]byte
	}{
		{
			// The witness order is settleSig, settleMsg, prevSig, prevMsg.
			name: "the settlement price edited after signing",
			witness: tamper(func(w [][]byte) {
				binary.LittleEndian.PutUint64(w[1][oraclePriceOffset:], 4_000_000)
			}),
		},
		{
			name: "the settlement timestamp edited after signing",
			witness: tamper(func(w [][]byte) {
				binary.LittleEndian.PutUint64(w[1][oracleTimestampOffset:], maturityTime+1)
			}),
		},
		{
			name: "the predecessor sequence edited after signing",
			witness: tamper(func(w [][]byte) {
				binary.LittleEndian.PutUint64(w[3][oracleSequenceOffset:], baseSequence+5)
			}),
		},
		{
			name: "a signature from the wrong key",
			witness: tamper(func(w [][]byte) {
				sig, err := SignOracleMessage(otherKey, w[1])
				if err != nil {
					t.Fatalf("signing: %v", err)
				}
				w[0] = sig
			}),
		},
		{
			name: "the two signatures swapped",
			witness: tamper(func(w [][]byte) {
				w[0], w[2] = w[2], w[0]
			}),
		},
		{
			name:    "an empty signature",
			witness: tamper(func(w [][]byte) { w[0] = nil }),
		},
		{
			name: "a garbage signature",
			witness: tamper(func(w [][]byte) {
				w[0] = bytes.Repeat([]byte{0x01}, 64)
			}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(s, tc.witness, midSettlement()); err == nil {
				t.Error("VM accepted an unauthenticated price")
			}
		})
	}
}

// A contract pins one feed by key, as AnyHedge does — there is no ticker, so a
// different feed must be a different key and must not settle this contract.
func TestSettlementPinsTheOracleKey(t *testing.T) {
	otherKey, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x33}, 32))

	terms := standard
	terms.OraclePubKey = schnorr.SerializePubKey(otherKey.PubKey())

	if err := Run(script(t, terms), maturedAt(t, midPrice), midSettlement()); err == nil {
		t.Error("VM accepted a message signed by a different oracle")
	}
}

func TestSettlementScriptRejectsBadOracleTerms(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Terms)
	}{
		{"missing oracle key", func(t *Terms) { t.OraclePubKey = nil }},
		{"oracle key of the wrong length", func(t *Terms) { t.OraclePubKey = make([]byte, 33) }},
		{"zero start timestamp", func(t *Terms) { t.StartTimestamp = 0 }},
		{"maturity before the start", func(t *Terms) { t.MaturityTimestamp = t.StartTimestamp - 1 }},
		{"maturity equal to the start", func(t *Terms) { t.MaturityTimestamp = t.StartTimestamp }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			terms := standard
			tc.mutate(&terms)
			if _, err := terms.SettlementScript(); err == nil {
				t.Error("SettlementScript() succeeded, want error")
			}
		})
	}
}
