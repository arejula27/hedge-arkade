# Arkade constraints

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
