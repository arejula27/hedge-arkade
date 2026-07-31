# Unilateral exit

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

The leaf is a `CSVMultisigClosure`. The CSV is not decoration: it is what makes arkd classify the
leaf as an exit rather than reject it as a forfeit closure missing its signer. The delay has to be
at or above the operator's `getInfo().exitDelay` — the lower bound is theirs, the value above it is
ours. Whether a block-based delay is allowed at all is the operator's policy, passed to arkd's
`Validate` as `blockTypeAllowed`; production operators configure seconds, the regtest stacks
configure blocks so timelocks fire on mining.

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

## What is built

`covenant/exit.go`. `NewSweep` builds the 2-of-3 destination and hands back everything needed to
spend it again — leaf, control block and scriptPubKey — because an exit that lands somewhere the
parties cannot reopen is the same as no exit. `PreSignExit` builds the transaction and collects both
signatures, refusing a key that is not the one the contract was built around. `Finalize` attaches
the witness and returns a transaction that can be broadcast as it stands.

The sweep leaf is the canonical BIP342 threshold, `<A> CHECKSIG <B> CHECKSIGADD <C> CHECKSIGADD 2
NUMEQUAL`. Because it ends in `NUMEQUAL` rather than a comparison, a *third* valid signature makes
the running total 3 and the spend fails, so `Sweep.Witness` puts in exactly two even when more are
supplied.

Nothing on this path goes through arkd or the emulator, which is what the tests exploit: the exit
leaf contains no Arkade opcodes, so the unit tests run the finalised witness through btcd's own
consensus engine, and the integration tests fund the contract address straight from the faucet and
broadcast through bitcoind. Both tiers pin *why* a bad exit fails — `non-BIP68-final` for one
broadcast before the delay, `Invalid Schnorr signature` for one rewritten after signing — rather
than only that it did.

> **Note**: every example contract in the compiler (`fuji_safe`, `cash_secured_put`,
> `stability_vault`, `bond_mint`…) uses a single-signature exit, which works for single-owner
> contracts. Here two parties have money inside, so a single-sig exit is a theft surface — the same
> criticism Arkade's documentation makes of `repayment_pool.ark`.
