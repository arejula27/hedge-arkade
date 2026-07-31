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

# Batch expiry, in blocks. arkd reads any delay below 512 as blocks and any
# above as seconds, and refuses to start if the delays disagree — the rest of
# this stack is block-based, so this has to stay under 512.
export ARKD_VTXO_TREE_EXPIRY="${ARKD_VTXO_TREE_EXPIRY:-400}"

fetch() {
    if [ ! -d "$DIR/.git" ]; then
        git clone --depth 1 --branch "$REF" "$REPO" "$DIR"
        return
    fi
    git -C "$DIR" fetch --depth 1 origin "$REF"
    git -C "$DIR" checkout -q FETCH_HEAD
}

bitcoin_cli() {
    docker exec bitcoin bitcoin-cli -regtest -rpcuser=admin1 -rpcpassword=123 "$@"
}

# Load a wallet that exists on disk but is not loaded.
#
# Upstream's bootstrapChain waits for the image to auto-load a wallet and
# otherwise creates one, but it never loads an existing one. That is fine on a
# clean volume and wedges every restart after `down`, which keeps the data:
# bitcoind loads nothing, `createwallet` fails because the wallet is already
# there, and the start times out on "Bitcoin Core wallet (created)".
load_existing_wallet() {
    [ "$(bitcoin_cli listwallets 2>/dev/null | tr -d '[:space:]')" = "[]" ] || return 1

    local name
    name=$(bitcoin_cli listwalletdir 2>/dev/null \
        | grep -o '"name": *"[^"]*"' | head -1 | sed 's/.*: *"\(.*\)"/\1/')
    [ -n "$name" ] || return 1

    echo "loading existing bitcoind wallet '$name'" >&2
    bitcoin_cli loadwallet "$name" >/dev/null 2>&1
}

case "${1:-}" in
    up)
        fetch
        if ! ( cd "$DIR" && node regtest.mjs start --profile emulator ); then
            # bitcoind is running by the time the wallet step fails, so the
            # wallet can be loaded now and the start resumed.
            load_existing_wallet || exit 1
            ( cd "$DIR" && node regtest.mjs start --profile emulator )
        fi
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
    # minetx <rawtxhex> — mines a transaction directly into a block, skipping
    # mempool policy.
    #
    # Unrolling broadcasts zero-fee v3 transactions that carry a P2A anchor and
    # are meant to be paid for by a CPFP child. Building that child is SDK
    # plumbing we do not own, and it is not what the exit tests are about, so
    # they put the transaction straight in a block instead. Consensus rules
    # still apply — generateblock validates what it includes.
    minetx)
        addr=$(bitcoin_cli getnewaddress)
        bitcoin_cli generateblock "$addr" "[\"$2\"]"
        ;;
    # testaccept <rawtxhex> — asks bitcoind why it would refuse a transaction.
    # Broadcasting only reports RPC error -26, so a rejection test that stopped
    # there could not tell the covenant's own refusal from a typo in the setup.
    testaccept)
        docker exec bitcoin bitcoin-cli -regtest -rpcuser=admin1 -rpcpassword=123 \
            testmempoolaccept "[\"$2\"]"
        ;;
    *)
        echo "usage: $0 {up|down|clean|logs|faucet <addr> <btc>|mine [n]|minetx <hex>|testaccept <hex>}" >&2
        exit 64
        ;;
esac
