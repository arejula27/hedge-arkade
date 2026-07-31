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

```
clampedPrice = max(min(oraclePrice, highLiquidationPrice), lowLiquidationPrice)
shortSats    = min(payoutSats - DUST, max(DUST, nominalUnitsXSatsPerBtc/clampedPrice - satsForNominalUnitsAtHighLiquidation))
longSats     = payoutSats - shortSats
```

Division truncates, and truncation costs the short side the fraction.

**Dust is a floor, not an omission.** Both sides always get an output, never fewer than `DUST`.
This differs from `stability_vault.ark`, which drops an output below 330 sats. `DUST` is 1332 on
BCH; the Bitcoin value has to be set for our own relay rules.

**The cap is where this leaves AnyHedge**, and Arkade forces it. AnyHedge floors both sides
independently — `longSats = max(DUST, payoutSats - shortSats)` — so at a liquidation the two
payouts can sum to more than `payoutSats`, with the difference coming out of the miner fee the
funder included. Arkade has no miner fee to draw on: arkd rebuilds every offchain transaction with
`offchain.BuildTxs`, which refuses an input amount that differs from the output amount
(`offchain/tx.go:64`), then compares txids (`service.go:979`). A settlement that does not balance
cannot be submitted. Capping the short at `payoutSats - DUST` keeps the long its dust output and
the sum exactly `payoutSats`; the difference — at most `DUST` — comes off the short instead of off
a fee.

**Outputs are exact.** Not `>=`:

```
tx.inputs.length == 1
tx.inputs[current].value == payoutSats
tx.outputs[0].value == shortSats  &&  tx.outputs[0].scriptPubKey == shortLockScript
tx.outputs[1].value == longSats   &&  tx.outputs[1].scriptPubKey == longLockScript
```

Checking values without checking the lock scripts is a hole: the amounts would be right and the
recipients arbitrary.

**The output count is not checked**, though AnyHedge checks it. An Arkade transaction carries the
emulator packet as an extension OP_RETURN and a P2A anchor besides the two payouts, so `== 2` would
reject every real settlement. Arithmetic replaces it and is stronger: the input is pinned to
`payoutSats`, the two payouts sum to `payoutSats`, and no transaction can pay out more than it
takes in — so every other output is necessarily worth zero. That holds by conservation of value,
not by arkd's cooperation.

Pinning the input is also what makes overfunding harmless. A VTXO holding anything other than
`payoutSats` cannot settle at all; it is unusable rather than exploitable, and it fails inside the
covenant with a clear cause instead of inside arkd's rebuild.

---

## The contract is one VTXO

A whole contract is a **single VTXO** — one output, holding exactly `payoutSats`. Both sides' money
sits in the same place, and which side owns how much of it is decided later, by whichever leaf ends
up being spent.

Two inputs appear at funding, one per party, but they belong to the funding transaction rather than
to the contract:

```
  FUNDING (one Arkade transaction, two inputs)

    short's VTXO  ──┐                    ┌──►  THE CONTRACT VTXO
                    ├──► funding tx ─────┤       exactly payoutSats
    long's VTXO   ──┘                    │       taproot, 3 leaves
                                         ├──►  short's change
                                         └──►  long's change

  SPENDING (one input — the covenant requires it)

                         ┌── leaf 1 ──►  short's payout + long's payout
    THE CONTRACT VTXO ───┼── leaf 2 ──►  whatever the two of them agree
                         └── leaf 3 ──►  a 2-of-3, on Bitcoin, after the CSV
```

So: one VTXO, created by a transaction with two inputs, and spent as one input. The covenant pins
that last part — `OP_INSPECTNUMINPUTS` must equal 1 — so nobody can settle the contract alongside
other money and blur whose sats went where.

## Renewal: the same contract, a later batch

A contract inherits the batch expiry of whatever funded it, so a fixed-term contract can outlive the
batch it lives in — and a VTXO whose batch has expired loses its unilateral route onchain. Renewal
is a **batch swap**, not an Arkade transaction: it forfeits the contract and recreates it, same
address and same sats, in a batch that expires later.

```
  RENEWAL — one intent, proved on leaf 3 by short + long

     batch A                                            batch B
     ─────────────────                                  ─────────────────
     THE CONTRACT VTXO ──┐                          ┌──► THE CONTRACT VTXO
       exactly payoutSats│   forfeit via leaf 2     │      exactly payoutSats
       expires with A    │   (short + long + arkd)  │      expires with B
                         ├──────  batch swap  ──────┤
     somebody's coin  ───┤                          ├──► their change
       pays arkd's fee   │                          │      coin − fee
                         └──────────────────────────┘

     same address, same tree, same terms — only the outpoint and the expiry move
```

### What a forfeit is, and why it is leaf 2

A batch swap is a trade: you give up the old VTXO, the operator gives you a new one in the new batch.
The giving-up half is a **forfeit transaction** — it pays the old VTXO to the operator's forfeit
address. On its own it is inert, because it also needs a *connector* input, an output that exists
only in a batch that really created your new VTXO. So the operator cannot take the old contract
without having already produced the new one. Signing a forfeit is not signing the money away.

The forfeit has to be signed on a leaf, and only one of ours will do:

| Leaf | Keys | Can it forfeit? |
|---|---|---|
| 1 — settlement | arkd + tweaked emulator key | **No.** The emulator only signs if the covenant passes, and the covenant demands the transaction pay `shortLockScript` and `longLockScript` exactly. A forfeit pays the operator |
| 2 — mutual redemption | short + long + arkd | **Yes.** A plain 3-of-3. No covenant, no oracle, no constraint on the outputs |
| 3 — exit | short + long, after a CSV | **No.** It carries no operator key, so it is not a collaborative path |

**The covenant is not in the tree, and it is not in every leaf.** It lives in leaf 1 alone, and even
there it is not script — it is a key, tweaked by the settlement script, that the emulator will only
sign with once it has run that script and seen it pass. Leaf 2 has no such key. That is exactly why
mutual redemption can pay any split the two of them agree, and exactly why it is the leaf a forfeit
can go through.

Leaf 3's role in renewal is a different artefact: it signs the **intent proof**, a BIP322 signature
that proves who owns the coin and is never broadcast. Proof and forfeit are two separate things
signed on two separate leaves.

Three things fall out of that picture:

- **The amount is untouchable.** The covenant pins the settlement input at exactly `payoutSats`, so
  arkd's fee cannot be taken off the contract — it would leave a VTXO leaf 1 can never settle. A
  second input brings its own sats and takes its change back
- **Leaf 2 is the forfeit path.** It is the one closure that is both a collaborative path and needs
  nobody but the two owners and the operator. Leaf 1 would need the covenant executed to sign
- **The pre-signed exit dies here.** A taproot signature commits to the outpoint it spends, and no
  sighash flag changes that, so the package signed at funding is worthless the moment the contract
  moves. Both parties have to sign a new one, against a VTXO whose identity nobody could know in
  advance — which is the part of renewal that cannot be delegated

See [arkade-constraints](arkade-constraints.md) for what was measured, and `integration/renewal_test.go`
for the contract being created in one batch, renewed, and closed through each leaf.

## Taproot structure

That single VTXO's output script is a taproot tree. Internal key = **NUMS** (Nothing Up My Sleeve)
— no key-path spend, forcing one of the branches.

```
                Taproot output (NUMS internal key)
                            |
        ┌───────────────────┼───────────────────┐
        |                   |                   |
      Leaf 1              Leaf 2              Leaf 3
   Settlement         Early mutual         Unilateral
   at maturity        close                exit
   or liquidation

   arkd signer        short + long         CSV, then
   + tweaked          + arkd signer        short + long
   emulator key

   ── collaborative paths (no CSV) ──   ── unilateral path ──
     must carry the operator key,        no operator key,
     spent offchain, instantly           spent on L1 after the CSV
```

Every leaf is a plain N-of-N multisig — one of the five shapes arkd's `DecodeClosure` accepts.
There is no covenant *in* the tree.

**The covenant enters through a key.** Leaf 1 carries
`ComputeArkadeScriptPublicKey(emulatorSigner, taggedHash("ArkScriptHash", settlementScript))`. The
Arkade Script itself travels in the emulator packet — an OP_RETURN TLV on the spending Arkade
transaction — alongside its witness. The emulator recomputes the tweak from the script it was
handed and looks for the result in the leaf being spent (`pkg/arkade/script.go:91`,
`ReadArkadeScript`). No match, no signature: `ErrTweakedArkadePubKeyNotFound`. A match means it
executes the script and only signs if it succeeds.

Two consequences. Editing one opcode of the covenant changes the key, so the leaf can only ever ask
for the script it was built for. And arkd sees an ordinary multisig, so the covenant needs no
special support from it.

Building this is `covenant/vtxo.go`; `Contract.Validate` runs arkd's own `TapscriptsVtxoScript.Validate`
against the tree before anything is funded.

## Leaf 1 — Settlement at maturity or liquidation

Keys: the arkd signer, and the emulator key tweaked by the settlement script.

**No party key.** Whoever holds two adjacent oracle messages can settle, because the covenant
leaves them no freedom in what the transaction pays — recipients and amounts are checked exactly.
AnyHedge's `payout` is permissionless for the same reason. In practice the service settles; the
counterparty or a third party could do it and the result would be identical to the sat.

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

Keys: short, long, and the arkd signer. No oracle and no output constraints — both owners of the
money agree on any split they like. Spends offchain, instantly.

AnyHedge's own comment names the use an oracle-priced early close cannot serve: *"useful for example
in the case of a funding error"* — an unwinding at a price no oracle message supports.

`enableMutualRedemption` comes across too: `Contract.EnableMutualRedemption` drops the leaf entirely,
and the tree is two leaves instead of three.

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
