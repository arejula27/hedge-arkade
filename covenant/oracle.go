package covenant

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// MaxOracleAge is how stale a signed price may be, in seconds, before the
// covenant refuses it.
const MaxOracleAge = 600

// Ticker identifies a price feed. Adding a feed does not require touching the
// contract, only funding a new one with a different ticker.
type Ticker [32]byte

// NewTicker derives a ticker from a feed name, e.g. NewTicker("BTC/USD").
func NewTicker(name string) Ticker { return sha256.Sum256([]byte(name)) }

// OracleMessage builds the digest the oracle signs:
//
//	sha256(ticker || price || timestamp)
//
// with price and timestamp as 8-byte little-endian unsigned integers. This is
// the Fuji/stability format, adopted unchanged — we operate the oracle
// ourselves, and this encoding is already proven through the compiler.
//
// The covenant reconstructs this digest on the stack with CAT and NUM2BIN and
// verifies it with CHECKSIGFROMSTACK, so the byte layout here is load-bearing:
// change it and every existing contract stops settling.
func OracleMessage(ticker Ticker, priceCents, unixTime uint64) [32]byte {
	var buf [48]byte
	copy(buf[:32], ticker[:])
	binary.LittleEndian.PutUint64(buf[32:40], priceCents)
	binary.LittleEndian.PutUint64(buf[40:48], unixTime)
	return sha256.Sum256(buf[:])
}

// CheckFreshness applies the covenant's two-sided window to a signed price.
//
// The upper bound is the obvious one: a price from an hour ago is not a price.
// The lower bound matters just as much and is easy to forget — a future-dated
// price would let whoever holds a signed message wait for the market to move and
// then settle at a price that was never current.
func CheckFreshness(oracleTime, txTime int64) error {
	age := txTime - oracleTime
	if age < 0 {
		return fmt.Errorf("future-dated oracle price: signed at %d, spending at %d", oracleTime, txTime)
	}
	if age > MaxOracleAge {
		return fmt.Errorf("stale oracle price: %ds old, limit is %ds", age, MaxOracleAge)
	}
	return nil
}
