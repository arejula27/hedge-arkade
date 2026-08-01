# service

The web service. Generated with [go-blueprint](https://github.com/melkeydev/go-blueprint)
(`--framework echo --driver postgres --feature react,tailwind,docker`) and then rearranged.

```sh
just db-up    # postgres, from docker-compose.yml
just dev      # the API on :8080 and the Vite dev server on :5173
just run      # the API alone
```

`just env` writes `.env` from `.env.example` on first use; the real `.env` is gitignored.

## Layers

Dependencies point inwards. Nothing below `internal/server` imports echo, and nothing above
`internal/postgres` knows SQL exists.

```
cmd/api              composition root — the only place that wires the layers together
cmd/oracle           the oracle's composition root
internal/server      transport: echo, routes, handlers. The framework stops here
internal/oracleserver the oracle's transport. echo lives in these two and nowhere else
internal/oracle      the publisher and its storage port. No HTTP, no SQL
internal/postgres    storage adapter: the pool, the health probe, the migrations
internal/config      the environment, read once at startup into typed values
frontend             Vite + React + TypeScript, Tailwind v4
```

The domain package joins them when the first contract use case arrives. There is no empty
interface waiting for it: a port with no methods documents nothing.

`GET /` is the demo endpoint the generated frontend's "Fetch from Server" button calls. It goes
when there is something real to serve.

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
| Covers | config, routes, handlers | the driver and the connection string against a real postgres |

The second tier is behind the `integration` build tag and starts its own throwaway postgres with
testcontainers, so it needs no stack to be up first.
