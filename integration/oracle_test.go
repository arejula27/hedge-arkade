//go:build integration

package integration

import (
	"bytes"
	"testing"

	"github.com/arejula27/hedge/contract"
	"github.com/btcsuite/btcd/btcec/v2"
)

// The oracle service in miniature: a key, and a sequence of signed prices.
// Fixed, so a CI failure reproduces.
var oracleKey, _ = btcec.PrivKeyFromBytes(bytes.Repeat([]byte{0x11}, 32))

const (
	startTime    = 1_800_000_000
	maturityTime = 1_800_086_400 // a day later
	baseSequence = 5_000
)

// settlementWitness is the four stack items the covenant expects: the
// settlement message and its immediate predecessor, each with its signature.
func settlementWitness(t *testing.T, price uint64) [][]byte {
	t.Helper()

	settle := contract.OracleMessage(maturityTime, baseSequence+1, price)
	prev := contract.OracleMessage(maturityTime-60, baseSequence, price)

	settleSig, err := contract.SignOracleMessage(oracleKey, settle)
	if err != nil {
		t.Fatalf("signing the settlement message: %v", err)
	}
	prevSig, err := contract.SignOracleMessage(oracleKey, prev)
	if err != nil {
		t.Fatalf("signing the previous message: %v", err)
	}

	return [][]byte{settleSig, settle, prevSig, prev}
}
