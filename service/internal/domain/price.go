package domain

// Price is one oracle publication as the service sees it. The signed bytes stay
// with the oracle client; what a use case needs is the number and when.
type Price struct {
	Sequence  uint64
	Timestamp int64
	// Price is in the quote asset's smallest unit per BTC — cents, so
	// 10_000_000 is $100,000.
	Price int64
}

// CentsPerBTC is the unit every price and both liquidation boundaries are in.
//
// The nominal hedge value is a different scale again: Terms carries
// hedgeValueCents × 1e8, so the covenant's division has room to work in
// integers. Confusing the two silently builds a contract for the wrong amount,
// which is why the conversion lives in one function with a table behind it.
const CentsPerBTC = 100

// NominalUnits scales a hedge value in cents to what Terms wants.
func NominalUnits(hedgeValueCents int64) int64 {
	return hedgeValueCents * 100_000_000
}

// HedgeValueCents is the inverse, for showing a contract back to the person who
// created it.
func HedgeValueCents(nominalUnits int64) int64 {
	return nominalUnits / 100_000_000
}
