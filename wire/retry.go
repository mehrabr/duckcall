package wire

import (
	"context"
	"time"
)

// retry runs fn until it succeeds, reports a non-retryable failure, or the
// attempt budget runs out. Backoff doubles from RetryBaseDelay. Only
// idempotent operations may come through here — in this protocol that is
// chunk fetches, which are keyed GETs.
func (c *Client) retry(ctx context.Context, fn func() (retryable bool, err error)) error {
	delay := c.cfg.RetryBaseDelay
	var lastErr error
	for attempt := 0; ; attempt++ {
		retryable, err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt >= c.cfg.MaxRetries || ctx.Err() != nil {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(delay):
		}
		delay *= 2
	}
}
