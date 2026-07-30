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
| Contract base | **AnyHedge v0.12**, ported as literally as Arkade allows | 2026-07-30 |
| Oracle | **Run by us**, AnyHedge message layout (sequence + timestamp + price) | 2026-07-30 |
| Emergency exit | One CSV 2-of-2 leaf, pre-signed at funding, sweeping to a 2-of-3 | 2026-07-30 |
| Mutual redemption | **Free split**, as AnyHedge. Reverses the oracle-priced decision — see Leaf 2 | 2026-07-30 |
| Structure | 3 leaves: 2 covenant (offchain) + 1 exit (L1). Only the exit touches Bitcoin | 2026-07-30 |
| Maturity gate | **Sequence adjacency**, AnyHedge's mechanism. No clock involved | 2026-07-30 |
| Oracle message | `(ticker, sequence, price, timestamp)` — the sequence is load-bearing | 2026-07-30 |
| Language | **Go** for the service and the contract builder; TypeScript only for the client-side verifier | 2026-07-30 |
| Contract distribution | The service builds the tree and sends it whole; the client recognises it or refuses | 2026-07-30 |
| Funding rate | **Dropped** — it is what makes stability perpetual | 2026-07-27 |

**The settlement math lives in the covenant**, executed by the emulator VM. No part of it is
reimplemented in the service. `covenant/` builds that script and runs it against the real VM; it
contains no formula of its own. See §`covenant/`.

**Viability: confirmed opcode by opcode**, and the payout covenant now executes on the real VM.
See §Verified viability and §`covenant/`.

---

## Starting point: AnyHedge v0.12

`@generalprotocols/anyhedge-contracts@0.12.1`, `contracts/v0.12/contract.cash` — 153 lines, in
production since 2020. **The goal is to stay as close to it as Arkade allows.** Where this spec and
that file disagree, the file is right unless the difference is forced by the platform.

`../compiler/examples/stability/stability_vault.ark` remains useful as a worked example of Arkade
Script — the oracle verification, the arithmetic, the introspection — but its *design* is a
perpetual with a funding rate, and we take the design from AnyHedge.

| AnyHedge | This port |
|---|---|
| `mutualRedeem`, two signatures | Leaf 2, plus the operator key Arkade requires |
| `payout`, liquidation or maturity | Leaf 1, same logic, as an Arkade covenant |
| — | Leaf 3, unilateral exit. Arkade requires it; BCH has no equivalent |
| `checkDataSig` | `OP_CHECKSIGFROMSTACK` |
| P2SH, one script | Taproot, three leaves |
| 4-byte ints throughout | BigNum, arbitrary precision |

**Forced differences** — Arkade leaves no choice:

- An exit path, so there is a third leaf and a pre-signed sweep
- Collaborative leaves must carry the operator pubkey
- The covenant runs on the emulator, not on node consensus
- Payouts land in VTXOs, so `shortLockScript`/`longLockScript` are Arkade scripts, not BCH P2PKH

**Chosen differences** — where ours is better and we keep it:

- **BigNum instead of 4-byte ints.** AnyHedge is stuck with uint32 prices and timestamps, which is
  why it needs a published numerical error analysis and why its timestamps die in 2106. Arkade's
  BigNum has no ceiling, so the analysis is unnecessary rather than merely passed

Everything else follows AnyHedge, including the parts an earlier draft of this spec had changed:
both liquidation boundaries, the clamp, the leverage term, dust as a floor rather than an omitted
output, exact output values and lock scripts, and a free mutual redemption.

---

## Actors

- **Short** (AnyHedge's name; the hedge side): wants to preserve the value of their BTC in terms of
  an external asset. A 1x short unless `satsForNominalUnitsAtHighLiquidation` says otherwise.
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

Mirroring `AnyHedge_v0_12` parameter for parameter, with BCH-specific names carried over so the two
can be diffed. AnyHedge calls the hedge side **short**; we use the same word here.

| Parameter | Description |
|---|---|
| `shortMutualRedeemPublicKey` | Short side key, for mutual redemption |
| `longMutualRedeemPublicKey` | Long side key, for mutual redemption |
| `enableMutualRedemption` | Flag; mutual redemption can be switched off at creation |
| `shortLockScript` | Where the short is paid. Any valid output script |
| `longLockScript` | Where the long is paid |
| `oraclePublicKey` | Price oracle public key |
| `nominalUnitsXSatsPerBch` | Nominal hedge value in units, scaled by 1e8 |
| `satsForNominalUnitsAtHighLiquidation` | Leverage term. `0` means a pure 1x hedge |
| `payoutSats` | Total payout, miner fee excluded |
| `lowLiquidationPrice` | Lower clamp boundary |
| `highLiquidationPrice` | Upper clamp boundary |
| `startTimestamp` | Earliest timestamp a liquidation may be redeemed at |
| `maturityTimestamp` | Required timestamp for maturity redemption |

**Both liquidation boundaries come back.** An earlier draft dropped `highLiquidationPrice` on the
grounds that a 1x hedge only binds below. That was wrong, and for a reason that matters: the clamp
is what makes every out-of-bounds message pay identically, which is half of why AnyHedge needs no
clock. Without the upper boundary there is no upper clamp and the long's win is unbounded.

`satsForNominalUnitsAtHighLiquidation` is the leverage term. At `0` the short is a pure 1x hedge,
which is the product we described; keeping the parameter costs nothing and leaves leveraged shorts
available later.

Arkade adds to that list:

| Parameter | Type | Description |
|---|---|---|
| `servicePk` | pubkey | Third key of the 2-of-3 the emergency exit sweeps into |
| `signerPk` | pubkey | The operator, required on both collaborative leaves |
| `exit` | int | CSV delay on the exit leaf (seconds, multiple of 512, ≥ `getInfo().exitDelay`) |
| `ticker` | bytes32 | Feed identifier, e.g. `sha256("BTC/USD")` |

---

## Formulas

Taken from `contract.cash` unchanged:

```
clampedPrice = max(min(oraclePrice, highLiquidationPrice), lowLiquidationPrice)
shortSats    = max(DUST, (nominalUnitsXSatsPerBch / clampedPrice) - satsForNominalUnitsAtHighLiquidation)
longSats     = max(DUST, payoutSats - shortSats)
```

Division truncates, and truncation costs the short side the fraction.

**Dust is a floor, not an omission.** AnyHedge pays `max(DUST, …)` and always produces exactly two
outputs. This differs from `stability_vault.ark`, which drops an output below 330 sats — we follow
AnyHedge. `DUST` is 1332 on BCH; the Bitcoin value has to be set for our own relay rules.

**Outputs are exact.** Not `>=`:

```
require(tx.inputs.length == 1);
require(tx.outputs.length == 2);
require(tx.outputs[0].value == shortSats);
require(tx.outputs[0].lockingBytecode == shortLockScript);
require(tx.outputs[1].value == longSats);
require(tx.outputs[1].lockingBytecode == longLockScript);
```

Checking values without checking the lock scripts is a hole: the amounts would be right and the
recipients arbitrary.

---

## Oracle message format

```
msg = sha256(ticker || sequence || price || timestamp)
sig = sign(oraclePk, msg)
```

- `sequence`, `price` and `timestamp`: **8-byte little-endian unsigned** integers
- `sequence` increments by one on every publication, with no gaps. This is the field the whole
  settlement rests on — see §Settlement timing below
- `price` in USD cents per BTC
- `ticker` lets us add feeds without touching the contract

Verified with `checkSigFromStack(oracleSig, oraclePk, oracleMsg)` — `OP_CHECKSIGFROMSTACK`
(`0xcc`), 64-byte compact signature, 32-byte x-only Schnorr pubkey.

The oracle is a **stateless signer**. It publishes signed prices on a fixed cadence, knows nothing
about any contract, and never touches a transaction. Whoever settles puts the message in the
witness. One oracle serves every contract, and it can be entirely disconnected from Arkade.

### Settlement timing — how AnyHedge does it without a clock

There is no trustworthy clock. `tx.offchainTime` does not exist: the compiler @ `3988a9d` maps only
`version`, `locktime`, `numInputs`, `numOutputs`, `weight` and `id`
(`src/compiler/introspection.rs:5`), and the VM's only clock is `OP_INSPECTLOCKTIME`, which reads a
`nLockTime` the spender chose. A freshness window is therefore unenforceable — and Bitcoin cannot
express "this expires after T" at all. CLTV and consensus are both lower bounds on time.

AnyHedge does not need one. Verified against `@generalprotocols/anyhedge-contracts@0.12.1`,
`contracts/v0.12/contract.cash`. Two mechanisms replace it.

**Sequence adjacency.** The spender must supply the settlement message *and its immediate
predecessor*:

```
require(settlementSequence - 1 == previousSequence);   // adjacent, no gaps
require(previousTimestamp < maturityTimestamp);        // predecessor is pre-maturity
```

If the predecessor is before maturity and the settlement message is the very next one, then the
settlement message is **the first message published on or after maturity**. Exactly one message
qualifies. The spender has no choice to make, so there is nothing to shop for.

**The clamp.** For liquidation the spender may use any message that crossed the boundary, but the
price is clamped to that boundary, so every crossing message pays exactly the same. Choosing among
them is meaningless.

Together these turn two questions that need a clock — *"what is the price now?"* — into two that do
not: *"did the price ever cross this line?"* and *"what was the first price after maturity?"* Both
have a unique answer regardless of when they are asked.

This is why a stale signed price is not an attack here. In a contract with a liquidation boundary,
touching the boundary **is** the event: it liquidates permanently, like a stop-out. The old message
is evidence of something that really happened.

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
- Witness carries two consecutive oracle messages; `settlementSeq == prevSeq + 1` and
  `prevTimestamp < maturityTime` pin the settlement message (see §Settlement timing)
- Trigger: `settlementTimestamp >= maturityTime` **or** `hedgePayoutSats >= totalCollateral`
  (early liquidation — the long ran out of collateral)

**What settling actually does.** It spends the contract VTXO and creates two new VTXOs, one owned
by the hedge and one owned by the long, sized by the formula. The sats do move to each side; they
move offchain, as an Arkade transaction, so it is instant and costs no block space.

Each party then holds an ordinary VTXO. Taking it to Bitcoin L1 is a separate, later decision —
collaboratively by offboarding, or unilaterally through its own exit path. The contract does not
force that step and does not need it.

**Why the maturity gate is not a CLTV.** A tapscript CLTV applies to the whole leaf
unconditionally, so a leaf gated on `CLTV(maturityTime)` could not also fire early on liquidation.
The `matured || liquidated` disjunction lives in the covenant instead, reading the oracle message's
own timestamp, and the tapscript segment carries no timelock.

Both triggers share one leaf because they differ only in a condition, not in the key set.

### Leaf 2 — Mutual redemption

AnyHedge's version, adopted:

```
require(bool(enableMutualRedemption));
require(checkSig(shortMutualRedeemSignature, shortMutualRedeemPublicKey));
require(checkSig(longMutualRedeemSignature, longMutualRedeemPublicKey));
```

Two signatures, no oracle, no output constraints. Plus the operator pubkey Arkade requires on a
collaborative path. Spends offchain, instantly.

> **This reverses the 2026-07-30 decision** to settle the early close at the oracle price. That
> decision rested on "a free split buys nothing Leaf 3 does not already provide", which was wrong:
> Leaf 3 costs a full CSV wait and lands in a 2-of-3 where the split is *still* unresolved. A free
> mutual redemption is instant and final.

It is also strictly more capable, and the cost is nothing. Both parties must sign, so no one can
take a discretionary split alone — and when both owners of the money agree, "no path settles at
discretion" is a principle with nobody left to protect. AnyHedge's own comment names the use the
oracle-priced version cannot serve: *"useful for example in the case of a funding error"* — an
unwinding at a price no oracle message supports.

`enableMutualRedemption` comes across too: a contract can be created without this path at all.

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
| Maturity | The oracle message's timestamp, pinned by sequence adjacency | Witness, not introspection |

All resolve through `ARKADE_OPS = { ...OP, ...ARKADE_OP }` in the SDK.

BigNum is arbitrary precision up to 520 bytes. AnyHedge's published numerical error analysis exists
because BCH worked with 64-bit integers; there is no overflow ceiling here. What remains is `DIV`
truncation, handled with a fixed 1e8 scale exactly as stability does.

---

## Stack

- **Environment**: `nix develop` (flake at the repo root) — Go 1.26.5, Node 22 and `just`
- **Service** (API, web, users, matching, oracle, arkd client): **Go**
- **Contract builder**: Go, in `covenant/`
- **Client verifier**: TypeScript, running in the browser — see §Verification
- **Covenant tests**: Go, against `github.com/arkade-os/emulator/pkg/arkade`
- **Integration**: nigiri + arkd + arkd-wallet + emulator via Docker Compose

`arkadec` (the `.ark` file) is kept as **readable spec** and is not on the build path — see
AGENTS.md §Toolchain.

```sh
just              # list recipes
just check        # fmt, vet, tests
```

Every recipe wraps itself in `nix develop`, so they work from a bare shell with only nix
installed.

### `covenant/` — the contract, and the VM it runs on

The settlement formula exists in exactly one place: the Arkade Script this package emits. Nothing
recomputes it in Go, and the tests assert on what the VM does rather than on what a parallel model
would predict.

| File | What it covers |
|---|---|
| `settlement.go` | `Terms` and `SettlementScript()` — AnyHedge's payout path in full: transaction shape, recipients, both oracle signatures, sequence adjacency, the clamp and the payouts |
| `oracle.go` | The message layout and the signing the oracle service will do |
| `vm.go` | The harness: an `ArkPrevOutFetcher`, a synthetic spending transaction, and `Run` to execute a script on `arkade.NewEngine` |

`pkg/arkade` is the same interpreter the emulator service runs before co-signing
(`internal/application/tx.go:67`), pulled in as a published module — no local `replace`, so the
repo builds anywhere. `Run` also applies `WithExactComputeLimits(DefaultComputeLimits())`, the
table the service uses when no `COMPUTE_LIMITS` overrides are set.

`CHECKSIGFROMSTACK` verifies over a 32-byte digest and deliberately does not hash for the caller,
so the witness carries the raw message and the script hashes it. The witness is, bottom to top:
`settlementSignature, settlementMessage, previousSignature, previousMessage`.

**69 cases**, all green:

- The clamp at both boundaries, one cent inside each, and far past them
- The dust floor, including where the two payouts sum to more than `payoutSats`
- Truncation direction, the leverage term, a nominal of 9e18
- **Exactness**: for every accepted settlement, six mutations — each side ±1, a sat moved either
  way — must all fail. Overpaying is a different settlement, not a generous one
- **Recipients**: payouts redirected, swapped, sent to a non-taproot output
- **Shape**: a third output, a missing output, an extra input
- **Timing**: settlement at maturity, a liquidation at either boundary before maturity, and a
  liquidation exactly at `startTimestamp`
- **The attack the sequence check exists for**: a mid-range price from mid-contract, a message that
  is not the first after maturity, a gap in the sequence, the predecessor and settlement swapped,
  the same message used twice, a zero sequence, a zero price
- **Authentication**: each field edited after signing, a signature from the wrong key, the two
  signatures swapped, an empty signature, a garbage signature, and a contract pinned to a different
  oracle key
- Every parameter changing the script, plus a golden hex fixture the TypeScript verifier pins to

Verified by mutation: deleting the adjacency check makes exactly the two cases only it can catch
fail, and nothing else — the rest are caught by the maturity-or-liquidation disjunction behind it.

```sh
just check        # fmt, vet, tests
just test-one Sequence
just script-hex   # the fixture the verifier must match
```

**What remains before deployment**: the tapscript segments, the taproot tree, and the pre-signed
exit package. The covenant itself is complete.

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
