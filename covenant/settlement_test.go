package covenant

import (
	"testing"

	"github.com/arkade-os/emulator/pkg/arkade"
)

// A $10,000 hedge against 0.2 BTC of collateral. At $100,000/BTC the hedge side
// is owed exactly 0.1 BTC, so the collateral splits down the middle.
var standard = Terms{
	HedgeValueCents: 1_000_000,  // $10,000
	TotalCollateral: 20_000_000, // 0.2 BTC
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

// Expected payouts are written out in the table, not computed. If the VM and
// this table disagree, the VM is what settles real money.
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
			name:  "price doubles, the hedge needs half the sats and the long profits",
			terms: standard,
			price: 20_000_000, // $200,000
			out:   Outputs{Hedge: 5_000_000, Long: 15_000_000},
		},
		{
			name:  "price up 10x, the hedge holds its dollar value and nothing more",
			terms: standard,
			price: 100_000_000, // $1,000,000
			out:   Outputs{Hedge: 1_000_000, Long: 19_000_000},
		},
		{
			// The long's collateral is exactly consumed. The clamp has to fire on
			// the boundary, not one sat past it.
			name:  "price halves, the long is exactly wiped out",
			terms: standard,
			price: 5_000_000, // $50,000
			out:   Outputs{Hedge: 20_000_000, Long: 0},
		},
		{
			name:  "below liquidation, the hedge still gets no more than the collateral",
			terms: standard,
			price: 1_000_000, // $10,000
			out:   Outputs{Hedge: 20_000_000, Long: 0},
		},
		{
			// 100_000_000 / 3 = 33_333_333.33…  OP_DIV truncates, so the third of
			// a sat stays with the long. Rounding up would pay the hedge out of
			// collateral it is not owed.
			name:  "truncation goes against the hedge side",
			terms: Terms{HedgeValueCents: 1, TotalCollateral: 100_000_000},
			price: 3,
			out:   Outputs{Hedge: 33_333_333, Long: 66_666_667},
		},
		{
			// hedgeValueCents * 1e8 is 1e19 here, past the int64 ceiling. On BCH
			// this is the arithmetic that needed a published error analysis; the
			// VM's BigNum has no such ceiling.
			name:  "intermediate product overflows int64",
			terms: Terms{HedgeValueCents: 100_000_000_000, TotalCollateral: 2_000_000_000_000},
			price: 10_000_000,
			out:   Outputs{Hedge: 1_000_000_000_000, Long: 1_000_000_000_000},
		},
		{
			name:  "overpaying a side is allowed",
			terms: standard,
			price: 10_000_000,
			out:   Outputs{Hedge: 12_000_000, Long: 10_000_000},
		},
		{
			// Just above liquidation the long is owed 4 sats, which is dust, so
			// no second output is created and everything goes to the hedge. The
			// dust band is a range of prices where liquidation is effectively
			// already complete, not a single point.
			name:  "just above liquidation the long's share is dust",
			terms: standard,
			price: 5_000_001,
			out:   Outputs{Hedge: 20_000_000, Long: 0},
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
		terms Terms
		price int64
		out   Outputs
	}{
		{
			name:  "hedge short by one sat",
			terms: standard,
			price: 10_000_000,
			out:   Outputs{Hedge: 9_999_999, Long: 10_000_001},
		},
		{
			name:  "long short by one sat",
			terms: standard,
			price: 10_000_000,
			out:   Outputs{Hedge: 10_000_000, Long: 9_999_999},
		},
		{
			name:  "hedge takes everything at a price that does not justify it",
			terms: standard,
			price: 10_000_000,
			out:   Outputs{Hedge: 20_000_000, Long: 0},
		},
		{
			name:  "long takes everything",
			terms: standard,
			price: 10_000_000,
			out:   Outputs{Hedge: 0, Long: 20_000_000},
		},
		{
			// Above the liquidation price the long is still owed a non-dust
			// share, so a full sweep to the hedge must fail.
			name:  "sweep to the hedge above liquidation",
			terms: standard,
			price: 5_100_000,
			out:   Outputs{Hedge: 20_000_000, Long: 0},
		},
		{
			name:  "both sides paid nothing",
			terms: standard,
			price: 10_000_000,
			out:   Outputs{Hedge: 0, Long: 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script, err := tc.terms.SettlementScript()
			if err != nil {
				t.Fatalf("SettlementScript() error = %v", err)
			}
			if err := Run(script, price(t, tc.price), tc.out); err == nil {
				t.Error("VM accepted an invalid settlement")
			}
		})
	}
}

func TestSettlementScriptRejectsBadTerms(t *testing.T) {
	tests := []struct {
		name  string
		terms Terms
	}{
		{"zero collateral", Terms{HedgeValueCents: 1_000_000}},
		{"negative collateral", Terms{HedgeValueCents: 1_000_000, TotalCollateral: -1}},
		{"negative hedge value", Terms{HedgeValueCents: -1, TotalCollateral: 20_000_000}},
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
// identical bytes. This is what lets a client rebuild the script from the
// parameters it was shown and compare, rather than decompile.
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
		t.Error("the same terms produced two different scripts")
	}

	other := Terms{HedgeValueCents: 1_000_001, TotalCollateral: 20_000_000}
	changed, err := other.SettlementScript()
	if err != nil {
		t.Fatalf("SettlementScript() error = %v", err)
	}
	if string(changed) == string(first) {
		t.Error("changing the hedge value did not change the script")
	}
}
