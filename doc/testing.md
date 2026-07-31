# Testing the covenant

Two tiers.

| | `covenant/` | `integration/` |
|---|---|---|
| Runs | `just check` | `just test-integration` |
| Needs | nothing | Docker, a live stack |
| VM | in process, synthetic transaction | the real emulator over gRPC |
| Catches | arithmetic, the clamp, timing, recipients, exact values | transaction shape, the emulator packet, protocol drift |

The split matters because the first tier builds its own transaction, so it cannot see anything
about the shape a real one has. That is not hypothetical: a `numOutputs == 2` check passed the
entire unit suite and would have rejected every settlement in production, because an Arkade
transaction also carries the emulator packet and a P2A anchor.

`integration/` is a separate Go module. `covenant/` has three direct dependencies and is what the
TypeScript verifier is pinned to; the client SDK and the emulator client belong nowhere near it.
Everything there is behind the `integration` build tag, so a machine without Docker is unaffected.

## Tier 1 — the in-process VM

`pkg/arkade` has its own `go.mod`, so it imports standalone. Build a synthetic `wire.MsgTx`,
implement `ArkPrevOutFetcher` (3 methods), and the real covenant VM runs under `go test` — no
Docker, no arkd, no nigiri.

It is the same interpreter the emulator service runs before co-signing
(`internal/application/tx.go:67`), consumed as a published module rather than a local `replace`, so
the repo builds anywhere. `Run` applies `WithExactComputeLimits(DefaultComputeLimits())`, the table
the service uses when no `COMPUTE_LIMITS` overrides are set, so a change to those defaults surfaces
as a test failure.

## Rules

**Expected payouts are constants in the test table, never computed.** Computing them in Go
recreates the parallel implementation the covenant exists to be the only copy of. If the table and
the VM disagree, the VM is what settles real money.

**Every accepted settlement generates its own rejections.** Each side ±1 and a sat moved either
way, all of which must fail — values are checked exactly, so overpaying is a different settlement
rather than a generous one.

## What is pinned

- The clamp at both boundaries, one cent inside each, and far past them
- The dust floor and the cap that keeps the two payouts summing to exactly `payoutSats`
- The input amount, which must equal `payoutSats`: over, under, and far over
- The four-output shape a real Arkade settlement has
- Truncation direction, the leverage term, a nominal past what BCH's 4-byte ints could hold
- Recipients: payouts redirected, swapped, sent to a non-taproot output
- Shape: a third output, a missing output, an extra input
- Timing: settlement at maturity, liquidation at either boundary, liquidation at `startTimestamp`
- Sequence: a mid-range price from mid-contract, a message that is not the first after maturity, a
  gap, predecessor and settlement swapped, the same message twice, a zero sequence
- Authentication: each field edited after signing, a signature from the wrong key, the two
  signatures swapped, an empty signature, a garbage signature, a different oracle key
- A golden hex fixture of the built script, which the TypeScript verifier will pin to

For the taproot tree, without a running arkd:

- arkd's own `TapscriptsVtxoScript.Validate` accepts the tree, and rejects a forfeit leaf built
  around the wrong signer or an exit delay below the operator's minimum
- Every leaf decodes back through `DecodeClosure` into one of arkd's five known shapes and
  re-encodes byte for byte
- Leaves land in the right class: two forfeit closures, one exit closure
- Every leaf's control block verifies against the funded output key, over the NUMS internal key
- Every contract parameter changes the address, so the address proves the tree
- The tweaked key in leaf 1 is the one the emulator's `ReadArkadeScript` will look for
- A golden hex fixture of the scriptPubKey

## Tier 2 — the live stack

`scripts/regtest.sh` clones [arkade-regtest](https://github.com/ArkLabsHQ/arkade-regtest) into
`.regtest/` and starts its `emulator` profile: bitcoind, the indexers, arkd, arkd-wallet and the
emulator. Boltz, LND, the solver and the web wallet are not needed to settle a covenant and are
three more ways for CI to fail on something else. `AUTOMINE_INTERVAL=0` keeps block height still.

Nothing about the contract is a constant the tests chose. The arkd signer key, the emulator signer
key, the unilateral exit delay, the dust threshold and the checkpoint tapscript all come from the
two services' `GetInfo`, so a version bump that changes any of them fails here rather than in
production.

What it pins:

- The live operator accepts the VTXO script, and rejects an exit delay below its own minimum
- The running arkd's decoder parses every leaf
- Both payouts clear the operator's dust threshold, which is a runtime value
- Every control block verifies against the address that would be funded
- **A settlement on a contract VTXO that really exists**, end to end: board from the stack's own
  faucet, settle into a VTXO, spend it into the contract address with exactly `payoutSats`, then
  settle the contract
- It refuses a sat moved to the short, and a redirected payout

**The emulator is the entry point, not arkd.** A covenant spend goes to the emulator, which parses
the emulator packet, matches the tweaked key against the leaf, executes the script, signs, and
forwards to arkd itself when it holds the last signature (`internal/application/tx.go:146`). So one
`SubmitTx` covers the covenant, arkd's value conservation, its output validation and its signature
checks. A transaction with no covenant on its input — the funding one — goes straight to arkd.

That is also why a synthesised funding transaction is not enough: the emulator would execute the
script against it happily and then arkd would reject a VTXO that never existed.

## Files

| File | What |
|---|---|
| `covenant/settlement.go` | `Terms` and `SettlementScript()` |
| `covenant/oracle.go` | Message layout and the signing the oracle service will do |
| `covenant/vtxo.go` | `Contract`: the three leaves, the taproot tree, control blocks, arkd validation |
| `covenant/vm.go` | `ArkPrevOutFetcher`, the synthetic spending transaction, and `Run` |
| `integration/stack.go` | Endpoints and the wait-for-ready loop |
| `integration/main_test.go` | Reads both services' `GetInfo` into the fixture |
| `integration/wallet_test.go` | A party with a real wallet: board, settle, sign, submit |
| `integration/settlement_test.go` | Funds the contract and settles it through the stack |
