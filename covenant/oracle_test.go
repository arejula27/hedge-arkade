package covenant

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The digest layout is what the covenant rebuilds on the stack, so it is pinned
// against an independently assembled preimage rather than against itself.
func TestOracleMessageLayout(t *testing.T) {
	ticker := NewTicker("BTC/USD")
	const price, ts = uint64(10_000_000), uint64(1_800_000_000)

	preimage := make([]byte, 0, 48)
	preimage = append(preimage, ticker[:]...)
	preimage = append(preimage,
		// price, 8 bytes little-endian: 10_000_000 = 0x989680
		0x80, 0x96, 0x98, 0x00, 0x00, 0x00, 0x00, 0x00,
		// timestamp, 8 bytes little-endian: 1_800_000_000 = 0x6B49D200
		0x00, 0xd2, 0x49, 0x6b, 0x00, 0x00, 0x00, 0x00,
	)
	want := sha256.Sum256(preimage)

	if got := OracleMessage(ticker, price, ts); got != want {
		t.Errorf("OracleMessage() = %s, want %s", hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
	}
}

// Every field has to reach the digest. A field that does not is a field an
// attacker can change after the fact.
func TestOracleMessageCommitsToEveryField(t *testing.T) {
	base := OracleMessage(NewTicker("BTC/USD"), 10_000_000, 1_800_000_000)

	tests := []struct {
		name string
		got  [32]byte
	}{
		{"ticker", OracleMessage(NewTicker("BTC/EUR"), 10_000_000, 1_800_000_000)},
		{"price", OracleMessage(NewTicker("BTC/USD"), 10_000_001, 1_800_000_000)},
		{"timestamp", OracleMessage(NewTicker("BTC/USD"), 10_000_000, 1_800_000_001)},
	}

	for _, tc := range tests {
		if tc.got == base {
			t.Errorf("changing the %s did not change the digest", tc.name)
		}
	}
}

func TestNewTickerIsPlainSha256(t *testing.T) {
	want := sha256.Sum256([]byte("BTC/USD"))
	if got := NewTicker("BTC/USD"); Ticker(want) != got {
		t.Errorf("NewTicker() = %x, want %x", got, want)
	}
}

func TestCheckFreshness(t *testing.T) {
	const now = int64(1_800_000_000)

	tests := []struct {
		name       string
		oracleTime int64
		wantErr    bool
	}{
		{"same second", now, false},
		{"one second old", now - 1, false},
		{"at the window edge", now - MaxOracleAge, false},
		{"one second past the window", now - MaxOracleAge - 1, true},
		{"an hour old", now - 3600, true},
		// A price signed for the future would let the holder wait for the market
		// to move and settle at a price that was never current.
		{"one second in the future", now + 1, true},
		{"a day in the future", now + 86_400, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckFreshness(tc.oracleTime, now)
			if (err != nil) != tc.wantErr {
				t.Errorf("CheckFreshness(%d, %d) error = %v, wantErr %v", tc.oracleTime, now, err, tc.wantErr)
			}
		})
	}
}
