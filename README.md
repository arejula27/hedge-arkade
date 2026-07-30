# Arkade Hedge Contract — Spec (after BCH's AnyHedge)

## Goal

Port AnyHedge (Bitcoin Cash, in production since 2020, ~$4.9M TVL through BCH Bull) to Arkade,
using the emulator's introspection + arithmetic VM for the business logic and solving the
unilateral exit — which on Bitcoin L1 has none of those opcodes — with a separate emergency path.

Original reference: `anyhedge.cash`
(https://gitlab.com/GeneralProtocols/anyhedge/library/-/blob/development/lib/anyhedge.cash)

> **Terminology**: Arkade's docs deprecate "ASP", "Ark server" and "round". This spec uses
> **operator** for the entity running the Arkade Service, **batch swap** for the process that
> settles VTXOs onchain, and **Arkade transaction** for an offchain transfer.

---

## Status and settled decisions

| Decision | Value | Date |
|---|---|---|
| Product | **Fixed term** (AnyHedge), not perpetual | 2026-07-27 |
| Contract base | `stability_vault.ark` from `arkade-os/compiler` | 2026-07-27 |
| Oracle | **Run by us**, Fuji/stability format | 2026-07-27 |
| Emergency exit | One CSV 2-of-2 leaf, pre-signed at funding, sweeping to a 2-of-3 | 2026-07-30 |
| Early mutual close | **At the oracle price**, not a free split. No leaf settles at anyone's discretion | 2026-07-30 |
| Structure | 3 leaves: 2 covenant (offchain) + 1 exit (L1). Only the exit touches Bitcoin | 2026-07-30 |
| Maturity gate | **Blocked** — no trustworthy clock exists in the VM. See §Blocked | 2026-07-30 |
| Language | **Go** for the service and the contract builder; TypeScript only for the client-side verifier | 2026-07-30 |
| Contract distribution | The service builds the tree and sends it whole; the client recognises it or refuses | 2026-07-30 |
| Funding rate | **Dropped** — it is what makes stability perpetual | 2026-07-27 |

**The settlement math lives in the covenant**, executed by the emulator VM. No part of it is
reimplemented in the service. `covenant/` builds that script and runs it against the real VM; it
contains no formula of its own. See §`covenant/`.

**Viability: confirmed opcode by opcode.** Everything the spec needs exists in the emulator VM and
resolves from the TypeScript SDK. See §Verified viability.

---

## Starting point: `stability_vault.ark`

`../compiler/examples/stability/stability_vault.ark` (355 lines) is this same contract under
different names. We adapt it rather than rewrite it.

| This spec | stability_vault |
|---|---|
| Hedge (short 1x, fixed USD value) | `seeker` — "holds a fixed USD value" |
| Long (extra collateral, leveraged long) | `provider` — "leveraged BTC long" |
| `hedgePayoutInBtc = hedgeValue / endPrice` | `seekerRaw = newTargetUSD * 100000000 / oraclePrice` |
| `output_hedge + output_long == totalCollateral` | `providerPayout = totalCollateral - seekerRaw` |
| `lowLiquidationPrice` | `if (seekerRaw >= totalCollateral)` → hedge takes everything |
| `highLiquidationPrice` | `if (seekerRaw <= 0)` → counterparty takes everything |

AnyHedge's liquidation thresholds and stability's clamp are the same thing: the condition
`seekerRaw >= totalCollateral` **is** the low liquidation price, computed instead of precomputed.
That removes the need to derive thresholds from leverage separately.

**What to add on top of stability**: `maturityTime` (stability is perpetual), the early mutual
close — which reuses the same covenant with a different trigger — and a two-party emergency exit,
because stability exits with a single signature.

**What to remove**: `fundingRatePerSec` and all the accrual around it (`lastUpdate`, `elapsed`,
`delta`). A fixed-term contract pays no funding; the price of the hedge is paid outside the
contract when the position is opened.

---

## Actors

- **Hedge**: wants to preserve the value of their BTC in terms of an external asset (USD, gold…).
  Equivalent to a 1x short.
- **Long**: the speculating counterparty, betting the opposite direction. Posts the extra
  collateral that backs the hedge payout.
- **Oracle**: signs price messages periodically. Run by us. Knows nothing about any particular
  contract.
- **Service**: coordinates, builds the settlement transaction, and is the third key of the 2-of-3
  the emergency exit sweeps into — not of any leaf. Never custodies funds and never decides the
  split: it only executes the formula.
- **Emulator**: co-signs settlement if the Arkade Script passes. Not Bitcoin consensus.
- **Operator**: runs the Arkade Service, co-signs collaborative paths, and settles batches onchain.

---

## Contract parameters (fixed at creation)

| Parameter | Type | Description |
|---|---|---|
| `hedgePk` | pubkey | Hedge side key |
| `longPk` | pubkey | Long side key |
| `servicePk` | pubkey | Service key — used only in the 2-of-3 exit destination |
| `oraclePk` | pubkey | Price oracle public key |
| `ticker` | bytes32 | Feed identifier, e.g. `sha256("BTC/USD")` |
| `hedgeValueCents` | int | USD value the hedge locks in, in cents. Constant |
| `totalCollateral` | int | Sum of both contributions, in sats. Constant |
| `maturityTime` | int | Unix seconds. Normal expiry |
| `exit` | int | CSV delay on the exit leaf (seconds, multiple of 512, ≥ `getInfo().exitDelay`) |

`hedgeLeverage` is 1x by definition. The long's leverage is implicit in the ratio
`totalCollateral / hedgeValueCents` — it is not a contract parameter, it is a consequence of how
much each side puts in at funding.

---

## Formulas

With no funding rate, `hedgeValueCents` is constant and the settlement math collapses to:

```
hedgePayoutSats = clamp(hedgeValueCents * 1e8 / oraclePrice, 0, totalCollateral)
longPayoutSats  = totalCollateral - hedgePayoutSats
```

Units: `hedgeValueCents` [cents] × `1e8` [sats/BTC] ÷ `oraclePrice` [cents/BTC] = [sats].
`OP_DIV` truncates, and truncation always costs the hedge side the fraction and hands it to the
long.

**Conservation invariant** — the reason the introspection stays simple. The total locked never
changes, it only gets redistributed:

```
output_hedge.value + output_long.value == totalCollateral
```

So the covenant only has to check:
1. `output_hedge.value >= hedgePayoutSats` (computed from the oracle-signed price)
2. `output_long.value >= totalCollateral - hedgePayoutSats` (the remainder, never recomputed)

With a dust guard: a payout at or below 330 sats gets no output at all (same pattern as
`stability_vault.ark:294`, which tests `> 330`).

---

## Oracle message format

We adopt the Fuji/stability format unchanged (`stability_vault.ark:22-28`), because we run the
oracle ourselves and this encoding is already proven through the compiler:

```
msg = sha256(ticker || price || timestamp)
sig = sign(oraclePk, msg)
```

- `price` and `timestamp`: **8-byte little-endian unsigned** integers
- `price` in USD cents per BTC
- `ticker` lets us add feeds without touching the contract

Freshness checks inside the covenant:

```
oracleAge = <now> - oracleTime
require(oracleAge >= 0,   "future-dated oracle")   // reject future-dated prices
require(oracleAge <= 600, "stale oracle")          // 10-minute window
```

Verified with `checkSigFromStack(oracleSig, oraclePk, oracleMsg)` — `OP_CHECKSIGFROMSTACK`
(`0xcc`), 64-byte compact signature, 32-byte x-only Schnorr pubkey.

### Blocked: there is no trustworthy clock

`tx.offchainTime` **does not exist**. It appears in `stability_vault.ark` and in the compiler's own
guidance, described as the TEE introspector's wallclock, but neither side implements it:

- The compiler @ `3988a9d` maps only `version`, `locktime`, `numInputs`, `numOutputs`, `weight` and
  `id` (`src/compiler/introspection.rs:5`). Anything else falls through to a placeholder branch
  that emits the literal string `<tx.offchainTime>`, which is not an opcode
- The VM's only clock is `OP_INSPECTLOCKTIME`, which reads the transaction's `nLockTime`

That leaves `nLockTime`, **chosen by whoever spends**. Two consequences:

**The freshness window does not hold.** `oracleAge = tx.locktime - oracleTime` is forgeable: take
an old favourable price, set `nLockTime = oracleTime + 300`, and it passes. Consensus places no
lower bound on `nLockTime`, so a past value is always legal.

**The maturity gate is one-sided.** On L1 a transaction cannot confirm before its `nLockTime`, so
settling early is blocked there. Offchain that binding is whatever the operator enforces, which is
not in the covenant.

Both freshness and maturity depend on this, so it blocks the contract. Open question for the Arkade
team: what is today's trustworthy time source for an oracle freshness window.

---

## Taproot structure

Internal key = **NUMS** (Nothing Up My Sleeve) — no key-path spend, forcing one of the branches.

```
                Taproot output (NUMS internal key)
                            |
        ┌───────────────────┼───────────────────┐
        |                   |                   |
      Leaf 1              Leaf 2              Leaf 3
   Settlement         Early mutual         Emergency
   at maturity        close                exit

   covenant+oracle    covenant+oracle      CSV 2-of-2
   trigger: matured   trigger: both         hedge+long
   or liquidated      parties sign         (pre-signed)

   ── collaborative paths (no CSV) ──   ── unilateral path ──
     must carry the operator key,        no operator key,
     spent offchain, instantly           spent on L1 after the CSV
```

### Leaf 1 — Settlement at maturity or liquidation
- Full covenant, co-signed by the emulator
- `checkSigFromStack` validates the oracle-signed price message
- `MUL`/`DIV` compute `hedgePayoutSats`
- `INSPECTOUTPUTVALUE` enforces conservation
- Trigger: `<now> >= maturityTime` **or** `hedgePayoutSats >= totalCollateral` (early
  liquidation — the long ran out of collateral). The `<now>` half is blocked; see §Blocked

**What settling actually does.** It spends the contract VTXO and creates two new VTXOs, one owned
by the hedge and one owned by the long, sized by the formula. The sats do move to each side; they
move offchain, as an Arkade transaction, so it is instant and costs no block space.

Each party then holds an ordinary VTXO. Taking it to Bitcoin L1 is a separate, later decision —
collaboratively by offboarding, or unilaterally through its own exit path. The contract does not
force that step and does not need it.

**Why the maturity gate is not a CLTV.** A tapscript CLTV applies to the whole leaf
unconditionally, so a leaf gated on `CLTV(maturityTime)` could not also fire early on liquidation.
The `matured || liquidated` disjunction lives in the covenant instead, and the tapscript segment
carries no timelock. Which clock the covenant reads is unresolved.

Both triggers share one leaf because they differ only in a condition, not in the key set.

### Leaf 2 — Early mutual close, at the oracle price
- Keys: hedge + long + emulator (tweaked) + operator
- **Same covenant and same formula as Leaf 1.** The only difference is the trigger: instead of
  `matured || liquidated`, both parties sign
- Offchain, as an Arkade transaction

The split is **not negotiated**: it comes from `hedgePayoutSats` on the latest signed price, same
as at settlement. No leaf in this contract settles at anyone's discretion — either the oracle
arbitrates or the output cannot be spent. A mutual close with a free split would be the contract's
only covenant-free path, and it buys nothing Leaf 3 does not already provide.

Accepted cost: if the oracle goes down there is **no fast cooperative way out**, because both
covenant leaves need it. A dead oracle is then handled exactly like a dead emulator — you leave
through Leaf 3. One failure mode with one escape hatch, instead of two paths to maintain.

### Why leaves 1 and 2 carry the operator key and leaf 3 does not

Arkade has two protocol rules for spending paths. arkd classifies each closure
(`vtxo_script.go:93`) and applies the matching rule:

- **Collaborative path** — no CSV (leaves 1 and 2). Must include the operator pubkey, or `Validate`
  returns `invalid forfeit closure, signer pubkey not found`. Spent offchain, instantly
- **Unilateral path** — CSV (leaf 3). Does not include the operator. Requires a CSV delay at or
  above `getInfo().exitDelay`, and is spent on L1 after unrolling the VTXO and waiting out the
  delay

Leaves 1 and 2 are fast and the operator can block them. Leaf 3 is slow and the operator cannot.

Timelock types are not interchangeable: collaborative paths use CLTV (absolute, Unix seconds), CSV
is reserved for unilateral exit paths. Both are seconds-based; block-height timelocks are rejected.

### Leaf 3 — Emergency exit

Arkade requires a unilateral exit that does not depend on the emulator. Since the covenant is gone
on that path it **cannot be single-signature**: whoever executed it would walk away with the whole
collateral, including the other side's.

Two layers have to be kept apart:

```
Leaf (spending condition):  CSV + 2-of-2 (hedgePk, longPk)       <- inside Arkade, N-of-N mandatory
Destination (output script): 2-of-3 {hedgePk, longPk, servicePk} <- plain Bitcoin, unconstrained
```

Inside the VTXO every closure arkd can decode is N-of-N — there are no thresholds. But the sweep
**destination** is *"any Bitcoin Output Script"*: once the CSV matures and the transaction is
onchain, arkd has no say and a real 2-of-3 is trivial.

**The exit transaction is pre-signed at funding**, with both parties cooperating:

```
input:   the VTXO, spent via Leaf 3 (nSequence = exit)
output:  the 2-of-3 {hedge, long, service}
sigs:    hedge + long, both collected at funding time
```

Pre-signing is what makes it unilateral: from then on **either party broadcasts it alone** when the
CSV matures, with nothing to negotiate. It is also what prevents theft — only **one** signed
transaction exists and its destination is fixed. Redirecting it would need the counterparty's
signature.

| Risk | How it is covered |
|---|---|
| One party steals the other's collateral | The only signed tx goes to the 2-of-3; nobody can redirect it |
| One party vanishes and blocks the exit | The tx is already signed; the other broadcasts it alone |
| One party vanishes after the exit | In the 2-of-3, the other party + the service move the funds |

**Consequence**: once the funds land in the 2-of-3 the covenant no longer settles. The split is
resolved by the vault's signers — by agreement, or with the service arbitrating on the oracle
price. This is inherent to any exit: an exit always drops the covenant.

Known and accepted risk: collusion between the service and one party inside the 2-of-3. Mitigation:
the service signs deterministically from the latest oracle-signed price, never at manual
discretion, and every signature is accompanied by the oracle's as publicly auditable evidence.

> **Note**: every example contract in the compiler (`fuji_safe`, `cash_secured_put`,
> `stability_vault`, `bond_mint`…) uses a single-signature exit, which works for single-owner
> contracts. Here two parties have money inside, so a single-sig exit is a theft surface — the same
> criticism Arkade's documentation makes of `repayment_pool.ark`.

---

## Verified viability

Checked against `arkade-os/emulator` (`pkg/arkade`), `@arkade-os/sdk` 0.4.51 and
`arkade-os/compiler` @ `3988a9d`.

| Need | Opcode | Source |
|---|---|---|
| `hedgeValue / endPrice` | `MUL` (0x95), `DIV` (0x96), `MOD` (0x97) | Re-enabled legacy, via `@scure/btc-signer`'s `OP` enum |
| Oracle-signed price | `CHECKSIGFROMSTACK` (0xcc) | `ARKADE_OP` |
| Rebuilding the message | `CAT`, `SUBSTR`, `NUM2BIN`, `BIN2NUM` | Mixed |
| Value conservation | `INSPECTOUTPUTVALUE` (0xcf) | `ARKADE_OP` |
| Thresholds | `GREATERTHANOREQUAL`, `LESSTHAN` | Bitcoin base |
| Maturity | **unresolved** — see §Blocked | — |

All resolve through `ARKADE_OPS = { ...OP, ...ARKADE_OP }` in the SDK.

BigNum is arbitrary precision up to 520 bytes. AnyHedge's published numerical error analysis exists
because BCH worked with 64-bit integers; there is no overflow ceiling here. What remains is `DIV`
truncation, handled with a fixed 1e8 scale exactly as stability does.

---

## Stack

- **Environment**: `nix develop` (flake at the repo root) — Go 1.26.5 and Node 22, reproducible
- **Service** (API, web, users, matching, oracle, arkd client): **Go**
- **Contract builder**: Go, in `covenant/`
- **Client verifier**: TypeScript, running in the browser — see §Verification
- **Covenant tests**: Go, against `github.com/arkade-os/emulator/pkg/arkade`
- **Integration**: nigiri + arkd + arkd-wallet + emulator via Docker Compose

`arkadec` (the `.ark` file) is kept as **readable spec** and is not on the build path — see
AGENTS.md §Toolchain.

```sh
nix develop                      # Go 1.26.5 + Node 22
cd covenant && go test ./...
```

### `covenant/` — the contract, and the VM it runs on

The settlement formula exists in exactly one place: the Arkade Script this package emits. Nothing
recomputes it in Go, and the tests assert on what the VM does rather than on what a parallel model
would predict.

| File | What it covers |
|---|---|
| `settlement.go` | `Terms` and `SettlementScript()` — builds the script with `txscript.NewScriptBuilder` and the opcodes `pkg/arkade` exports |
| `vm.go` | The harness: an `ArkPrevOutFetcher`, a synthetic spending transaction, and `Run` to execute a script on `arkade.NewEngine` |

All arithmetic runs on the VM, including `hedgeValueCents * 1e8` — folding that into a build-time
constant overflows int64 for a large enough position, and BigNum is the only arithmetic here
without a ceiling.

Expected payouts are written out in the test table, never computed. If the table and the VM
disagree, the VM is what settles real money.

Currently pinned: the split at four prices, the clamp exactly on the liquidation boundary,
truncation direction, the int64 overflow, six ways of underpaying a side, and script determinism.

> **What the VM taught us**: just above the liquidation price the long is owed 4 sats, which is
> dust, so no second output is created and everything legitimately goes to the hedge. Liquidation
> has a dust *band*, not a single point.

### Verification

The service builds the tree and sends it whole. The client does not rebuild it — it **recognises**
it:

1. Derive the taproot address from the leaves it was sent and compare it with the address it is
   about to fund. A match proves there is no fourth leaf, since the address commits to the whole
   tree
2. Match each leaf against the known contract templates. A hit renders human-readably from the
   parameters, which arrive structured. A miss is a hard stop — unknown contract, do not fund

Failing closed is the point: never "unrecognised but probably fine". This is the same shape as
arkd's own closure whitelist. As contract versions accumulate the client carries several templates
and tries each.

The verifier duplicates the builder, so CI pins both to a golden hex fixture. Two implementations
that must agree byte for byte will diverge silently otherwise.

---

## Key differences vs. AnyHedge (BCH)

| | BCH (AnyHedge) | Arkade |
|---|---|---|
| Where the rich covenant runs | BCH node consensus, onchain, always | Emulator VM, offchain, while the operator cooperates |
| Emergency path | None — the rich path is already the final layer | Leaf 3 + the pre-signed package, needed because Bitcoin L1 cannot validate introspection |
| Settlement speed | Needs a BCH block confirmation | Instant, offchain (except Leaf 3) |
| Base security | BCH hashrate | Inherits Bitcoin L1 |
| Trust on the normal path | None — the node validates | Emulator/operator honesty (mitigated by Leaf 3 as a backstop) |
| Liquidation thresholds | Precomputed from leverage | Implicit in the clamp |

---

## Open risk: a fixed-term contract can outlive its batch

VTXOs are not permanent. Every VTXO lives inside a batch output with an expiry window, and *"if a
user's VTXO is still active when the batch expires and they have not renewed it, the operator can
claim those funds"* — the user keeps a recovery route but **loses the ability to enforce the claim
unilaterally onchain**. Renewal means participating in a batch swap before expiry.

`maturityTime` can sit past the batch expiry. Renewing means spending and recreating the VTXO,
which for a two-party contract VTXO is not the automatic background operation the wallet SDK runs
for ordinary funds.

Unresolved, and it gates production:

- Whether `maturityTime` must be capped at the batch expiry window
- Whether the service can drive renewal, and what signatures that needs from both parties
- What happens to the pre-signed exit package after a renewal. It references the old VTXO, so it
  has to be re-signed on every renewal, which puts a liveness requirement on both parties

---

## Still to define

- [ ] **Matching**: bilateral (both parties know each other at creation) or an order book in the
      service? Decides whether the service is a stateless co-signer or a stateful component
- [ ] **High liquidation threshold**: with `hedgeLeverage = 1x` and the conservation formulation,
      only the low threshold (`hedgePayoutSats >= totalCollateral`) appears to bind. Confirm
      AnyHedge's high threshold is only needed when the hedge side is leveraged
- [ ] **CSV window** on the exit leaf — must be seconds, a multiple of 512, and at or above
      `getInfo().exitDelay`. The lower bound is protocol-imposed; the value above it is ours
- [ ] **Service/protocol fee** (AnyHedge charges it trustlessly inside the contract)
- [ ] **Minimum collateral ratio** at funding, so the long cannot open an already-liquidatable
      position
- [ ] Web service scope: API only, or a UI too?
- [ ] **The split out of the 2-of-3 after an emergency exit**: does the service arbitrate by
      signing the same formula on the latest oracle price, or do the parties agree between
      themselves? The covenant is no longer there to impose it
- [ ] **Persistence of the pre-signed package**: where it lives, who holds it, how it is recovered
      if a party loses it
