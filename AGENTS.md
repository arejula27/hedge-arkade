# AGENTS.md — Arkade Hedge

> **Language rule**: everything in **English** — code, identifiers, comments, prose docs and
> commit messages are the exception: commits stay Spanish, with no `Co-Authored-By` trailer.

> **Terminology**: Arkade's docs deprecate "ASP", "Ark server", "round" and "Ark transaction". Use
> **operator**, **Arkade Service**, **batch swap** and **Arkade transaction**.

## Project overview

A fixed-term, BTC-collateralized hedge contract on Arkade — a port of BCH's AnyHedge. One party
(**hedge**) locks in a USD-denominated value; the counterparty (**long**) posts the extra
collateral and takes the opposite side of the price move. At maturity, or earlier if the long runs
out of collateral, a signed oracle price determines the split. Total collateral is conserved: the
covenant only computes the hedge payout and gives the remainder to the long.

**Scope**: contract logic + a web service that coordinates and co-signs. The service never
custodies funds.

Start with `doc/oracle.md` — settlement without a clock is the part that explains the rest.

## Stack

- **Environment**: `nix develop` (flake at the repo root). Pins Go 1.26.5 — the exact version
  `../emulator/pkg/arkade` requires — and Node 22. Nothing is installed globally on this machine,
  so every `go`/`node` invocation goes through `nix develop --command`
- **Service** (API, web, users, matching, oracle, arkd client): **Go**
- **Contract builder**: Go, in `covenant/`. Scripts are assembled with
  `txscript.NewScriptBuilder` and the opcodes `pkg/arkade` exports. `arkadec`/`.ark` is spec-only,
  off the build path
- **Client verifier**: TypeScript in the browser. Recognises a contract against known templates or
  refuses to fund. Pinned to the Go builder by a golden hex fixture in CI
- **Co-signer**: Arkade `emulator` — executes Arkade Script, required for every covenant path
- **Covenant tests**: Go, against `github.com/arkade-os/emulator/pkg/arkade`
- **Regtest**: nigiri + arkd + arkd-wallet + emulator via Docker Compose

## Sibling repos (all under `../`)

| Path | What it is | Why you care |
|---|---|---|
| AnyHedge | `@generalprotocols/anyhedge-contracts@0.12.1`, `contracts/v0.12/contract.cash` | **The contract we are porting.** 153 lines, in production since 2020. Where this repo and that file disagree, it is right unless Arkade forces the difference |
| `../compiler` | `arkade-os/compiler` @ `3988a9d` | Worked Arkade Script examples (`stability_vault`, `fuji_safe`, `threshold_oracle`). Reference only — the contract's *design* comes from AnyHedge |
| `../emulator` | `arkade-os/emulator` @ `1359823` | The VM. `pkg/arkade` is a standalone Go module; we consume it published, not from here |
| `../bond-protocol` | Previous Arkade project, same author | Regtest Docker Compose and justfile worth reusing |

## Testing the covenant against the real VM

This is the part worth getting right, and it is not obvious.

`../emulator/pkg/arkade` has its **own `go.mod`** (`github.com/arkade-os/emulator/pkg/arkade`), so
it can be imported standalone. `NewEngine`, `Engine.Execute()`, `SetStack`, `GetStack`, `Step` and
`WithDebugCallback` are all exported. Build a synthetic `wire.MsgTx`, implement the
`ArkPrevOutFetcher` interface (3 methods), and the **real covenant VM runs in `go test`** — no
Docker, no arkd, no nigiri.

This is wired up in `covenant/vm.go` and green. `Run(script, witness, spend)` builds the spending
transaction, sets the witness stack and executes. `pkg/arkade` comes from the published module —
no local `replace`, so the repo builds anywhere — and is the same interpreter the emulator service
runs before co-signing (`internal/application/tx.go:67`).

`just check` runs fmt, vet and the tests; every recipe wraps itself in `nix develop`.

**But an in-process VM cannot see transaction shape.** It builds its own transaction, so it has no
opinion about the emulator packet or the anchor a real one carries. `integration/` is the second
tier: a separate Go module, behind the `integration` build tag, that submits a real settlement to a
real emulator over gRPC. `just regtest-up` starts the stack (needs Docker), `just test-integration`
runs it. Both tiers run in CI; only the first runs without Docker. Full write-up in
`doc/testing.md`.

This matters more here than in `../bond-protocol`. That project had to write a symbolic
`stackSim.ts`, whose own header admits *"it is not an interpreter: comparisons and arithmetic
produce opaque symbols"*. Fine for comparisons and asset lookups; useless for a contract whose
core **is** truncating arithmetic. Rounding edges, prices at the clamp boundary and theft attempts
all need real execution.

## Critical rules

- **The long's payout is the remainder.** `shortSats` is computed, `longSats = payoutSats -
  shortSats`. Never derive both sides independently — two computations can disagree by a truncated
  sat and leave the transaction unspendable
- **The payouts sum to exactly `payoutSats`, and the input is pinned to it.** Arkade conserves
  value: arkd rebuilds every offchain tx with `offchain.BuildTxs`, which rejects an input amount
  differing from the output amount (`offchain/tx.go:64`), and compares txids. AnyHedge floors both
  sides independently and lets the total exceed `payoutSats`, covering the difference from the
  miner fee — there is no fee here. The short is capped at `payoutSats - DUST` instead. Do not
  "restore" AnyHedge's independent floors
- **The output count is deliberately not checked**, unlike AnyHedge. A real Arkade transaction has
  four outputs: the two payouts, the extension OP_RETURN carrying the emulator packet, and the P2A
  anchor. `numOutputs == 2` rejects every real settlement. What replaces it is arithmetic — input
  pinned, payouts summing to it, so no other output can carry value without the transaction paying
  out more than it takes in
- **`OP_DIV` truncates**, and the truncated fraction stays with the long. Rounding up would pay the
  short out of collateral it is not owed
- **Oracle message layout is fixed**: 24 bytes, 8-byte little-endian fields —
  `timestamp | sequence | price`. No ticker: a different feed is a different oracle key, as in
  AnyHedge. `CHECKSIGFROMSTACK` verifies over a 32-byte digest and does not hash for the caller, so
  the witness carries the raw message and the script does the `SHA256`
- **The sequence is load-bearing.** It must increase by exactly one per publication with no gaps.
  Settlement requires the message *and its immediate predecessor*, with
  `settlementSeq == prevSeq + 1` and `prevTimestamp < maturityTimestamp` — that is what makes "the
  first price after maturity" have one answer and removes any need for a clock
- **The clamp is load-bearing too.** Every price past a boundary settles identically, so it does
  not matter which out-of-bounds message a spender picks. Removing either liquidation boundary
  breaks the no-clock property, not just the economics
- **The settlement math runs in the covenant, never on our server.** `covenant/` emits that script
  and runs it; it holds no formula of its own. Any design that computes the split service-side is
  wrong
- **All arithmetic runs on the VM**, including `hedgeValueCents * 1e8`. Folding it into a
  build-time Go constant overflows int64 for a large position — BigNum is the only arithmetic here
  without a ceiling
- **Exit leaves drop the covenant.** One CSV 2-of-2 leaf (hedge + long); the exit transaction is
  **pre-signed at funding** and sweeps to a 2-of-3 `{hedge, long, service}`. Pre-signing is what
  makes it unilateral — either party broadcasts it alone once the CSV matures, and neither can
  redirect the destination. Full write-up in `doc/unilateral-exit.md`
- **Two path classes, two rules.** Collaborative paths (no CSV) must carry the operator pubkey and
  use CLTV for timelocks; unilateral paths (CSV) omit the operator and need a delay at or above
  `getInfo().exitDelay`. arkd classifies each closure and applies the matching rule
  (`vtxo_script.go:93`)
- **The covenant is not in the taproot tree.** Every leaf is a plain N-of-N multisig. The Arkade
  Script enters through a key: leaf 1 carries
  `arkade.ComputeArkadeScriptPublicKey(emulatorSigner, arkade.ArkadeScriptHash(script))`, and the
  script itself travels in the emulator packet (an OP_RETURN TLV) of the spending transaction. The
  emulator recomputes the tweak and refuses with `ErrTweakedArkadePubKeyNotFound` if it does not
  match (`pkg/arkade/script.go:91`). Do not reach for `ConditionMultisigClosure` — its
  `EvaluateScriptToBool` runs btcd's engine, which does not know Arkade opcodes
- **Settlement carries no party key.** The covenant fixes recipients and amounts exactly, so anyone
  holding two adjacent oracle messages may settle. AnyHedge's `payout` is permissionless too
- **Maturity is gated in the covenant, not by a tapscript CLTV.** A leaf-level CLTV is
  unconditional and would block early liquidation. The gate reads the oracle message's own
  timestamp
- **There is no clock, and the contract does not need one.** `tx.offchainTime` does not exist:
  `introspection.rs:5` maps only `version`/`locktime`/`numInputs`/`numOutputs`/`weight`/`id`, and
  the VM's only clock is `OP_INSPECTLOCKTIME` — `nLockTime`, chosen by the spender. Any design
  that reaches for a wallclock or a freshness window is going the wrong way; sequence adjacency
  plus the clamp is the answer
- **Values and recipients are both checked, exactly.** `==` on `tx.outputs[i].value` *and* on the
  lock script. `OP_INSPECTOUTPUTSCRIPTPUBKEY` does not push the script: for a witness program it
  pushes the program then the version, otherwise `sha256(script)` then `-1`
- **The 2-of-3 lives in the sweep destination, not in a leaf.** Inside a VTXO every closure is
  N-of-N; outside it, the destination is any Bitcoin output script and a real threshold is fine
- **No m-of-n in a `tapscript` leaf.** arkd's `MultisigClosure` is always N-of-N; its decoder
  requires the pushed integer to equal the key count, then re-encodes and demands byte equality
  (`closure.go`). `DecodeClosure` (`closure.go:31`) is a closed whitelist of 5 shapes — arkd
  classifies leaves, it does not merely verify them. `OP_CHECKMULTISIG` is separately disabled in
  tapscript by BIP342. m-of-n exists only *inside* a covenant, which an exit path does not have
- **Timelock encoding is BIP68; the *type* is the operator's policy.** Seconds-based CSV values
  must be multiples of 512, and CLTV values must be Unix timestamps (>= 500,000,000). Whether
  block-based timelocks are accepted is a parameter arkd takes (`Validate(..., blockTypeAllowed)`):
  production operators say no, the regtest stacks configure blocks so timelocks fire on mining.
  `Contract.Validate` takes the flag; do not hardcode it — doing so made the contract untestable
  against a standard regtest
- **`cltv` and `csv` are mutually exclusive** in a single leaf
- **Never trust a green compile.** Run it on the VM. And expected payouts go in the test table as
  constants — computing them in Go recreates the parallel implementation the covenant exists to be
  the only copy of

## Toolchain

`arkadec` is **not in the build path**. The compiler/VM drift is confirmed at both ends
(re-verified 2026-07-27 against `../compiler` @ `3988a9d` and `../emulator` @ `1359823`):

- The compiler still emits the 64-bit family — `../compiler/src/opcodes/mod.rs:82-85` defines
  `OP_ADD64`/`OP_SUB64`/`OP_MUL64`/`OP_DIV64`, and `src/compiler/comparison.rs:220` emits
  `OP_GREATERTHAN64`
- The VM no longer has them: zero occurrences in `../emulator/pkg/arkade/opcode.go`. Arithmetic
  was unified onto BigNum — `OP_MUL = 0x95` with `opcodeMul` (`opcode.go:194`, `:493`, `:2394`)

A contract with arithmetic compiled by `arkadec` **will not execute on the current VM**, and this
contract is arithmetic almost end to end.

Consequence: scripts are assembled by hand in Go with `txscript.NewScriptBuilder`. `.ark` files
are kept as readable spec.

## SDK gotchas (verified against 0.4.51)

- Opcode names in a `Program`'s `asm` take **no `OP_` prefix**. `"INSPECTNUMOUTPUTS"` compiles;
  `"OP_INSPECTNUMOUTPUTS"` throws `Unknown opcode`
- Two encoders disagree: `arkade.Script.encode` rejects Arkade extension names, `arkade.asmToBytes`
  accepts both. Use `bytesToASM` for debugging, never `toASM(Script.decode(...))`
- Keys must be real curve points. `new Uint8Array(32).fill(3)` fails inside `lift_x`. Use
  `SingleKey.fromRandomBytes().xOnlyPublicKey()` — note it is **async**
- Use `satisfies Program`, not `: Program` — the latter widens the literal type away and you lose
  the typed `functions.<name>(...)` signatures
- Arithmetic opcodes (`MUL`, `DIV`, `MOD`, `CAT`, `SUBSTR`) are **not** in `ARKADE_OP`. They come
  from `@scure/btc-signer`'s `OP` enum and resolve via `ARKADE_OPS = { ...OP, ...ARKADE_OP }`

## Trust model — state it honestly in any writeup

The covenant is enforced by the emulator co-signing, **not** by Bitcoin consensus.

- A malicious emulator can sign a transaction its script would have rejected. There is no on-chain
  proof the script ran. Arkade's mitigation is running the VM in a TEE with attestation
- An unavailable emulator freezes every covenant path — hence the exit leaves
- This is strictly weaker than Bitcoin consensus and strictly stronger than a custodian: the
  emulator is one signature in a multisig and cannot move funds alone

## Where things are written down

- `README.md` — what the project is. Kept short on purpose
- `doc/` — how the contract works. Durable design, no status and no decision log
- `NOTES.local.md` — **gitignored, read it first.** Current status, settled decisions, open
  questions, test counts, loose ends. Anything that goes stale lives here, not in git

Keep that separation when writing: a fact about the mechanism goes in `doc/`, a fact about where we
are goes in `NOTES.local.md`.
