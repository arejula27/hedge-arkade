# Arkade Hedge — run `just` for the list.
#
# Every recipe wraps itself in `nix develop`, so they work from a bare shell with
# only nix installed. Inside the dev shell the wrapper is a cheap no-op.

# Run from the repo root so nix finds the flake without searching upwards.
go := "nix develop --command bash -c"

_default:
    @just --list

# Format, vet and test. No Docker, no network.
check: fmt-check vet test test-arkade test-service

# Everything, including the live stack. Needs Docker, and starts it clean.
check-all: check regtest-reset test-integration test-service-integration

# Covenant tests against the real Arkade VM.
test:
    @{{go}} 'cd contract && go test ./...'

# The Arkade client, without a stack. Covers what is decidable offline: how a
# delay is read, where the emulator packet lands, how signers are walked round.
test-arkade:
    @{{go}} 'cd arkade && go test ./...'

# The service, without Docker.
test-service:
    @{{go}} 'cd service && go test ./...'

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
    @{{go}} 'gofmt -w contract arkade integration service'

# Fail if anything is not gofmt'd.
fmt-check:
    @{{go}} 'test -z "$(gofmt -l contract arkade integration service)" || { echo "not gofmt'"'"'d:"; gofmt -l contract arkade integration service; exit 1; }'

vet:
    @{{go}} 'cd contract && go vet ./...'
    @{{go}} 'cd arkade && go vet ./...'
    @{{go}} 'cd integration && go vet -tags integration ./...'
    @{{go}} 'cd service && go vet ./... && go vet -tags integration ./...'

tidy:
    @{{go}} 'cd contract && go mod tidy'
    @{{go}} 'cd arkade && go mod tidy'
    @{{go}} 'cd integration && go mod tidy'
    @{{go}} 'cd service && go mod tidy'

# --- Integration -------------------------------------------------------------
#
# These run against a live arkd + emulator on regtest, so they need Docker.
# `just check` never touches them.

# Start the regtest stack (bitcoind, arkd, arkd-wallet, emulator), keeping
# whatever chain is already there.
regtest-up:
    @./scripts/regtest.sh up

# Start it on an empty chain, keeping the clone. This is what a test run wants:
# a stack left over from a previous run has its height wherever a timelock test
# put it, and bitcoind comes back with no wallet loaded.
regtest-reset:
    @./scripts/regtest.sh reset

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

# The service against a real postgres, in a throwaway container.
test-service-integration:
    @{{go}} 'cd service && go test -tags integration -count=1 -v ./...'

# --- Service -----------------------------------------------------------------
#
# The web service and its frontend. The database here is the service's own and
# has nothing to do with the regtest stack.

# Create the .env files from their examples if they are not there yet.
env:
    @test -f service/.env || { cp service/.env.example service/.env; echo "wrote service/.env"; }
    @test -f service/frontend/.env || { cp service/frontend/.env.example service/frontend/.env; echo "wrote service/frontend/.env"; }

# Start postgres and wait for it to answer.
db-up: env
    @{{go}} 'docker compose --env-file service/.env -f service/docker-compose.yml up -d --wait db'

# Stop it, keeping the data.
db-down:
    @{{go}} 'docker compose --env-file service/.env -f service/docker-compose.yml down'

# Stop it and delete the volume.
db-clean:
    @{{go}} 'docker compose --env-file service/.env -f service/docker-compose.yml down -v'

# Install the frontend's dependencies.
web-install: env
    @{{go}} 'npm --prefix service/frontend install --prefer-offline --no-fund'

web-build: web-install
    @{{go}} 'npm --prefix service/frontend run build'

web-lint: web-install
    @{{go}} 'npm --prefix service/frontend run lint'

# The API alone, on $PORT.
run: env
    @{{go}} 'cd service && go run ./cmd/api'

# The API and the Vite dev server together. Ctrl-C stops both.
dev: db-up web-install
    @{{go}} 'trap "kill 0" EXIT INT TERM; \
             (cd service && go run ./cmd/api) & \
             npm --prefix service/frontend run dev'

# Print the settlement script hex the TypeScript verifier must match.
script-hex:
    @{{go}} 'cd contract && go test ./... -run TestSettlementScriptIsStable -v'

# Drop into the dev shell.
shell:
    nix develop
