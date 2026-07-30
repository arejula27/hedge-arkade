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

Full protocol spec in `README.md`. Read it first.

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

This is wired up in `covenant/vm.go` and green. `Run(script, stack, outputs)` builds the spending
transaction, feeds the witness and executes.

Expected payouts live in the test table as constants. Never compute them in Go — that reintroduces
the parallel implementation the covenant exists to be the only copy of.

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
- **The settlement math runs in the covenant, never on our server.** `covenant/` emits that script
  and runs it; it holds no formula of its own. Any design that computes the split service-side is
  wrong
- **All arithmetic runs on the VM**, including `hedgeValueCents * 1e8`. Folding it into a
  build-time Go constant overflows int64 for a large position — BigNum is the only arithmetic here
  without a ceiling
- **Exit leaves drop the covenant.** One CSV 2-of-2 leaf (hedge + long); the exit transaction is
  **pre-signed at funding** and sweeps to a 2-of-3 `{hedge, long, service}`. Pre-signing is what
  makes it unilateral — either party broadcasts it alone once the CSV matures, and neither can
  redirect the destination. Full write-up in README §"Leaf 3"
- **Two path classes, two rules.** Collaborative paths (no CSV) must carry the operator pubkey and
  use CLTV for timelocks; unilateral paths (CSV) omit the operator and need a delay at or above
  `getInfo().exitDelay`. arkd classifies each closure and applies the matching rule
  (`vtxo_script.go:93`)
- **Maturity is gated in the covenant, not by a tapscript CLTV.** A leaf-level CLTV is
  unconditional and would block early liquidation
- **`tx.offchainTime` does not exist.** It is in `stability_vault.ark` and the compiler's guidance
  but neither implements it: `introspection.rs:5` maps only `version`/`locktime`/`numInputs`/
  `numOutputs`/`weight`/`id`, and the VM's only clock is `OP_INSPECTLOCKTIME` (`nLockTime`, chosen
  by the spender). Do not write a covenant that depends on a wallclock until this is answered —
  README §Blocked
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

## Current status

**The settlement covenant executes on the real VM and its tests are green** (18 cases). `covenant/`
builds the script for Leaf 1's payout logic and runs it through `arkade.NewEngine`. What is pinned:
the split at several prices, the liquidation clamp on the boundary, truncation direction, the int64
overflow, six underpayment attempts, and script determinism.

What is **not** built yet, in the order it blocks things:

1. **The clock.** Freshness and maturity both need a trustworthy time source and there isn't one —
   see README §Blocked. This gates the rest of Leaf 1
2. Oracle signature verification (`CHECKSIGFROMSTACK`, message reassembly with `CAT`/`NUM2BIN`)
3. Output script constraints — the covenant currently checks output *values*, not that they pay
   the right keys
4. The tapscript segments, the taproot tree, and the pre-signed exit package
5. The service (Go): API, users, matching, oracle signing, storage

Note for integration work: in `../bond-protocol` the regtest stack could never be started because
Docker Desktop's WSL integration is disabled on this machine. Resolve that before planning
integration tests.
