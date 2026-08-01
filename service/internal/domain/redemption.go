package domain

import "github.com/google/uuid"

// Redemption is an early close through leaf 2: both parties agree to end the
// contract at a split they choose, with no oracle and no covenant involved.
//
// It is the only leaf whose authority is the two signatures alone, which is
// also why it is the only one where getting the signing wrong is invisible
// until somebody tries to use it.
type Redemption struct {
	ID         uuid.UUID
	ContractID uuid.UUID
	ProposedBy uuid.UUID

	ShortSats int64
	LongSats  int64

	// The oracle publication the split was derived from, when it was. It
	// travels with the proposal so the other party can check the numbers
	// against the same evidence rather than against a promise, and so the
	// close can be audited afterwards.
	//
	// Zero for a split the two of them simply agreed on: there is nothing to
	// check it against, and that is the point of the leaf.
	Price     int64
	Message   []byte
	Signature []byte

	// The transaction, base64. The parties' signatures accumulate on these
	// packets across two requests, so they have to survive a round trip
	// through storage.
	ArkTx       string
	Checkpoints []string

	ShortSigned bool
	LongSigned  bool
}

// Signed says whether both parties have signed and it can be submitted.
func (r *Redemption) Signed() bool { return r.ShortSigned && r.LongSigned }

// SignedBy marks a side as having signed.
func (r *Redemption) SignedBy(side Side) {
	if side == Short {
		r.ShortSigned = true
		return
	}
	r.LongSigned = true
}

// HasSigned says whether a side already has.
func (r *Redemption) HasSigned(side Side) bool {
	if side == Short {
		return r.ShortSigned
	}
	return r.LongSigned
}

// FromOracle says whether the split came from a signed price rather than from
// the two of them agreeing on a number.
func (r *Redemption) FromOracle() bool { return len(r.Signature) > 0 }
