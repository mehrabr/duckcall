// Package wire is the transport layer of the Quack protocol: connect and
// auth, query submission, and chunk retrieval, with retry and parallel
// fetch. Result payloads are opaque []byte here — interpreting them is the
// codec's job, and that boundary is what lets a proxy be built on this
// package alone.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
)

// Client is one authenticated session against a quack_serve endpoint. It is
// safe for concurrent use; the protocol is client-driven request/response,
// so concurrency is just multiple HTTP requests carrying the same session.
type Client struct {
	cfg    Config
	hs     Handshake
	closed atomic.Bool
}

// Handshake is what the server reported at connect time. The version pair
// picks the codec; the transport itself only checks the protocol range.
type Handshake struct {
	SessionID            string `json:"session_id"`
	ProtocolVersion      int    `json:"protocol_version"`
	SerializationVersion int    `json:"serialization_version"`
	ServerVersion        string `json:"server_version"`
}

// Result identifies a server-side materialized query result. Chunks are
// fetched by index, which is what makes parallel retrieval possible.
type Result struct {
	QueryID    string
	ChunkCount int
	// Schema is the serialized result header, opaque at this layer.
	Schema []byte
}

// Connect performs the auth handshake and returns a live client.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg}

	ctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"protocol_min": protocolVersionMin,
		"protocol_max": protocolVersionMax,
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, pathConnect, bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := c.checkStatus(resp); err != nil {
		return nil, err
	}
	var hs Handshake
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&hs); err != nil {
		return nil, c.redactErr(fmt.Errorf("wire: bad handshake response: %w", err))
	}
	if hs.SessionID == "" {
		return nil, fmt.Errorf("wire: handshake missing session id")
	}
	if hs.ProtocolVersion < protocolVersionMin || hs.ProtocolVersion > protocolVersionMax {
		return nil, fmt.Errorf("wire: server negotiated protocol %d, this build speaks %d..%d",
			hs.ProtocolVersion, protocolVersionMin, protocolVersionMax)
	}
	c.hs = hs
	return c, nil
}

func (c *Client) Handshake() Handshake { return c.hs }

// queryResponse is the JSON control-plane reply to a query submission; the
// schema payload rides along base64ed, chunks are fetched separately as
// octet-streams.
type queryResponse struct {
	QueryID    string `json:"query_id"`
	ChunkCount int    `json:"chunk_count"`
	Schema     []byte `json:"schema"`
}

// Execute submits SQL and returns the result handle. No rows have moved yet.
func (c *Client) Execute(ctx context.Context, sql string) (*Result, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	body, err := json.Marshal(map[string]string{"sql": sql})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, pathQuery, bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := c.checkStatus(resp); err != nil {
		return nil, err
	}
	var qr queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		return nil, c.redactErr(fmt.Errorf("wire: bad query response: %w", err))
	}
	if qr.QueryID == "" || qr.ChunkCount < 0 {
		return nil, fmt.Errorf("wire: malformed query response (id=%q chunks=%d)", qr.QueryID, qr.ChunkCount)
	}
	return &Result{QueryID: qr.QueryID, ChunkCount: qr.ChunkCount, Schema: qr.Schema}, nil
}

// FetchChunk retrieves one serialized chunk by index. The GET is idempotent,
// so it retries on network errors and retryable statuses.
func (c *Client) FetchChunk(ctx context.Context, queryID string, index int) ([]byte, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	path := pathQuery + "/" + queryID + "/chunk/" + strconv.Itoa(index)
	var payload []byte
	err := c.retry(ctx, func() (retryable bool, err error) {
		resp, err := c.do(ctx, http.MethodGet, path, nil, "")
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		if err := c.checkStatus(resp); err != nil {
			return retryableStatus(resp.StatusCode), err
		}
		payload, err = io.ReadAll(resp.Body)
		return true, err
	})
	return payload, err
}

// CloseQuery releases the server-side result. Callers should do this as soon
// as they are done streaming; results hold memory on the server.
func (c *Client) CloseQuery(ctx context.Context, queryID string) error {
	if c.closed.Load() {
		return ErrClosed
	}
	resp, err := c.do(ctx, http.MethodDelete, pathQuery+"/"+queryID, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.checkStatus(resp)
}

// Close ends the session. The client is unusable afterwards.
func (c *Client) Close(ctx context.Context) error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	resp, err := c.do(ctx, http.MethodDelete, pathSession, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.checkStatus(resp)
}

// Do sends a raw request on this session: method, path relative to the
// endpoint, optional body. This is the pass-through hook a proxy needs —
// quackbouncer forwards payloads through here without interpreting them.
// The caller owns the response body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	return c.do(ctx, method, path, body, contentType)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.Endpoint+path, body)
	if err != nil {
		return nil, c.redactErr(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.hs.SessionID != "" {
		req.Header.Set(sessionHeader, c.hs.SessionID)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, c.redactErr(err)
	}
	return resp, nil
}

// checkStatus drains an error response into a *ServerError. The body is
// size-capped and token-redacted before it can reach a log line.
func (c *Client) checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg := ""
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var je struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &je) == nil && je.Error != "" {
		msg = je.Error
	} else if len(b) > 0 {
		msg = string(b)
	}
	return &ServerError{Status: resp.StatusCode, Message: redactToken(c.cfg.Token, msg)}
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code != http.StatusNotImplemented)
}
