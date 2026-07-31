# Arkade constraints

## Terminology

Arkade's docs deprecate "refresh". The term for swapping an old VTXO for a fresh one in a new batch
is **renewal**.

## Open risk: a fixed-term contract can outlive its batch

VTXOs are not permanent. Every VTXO lives inside a batch output with an expiry window, and *"if a
user's VTXO is still active when the batch expires and they have not renewed it, the operator can
claim those funds"* — the user keeps a recovery route but **loses the ability to enforce the claim
unilaterally onchain**. Renewal means participating in a batch swap before expiry.

`maturityTime` can sit past the batch expiry. Renewing means spending and recreating the VTXO,
which for a two-party contract VTXO is not the automatic background operation the wallet SDK runs
for ordinary funds.

### Measured on the regtest stack

| Fact | Value |
|---|---|
| Batch expiry | 180s (`ARKD_VTXO_TREE_EXPIRY`, overridable) |
| Session duration | 30s; rounds cycle roughly every 5s |
| Scheduled sessions | none — batches form on demand |

**An Arkade transaction does not renew anything.** A contract VTXO funded from a party's VTXO
expires at *exactly* the same instant as the VTXO it was funded from — it inherits its ancestor's
batch expiry rather than starting its own. Measured: the funding VTXO and the contract VTXO created
from it both expired at 19:49:20.

That matches the docs: renewal means participating in a **batch swap**, which "creates a fresh VTXO
in a new batch with a reset expiry timer"
([batch expiry](https://docs.arkadeos.com/learn/core-concepts/vtxo-lifecycle-and-liveness#batch-expiry)).
Note a conflict worth resolving before relying on either: the
[Lightning channels page](https://docs.arkadeos.com/contracts/lightning-channels#renewal) says a
two-party channel VTXO renews when "either party submits an Arkade transaction attaching the channel
VTXO to a new output", which our measurement does not support.

### Blocker found while trying to implement renewal

**A second `Settle` from the same wallet hangs.** The first one works; the next one blocks inside
`RegisterIntent` until the context expires, and arkd meanwhile cycles rounds aborting with
`not enough intents registered 0/1` — so the intent never lands. It is not a mining problem (it
hangs with and without a miner running) and not boarding-versus-VTXO (it hangs with a fresh boarding
UTXO present too).

Nothing caught this before because every integration test creates a fresh party and funds it once.
It blocks the easy path to renewal, since renewal *is* a batch swap. A hand-rolled implementation
driving `RegisterIntent`/forfeits directly for the contract VTXO may sidestep it, but that is the
full batch protocol — intent proof, tree nonces, tree signatures, forfeits — not a small piece.

### The design renewal would take

Leaf 2 is `short + long + arkd`: a forfeit closure, which is exactly what a batch swap needs to
forfeit the old VTXO. So a collaborative renewal is a batch swap that spends the contract VTXO
through leaf 2 and recreates it at the same contract address, for the same amount.

Both parties are present for that anyway, which is convenient, because **renewal changes the
outpoint and therefore invalidates the pre-signed exit package**. It has to be re-signed for the new
outpoint in the same ceremony. Arkade supports delegating renewal via presigned intents without
giving up custody ([intent delegation](https://docs.arkadeos.com/arkd/components/intent-delegation)),
which would fit the service, but the delegated forfeit is signed `SIGHASH_ALL | ANYONECANPAY` on a
path the delegate is part of — worth checking against our leaf 2.

Still unresolved, and it gates production:

- Whether `maturityTime` must be capped at the batch expiry window
- Whether the service can drive renewal, and what signatures that needs from both parties
- Re-signing the exit package on every renewal, and the liveness requirement that puts on both
  parties

---

## Verification

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
