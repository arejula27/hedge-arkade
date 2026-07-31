# Porting AnyHedge

The contract stays as close to `AnyHedge_v0_12` as Arkade allows.

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

## Key differences vs. AnyHedge (BCH)

| | BCH (AnyHedge) | Arkade |
|---|---|---|
| Where the rich covenant runs | BCH node consensus, onchain, always | Emulator VM, offchain, while the operator cooperates |
| Emergency path | None — the rich path is already the final layer | Leaf 3 + the pre-signed package, needed because Bitcoin L1 cannot validate introspection |
| Settlement speed | Needs a BCH block confirmation | Instant, offchain (except Leaf 3) |
| Base security | BCH hashrate | Inherits Bitcoin L1 |
| Trust on the normal path | None — the node validates | Emulator/operator honesty (mitigated by Leaf 3 as a backstop) |
| Liquidation thresholds | Precomputed from leverage | Implicit in the clamp |
