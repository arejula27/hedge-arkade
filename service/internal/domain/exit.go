package domain

import "github.com/google/uuid"

// Sweep is where a unilateral exit lands: a 2-of-3 between the two parties and
// the service, on plain Bitcoin.
//
// Inside a VTXO every closure the operator decodes is N-of-N, so the leaf that
// authorises the exit is a 2-of-2 after a delay. The *destination* is an
// ordinary Bitcoin output script and can be a real threshold, which is what
// lets a party who has lost their counterparty still be paid.
type Sweep struct {
	PkScript     []byte
	Leaf         []byte
	ControlBlock []byte
}

// Exit is the transaction that takes a contract out of Arkade.
//
// It is built at funding, before either party needs it. RawTx is the unsigned
// transaction, serialized: both parties derive the same bytes independently
// from the contract and the outpoint, so neither has to trust the service's
// copy of it.
type Exit struct {
	ContractID uuid.UUID
	RawTx      []byte
	// Amount is what the contract VTXO held. Taproot signs over input values,
	// so verifying a signature needs it.
	Amount int64
	Sweep  Sweep
}

// ExitPackage is the exit with both signatures on it. From the moment it is
// complete, either party can leave alone.
type ExitPackage struct {
	Exit
	ShortSig []byte
	LongSig  []byte

	// Swept is where the exit landed, once it has been broadcast. It is written
	// as soon as the transaction is on the chain, because everything after that
	// point — arbitrating, signing, paying — has to be able to start again from
	// the row alone.
	Swept     *Outpoint
	SweptSats int64
}

// Complete says whether both parties have signed.
func (p ExitPackage) Complete() bool {
	return len(p.ShortSig) > 0 && len(p.LongSig) > 0
}

// OnChain says whether the exit has been broadcast and found.
func (p ExitPackage) OnChain() bool { return p.Swept != nil }
