# Arkade Hedge

A fixed-term, BTC-collateralized hedge contract on Arkade. One party (**short**) locks in a
USD-denominated value; the counterparty (**long**) posts the extra collateral and takes the
opposite side of the price move. At maturity, or earlier if the price crosses a liquidation
boundary, a signed oracle price determines the split.

It is a port of [AnyHedge](https://gitlab.com/GeneralProtocols/anyhedge), which has run on Bitcoin
Cash since 2020. The contract logic stays as close to `AnyHedge_v0_12` as Arkade allows: the
covenant runs on the Arkade emulator's VM rather than node consensus, and Arkade's unilateral exit
requirement adds a path BCH has no equivalent for.

The service never custodies funds.

**Work in progress — not deployable.**

## Getting started

```sh
just                    # list recipes
just check              # fmt, vet, tests — no Docker, no network
just regtest-reset      # start arkd + emulator on a clean regtest chain (needs Docker)
just test-integration   # the covenant against the live stack
```

Every recipe wraps itself in `nix develop`, so a bare shell with nix is enough. The dev shell pins
Go 1.26.5 — the version `pkg/arkade` requires — plus Node 22 and `just`.

## Layout

| Path | What |
|---|---|
| `contract/` | The contract. Builds the Arkade Script and runs it against the VM |
| `arkade/` | The live-stack client: wallets, funding, submitting to arkd and the emulator |
| `integration/` | The same contract against a live arkd and emulator |
| `service/` | The web service: Go + echo + postgres, and a Vite/React frontend |
| `doc/` | Design notes |

Four Go modules, on purpose. `contract/` has seven direct dependencies and is what the client
verifier is pinned to, so the go-sdk, the emulator client and the service's own dependencies stay
out of it. `arkade/` is where those live instead, and both the service and the integration tests
depend on it — so the tests exercise the code that runs in production rather than a parallel copy
of it. Everything depends on `contract/` through a `replace`.

```sh
just demo         # a clean chain, a clean database, two people with money, three processes
just demo-clean   # and everything it made, gone
```

Then two browser tabs on <http://localhost:5173>, one person in each —
[doc/demo.md](doc/demo.md) has the whole walkthrough, button by button.

## How it settles

```
clampedPrice = max(min(oraclePrice, highLiquidationPrice), lowLiquidationPrice)
shortSats    = min(payoutSats - DUST, max(DUST, nominalUnitsXSatsPerBtc/clampedPrice - satsForNominalUnitsAtHighLiquidation))
longSats     = payoutSats - shortSats
```

Three taproot leaves: settlement (covenant), mutual redemption (both parties), and a CSV emergency
exit. Only the exit touches Bitcoin; the other two resolve offchain. The exit transaction is
pre-signed by both parties at funding and sweeps to a 2-of-3, so either side can leave alone if the
operator stops answering, and neither can leave with the other's collateral —
[doc/unilateral-exit.md](doc/unilateral-exit.md).

The contract needs no clock. Settlement requires the oracle message *and its immediate
predecessor*, which pins it to the first message published after maturity; and liquidation prices
are clamped to the boundary, so every qualifying message settles identically —
[doc/oracle.md](doc/oracle.md).

## Design notes

| Document | What |
|---|---|
| [doc/oracle.md](doc/oracle.md) | Message layout, the sequence, and settling without a clock |
| [doc/contract.md](doc/contract.md) | Parameters, formulas, the taproot tree, actors |
| [doc/unilateral-exit.md](doc/unilateral-exit.md) | The emergency exit and its pre-signed sweep |
| [doc/porting-anyhedge.md](doc/porting-anyhedge.md) | What was kept, what Arkade forces, where we chose to differ |
| [doc/arkade-constraints.md](doc/arkade-constraints.md) | Path rules, batch expiry, client verification |
| [doc/testing.md](doc/testing.md) | How the covenant is tested against the real VM |
| [doc/demo.md](doc/demo.md) | Running the demo: what to start, and what to press |
