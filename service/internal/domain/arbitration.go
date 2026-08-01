package domain

import "github.com/google/uuid"

// Arbitration is what happens after a unilateral exit.
//
// The covenant is gone — the money is sitting in a 2-of-3 on plain Bitcoin —
// so the service works out the split instead. It has no discretion in it:
// without a valid oracle signature it cannot produce a proposal at all, and the
// message and signature travel with the proposal so the numbers can be checked
// before anyone signs and audited afterwards.
//
// It also cannot move the money alone. Two of the three keys are needed, and
// the service holds one.
type Arbitration struct {
	ID         uuid.UUID
	ContractID uuid.UUID

	ShortSats int64
	LongSats  int64

	// The evidence the split was derived from. The price here is the clamped
	// one, which is what the formula actually used.
	Price     int64
	Message   []byte
	Signature []byte

	// RawTx is the unsigned transaction spending the sweep, serialized.
	RawTx string

	// Available is what the sweep output held. Taproot signs over input values,
	// so verifying a signature needs it.
	Available int64

	// Signatures, keyed by the x-only public key that made each one. The sweep
	// takes exactly two, and a third would make the total three and fail the
	// leaf's NUMEQUAL.
	Signatures map[string]string

	// Txid is set once it is on the chain.
	Txid string
}

// Signed says whether there are enough signatures to spend the 2-of-3.
func (a *Arbitration) Signed() bool { return len(a.Signatures) >= 2 }
