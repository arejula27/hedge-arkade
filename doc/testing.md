# Testing the covenant

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

## Files

| File | What |
|---|---|
| `settlement.go` | `Terms` and `SettlementScript()` |
| `oracle.go` | Message layout and the signing the oracle service will do |
| `vtxo.go` | `Contract`: the three leaves, the taproot tree, control blocks, arkd validation |
| `vm.go` | `ArkPrevOutFetcher`, the synthetic spending transaction, and `Run` |
