# Arkade Hedge — run `just` for the list.
#
# Every recipe wraps itself in `nix develop`, so they work from a bare shell with
# only nix installed. Inside the dev shell the wrapper is a cheap no-op.

# Run from the repo root so nix finds the flake without searching upwards.
go := "nix develop --command bash -c"

_default:
    @just --list

# Format, vet and test. No Docker, no network.
check: fmt-check vet test

# Everything, including the live stack. Needs Docker.
check-all: check regtest-up test-integration

# Covenant tests against the real Arkade VM.
test:
    @{{go}} 'cd contract && go test ./...'

# The same, with every case named.
test-verbose:
    @{{go}} 'cd contract && go test ./... -v'

# Run one test or subtest by regex, e.g. `just test-one Sequence`.
test-one pattern:
    @{{go}} 'cd contract && go test ./... -run "{{pattern}}" -v'

# Tests with the race detector and no result cache.
test-race:
    @{{go}} 'cd contract && go test ./... -race -count=1'

# Coverage, per function.
test-cover:
    @{{go}} 'cd contract && go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out | tail -20'

# Rewrite files that are not gofmt'd.
fmt:
    @{{go}} 'gofmt -w contract integration'

# Fail if anything is not gofmt'd.
fmt-check:
    @{{go}} 'test -z "$(gofmt -l contract integration)" || { echo "not gofmt'"'"'d:"; gofmt -l contract integration; exit 1; }'

vet:
    @{{go}} 'cd contract && go vet ./...'
    @{{go}} 'cd integration && go vet -tags integration ./...'

tidy:
    @{{go}} 'cd contract && go mod tidy'
    @{{go}} 'cd integration && go mod tidy'

# --- Integration -------------------------------------------------------------
#
# These run against a live arkd + emulator on regtest, so they need Docker.
# `just check` never touches them.

# Start the regtest stack (bitcoind, arkd, arkd-wallet, emulator).
regtest-up:
    @./scripts/regtest.sh up

# Stop it, keeping the data.
regtest-down:
    @./scripts/regtest.sh down

# Stop it and delete everything, including the clone.
regtest-clean:
    @./scripts/regtest.sh clean

regtest-logs *args:
    @./scripts/regtest.sh logs {{args}}

# Run the covenant against the live stack. Fails if it is not up.
test-integration:
    @{{go}} 'cd integration && go test -tags integration -count=1 -v ./...'

# Print the settlement script hex the TypeScript verifier must match.
script-hex:
    @{{go}} 'cd contract && go test ./... -run TestSettlementScriptIsStable -v'

# Drop into the dev shell.
shell:
    nix develop
