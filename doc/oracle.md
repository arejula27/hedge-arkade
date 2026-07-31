# The oracle

```
msg = sha256(ticker || sequence || price || timestamp)
sig = sign(oraclePk, msg)
```

- `sequence`, `price` and `timestamp`: **8-byte little-endian unsigned** integers
- `sequence` increments by one on every publication, with no gaps. This is the field the whole
  settlement rests on — see "Settlement timing" below
- `price` in USD cents per BTC
- `ticker` lets us add feeds without touching the contract

Verified with `checkSigFromStack(oracleSig, oraclePk, oracleMsg)` — `OP_CHECKSIGFROMSTACK`
(`0xcc`), 64-byte compact signature, 32-byte x-only Schnorr pubkey.

The oracle is a **stateless signer**. It publishes signed prices on a fixed cadence, knows nothing
about any contract, and never touches a transaction. Whoever settles puts the message in the
witness. One oracle serves every contract, and it can be entirely disconnected from Arkade.

## Settlement timing — how AnyHedge does it without a clock

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
