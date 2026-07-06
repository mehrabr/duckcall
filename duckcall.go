// Package duckcall is a pure-Go, read-only client for DuckDB's Quack
// protocol: no CGO, no embedded engine, just the wire. This package is the
// native API — connections, streaming results, decoded chunks. Callers who
// want database/sql instead should import the driver subpackage and never
// touch this one.
//
// The layering underneath is wire (transport, opaque payloads) and codec
// (bytes to typed values, selected by the negotiated protocol version);
// duckcall glues them together. If the data is local, embed DuckDB via the
// official bindings instead — this client exists for the remote case.
package duckcall

import (
	"context"
	"fmt"
	"iter"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/wire"
)

// Config mirrors wire.Config; see that type for field semantics.
type Config = wire.Config

// Conn is an authenticated session with a negotiated codec.
type Conn struct {
	w  *wire.Client
	cd *codec.Codec
}

// Dial connects, authenticates, and picks a decoder for the version pair
// the server negotiated. It fails outright if this build has no codec for
// that pair — better to refuse at dial time than to garble rows later.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	w, err := wire.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	hs := w.Handshake()
	cd, err := codec.For(hs.ProtocolVersion, hs.SerializationVersion)
	if err != nil {
		w.Close(ctx)
		return nil, err
	}
	return &Conn{w: w, cd: cd}, nil
}

func (c *Conn) ServerVersion() string { return c.w.Handshake().ServerVersion }

func (c *Conn) Close(ctx context.Context) error { return c.w.Close(ctx) }

// Result is a streaming query result. Rows live on the server until fetched;
// Close releases them without reading the rest.
type Result struct {
	conn   *Conn
	res    *wire.Result
	schema *codec.Schema
}

// Query submits SQL and decodes the result schema. No data chunks have been
// fetched yet when it returns.
func (c *Conn) Query(ctx context.Context, sql string) (*Result, error) {
	res, err := c.w.Execute(ctx, sql)
	if err != nil {
		return nil, err
	}
	schema, err := c.cd.DecodeSchema(res.Schema)
	if err != nil {
		c.w.CloseQuery(ctx, res.QueryID)
		return nil, fmt.Errorf("duckcall: decoding result schema: %w", err)
	}
	return &Result{conn: c, res: res, schema: schema}, nil
}

func (r *Result) Schema() *codec.Schema { return r.schema }

// Chunks streams decoded chunks in row order, fetching in parallel under the
// hood. Iteration stops at the first transport or decode error, delivered as
// the second range value.
func (r *Result) Chunks(ctx context.Context) iter.Seq2[*codec.Chunk, error] {
	return func(yield func(*codec.Chunk, error) bool) {
		for payload, err := range r.conn.w.StreamChunks(ctx, r.res) {
			if err != nil {
				yield(nil, err)
				return
			}
			ch, err := r.conn.cd.DecodeChunk(payload)
			if !yield(ch, err) || err != nil {
				return
			}
		}
	}
}

// Close releases the server-side result. Safe to call after a completed or
// abandoned stream.
func (r *Result) Close(ctx context.Context) error {
	return r.conn.w.CloseQuery(ctx, r.res.QueryID)
}
