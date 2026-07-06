package wire

import (
	"context"
	"iter"
	"sync"
)

// StreamChunks fetches every chunk of a result with FetchWorkers concurrent
// requests and yields payloads strictly in index order. The protocol keys
// chunks by index precisely so a client can do this; delivery order is
// duckcall's promise, not the server's.
//
// Iteration stops at the first error. Breaking out of the range cancels the
// remaining fetches.
func (c *Client) StreamChunks(ctx context.Context, r *Result) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		if r.ChunkCount == 0 {
			return
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		type slot struct {
			payload []byte
			err     error
		}
		slots := make([]chan slot, r.ChunkCount)
		for i := range slots {
			slots[i] = make(chan slot, 1)
		}

		var wg sync.WaitGroup
		next := make(chan int)
		for w := 0; w < min(c.cfg.FetchWorkers, r.ChunkCount); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range next {
					payload, err := c.FetchChunk(ctx, r.QueryID, idx)
					select {
					case slots[idx] <- slot{payload, err}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		go func() {
			defer close(next)
			for i := 0; i < r.ChunkCount; i++ {
				select {
				case next <- i:
				case <-ctx.Done():
					return
				}
			}
		}()
		defer wg.Wait()

		for i := 0; i < r.ChunkCount; i++ {
			select {
			case s := <-slots[i]:
				if s.err != nil {
					yield(nil, s.err)
					return
				}
				if !yield(s.payload, nil) {
					return
				}
			case <-ctx.Done():
				yield(nil, context.Cause(ctx))
				return
			}
		}
	}
}
