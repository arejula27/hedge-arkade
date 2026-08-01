# service

The web service. Generated with [go-blueprint](https://github.com/melkeydev/go-blueprint)
(`--framework echo --driver postgres --feature react,tailwind,docker`) and then rearranged.

```sh
just migrate  # postgres, and the schema
just oracle   # the oracle on :8081
just dev      # the API on :8080 and the Vite dev server on :5173
just run      # the API alone
```

Migrating is its own step because two services share one database. If both
migrated at startup they would race, one creating tables while the other read a
schema that was half there — which is exactly what happened the first time they
were started together.

`just env` writes `.env` from `.env.example` on first use; the real `.env` is gitignored.

## Layers

Dependencies point inwards. Nothing below `internal/server` imports echo, and nothing above
`internal/postgres` knows SQL exists.

```
cmd/api                composition root — the only place that wires the layers together
cmd/oracle             the oracle's composition root
internal/server        transport: echo, routes, handlers. The framework stops here
internal/oracleserver  the oracle's transport. echo lives in these two and nowhere else
internal/app           the use cases, and the ports they reach the world through
internal/domain        what a contract is and what may follow what. No I/O
internal/oracle        the publisher and its storage port. No HTTP, no SQL
internal/postgres      storage adapter: the pool, the repositories, the migrations
internal/arkadeadapter the live stack, behind the use cases' port
internal/oracleclient  the oracle, over HTTP
internal/signer        signing on a party's behalf — the demo's whole custody story
internal/wallets       one Arkade wallet per demo user
internal/events        contract transitions, fanned out to whoever is watching
internal/config        the environment, read once at startup into typed values
frontend               Vite + React + TypeScript, Tailwind v4
```

## Where the demo ends and the service begins

`internal/signer` and `internal/wallets` are the only packages that ever see a party's private key,
and they are the only ones with no place in the service that ships. The coordinator holds the
oracle's key and its own third of the 2-of-3, and never a party's.

Nothing above them changes when the wallets move to the user's own device. Every signature already
goes through `app.Signer`, whose methods take a user and return bytes — no private key crosses that
line in either direction. It is also why the service never calls `contract.PreSignExit`, which
wants both parties' keys in one call: the exit is composed from one deterministic `ExitTx` and two
independent `SignExit` calls that may arrive minutes apart.

## Long steps

Funding and settling take tens of seconds against a live stack — a faucet payment to confirm, a
batch to close, an emulator to run a script — which is longer than a request should hold and longer
than a process is guaranteed to live. So a request moves the contract into a transient state and
stops, and `app.Worker` carries it the rest of the way, from the row alone, restart or no restart.

Every step is safe to repeat, and anything irreversible is written before the next thing that might
fail: the funding outpoint is persisted the moment the operator has the transaction, because a
retry that started from a row which never heard about it would spend both parties' collateral
twice.

## The oracle

A separate binary on `:8081`, because it is a separate thing: it knows about no contract, holds no
funds, and could be run by someone else entirely. It shares only the database.

```sh
just oracle   # publishes every ORACLE_INTERVAL_SECONDS
curl localhost:8081/oracle/pair
curl -X POST localhost:8081/oracle/price -d '{"price":5000000}' -H 'Content-Type: application/json'
```

`/oracle/pair` serves a message *and its immediate predecessor* rather than letting a caller
assemble the two, because the oracle is the only thing that can promise they are adjacent — and
that adjacency is what pins settlement to the first message published after maturity.

The sequence has to be dense, so it is allocated by the application under an advisory lock rather
than by a `BIGSERIAL`. A Postgres sequence is monotonic but not gapless: a rolled-back transaction
burns its number, and a burnt number can never be published, which makes every settlement that
would have needed it as a predecessor impossible. `TestAppendIsDenseUnderConcurrency` is the test
that a `BIGSERIAL` fails.

## Tests

| | `just test-service` | `just test-service-integration` |
|---|---|---|
| Needs | nothing | Docker |
| Covers | domain, use cases, every endpoint, the event stream | the repositories and the oracle's storage against a real postgres |

The second tier is behind the `integration` build tag and starts its own throwaway postgres with
testcontainers, so it needs no stack to be up first.

The stubs behind the use cases live in `internal/apptest` rather than in one suite's test files,
because two suites need them: the use cases' own, and the HTTP layer's, which drives requests
through a real `App` so the status codes are the ones a real outcome produces. They hold real keys
and sign for real, so an exit signature is verified in a test the way it will be in production.
