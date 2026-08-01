package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Oracle is what the oracle binary reads from its environment.
//
// It shares the database with the service and nothing else: the oracle knows
// about no contract, and the service reaches it over HTTP like any other
// client.
type Oracle struct {
	// Port the oracle listens on.
	Port int

	// Database is a libpq connection string.
	Database string

	// Interval between publications. The covenant settles on a message and its
	// immediate predecessor, so this is also how far apart those two are.
	Interval time.Duration

	// Seed is the 32-byte signing key, hex encoded.
	//
	// It must be the same across restarts. The public half is baked into every
	// contract's taproot address, so a new key orphans every live contract.
	Seed string

	// AllowManual lets a client set the price over HTTP. That is a demo
	// control, not something a real feed would expose.
	AllowManual bool

	// StartPrice is what the oracle publishes until told otherwise, in the
	// quote asset's smallest unit per BTC — cents, so 10_000_000 is $100,000.
	StartPrice int64
}

// LoadOracle reads the environment. It fails rather than defaulting on the two
// values that have no safe default.
func LoadOracle() (Oracle, error) {
	port, err := intVar("ORACLE_PORT", 8081)
	if err != nil {
		return Oracle{}, err
	}

	db, err := databaseURL()
	if err != nil {
		return Oracle{}, err
	}

	interval, err := intVar("ORACLE_INTERVAL_SECONDS", 5)
	if err != nil {
		return Oracle{}, err
	}
	if interval < 1 {
		return Oracle{}, fmt.Errorf("ORACLE_INTERVAL_SECONDS must be at least 1, got %d", interval)
	}

	seed, err := seedVar("ORACLE_SEED")
	if err != nil {
		return Oracle{}, err
	}

	price, err := intVar("ORACLE_START_PRICE", 10_000_000)
	if err != nil {
		return Oracle{}, err
	}
	if price < 1 {
		return Oracle{}, fmt.Errorf("ORACLE_START_PRICE must be positive, got %d", price)
	}

	manual, err := boolVar("ORACLE_ALLOW_MANUAL", false)
	if err != nil {
		return Oracle{}, err
	}

	return Oracle{
		Port:        port,
		Database:    db,
		Interval:    time.Duration(interval) * time.Second,
		Seed:        seed,
		AllowManual: manual,
		StartPrice:  int64(price),
	}, nil
}

// seedVar checks the key is well formed here rather than at first use. A
// mistyped seed that only fails when the first price is signed is a service
// that starts and then cannot do its one job.
func seedVar(name string) (string, error) {
	raw, err := required(name)
	if err != nil {
		return "", err
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("%s must be hex: %w", name, err)
	}
	if len(decoded) != 32 {
		return "", fmt.Errorf("%s must be 32 bytes, got %d", name, len(decoded))
	}
	return raw, nil
}

func boolVar(name string, fallback bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", name, raw)
	}
	return v, nil
}
