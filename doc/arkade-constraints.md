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
| Batch expiry | 400 **blocks** (`ARKD_VTXO_TREE_EXPIRY`; the stack ships 180) |
| Session duration | 30s; rounds cycle roughly every 5s |
| Scheduled sessions | none — batches form on demand |

Delays are blocks below 512 and seconds above it, and arkd refuses to start if they disagree with
each other — so this has to stay under 512 while the rest of the stack is block-based.

**An Arkade transaction does not renew anything.** A contract VTXO funded from a party's VTXO
expires at *exactly* the same instant as the VTXO it was funded from — it inherits its ancestor's
batch expiry rather than starting its own.

That matches the docs: renewal means participating in a **batch swap**, which "creates a fresh VTXO
in a new batch with a reset expiry timer"
([batch expiry](https://docs.arkadeos.com/learn/core-concepts/vtxo-lifecycle-and-liveness#batch-expiry)).
Note a conflict worth resolving before relying on either: the
[Lightning channels page](https://docs.arkadeos.com/contracts/lightning-channels#renewal) says a
two-party channel VTXO renews when "either party submits an Arkade transaction attaching the channel
VTXO to a new output", which our measurement does not support.

### Renewing needs the previous commitment confirmed

A second batch swap from the same wallet appears to hang: `Settle` blocks and arkd cycles rounds
aborting with `not enough intents registered 0/1`. The error it eventually reports —
`failed to register intent: context deadline exceeded` — is the last retry hitting the deadline, not
the cause. arkd's own log gives the real one:

```
method=/ark.v1.ArkService/RegisterIntent duration=23ms     <- the intent is accepted
boarding input 0b4d6735...:1 is spent  intent_id=b95ed511  <- and then dropped
```

The first swap's commitment transaction is still in the mempool, because nothing mines unless a test
asks. The boarding UTXO it spent therefore still looks unspent, the SDK includes it in the next
intent, and arkd drops the intent for containing a spent input.

**Mine after settling.** With the commitment confirmed, renewal takes about two seconds and the
expiry resets. This is a property of a chain with no automatic mining, not a defect in arkd or the
SDK.

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

Renewing the *contract* VTXO is the part that is not built. The SDK's `Settle` only swaps VTXOs the
wallet owns, and its exported `RegisterIntent` signs the intent proof with the wallet's key, whereas
a contract VTXO's proof has to be signed by the parties on leaf 2. Driving the rest — tree nonces,
tree signatures, forfeits — is `handleBatchEvents`, which is unexported. So contract renewal means
reimplementing batch participation with multi-party signing.

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
