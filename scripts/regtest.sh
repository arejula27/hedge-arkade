#!/usr/bin/env bash
#
# Brings the Arkade regtest stack up or down.
#
# The stack is ArkLabsHQ/arkade-regtest, cloned into .regtest/ (gitignored) so
# the version is visible and pinnable rather than hidden in CI. Only the
# `emulator` profile is started: bitcoind, the indexers, arkd, arkd-wallet and
# the emulator. Boltz, LND, the solver and the web wallet are not needed to
# settle a covenant and are three more ways for CI to fail on something else.
set -euo pipefail

REPO="${REGTEST_REPO:-https://github.com/ArkLabsHQ/arkade-regtest.git}"
REF="${REGTEST_REF:-master}"
DIR="${REGTEST_DIR:-.regtest}"

# Deterministic tests: no background miner moving block height under us.
export AUTOMINE_INTERVAL="${AUTOMINE_INTERVAL:-0}"

fetch() {
    if [ ! -d "$DIR/.git" ]; then
        git clone --depth 1 --branch "$REF" "$REPO" "$DIR"
        return
    fi
    git -C "$DIR" fetch --depth 1 origin "$REF"
    git -C "$DIR" checkout -q FETCH_HEAD
}

case "${1:-}" in
    up)
        fetch
        ( cd "$DIR" && node regtest.mjs start --profile emulator )
        ;;
    down)
        [ -d "$DIR" ] && ( cd "$DIR" && node regtest.mjs stop ) || true
        ;;
    clean)
        [ -d "$DIR" ] && ( cd "$DIR" && node regtest.mjs clean ) || true
        rm -rf "$DIR"
        ;;
    logs)
        ( cd "$DIR" && node regtest.mjs logs "${@:2}" )
        ;;
    # faucet <address> <amountBtc> — pays and confirms, so a caller only has to
    # wait for arkd to notice rather than for a block.
    faucet)
        ( cd "$DIR" && node regtest.mjs faucet "$2" "$3" --confirm )
        ;;
    mine)
        ( cd "$DIR" && node regtest.mjs mine "${2:-1}" )
        ;;
    *)
        echo "usage: $0 {up|down|clean|logs|faucet <addr> <btc>|mine [n]}" >&2
        exit 64
        ;;
esac
