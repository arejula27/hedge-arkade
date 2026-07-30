// Package covenant is the reference implementation of the Arkade Hedge
// settlement math.
//
// The arithmetic mirrors what the covenant does opcode for opcode, truncation
// included, so a disagreement between this package and the Arkade VM is a bug in
// one of the two and not a rounding difference. It is deliberately dependency
// free: the VM harness builds on top of it, it does not build on the VM.
package covenant

import (
	"errors"
	"fmt"
	"math/big"
)

// SatsPerBtc is the fixed scale the settlement formula multiplies by. Prices are
// quoted in USD cents per BTC and payouts are in sats, so this is what converts
// between the two.
const SatsPerBtc = 100_000_000

// DustLimit is the payout below which no output is created at all. A payout of
// exactly DustLimit is dropped — stability_vault.ark:294 tests `> 330`, and we
// keep the same boundary.
const DustLimit = 330

// Terms are the contract parameters fixed at funding time. Neither side can
// change them afterwards, and the covenant reads them as constants.
type Terms struct {
	// HedgeValueCents is the USD value the hedge side locks in, in cents. It
	// does not move over the life of the contract: this is a fixed-term hedge,
	// not a perpetual, so there is no funding rate accruing against it.
	HedgeValueCents int64

	// TotalCollateral is the sum of both sides' contributions, in sats. It is
	// the invariant the covenant enforces on the settlement outputs.
	TotalCollateral int64
}

// Validate reports whether the terms are usable. It does not judge whether they
// are a sensible trade — a long with almost no collateral is liquidatable on
// day one, but it is not malformed.
func (t Terms) Validate() error {
	if t.HedgeValueCents < 0 {
		return fmt.Errorf("hedge value is negative: %d cents", t.HedgeValueCents)
	}
	if t.TotalCollateral <= 0 {
		return fmt.Errorf("total collateral must be positive, got %d sats", t.TotalCollateral)
	}
	return nil
}

// LiquidationPrice is the highest oracle price at which the long is already
// wiped out. At this price and below, the hedge payout reaches the whole
// collateral and the hedge side takes everything.
//
// This is AnyHedge's low liquidation threshold. We never store it: it is a
// consequence of the terms, so it is computed rather than precomputed, which is
// also what keeps the covenant from having to trust a parameter it cannot check.
func (t Terms) LiquidationPrice() int64 {
	// floor(V*1e8/P) >= C  <=>  V*1e8/P >= C  <=>  P <= V*1e8/C
	// so the largest qualifying price is the truncated quotient.
	n := new(big.Int).Mul(big.NewInt(t.HedgeValueCents), big.NewInt(SatsPerBtc))
	return n.Quo(n, big.NewInt(t.TotalCollateral)).Int64()
}

// Settlement is how the collateral splits at a given oracle price.
//
// Hedge + Long always equals Terms.TotalCollateral. The covenant computes the
// hedge payout and assigns the remainder to the long; it never derives the two
// sides independently, because two independent computations can disagree by a
// truncated sat and leave the transaction unspendable.
type Settlement struct {
	Hedge int64
	Long  int64
}

// ErrPriceNotPositive is returned for an oracle price of zero or less. The
// formula divides by the price, so there is nothing sensible to return.
var ErrPriceNotPositive = errors.New("oracle price must be positive")

// Settle applies an oracle price to the terms.
//
//	hedgePayoutSats = clamp(hedgeValueCents * 1e8 / oraclePrice, 0, totalCollateral)
//	longPayoutSats  = totalCollateral - hedgePayoutSats
//
// The division truncates. Truncation always costs the hedge side a fraction of a
// sat and hands it to the long, which is the direction we want: the party the
// covenant pays first should never be able to round its way into the other
// party's collateral.
func Settle(t Terms, oraclePriceCents int64) (Settlement, error) {
	if err := t.Validate(); err != nil {
		return Settlement{}, err
	}
	if oraclePriceCents <= 0 {
		return Settlement{}, fmt.Errorf("%w, got %d", ErrPriceNotPositive, oraclePriceCents)
	}

	raw := rawHedgePayout(t.HedgeValueCents, oraclePriceCents)

	// Upper clamp: the long has run out of collateral.
	if raw.Cmp(big.NewInt(t.TotalCollateral)) >= 0 {
		return Settlement{Hedge: t.TotalCollateral, Long: 0}, nil
	}

	// The lower clamp is unreachable while HedgeValueCents >= 0, which Validate
	// guarantees, but the covenant still carries the branch and so do we.
	hedge := raw.Int64()
	if hedge < 0 {
		hedge = 0
	}

	return Settlement{Hedge: hedge, Long: t.TotalCollateral - hedge}, nil
}

// rawHedgePayout is hedgeValueCents * 1e8 / oraclePrice before clamping.
//
// It is computed in arbitrary precision for two reasons: the VM's BigNum is
// arbitrary precision, so matching it is the point of this package; and the
// intermediate product overflows int64 for a large enough position, which is
// exactly the class of bug the published AnyHedge error analysis exists to
// document on BCH's 64-bit integers.
func rawHedgePayout(valueCents, priceCents int64) *big.Int {
	n := new(big.Int).Mul(big.NewInt(valueCents), big.NewInt(SatsPerBtc))
	return n.Quo(n, big.NewInt(priceCents))
}

// Liquidated reports whether the long side was wiped out at this price.
func (s Settlement) Liquidated() bool { return s.Long == 0 }

// Paid reports whether a payout is large enough to get its own output. A payout
// at or below the dust limit is not paid to anyone — the output is omitted and
// the value is left behind as fee.
func Paid(sats int64) bool { return sats > DustLimit }
