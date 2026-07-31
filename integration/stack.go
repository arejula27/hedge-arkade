// Package integration runs the covenant against a live Arkade stack: real
// arkd, real arkd-wallet, real emulator, real bitcoind on regtest.
//
// It is a separate Go module on purpose. `covenant` has three direct
// dependencies and is what the TypeScript verifier is pinned to; the client
// SDK, the explorer and the emulator client belong nowhere near it.
//
// Everything here is behind the `integration` build tag, so `just test` never
// reaches it and a machine without Docker is unaffected.
package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"
)

// Endpoints the regtest stack exposes. arkade-regtest publishes these on
// localhost; override any of them for a stack that does not.
var (
	ArkdURL     = env("HEDGE_ARKD_URL", "localhost:7070")
	EmulatorURL = env("HEDGE_EMULATOR_URL", "localhost:7073")
	ExplorerURL = env("HEDGE_EXPLORER_URL", "http://localhost:3000")
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// WaitForStack blocks until every service accepts a TCP connection, or the
// context expires. Starting the stack and running the tests are separate steps,
// and arkd is not ready the moment its port opens on the way up.
func WaitForStack(ctx context.Context) error {
	for _, target := range []struct{ name, addr string }{
		{"arkd", ArkdURL},
		{"emulator", EmulatorURL},
	} {
		if err := waitForPort(ctx, target.name, target.addr); err != nil {
			return err
		}
	}
	return nil
}

func waitForPort(ctx context.Context, name, addr string) error {
	var last error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s at %s never came up: %w (last dial: %v)",
				name, addr, ctx.Err(), last)
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		last = err
		time.Sleep(time.Second)
	}
}
