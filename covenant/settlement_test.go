package covenant

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

var (
	shortScript = P2TR(bytes.Repeat([]byte{0xaa}, 32))
	longScript  = P2TR(bytes.Repeat([]byte{0xbb}, 32))
	thiefScript = P2TR(bytes.Repeat([]byte{0xcc}, 32))
)

const (
	startTime    = 1_800_000_000
	maturityTime = 1_800_086_400 // a day later
	baseSequence = 5_000
)

// oracleKey is fixed rather than random so a failure reproduces exactly.
var oracleKey, _ = btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x11}, 32))

func oraclePubKey() []byte { return schnorr.SerializePubKey(oracleKey.PubKey()) }

// withDefaults fills in the parameters every case shares.
func withDefaults(t Terms) Terms {
	if t.ShortLockScript == nil {
		t.ShortLockScript = shortScript
	}
	if t.LongLockScript == nil {
		t.LongLockScript = longScript
	}
	if t.OraclePubKey == nil {
		t.OraclePubKey = oraclePubKey()
	}
	if t.StartTimestamp == 0 {
		t.StartTimestamp = startTime
	}
	if t.MaturityTimestamp == 0 {
		t.MaturityTimestamp = maturityTime
	}
	return t
}

// A $10,000 hedge against 0.2 BTC, liquidating at $50,000 and $200,000.
// Prices are USD cents per BTC, so the nominal is 1,000,000 cents scaled by 1e8.
var standard = withDefaults(Terms{
	NominalUnitsXSatsPerBtc:              100_000_000_000_000, // 1e6 cents × 1e8
	SatsForNominalUnitsAtHighLiquidation: 0,                   // pure 1x hedge
	PayoutSats:                           20_000_000,          // 0.2 BTC
	LowLiquidationPrice:                  5_000_000,           // $50,000
	HighLiquidationPrice:                 20_000_000,          // $200,000
})

// message is one oracle publication.
type message struct {
	timestamp, sequence, price uint64
}

// signed returns the witness for a settlement message and its predecessor,
// bottom of the stack first.
func signed(t *testing.T, settle, prev message) [][]byte {
	t.Helper()

	settleMsg := OracleMessage(settle.timestamp, settle.sequence, settle.price)
	prevMsg := OracleMessage(prev.timestamp, prev.sequence, prev.price)

	settleSig, err := SignOracleMessage(oracleKey, settleMsg)
	if err != nil {
		t.Fatalf("signing settlement message: %v", err)
	}
	prevSig, err := SignOracleMessage(oracleKey, prevMsg)
	if err != nil {
		t.Fatalf("signing previous message: %v", err)
	}

	return [][]byte{settleSig, settleMsg, prevSig, prevMsg}
}

// maturedAt is the ordinary settlement witness: the predecessor lands just
// before maturity and the settlement message is the very next publication.
func maturedAt(t *testing.T, price int64) [][]byte {
	t.Helper()
	return signed(t,
		message{timestamp: maturityTime, sequence: baseSequence + 1, price: uint64(price)},
		message{timestamp: maturityTime - 60, sequence: baseSequence, price: uint64(price)},
	)
}

func script(t *testing.T, terms Terms) []byte {
	t.Helper()
	s, err := terms.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript() error = %v", err)
	}
	return s
}

// settlement builds a well-formed spend paying the two configured recipients.
func settlement(short, long int64) Spend {
	return Spend{Outputs: []Payout{
		{Sats: short, LockScript: shortScript},
		{Sats: long, LockScript: longScript},
	}}
}

// The one source of expected values. Every other test in this file derives from
// it, so a payout is written down exactly once and never computed in Go.
type settlementCase struct {
	name  string
	terms Terms
	price int64
	short int64
	long  int64
}

var accepted = []settlementCase{
	{
		name:  "price unchanged, each side keeps its own",
		terms: standard,
		price: 10_000_000, // $100,000
		short: 10_000_000, long: 10_000_000,
	},
	{
		name:  "at the high boundary the long has won all it can",
		terms: standard,
		price: 20_000_000, // $200,000
		short: 5_000_000, long: 15_000_000,
	},
	{
		// The clamp is why no clock is needed: every price past a boundary
		// settles identically, so it does not matter which out-of-bounds oracle
		// message a spender picks.
		name:  "far above the high boundary settles the same as at it",
		terms: standard,
		price: 500_000_000,
		short: 5_000_000, long: 15_000_000,
	},
	{
		name:  "one cent inside the high boundary is not clamped",
		terms: standard,
		price: 19_999_999,
		short: 5_000_000, long: 15_000_000,
	},
	{
		// The long is wiped out but still gets a dust output rather than none.
		name:  "at the low boundary the long is left with dust",
		terms: standard,
		price: 5_000_000, // $50,000
		short: 20_000_000, long: Dust,
	},
	{
		name:  "far below the low boundary settles the same as at it",
		terms: standard,
		price: 1,
		short: 20_000_000, long: Dust,
	},
	{
		// 1e14/5_000_001 leaves the long 4 sats, which the dust floor raises to
		// Dust. The two payouts therefore sum to more than PayoutSats — that is
		// AnyHedge's behaviour, and the input covers it.
		name:  "one cent inside the low boundary is not clamped",
		terms: standard,
		price: 5_000_001,
		short: 19_999_996, long: Dust,
	},
	{
		// 100_000_000 / 3 = 33_333_333.33…  OP_DIV truncates, and the third of a
		// sat stays with the long.
		name: "truncation goes against the short side",
		terms: withDefaults(Terms{
			NominalUnitsXSatsPerBtc: 100_000_000,
			PayoutSats:              100_000_000,
			LowLiquidationPrice:     1,
			HighLiquidationPrice:    1_000_000,
			ShortLockScript:         shortScript,
			LongLockScript:          longScript,
		}),
		price: 3,
		short: 33_333_333, long: 66_666_667,
	},
	{
		// 9e18 is eleven orders of magnitude past the uint32 ceiling BCH works
		// in — the constraint behind AnyHedge's published error analysis.
		name: "a nominal far past what BCH's 4-byte ints could hold",
		terms: withDefaults(Terms{
			NominalUnitsXSatsPerBtc: 9_000_000_000_000_000_000,
			PayoutSats:              2_000_000,
			LowLiquidationPrice:     1_000_000_000_000,
			HighLiquidationPrice:    10_000_000_000_000,
			ShortLockScript:         shortScript,
			LongLockScript:          longScript,
		}),
		price: 9_000_000_000_000,
		short: 1_000_000, long: 1_000_000,
	},
	{
		// With the leverage term set the short is no longer a pure hedge: it
		// gives up a fixed slice of its payout in exchange for leverage.
		name: "the leverage term shifts sats to the long",
		terms: withDefaults(Terms{
			NominalUnitsXSatsPerBtc:              100_000_000_000_000,
			SatsForNominalUnitsAtHighLiquidation: 5_000_000,
			PayoutSats:                           20_000_000,
			LowLiquidationPrice:                  5_000_000,
			HighLiquidationPrice:                 20_000_000,
			ShortLockScript:                      shortScript,
			LongLockScript:                       longScript,
		}),
		price: 10_000_000,
		short: 5_000_000, long: 15_000_000,
	},
}

func TestSettlementAccepts(t *testing.T) {
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(script(t, tc.terms), maturedAt(t, tc.price), settlement(tc.short, tc.long)); err != nil {
				t.Errorf("VM rejected a correct settlement: %v", err)
			}
		})
	}
}

// Values are checked exactly, not as a lower bound. For every accepted
// settlement, nudging either side by a sat in either direction must fail —
// including nudging *up*, because overpaying is a different settlement rather
// than a generous one.
func TestSettlementIsExactToTheSat(t *testing.T) {
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			s := script(t, tc.terms)

			for _, nudge := range []struct {
				what        string
				short, long int64
			}{
				{"short one sat under", tc.short - 1, tc.long},
				{"short one sat over", tc.short + 1, tc.long},
				{"long one sat under", tc.short, tc.long - 1},
				{"long one sat over", tc.short, tc.long + 1},
				{"a sat moved from long to short", tc.short + 1, tc.long - 1},
				{"a sat moved from short to long", tc.short - 1, tc.long + 1},
			} {
				if err := Run(s, maturedAt(t, tc.price), settlement(nudge.short, nudge.long)); err == nil {
					t.Errorf("VM accepted %s", nudge.what)
				}
			}
		})
	}
}

// Getting the amounts right is pointless if they can be sent anywhere.
func TestSettlementPinsRecipients(t *testing.T) {
	const p, short, long = 10_000_000, 10_000_000, 10_000_000
	s := script(t, standard)

	tests := []struct {
		name  string
		spend Spend
	}{
		{"the short payout redirected", Spend{Outputs: []Payout{
			{Sats: short, LockScript: thiefScript}, {Sats: long, LockScript: longScript},
		}}},
		{"the long payout redirected", Spend{Outputs: []Payout{
			{Sats: short, LockScript: shortScript}, {Sats: long, LockScript: thiefScript},
		}}},
		{"both payouts redirected", Spend{Outputs: []Payout{
			{Sats: short, LockScript: thiefScript}, {Sats: long, LockScript: thiefScript},
		}}},
		{"recipients swapped", Spend{Outputs: []Payout{
			{Sats: short, LockScript: longScript}, {Sats: long, LockScript: shortScript},
		}}},
		{"the short paid to a bare P2WPKH instead of taproot", Spend{Outputs: []Payout{
			{Sats: short, LockScript: append([]byte{0x00, 0x14}, bytes.Repeat([]byte{0xaa}, 20)...)},
			{Sats: long, LockScript: longScript},
		}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(s, maturedAt(t, p), tc.spend); err == nil {
				t.Error("VM accepted a settlement paying the wrong recipient")
			}
		})
	}
}

// Amounts and recipients are correct in every case here, so a rejection can only
// come from the shape check.
func TestSettlementPinsTransactionShape(t *testing.T) {
	const p, short, long = 10_000_000, 10_000_000, 10_000_000
	s := script(t, standard)

	t.Run("a third output", func(t *testing.T) {
		spend := settlement(short, long)
		spend.Outputs = append(spend.Outputs, Payout{Sats: 1_000, LockScript: thiefScript})
		if err := Run(s, maturedAt(t, p), spend); err == nil {
			t.Error("VM accepted a third output")
		}
	})

	t.Run("only one output", func(t *testing.T) {
		spend := Spend{Outputs: []Payout{{Sats: short, LockScript: shortScript}}}
		if err := Run(s, maturedAt(t, p), spend); err == nil {
			t.Error("VM accepted a single output")
		}
	})

	t.Run("a second input", func(t *testing.T) {
		spend := settlement(short, long)
		spend.ExtraInputs = 1
		if err := Run(s, maturedAt(t, p), spend); err == nil {
			t.Error("VM accepted an extra input")
		}
	})
}

// As the price rises the short needs fewer sats and never more. A break here
// means the two sides are inverted somewhere.
func TestShortPayoutIsMonotonicInPrice(t *testing.T) {
	byPrice := map[int64]int64{}
	for _, tc := range accepted {
		if tc.terms.LowLiquidationPrice == standard.LowLiquidationPrice &&
			tc.terms.SatsForNominalUnitsAtHighLiquidation == 0 {
			byPrice[tc.price] = tc.short
		}
	}

	var last int64 = 1 << 62
	for _, p := range []int64{1, 5_000_000, 5_000_001, 10_000_000, 19_999_999, 20_000_000, 500_000_000} {
		short, ok := byPrice[p]
		if !ok {
			t.Fatalf("no accepted case for price %d", p)
		}
		if short > last {
			t.Errorf("at price %d the short payout rose to %d from %d as the price rose", p, short, last)
		}
		last = short
	}
}

func TestSettlementScriptRejectsBadTerms(t *testing.T) {
	valid := standard

	tests := []struct {
		name   string
		mutate func(*Terms)
	}{
		{"zero nominal", func(t *Terms) { t.NominalUnitsXSatsPerBtc = 0 }},
		{"negative nominal", func(t *Terms) { t.NominalUnitsXSatsPerBtc = -1 }},
		{"zero payout", func(t *Terms) { t.PayoutSats = 0 }},
		{"zero low boundary", func(t *Terms) { t.LowLiquidationPrice = 0 }},
		{"boundaries inverted", func(t *Terms) {
			t.LowLiquidationPrice, t.HighLiquidationPrice = t.HighLiquidationPrice, t.LowLiquidationPrice
		}},
		{"boundaries equal", func(t *Terms) { t.HighLiquidationPrice = t.LowLiquidationPrice }},
		{"negative leverage term", func(t *Terms) { t.SatsForNominalUnitsAtHighLiquidation = -1 }},
		{"missing short lock script", func(t *Terms) { t.ShortLockScript = nil }},
		{"missing long lock script", func(t *Terms) { t.LongLockScript = nil }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			terms := valid
			tc.mutate(&terms)
			if _, err := terms.SettlementScript(); err == nil {
				t.Error("SettlementScript() succeeded, want error")
			}
		})
	}
}

// Every parameter must reach the script. One that does not is a parameter an
// attacker can change without changing the address the counterparty verified.
func TestEveryParameterChangesTheScript(t *testing.T) {
	base := script(t, standard)

	tests := []struct {
		name   string
		mutate func(*Terms)
	}{
		{"nominal", func(t *Terms) { t.NominalUnitsXSatsPerBtc++ }},
		{"leverage term", func(t *Terms) { t.SatsForNominalUnitsAtHighLiquidation++ }},
		{"payout", func(t *Terms) { t.PayoutSats++ }},
		{"low boundary", func(t *Terms) { t.LowLiquidationPrice++ }},
		{"high boundary", func(t *Terms) { t.HighLiquidationPrice++ }},
		{"short recipient", func(t *Terms) { t.ShortLockScript = thiefScript }},
		{"long recipient", func(t *Terms) { t.LongLockScript = thiefScript }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			terms := standard
			tc.mutate(&terms)
			if bytes.Equal(script(t, terms), base) {
				t.Errorf("changing the %s did not change the script", tc.name)
			}
		})
	}
}

// The client verifies a contract by rebuilding it and comparing bytes, so the
// build must be deterministic and stable. When this fails on purpose, update the
// constant — and remember the TypeScript verifier has to move with it.
const standardScriptHex = "d4519dd5529d00d1519d20aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa8851d1519d20bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb88766ba8204f355bdcb7cc0af728ef3cceb9615d90684bb5b2ca5f859ab0f0b704075871aacc69766ba8204f355bdcb7cc0af728ef3cceb9615d90684bb5b2ca5f859ab0f0b704075871aacc696c6c7600587fd80480234b6b9f6958587fd87600a06951937858587fd89d7600587fd8760400d2496ba2697c60587fd87600a06904002d3101a303404b4ca4766b7603404b4c9c7c04002d31019c9b7c0480234b6ba29b696c0600407a10f35a7c960094023405a47604002d31017c94023405a451cf9d00cf9c"

func TestSettlementScriptIsStable(t *testing.T) {
	got := hex.EncodeToString(script(t, standard))

	if got != standardScriptHex {
		t.Errorf("script changed.\n got: %s\nwant: %s", got, standardScriptHex)
	}
}

func TestSettlementScriptIsDeterministic(t *testing.T) {
	if !bytes.Equal(script(t, standard), script(t, standard)) {
		t.Error("the same terms produced two different scripts")
	}
}
