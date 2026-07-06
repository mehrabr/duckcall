# duckcall

Quack turned DuckDB into a client-server database over HTTP, and so far every
Quack client is itself a DuckDB instance. duckcall speaks the wire without
being one: a pure-Go, read-only client, no CGO, no embedded engine. A duck
call is the device a non-duck uses to produce a quack, which is exactly what
this is — and a query through it is, literally, a remote procedure call to a
duck.

```go
import (
    "database/sql"
    _ "github.com/mehrabr/duckcall/driver"
)

db, err := sql.Open("duckcall", "quack://analytics.internal:8888?token=env:QUACK_TOKEN")
rows, err := db.QueryContext(ctx, "SELECT product, sum(total) FROM sales GROUP BY 1")
```

Callers who want chunks instead of rows use the native API:

```go
conn, err := duckcall.Dial(ctx, duckcall.Config{
    Endpoint: "http://analytics.internal:8888",
    Token:    os.Getenv("QUACK_TOKEN"),
})
res, err := conn.Query(ctx, "FROM sales")
for chunk, err := range res.Chunks(ctx) {
    // decoded vectors, validity included, fetched in parallel under the hood
}
```

If your data is local, stop reading and embed DuckDB through the official
bindings; duckcall exists for the remote case — services, cron jobs, and
dashboards querying one central `quack_serve` without CGO in their builds.

## Layout

```
wire/     transport: connect/auth, execute, chunk fetch, retry, parallel
          scheduling. Result payloads are opaque bytes here.
codec/    DuckDB result deserialization: type trees, chunks, validity.
          No HTTP anywhere in it; selected by negotiated protocol version.
driver/   database/sql on top of the two.
cmd/quackbouncer   pooling proxy built on wire/ alone: TLS termination,
          per-client tokens, Prometheus metrics.
```

The boundary that matters: codec never learns about the transport, so
anything that has Quack bytes can use it, and a protocol rewrite lands as a
new registered codec version rather than a fork of the tree.

## Type coverage

Tier 1 ships now: BOOLEAN, the integer family (signed and unsigned),
FLOAT/DOUBLE, DECIMAL at every physical width including INT128, VARCHAR and
BLOB in both the inlined and heap forms of the 16-byte string layout, DATE,
TIME, and all four TIMESTAMP precisions, with NULL/validity everywhere.

Nested and exotic types (LIST, STRUCT, MAP, HUGEINT, UUID, ...) come later.
A column duckcall cannot decode reports `codec.ErrUnsupportedType` for that
column; its neighbors still decode.

## Placeholders

The wire has no bind-parameter message today, so `?` placeholders are
escaped and interpolated client-side by the driver. Treat it as the
convenience it is; the moment the protocol grows real parameters, duckcall
switches and the interpolator gets deleted.

## Protocol status

Quack is beta until its production release alongside DuckDB 2.0. duckcall
tracks the latest stable minor (1.5.x today) and makes no promises to older
betas. Version assumptions sit in one `versions.go` per package, and codecs
register per negotiated version, so 1.5.x and 2.0 decoders will coexist in
one binary during the transition. Expect breaking releases while upstream
breaks; each one gets a changelog entry naming the upstream commit.

Current fixtures are synthetic, built by `codec/codectest` and fuzzed from
day one. A captured golden corpus from a live `quack_serve`, plus a
differential conformance harness against the official client, is the next
milestone.

## quackbouncer

Quack's auth is a single shared token over plain HTTP. quackbouncer is the
deployment answer: it terminates TLS, holds the real server token, hands
each client its own revocable token, pools upstream sessions, and exposes
`/metrics`. It consumes only `wire/` and forwards payloads without decoding
them.

```
QUACKBOUNCER_UPSTREAM_TOKEN=... quackbouncer \
    -listen :8443 -tls-cert cert.pem -tls-key key.pem \
    -upstream http://127.0.0.1:8888 -tokens tokens.conf
```

`tokens.conf` is `name:token`, one per line.

## Development

```
git config core.hooksPath .githooks
go test ./...
```

The hooks run `scripts/deslop.sh`, which rejects commits containing
tool-attribution trailers or canned machine prose; patterns are in
`scripts/deslop-patterns.txt`, escapable inline with `deslop:allow` and a
reason. CI runs the same check over pull-request ranges, plus vet, race
tests, and short fuzz passes over the codec.

MIT licensed.
