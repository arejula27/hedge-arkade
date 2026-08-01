// Package integration runs the contract against a live Arkade stack: real
// arkd, real arkd-wallet, real emulator, real bitcoind on regtest.
//
// It is a separate Go module on purpose. `contract` has seven direct
// dependencies and is what the TypeScript verifier is pinned to; the client
// SDK, the explorer and the emulator client belong nowhere near it. What talks
// to those services lives in `arkade`, which the web service uses too — so
// these tests exercise the code that runs in production rather than a parallel
// copy of it.
//
// Everything else here is behind the `integration` build tag, so `just test`
// never reaches it and a machine without Docker is unaffected.
package integration

import "github.com/arejula27/hedge/arkade"

// stackConfig points at the regtest stack. arkade-regtest publishes the
// endpoints on localhost; the HEDGE_* variables override them.
var stackConfig = arkade.DefaultConfig()
