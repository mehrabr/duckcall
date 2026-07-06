// Package wire is the transport layer of the Quack protocol: the message
// envelope, connect and auth, query submission, and batch fetching. Result
// payloads — PREPARE_RESPONSE and FETCH_RESPONSE bodies — are opaque []byte
// here; interpreting them is the codec's job, and that boundary is what lets
// a proxy be built on this package alone.
//
// Every exchange is one POST to /quack carrying two serialized documents:
// a typed header and a body. Failures arrive in-band as ERROR_RESPONSE
// messages inside HTTP 200; a non-200 status means the request never
// reached a Quack handler.
package wire

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync/atomic"
)

// Client is one authenticated connection against a quack_serve endpoint.
// It is safe for concurrent use; the protocol is client-driven
// request/response, so concurrency is just multiple HTTP requests carrying
// the same connection id.
type Client struct {
	cfg    Config
	connID string
	server ConnectionResponse
	qid    atomic.Uint64
	closed atomic.Bool
}

// Connect performs the auth handshake and returns a live client. This is
// the only operation that retries: the request is idempotent (a lost
// response leaks at most an idle server-side connection), which is not true
// of anything that follows.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg}

	ctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	msg := EncodeEnvelope(
		Header{Type: MsgConnectionRequest, QueryID: QueryIDAbsent},
		ConnectionRequest{
			AuthString:    cfg.Token,
			ClientVersion: clientVersion,
			Platform:      runtime.GOOS + "_" + runtime.GOARCH,
			MinVersion:    quackVersionMin,
			MaxVersion:    quackVersionMax,
		}.Encode(),
	)

	var hdr Header
	var body []byte
	err = c.retry(ctx, func() (bool, error) {
		var status int
		var err error
		hdr, body, status, err = c.roundTrip(ctx, msg)
		return status == 0 || retryableStatus(status), err
	})
	if err != nil {
		return nil, err
	}

	switch hdr.Type {
	case MsgConnectionResponse:
	case MsgError:
		return nil, c.serverError(body)
	default:
		return nil, fmt.Errorf("wire: connect answered with %s", hdr.Type)
	}
	if hdr.ConnectionID == "" {
		return nil, fmt.Errorf("wire: connection response missing connection id")
	}
	resp, err := DecodeConnectionResponse(body)
	if err != nil {
		return nil, err
	}
	if resp.QuackVersion < quackVersionMin || resp.QuackVersion > quackVersionMax {
		return nil, fmt.Errorf("wire: server speaks quack version %d, this build speaks %d..%d",
			resp.QuackVersion, quackVersionMin, quackVersionMax)
	}
	c.connID = hdr.ConnectionID
	c.server = resp
	return c, nil
}

// Server reports what the server announced at connect time.
func (c *Client) Server() ConnectionResponse { return c.server }

// Config returns the resolved configuration, defaults applied.
func (c *Client) Config() Config { return c.cfg }

// ConnectionID exposes the server-assigned id. Treat it as a bearer
// credential: whoever holds it can run queries on this connection.
func (c *Client) ConnectionID() string { return c.connID }

// Prepare submits SQL and returns the raw PREPARE_RESPONSE body for the
// codec: schema, inline chunks, and the fetch uuid if the result is larger
// than the server's inline budget.
func (c *Client) Prepare(ctx context.Context, sql string) ([]byte, error) {
	return c.call(ctx, MsgPrepareRequest, PrepareRequest{SQL: sql}.Encode(), MsgPrepareResponse)
}

// Fetch retrieves the next batch of a result as a raw FETCH_RESPONSE body.
// Fetching is a destructive read — the server pops the batch before we see
// the response — so it is never retried; a transport failure here means the
// query must be re-run.
func (c *Client) Fetch(ctx context.Context, uuid Hugeint) ([]byte, error) {
	return c.call(ctx, MsgFetchRequest, FetchRequest{UUID: uuid}.Encode(), MsgFetchResponse)
}

// Close disconnects. The client is unusable afterwards.
func (c *Client) Close(ctx context.Context) error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	hdr, body, _, err := c.roundTrip(ctx, EncodeEnvelope(
		Header{Type: MsgDisconnect, ConnectionID: c.connID, QueryID: QueryIDAbsent},
		EncodeEmptyBody(),
	))
	if err != nil {
		return err
	}
	switch hdr.Type {
	case MsgSuccess:
		return nil
	case MsgError:
		return c.serverError(body)
	default:
		return fmt.Errorf("wire: disconnect answered with %s", hdr.Type)
	}
}

func (c *Client) call(ctx context.Context, req MessageType, body []byte, want MessageType) ([]byte, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	hdr, respBody, _, err := c.roundTrip(ctx, EncodeEnvelope(
		Header{Type: req, ConnectionID: c.connID, QueryID: c.qid.Add(1)},
		body,
	))
	if err != nil {
		return nil, err
	}
	switch hdr.Type {
	case want:
		return respBody, nil
	case MsgError:
		return nil, c.serverError(respBody)
	default:
		return nil, fmt.Errorf("wire: %s answered with %s", req, hdr.Type)
	}
}

// RoundTripRaw sends an already-framed message and returns the split
// response. This is the pass-through hook a proxy needs — quackbouncer
// rewrites envelopes and forwards bodies through here without interpreting
// them.
func (c *Client) RoundTripRaw(ctx context.Context, msg []byte) (Header, []byte, error) {
	if c.closed.Load() {
		return Header{}, nil, ErrClosed
	}
	hdr, body, _, err := c.roundTrip(ctx, msg)
	return hdr, body, err
}

// roundTrip POSTs one message and splits the response. status is the HTTP
// status when the server answered at all (0 on transport error); callers
// deciding on retry need it because a 503 from a proxy in front of the
// server is retryable where an in-band error is not.
func (c *Client) roundTrip(ctx context.Context, msg []byte) (Header, []byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+pathRPC, bytes.NewReader(msg))
	if err != nil {
		return Header{}, nil, 0, c.redactErr(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return Header{}, nil, 0, c.redactErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Header{}, nil, resp.StatusCode, &ServerError{
			Status:  resp.StatusCode,
			Message: redactToken(c.cfg.Token, string(b)),
		}
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Header{}, nil, resp.StatusCode, c.redactErr(err)
	}
	hdr, body, err := SplitEnvelope(raw)
	return hdr, body, resp.StatusCode, err
}

func (c *Client) serverError(body []byte) error {
	em, err := DecodeErrorMessage(body)
	if err != nil {
		return err
	}
	return &ServerError{Message: redactToken(c.cfg.Token, em.Message)}
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code != http.StatusNotImplemented)
}
