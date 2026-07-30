package covenant

import (
	"testing"

	"github.com/arkade-os/emulator/pkg/arkade"
)

// A $10,000 hedge against 0.2 BTC, liquidating at $50,000 and $200,000.
// Prices are USD cents per BTC, so the nominal is 1,000,000 cents scaled by 1e8.
var standard = Terms{
	NominalUnitsXSatsPerBtc:              100_000_000_000_000, // 1e6 cents × 1e8
	SatsForNominalUnitsAtHighLiquidation: 0,                   // pure 1x hedge
	PayoutSats:                           20_000_000,          // 0.2 BTC
	LowLiquidationPrice:                  5_000_000,           // $50,000
	HighLiquidationPrice:                 20_000_000,          // $200,000
}

// price encodes an oracle price as the script's witness.
func price(t *testing.T, cents int64) [][]byte {
	t.Helper()
	b, err := arkade.BigNumFromInt64(cents).Bytes()
	if err != nil {
		t.Fatalf("encoding price %d: %v", cents, err)
	}
	return [][]byte{b}
}

// Expected payouts are written out, never computed. If the table and the VM
// disagree, the VM is what settles real money.
func TestSettlementAccepts(t *testing.T) {
	tests := []struct {
		name  string
		terms Terms
		price int64
		out   Outputs
	}{
		{
			name:  "price unchanged, each side keeps its own",
			terms: standard,
			price: 10_000_000, // $100,000
			out:   Outputs{Hedge: 10_000_000, Long: 10_000_000},
		},
		{
			name:  "price doubles, the short needs half the sats and the long profits",
			terms: standard,
			price: 20_000_000, // $200,000, exactly the high boundary
			out:   Outputs{Hedge: 5_000_000, Long: 15_000_000},
		},
		{
			// The clamp is why no clock is needed: every price past a boundary
			// settles identically, so it does not matter which out-of-bounds
			// oracle message a spender picks.
			name:  "far above the high boundary settles the same as at it",
			terms: standard,
			price: 500_000_000, // $5,000,000
			out:   Outputs{Hedge: 5_000_000, Long: 15_000_000},
		},
		{
			// The long is wiped out, but still gets a dust output rather than
			// none. AnyHedge always emits exactly two outputs.
			name:  "at the low boundary the long is left with dust",
			terms: standard,
			price: 5_000_000, // $50,000
			out:   Outputs{Hedge: 20_000_000, Long: Dust},
		},
		{
			name:  "far below the low boundary settles the same as at it",
			terms: standard,
			price: 1, // absurd, and clamped away
			out:   Outputs{Hedge: 20_000_000, Long: Dust},
		},
		{
			// 100_000_000 / 3 = 33_333_333.33…  OP_DIV truncates, and the third
			// of a sat stays with the long. Rounding up would pay the short out
			// of collateral it is not owed.
			name: "truncation goes against the short side",
			terms: Terms{
				NominalUnitsXSatsPerBtc: 100_000_000,
				PayoutSats:              100_000_000,
				LowLiquidationPrice:     1,
				HighLiquidationPrice:    1_000_000,
			},
			price: 3,
			out:   Outputs{Hedge: 33_333_333, Long: 66_666_667},
		},
		{
			// 9e18 is eleven orders of magnitude past the uint32 ceiling BCH
			// works in — the constraint behind AnyHedge's published numerical
			// error analysis. BigNum carries it without comment.
			name: "a nominal far past what BCH's 4-byte ints could hold",
			terms: Terms{
				NominalUnitsXSatsPerBtc: 9_000_000_000_000_000_000, // 9e18
				PayoutSats:              2_000_000,
				LowLiquidationPrice:     1_000_000_000_000,
				HighLiquidationPrice:    10_000_000_000_000,
			},
			price: 9_000_000_000_000,
			out:   Outputs{Hedge: 1_000_000, Long: 1_000_000},
		},
		{
			// With the leverage term set, the short is no longer a pure hedge:
			// it gives up a fixed slice of its payout in exchange for leverage.
			name: "the leverage term shifts sats to the long",
			terms: Terms{
				NominalUnitsXSatsPerBtc:              100_000_000_000_000,
				SatsForNominalUnitsAtHighLiquidation: 5_000_000,
				PayoutSats:                           20_000_000,
				LowLiquidationPrice:                  5_000_000,
				HighLiquidationPrice:                 20_000_000,
			},
			price: 10_000_000,
			out:   Outputs{Hedge: 5_000_000, Long: 15_000_000},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script, err := tc.terms.SettlementScript()
			if err != nil {
				t.Fatalf("SettlementScript() error = %v", err)
			}
			if err := Run(script, price(t, tc.price), tc.out); err != nil {
				t.Errorf("VM rejected a correct settlement: %v", err)
			}
		})
	}
}

func TestSettlementRejects(t *testing.T) {
	tests := []struct {
		name  string
		price int64
		out   Outputs
	}{
		{
			name:  "short over by one sat",
			price: 10_000_000,
			out:   Outputs{Hedge: 10_000_001, Long: 9_999_999},
		},
		{
			name:  "short short by one sat",
			price: 10_000_000,
			out:   Outputs{Hedge: 9_999_999, Long: 10_000_001},
		},
		{
			// AnyHedge checks values exactly, not as a lower bound. Overpaying a
			// side is a different settlement, not a generous one.
			name:  "overpaying the short",
			price: 10_000_000,
			out:   Outputs{Hedge: 12_000_000, Long: 10_000_000},
		},
		{
			name:  "the two payouts swapped",
			price: 20_000_000,
			out:   Outputs{Hedge: 15_000_000, Long: 5_000_000},
		},
		{
			name:  "short sweeps the whole payout at a price that does not justify it",
			price: 10_000_000,
			out:   Outputs{Hedge: 20_000_000, Long: Dust},
		},
		{
			name:  "long sweeps the whole payout",
			price: 10_000_000,
			out:   Outputs{Hedge: Dust, Long: 20_000_000},
		},
		{
			name:  "both sides paid nothing",
			price: 10_000_000,
			out:   Outputs{Hedge: 0, Long: 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script, err := standard.SettlementScript()
			if err != nil {
				t.Fatalf("SettlementScript() error = %v", err)
			}
			if err := Run(script, price(t, tc.price), tc.out); err == nil {
				t.Error("VM accepted an invalid settlement")
			}
		})
	}
}

// A spender must not be able to bring extra inputs or outputs along.
func TestSettlementRejectsWrongShape(t *testing.T) {
	script, err := standard.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript() error = %v", err)
	}

	for _, n := range []int{1, 3} {
		if err := RunWithOutputCount(script, price(t, 10_000_000), n); err == nil {
			t.Errorf("VM accepted a transaction with %d outputs, want exactly 2", n)
		}
	}
}

func TestSettlementScriptRejectsBadTerms(t *testing.T) {
	tests := []struct {
		name  string
		terms Terms
	}{
		{"zero nominal", Terms{PayoutSats: 1, LowLiquidationPrice: 1, HighLiquidationPrice: 2}},
		{"zero payout", Terms{NominalUnitsXSatsPerBtc: 1, LowLiquidationPrice: 1, HighLiquidationPrice: 2}},
		{"zero low boundary", Terms{NominalUnitsXSatsPerBtc: 1, PayoutSats: 1, HighLiquidationPrice: 2}},
		{"boundaries inverted", Terms{NominalUnitsXSatsPerBtc: 1, PayoutSats: 1, LowLiquidationPrice: 9, HighLiquidationPrice: 2}},
		{"negative leverage term", Terms{
			NominalUnitsXSatsPerBtc: 1, PayoutSats: 1, LowLiquidationPrice: 1, HighLiquidationPrice: 2,
			SatsForNominalUnitsAtHighLiquidation: -1,
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.terms.SettlementScript(); err == nil {
				t.Error("SettlementScript() succeeded, want error")
			}
		})
	}
}

// The parameters are baked into the script, so identical terms must produce
// identical bytes. This is what lets a client recognise a contract by rebuilding
// it, rather than decompiling it.
func TestSettlementScriptIsDeterministic(t *testing.T) {
	first, err := standard.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript() error = %v", err)
	}
	second, err := standard.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("the same terms produced two different scripts")
	}

	nudged := standard
	nudged.NominalUnitsXSatsPerBtc++
	changed, err := nudged.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript() error = %v", err)
	}
	if string(changed) == string(first) {
		t.Error("changing the nominal did not change the script")
	}
}
