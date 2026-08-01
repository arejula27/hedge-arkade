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
cmd/api            composition root — the only place that wires the layers together
internal/server    transport: echo, routes, handlers. The framework stops here
internal/postgres  storage adapter: the connection pool and the health probe
internal/config    the environment, read once at startup into typed values
frontend           Vite + React + TypeScript, Tailwind v4
```

The domain package joins them when the first use case arrives, along with the `replace` onto
`covenant`. There is no empty interface waiting for it: a port with no methods documents nothing.

`GET /` is the demo endpoint the generated frontend's "Fetch from Server" button calls. It goes
when there is something real to serve.

## Tests

| | `just test-service` | `just test-service-integration` |
|---|---|---|
| Needs | nothing | Docker |
| Covers | config, routes, handlers | the driver and the connection string against a real postgres |

The second tier is behind the `integration` build tag and starts its own throwaway postgres with
testcontainers, so it needs no stack to be up first.
