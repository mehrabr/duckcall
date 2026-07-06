package main

import (
	"context"
	"sync"
	"time"

	"github.com/mehrabr/duckcall/wire"
)

// pool holds idle upstream sessions for reuse. Reuse is sound because v0.1
// scope is read-only with no session state — no temp tables, no SET
// persistence — so one upstream session is as good as another. The day the
// protocol grows session state, this pool needs a reset message or a
// shorter life.
type pool struct {
	upstream wire.Config

	mu   sync.Mutex
	idle []*wire.Client
	max  int
}

func newPool(upstream wire.Config, max int) *pool {
	return &pool{upstream: upstream, max: max}
}

// get returns an idle upstream session or dials a new one.
func (p *pool) get(ctx context.Context) (*wire.Client, bool, error) {
	p.mu.Lock()
	if n := len(p.idle); n > 0 {
		c := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return c, true, nil
	}
	p.mu.Unlock()
	c, err := wire.Connect(ctx, p.upstream)
	return c, false, err
}

// put returns a connection to the pool, closing it if the pool is full.
// The goodbye gets its own bounded context: the caller's request context is
// usually done by now.
func (p *pool) put(c *wire.Client) {
	p.mu.Lock()
	if len(p.idle) < p.max {
		p.idle = append(p.idle, c)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Close(ctx)
}

func (p *pool) idleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle)
}
