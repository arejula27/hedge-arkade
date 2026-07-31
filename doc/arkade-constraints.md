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

### The design renewal takes

[Intent delegation](https://docs.arkadeos.com/arkd/components/intent-delegation) gives the shape,
and our tree already has it. Step 3 of that page is the load-bearing detail: the BIP322 intent proof
uses the **exit path**, not a collaborative one. So neither the operator nor a delegate is committed
by it.

| Piece | Arkade's path | Our leaf | Signed by |
|---|---|---|---|
| BIP322 intent proof | A+CSV (exit) | **leaf 3** | short + long |
| Forfeit tx (`ANYONECANPAY`) | A+B+S | **leaf 2** | short + long, operator completes |

Renewal needs no new leaf, and **the service must not be added to one**. In Arkade's model the
delegate occupies the `B` slot because there is one user and a third-party delegate; here `B` is
already the counterparty. A fourth key would give the service a veto over renewal — the power it is
denied everywhere else — and buys nothing, because the presigned intent and forfeit already pin what
it may submit.

The proof spends a leaf with a CSV, so it carries a BIP68 sequence: `intent.Verify` runs the proof
through btcd's script engine (`intent/proof.go:52`), which checks the sequence against the script.
How old the VTXO is belongs to consensus, which never sees this transaction.

**Renewal changes the outpoint and therefore invalidates the pre-signed exit package.** Even with
`ANYONECANPAY`, BIP341 still commits to the spent input's outpoint, and the new outpoint depends on
the whole batch, so it cannot be pre-signed. Two consequences: a presigned intent covers exactly one
renewal, not a chain; and both parties have to re-sign the exit for the new outpoint afterwards.
Delegation therefore removes the need to be online *during the interactive batch*, not the need to
appear once per expiry window.

Arkade's own docs put a limit on how much delegation buys: delegated renewals "keep your VTXOs in
the preconfirmation state and do not achieve Bitcoin finality". Prefiguring the forfeit means
committing to give up the old VTXO before seeing the tree that creates the new one. So the delegated
path is an option, not a replacement for the bilateral one.

### Measured: what arkd accepts, and what it charges

Registering an intent for a real contract VTXO on the live stack (`integration/renewal_test.go`):

| Question | Answer |
|---|---|
| Intent proved on leaf 3, signed by short + long only | **accepted** |
| Intent proved on leaf 2 | also accepted |
| Intent signed by one party alone | rejected, `missing signature for <key>` |
| Cost | a fee, quoted by `EstimateIntentFee` |

The fee is the finding that changes the design. arkd quoted 200,000 sats for a one-input renewal of
a 20,000,000 sat contract, and 495,000 once the fee-paying coin and its change were added — it is
priced from the intent it is charged on, so the estimate is a fixed point rather than a lookup.

**The fee cannot come out of the contract.** The covenant pins the settlement input at exactly
`payoutSats`, so a contract that had paid one renewal fee could never settle through leaf 1 again.
The renewal intent therefore carries a second input — somebody's own coin — that pays the fee and
takes its change back, leaving the contract output at exactly `payoutSats`. This is the natural
place for the service to earn its keep as the delegate.

### Joining a batch on the contract's behalf

The SDK's `Settle` only swaps VTXOs its wallet owns and signs everything with the wallet's key, so
it cannot renew a contract. The event loop underneath it can: `arksdk.JoinBatchSession` takes a
`BatchEventsHandler` interface, so only the signing has to be replaced, not the protocol.

What that replacement has to get right:

- **Forfeit through leaf 2, not through `ForfeitClosures()[0]`.** That helper returns leaves 1 and 2
  and the SDK takes the first, which here is the settlement leaf — whose second key is the tweaked
  emulator key, so that forfeit could only be signed by running the covenant. Leaf 2 is a forfeit
  closure in the ordinary sense: both owners hand the money over and the operator co-signs
- **The cosigner key signs the tree, not the money.** The branch it signs pays the contract address
  and nothing else, so the role can be delegated without giving anything up
- **Mine after the commitment.** Nothing mines on this stack unless a test asks, and a commitment
  left in the mempool makes the next intent look like it spends an input that is still unspent

Proven end to end in `integration/renewal_test.go`: a contract created in one batch, renewed into
another, and then closed through each of its three leaves in turn.

Still unresolved, and it gates production:

- Whether `maturityTime` must be capped at the batch expiry window
- Who pays the renewal fee in production, and how that is priced into the contract
- Re-signing the exit package on every renewal, and the gap between the batch confirming and both
  signatures arriving, during which neither party can exit without the other
- Why Arkade's model puts the delegate in the forfeit path — convention, or something arkd enforces

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
