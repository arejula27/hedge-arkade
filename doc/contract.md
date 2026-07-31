# The contract

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

## Leaf 1 — Settlement at maturity or liquidation
- Full covenant, co-signed by the emulator
- `checkSigFromStack` validates the oracle-signed price message
- `MUL`/`DIV` compute `hedgePayoutSats`
- `INSPECTOUTPUTVALUE` enforces conservation
- Witness carries two consecutive oracle messages; `settlementSeq == prevSeq + 1` and
  `prevTimestamp < maturityTime` pin the settlement message (see "Settlement timing")
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

## Leaf 2 — Mutual redemption

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

## Why leaves 1 and 2 carry the operator key and leaf 3 does not

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
