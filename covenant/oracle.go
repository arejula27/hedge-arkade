package covenant

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// The oracle message layout, mirroring AnyHedge's but with 8-byte fields
// instead of 4. AnyHedge is stuck with uint32, which is why its timestamps stop
// working in 2106; BigNum has no such constraint.
//
//	 0..8   timestamp, Unix seconds
//	 8..16  sequence, incremented by one on every publication
//	16..24  price, in the quote asset's smallest unit per BTC
//
// All fields are little-endian unsigned. There is no ticker: as in AnyHedge, a
// separate feed means a separate oracle key, which the contract pins as a
// parameter.
const (
	oracleTimestampOffset = 0
	oracleSequenceOffset  = 8
	oraclePriceOffset     = 16

	oracleFieldSize     = 8
	OracleMessageLength = 24
)

// OracleMessage builds the payload the oracle signs.
//
// The sequence is the field the whole settlement rests on. It must increase by
// exactly one per publication with no gaps: the covenant identifies the
// settlement message by requiring the spender to also supply its immediate
// predecessor, which is what makes "the first price after maturity" a question
// with one answer and removes any need for a clock.
func OracleMessage(timestamp, sequence, price uint64) []byte {
	msg := make([]byte, OracleMessageLength)
	binary.LittleEndian.PutUint64(msg[oracleTimestampOffset:], timestamp)
	binary.LittleEndian.PutUint64(msg[oracleSequenceOffset:], sequence)
	binary.LittleEndian.PutUint64(msg[oraclePriceOffset:], price)
	return msg
}

// SignOracleMessage signs a message the way the covenant will verify it:
// BIP340 Schnorr over sha256(message).
//
// The oracle is a stateless signer. It publishes signed prices on a fixed
// cadence, knows nothing about any contract, and never touches a transaction —
// whoever settles puts the message in the witness.
func SignOracleMessage(key *btcec.PrivateKey, msg []byte) ([]byte, error) {
	digest := sha256.Sum256(msg)
	sig, err := schnorr.Sign(key, digest[:])
	if err != nil {
		return nil, err
	}
	return sig.Serialize(), nil
}
