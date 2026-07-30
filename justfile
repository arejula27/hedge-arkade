# Arkade Hedge — run `just` for the list.
#
# Every recipe wraps itself in `nix develop`, so they work from a bare shell with
# only nix installed. Inside the dev shell the wrapper is a cheap no-op.

# Run from the repo root so nix finds the flake without searching upwards.
go := "nix develop --command bash -c"

_default:
    @just --list

# Format, vet and test.
check: fmt-check vet test

# Covenant tests against the real Arkade VM.
test:
    @{{go}} 'cd covenant && go test ./...'

# The same, with every case named.
test-verbose:
    @{{go}} 'cd covenant && go test ./... -v'

# Run one test or subtest by regex, e.g. `just test-one Sequence`.
test-one pattern:
    @{{go}} 'cd covenant && go test ./... -run "{{pattern}}" -v'

# Tests with the race detector and no result cache.
test-race:
    @{{go}} 'cd covenant && go test ./... -race -count=1'

# Coverage, per function.
test-cover:
    @{{go}} 'cd covenant && go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out | tail -20'

# Rewrite files that are not gofmt'd.
fmt:
    @{{go}} 'cd covenant && gofmt -w .'

# Fail if anything is not gofmt'd.
fmt-check:
    @{{go}} 'cd covenant && test -z "$(gofmt -l .)" || { echo "not gofmt'"'"'d:"; gofmt -l .; exit 1; }'

vet:
    @{{go}} 'cd covenant && go vet ./...'

tidy:
    @{{go}} 'cd covenant && go mod tidy'

# Print the settlement script hex the TypeScript verifier must match.
script-hex:
    @{{go}} 'cd covenant && go test ./... -run TestSettlementScriptIsStable -v'

# Drop into the dev shell.
shell:
    nix develop
