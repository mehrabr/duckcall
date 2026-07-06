package wire

import (
	"errors"
	"net/http"
	"net/url"
	"time"
)

// Config describes one quack_serve endpoint.
type Config struct {
	// Endpoint is the server base URL, e.g. "http://analytics.internal:8888".
	// Quack defaults to plain HTTP; https endpoints are used as given, and
	// the recommended production setup is TLS terminated in front of the
	// server (that is quackbouncer's job).
	Endpoint string

	// Token is the shared auth token. It is sent as a bearer header, kept
	// out of URLs, and redacted from every error this package returns.
	Token string

	// FetchWorkers caps concurrent batch fetches per result. The server
	// hands out batches to competing requests, so workers scale throughput
	// without coordination. 0 means 4.
	FetchWorkers int

	// ConnectTimeout bounds the connect/auth exchange. 0 means 10s.
	ConnectTimeout time.Duration

	// MaxRetries is how many times the connect handshake is retried after a
	// network error or retryable status. Nothing else retries: a fetch is a
	// destructive read and re-running a query is the caller's decision.
	// 0 means 3; negative disables.
	MaxRetries int

	// RetryBaseDelay is the first backoff step, doubling per attempt.
	// 0 means 100ms.
	RetryBaseDelay time.Duration

	// HTTPClient overrides the transport, mainly for tests and custom TLS.
	HTTPClient *http.Client
}

func (c Config) withDefaults() (Config, error) {
	if c.Endpoint == "" {
		return c, errors.New("wire: empty endpoint")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return c, errors.New("wire: unparseable endpoint")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return c, errors.New("wire: endpoint scheme must be http or https")
	}
	if c.FetchWorkers <= 0 {
		c.FetchWorkers = 4
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 3
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = 100 * time.Millisecond
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{}
	}
	return c, nil
}
