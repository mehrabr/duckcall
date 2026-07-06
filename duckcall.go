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
	"sync"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/wire"
)

// Config mirrors wire.Config; see that type for field semantics.
type Config = wire.Config

// Conn is an authenticated connection with a negotiated codec.
type Conn struct {
	w  *wire.Client
	cd *codec.Codec
}

// Dial connects, authenticates, and picks a decoder for the quack version
// the server negotiated. It fails outright if this build has no codec for
// that version — better to refuse at dial time than to garble rows later.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	w, err := wire.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cd, err := codec.For(w.Server().QuackVersion)
	if err != nil {
		w.Close(ctx)
		return nil, err
	}
	return &Conn{w: w, cd: cd}, nil
}

func (c *Conn) ServerVersion() string { return c.w.Server().ServerVersion }

func (c *Conn) Close(ctx context.Context) error { return c.w.Close(ctx) }

// Result is a streaming query result. Small results arrive whole with the
// prepare response; larger ones hold batches on the server until fetched.
// Server-side result state lives and dies with the connection — there is no
// release message in the protocol, so Close only stops the stream.
type Result struct {
	conn    *Conn
	pr      *codec.PrepareResult
	stopped sync.Once
	stop    chan struct{}
}

// Query submits SQL. When it returns, the schema and the server's inline
// chunk budget have arrived; anything past that is fetched during Chunks.
func (c *Conn) Query(ctx context.Context, sql string) (*Result, error) {
	body, err := c.w.Prepare(ctx, sql)
	if err != nil {
		return nil, err
	}
	pr, err := c.cd.DecodePrepare(body)
	if err != nil {
		return nil, fmt.Errorf("duckcall: decoding prepare response: %w", err)
	}
	return &Result{conn: c, pr: pr, stop: make(chan struct{})}, nil
}

func (r *Result) Schema() *codec.Schema { return r.pr.Schema }

// Chunks streams decoded chunks: first whatever the server inlined, then —
// for results that need it — batches fetched by FetchWorkers concurrent
// requests racing on the result uuid. Chunks arrive in fetch-completion
// order, which is not row order across batches. Iteration stops at the
// first transport or decode error, delivered as the second range value.
//
// A fetch is a destructive read: if one fails mid-stream the remaining rows
// are unrecoverable and the query must be re-run. duckcall surfaces that as
// an error, never as a silently short result.
func (r *Result) Chunks(ctx context.Context) iter.Seq2[*codec.Chunk, error] {
	return func(yield func(*codec.Chunk, error) bool) {
		for _, ch := range r.pr.Chunks {
			if !yield(ch, nil) {
				return
			}
		}
		if !r.pr.NeedsMoreFetch {
			return
		}

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		type batch struct {
			chunks []*codec.Chunk
			err    error
		}
		workers := r.conn.w.Config().FetchWorkers
		out := make(chan batch, workers)
		var wg sync.WaitGroup
		var doneOnce sync.Once
		done := make(chan struct{})
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case <-done:
						return
					case <-r.stop:
						return
					default:
					}
					body, err := r.conn.w.Fetch(ctx, r.pr.ResultUUID)
					if err != nil {
						// Dropping the send on a dead context is fine: the
						// post-loop check reports the cancellation instead.
						select {
						case out <- batch{err: err}:
						case <-ctx.Done():
						}
						return
					}
					fr, err := r.conn.cd.DecodeFetch(body)
					if err != nil {
						select {
						case out <- batch{err: fmt.Errorf("duckcall: decoding fetch response: %w", err)}:
						case <-ctx.Done():
						}
						return
					}
					if len(fr.Chunks) == 0 {
						// Drained. Every other worker will hit this too.
						doneOnce.Do(func() { close(done) })
						return
					}
					select {
					case out <- batch{chunks: fr.Chunks}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		go func() {
			wg.Wait()
			close(out)
		}()

		for b := range out {
			if b.err != nil {
				yield(nil, b.err)
				return
			}
			for _, ch := range b.chunks {
				if !yield(ch, nil) {
					return
				}
			}
		}
		// Workers can end without an error batch when the caller's context
		// dies mid-stream; that is still a failed stream, not a short one.
		if ctx.Err() != nil {
			yield(nil, context.Cause(ctx))
		}
	}
}

// Close stops any in-flight fetching. The protocol has no per-result
// release; server-side leftovers are reclaimed when the connection closes.
func (r *Result) Close(ctx context.Context) error {
	r.stopped.Do(func() { close(r.stop) })
	return nil
}
