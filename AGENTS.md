# AGENTS.md — Arkade Hedge

> **Language rule**: code, identifiers and code comments in **English**, always.
> Prose docs (`README.md`, specs) are currently written in Spanish — keep them Spanish.

## Project overview

A fixed-term, BTC-collateralized hedge contract on Arkade — a port of BCH's AnyHedge. One party
(**hedge**) locks in a USD-denominated value; the counterparty (**long**) posts the extra
collateral and takes the opposite side of the price move. At maturity, or earlier if the long runs
out of collateral, a signed oracle price determines the split. Total collateral is conserved: the
covenant only computes the hedge payout and gives the remainder to the long.

**Scope**: contract logic + a web service that coordinates and co-signs. The service never
custodies funds.

Full protocol spec in `README.md`. Read it first.

## Stack

- **Environment**: `nix develop` (flake at the repo root). Pins Go 1.26.5 — the exact version
  `../emulator/pkg/arkade` requires — and Node 22. Nothing is installed globally on this machine,
  so every `go`/`node` invocation goes through `nix develop --command`
- **Language**: TypeScript (Node.js 22)
- **Contract**: SDK `Program` object. `arkadec`/`.ark` is spec-only, off the build path
- **Client SDK**: `@arkade-os/sdk` (0.4.51+)
- **Co-signer**: Arkade `emulator` — executes Arkade Script, required for every covenant path
- **Unit tests (math)**: `vitest`
- **Unit tests (covenant)**: Go, against `github.com/arkade-os/emulator/pkg/arkade`
- **Regtest**: nigiri + arkd + arkd-wallet + emulator via Docker Compose

## Sibling repos (all under `../`)

| Path | What it is | Why you care |
|---|---|---|
| `../compiler` | `arkade-os/compiler` @ `3988a9d` | `examples/stability/stability_vault.ark` is our starting point. Also `fuji_safe`, `options`, `threshold_oracle` |
| `../emulator` | `arkade-os/emulator` @ `1359823` | The VM. `pkg/arkade` is a standalone Go module — our test harness target |
| `../bond-protocol` | Previous Arkade project, same author | Working `Program` objects, regtest Docker Compose, justfile. Reuse its setup |

## Testing the covenant against the real VM

This is the part worth getting right, and it is not obvious.

`../emulator/pkg/arkade` has its **own `go.mod`** (`github.com/arkade-os/emulator/pkg/arkade`), so
it can be imported standalone. `NewEngine`, `Engine.Execute()`, `SetStack`, `GetStack`, `Step` and
`WithDebugCallback` are all exported. Build a synthetic `wire.MsgTx`, implement the
`ArkPrevOutFetcher` interface (3 methods), and the **real covenant VM runs in `go test`** — no
Docker, no arkd, no nigiri.

Workflow: the `Program` is defined in TypeScript (source of truth) → the compiled `arkadeScript`
is dumped to hex as a fixture → the Go test loads the fixture and executes it against the VM with
different prices, stacks and transactions.

This matters more here than in `../bond-protocol`. That project had to write a symbolic
`stackSim.ts`, whose own header admits *"it is not an interpreter: comparisons and arithmetic
produce opaque symbols"*. Fine for comparisons and asset lookups; useless for a contract whose
core **is** truncating arithmetic. Rounding edges, prices at the clamp boundary and theft attempts
all need real execution.

## Critical rules

- **Total collateral is conserved.** The covenant computes the hedge payout and assigns the
  remainder to the long. Never recompute both sides independently
- **`OP_DIV` truncates.** Fixed scale of 1e8, and truncation always goes against whoever builds
  the transaction
- **Oracle message format is fixed**: `sha256(ticker || price || timestamp)`, price and timestamp
  as 8-byte LE unsigned, price in USD cents per BTC. Freshness `0 <= age <= 600s`. Reject
  future-dated prices explicitly
- **Exit leaves drop the covenant.** One CSV 2-of-2 leaf (hedge + long); the exit transaction is
  **pre-signed at funding** and sweeps to a 2-of-3 `{hedge, long, service}`. Pre-signing is what
  makes it unilateral — either party broadcasts it alone once the CSV matures, and neither can
  redirect the destination. Full write-up in README §"Leaf 3"
- **The 2-of-3 lives in the sweep destination, not in a leaf.** Inside a VTXO every closure is
  N-of-N; outside it, the destination is any Bitcoin output script and a real threshold is fine
- **No m-of-n in a `tapscript` leaf.** arkd's `MultisigClosure` is always N-of-N; its decoder
  requires the pushed integer to equal the key count, then re-encodes and demands byte equality
  (`closure.go`). `DecodeClosure` (`closure.go:31`) is a closed whitelist of 5 shapes — arkd
  classifies leaves, it does not merely verify them. `OP_CHECKMULTISIG` is separately disabled in
  tapscript by BIP342. m-of-n exists only *inside* a covenant, which an exit path does not have
- **Ark requires seconds-based timelocks.** CLTV values must be Unix timestamps (>= 500,000,000);
  CSV values must be multiples of 512 seconds. Block-based timelocks are rejected
- **`cltv` and `csv` are mutually exclusive** in a single leaf
- **Never trust a green compile.** `isScriptValid()` validates structure, not stack semantics — it
  returns `true` for a condition script that eats a signature. Read the emitted asm; run it

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

Consequence: contracts are hand-written as SDK `Program` objects. `.ark` files are kept as
readable spec.

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

## Current status

`covenant/` holds the reference settlement math in Go (`Settle`, `LiquidationPrice`, oracle digest
and freshness) with unit tests — dependency free, and the thing the VM will be checked against.

Everything else is spec. Viability is confirmed at the opcode level (every operation the contract
needs exists and resolves from the TypeScript SDK), but **no covenant has been executed** — not
against the VM, not against a live stack. The `Program` object does not exist yet.

Note for integration work: in `../bond-protocol` the regtest stack could never be started because
Docker Desktop's WSL integration is disabled on this machine. Resolve that before planning
integration tests.
