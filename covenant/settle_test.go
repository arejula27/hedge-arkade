package covenant

import (
	"errors"
	"math"
	"testing"
)

// A $10,000 hedge against 0.2 BTC of total collateral. At $100,000/BTC the hedge
// side is owed exactly 0.1 BTC, so half the collateral is each side's.
var standard = Terms{
	HedgeValueCents: 1_000_000,  // $10,000
	TotalCollateral: 20_000_000, // 0.2 BTC
}

func TestSettle(t *testing.T) {
	tests := []struct {
		name       string
		terms      Terms
		price      int64
		wantHedge  int64
		wantLong   int64
		wantLiquid bool
	}{
		{
			name:      "price unchanged, each side keeps its own",
			terms:     standard,
			price:     10_000_000, // $100,000
			wantHedge: 10_000_000,
			wantLong:  10_000_000,
		},
		{
			name:      "price doubles, hedge needs half the sats and the long profits",
			terms:     standard,
			price:     20_000_000, // $200,000
			wantHedge: 5_000_000,
			wantLong:  15_000_000,
		},
		{
			name:      "price up 10x, the hedge holds its dollar value and nothing more",
			terms:     standard,
			price:     100_000_000, // $1,000,000
			wantHedge: 1_000_000,
			wantLong:  19_000_000,
		},
		{
			// The long's collateral is exactly consumed here. This is the
			// liquidation price, and the clamp has to fire on the boundary, not
			// one sat past it.
			name:       "price halves, the long is exactly wiped out",
			terms:      standard,
			price:      5_000_000, // $50,000
			wantHedge:  20_000_000,
			wantLong:   0,
			wantLiquid: true,
		},
		{
			name:       "price below liquidation, the hedge still gets no more than the collateral",
			terms:      standard,
			price:      1_000_000, // $10,000
			wantHedge:  20_000_000,
			wantLong:   0,
			wantLiquid: true,
		},
		{
			// 100_000_000 / 3 = 33_333_333.33...  The covenant's OP_DIV truncates,
			// so the third of a sat stays with the long. It must never round up:
			// that would pay the hedge out of collateral it is not owed.
			name:      "truncation goes against the hedge side",
			terms:     Terms{HedgeValueCents: 1, TotalCollateral: 100_000_000},
			price:     3,
			wantHedge: 33_333_333,
			wantLong:  66_666_667,
		},
		{
			// hedgeValueCents * 1e8 is 1e19 here, past the int64 ceiling. On BCH
			// this is the arithmetic that needed a published error analysis; the
			// Arkade VM is arbitrary precision, and so is this package.
			name:      "intermediate product overflows int64",
			terms:     Terms{HedgeValueCents: 100_000_000_000, TotalCollateral: 2_000_000_000_000},
			price:     10_000_000,
			wantHedge: 1_000_000_000_000,
			wantLong:  1_000_000_000_000,
		},
		{
			name:      "zero hedge value leaves everything to the long",
			terms:     Terms{HedgeValueCents: 0, TotalCollateral: 20_000_000},
			price:     10_000_000,
			wantHedge: 0,
			wantLong:  20_000_000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Settle(tc.terms, tc.price)
			if err != nil {
				t.Fatalf("Settle() error = %v", err)
			}
			if got.Hedge != tc.wantHedge || got.Long != tc.wantLong {
				t.Errorf("Settle() = {Hedge: %d, Long: %d}, want {Hedge: %d, Long: %d}",
					got.Hedge, got.Long, tc.wantHedge, tc.wantLong)
			}
			if got.Liquidated() != tc.wantLiquid {
				t.Errorf("Liquidated() = %v, want %v", got.Liquidated(), tc.wantLiquid)
			}
		})
	}
}

// The invariant the covenant actually enforces on-chain. Everything else is
// commentary: if this fails the settlement transaction is unspendable.
func TestSettleConservesCollateral(t *testing.T) {
	terms := standard
	for price := int64(1); price < 200_000_000; price += 7919 {
		s, err := Settle(terms, price)
		if err != nil {
			t.Fatalf("Settle(price=%d) error = %v", price, err)
		}
		if s.Hedge+s.Long != terms.TotalCollateral {
			t.Fatalf("at price %d: %d + %d = %d, want %d",
				price, s.Hedge, s.Long, s.Hedge+s.Long, terms.TotalCollateral)
		}
		if s.Hedge < 0 || s.Long < 0 {
			t.Fatalf("at price %d: negative payout {Hedge: %d, Long: %d}", price, s.Hedge, s.Long)
		}
	}
}

// The hedge side is hedged: as the price falls its payout in sats rises, and it
// never falls as the price falls. A break here means the sides are inverted
// somewhere.
func TestHedgePayoutIsMonotonic(t *testing.T) {
	var prev int64 = math.MaxInt64
	for price := int64(1_000_000); price <= 100_000_000; price += 100_000 {
		s, err := Settle(standard, price)
		if err != nil {
			t.Fatalf("Settle(price=%d) error = %v", price, err)
		}
		if s.Hedge > prev {
			t.Fatalf("at price %d: hedge payout rose to %d from %d as the price rose", price, s.Hedge, prev)
		}
		prev = s.Hedge
	}
}

func TestSettleRejectsBadInput(t *testing.T) {
	tests := []struct {
		name  string
		terms Terms
		price int64
	}{
		{"zero price", standard, 0},
		{"negative price", standard, -10_000_000},
		{"zero collateral", Terms{HedgeValueCents: 1_000_000}, 10_000_000},
		{"negative collateral", Terms{HedgeValueCents: 1_000_000, TotalCollateral: -1}, 10_000_000},
		{"negative hedge value", Terms{HedgeValueCents: -1, TotalCollateral: 20_000_000}, 10_000_000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Settle(tc.terms, tc.price); err == nil {
				t.Error("Settle() succeeded, want error")
			}
		})
	}
}

func TestSettlePriceErrorIsIdentifiable(t *testing.T) {
	_, err := Settle(standard, 0)
	if !errors.Is(err, ErrPriceNotPositive) {
		t.Errorf("Settle(price=0) error = %v, want it to wrap ErrPriceNotPositive", err)
	}
}

func TestLiquidationPrice(t *testing.T) {
	tests := []struct {
		name  string
		terms Terms
		want  int64
	}{
		{"standard terms", standard, 5_000_000},
		{"more collateral liquidates further down", Terms{HedgeValueCents: 1_000_000, TotalCollateral: 40_000_000}, 2_500_000},
		{"a thin long liquidates almost immediately", Terms{HedgeValueCents: 1_000_000, TotalCollateral: 10_000_100}, 9_999_900},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.terms.LiquidationPrice(); got != tc.want {
				t.Fatalf("LiquidationPrice() = %d, want %d", got, tc.want)
			}
		})
	}
}

// LiquidationPrice has to agree with Settle exactly, or the service will show a
// user one number and the covenant will act on another.
func TestLiquidationPriceAgreesWithSettle(t *testing.T) {
	for _, terms := range []Terms{
		standard,
		{HedgeValueCents: 1_000_000, TotalCollateral: 40_000_000},
		{HedgeValueCents: 777_777, TotalCollateral: 13_000_013},
		{HedgeValueCents: 1, TotalCollateral: 3},
	} {
		p := terms.LiquidationPrice()

		at, err := Settle(terms, p)
		if err != nil {
			t.Fatalf("Settle(price=%d) error = %v", p, err)
		}
		if !at.Liquidated() {
			t.Errorf("terms %+v: not liquidated at the liquidation price %d", terms, p)
		}

		above, err := Settle(terms, p+1)
		if err != nil {
			t.Fatalf("Settle(price=%d) error = %v", p+1, err)
		}
		if above.Liquidated() {
			t.Errorf("terms %+v: already liquidated one cent above the liquidation price %d", terms, p)
		}
	}
}

func TestPaidAppliesDustLimitOnTheBoundary(t *testing.T) {
	tests := []struct {
		sats int64
		want bool
	}{
		{0, false},
		{329, false},
		{330, false}, // the covenant tests `> 330`, so exactly the limit is dropped
		{331, true},
		{10_000_000, true},
	}

	for _, tc := range tests {
		if got := Paid(tc.sats); got != tc.want {
			t.Errorf("Paid(%d) = %v, want %v", tc.sats, got, tc.want)
		}
	}
}

// A long left with dust gets no output at all — worth pinning, because the value
// is not redistributed, it is abandoned to fees.
func TestDustLongGetsNoOutput(t *testing.T) {
	terms := Terms{HedgeValueCents: 1_000_000, TotalCollateral: 10_000_330}

	s, err := Settle(terms, 10_000_000)
	if err != nil {
		t.Fatalf("Settle() error = %v", err)
	}
	if s.Long != 330 {
		t.Fatalf("Long = %d, want 330", s.Long)
	}
	if Paid(s.Long) {
		t.Error("a 330 sat long payout would get an output, want it dropped as dust")
	}
	if !Paid(s.Hedge) {
		t.Error("the hedge payout was dropped as dust")
	}
}
