package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/mehrabr/duckcall/wire"
)

// proxy terminates TLS and client auth in front of a plain-HTTP quack_serve,
// multiplexing client connections onto pooled upstream connections. Every
// message's routing lives in its envelope header, so the proxy decodes
// headers, swaps connection ids, and forwards bodies untouched — nothing
// here decodes a result, so nothing here breaks when the chunk
// serialization changes.
type proxy struct {
	tokens map[string]string // client token -> client name, for metrics
	pool   *pool
	m      *metrics

	mu    sync.Mutex
	conns map[string]*wire.Client // virtual connection id -> upstream
}

func newProxy(tokens map[string]string, p *pool, m *metrics) *proxy {
	px := &proxy{tokens: tokens, pool: p, m: m, conns: map[string]*wire.Client{}}
	m.gauge("quackbouncer_active_connections", func() float64 {
		px.mu.Lock()
		defer px.mu.Unlock()
		return float64(len(px.conns))
	})
	m.gauge("quackbouncer_pool_idle", func() float64 { return float64(p.idleCount()) })
	return px
}

func (p *proxy) count(route string, code int) {
	p.m.inc("quackbouncer_requests_total",
		fmt.Sprintf(`{route=%q,code=%q}`, route, strconv.Itoa(code)))
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/metrics":
		p.m.ServeHTTP(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/quack":
		p.handleRPC(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/":
		p.count("banner", http.StatusOK)
		fmt.Fprintln(w, "quackbouncer: a pooling proxy for DuckDB Quack endpoints.")
	default:
		p.count("other", http.StatusNotFound)
		http.Error(w, "no such endpoint", http.StatusNotFound)
	}
}

// reply writes a message the bouncer authored itself. Protocol failures go
// in-band like the real server's: ERROR_RESPONSE inside HTTP 200.
func (p *proxy) reply(w http.ResponseWriter, hdr wire.Header, body []byte) {
	if hdr.QueryID == 0 {
		hdr.QueryID = wire.QueryIDAbsent
	}
	w.Header().Set("Content-Type", "application/vnd.duckdb")
	w.Write(wire.EncodeEnvelope(hdr, body))
}

func (p *proxy) errorReply(w http.ResponseWriter, route string, msg string) {
	p.count(route, http.StatusOK)
	p.reply(w, wire.Header{Type: wire.MsgError}, wire.ErrorMessage{Message: msg}.Encode())
}

func (p *proxy) handleRPC(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		p.count("read", http.StatusBadRequest)
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	hdr, body, err := wire.SplitEnvelope(raw)
	if err != nil {
		p.errorReply(w, "garbled", "quackbouncer: "+err.Error())
		return
	}
	switch hdr.Type {
	case wire.MsgConnectionRequest:
		p.handleConnect(w, r, hdr, body)
	case wire.MsgDisconnect:
		p.handleDisconnect(w, hdr)
	default:
		p.forward(w, r, hdr, body)
	}
}

func (p *proxy) handleConnect(w http.ResponseWriter, r *http.Request, hdr wire.Header, body []byte) {
	req, err := wire.DecodeConnectionRequest(body)
	if err != nil {
		p.errorReply(w, "connect", "quackbouncer: "+err.Error())
		return
	}
	if _, ok := p.tokens[req.AuthString]; !ok {
		// Do not echo the credential; that mistake is upstream's to make.
		p.errorReply(w, "connect", "Authentication failed")
		return
	}
	up, reused, err := p.pool.get(r.Context())
	if err != nil {
		p.errorReply(w, "connect", "quackbouncer: upstream connect failed: "+err.Error())
		return
	}
	if !reused {
		p.m.inc("quackbouncer_upstream_connects_total", "")
	}

	vid := rand.Text()
	p.mu.Lock()
	p.conns[vid] = up
	p.mu.Unlock()

	server := up.Server()
	p.count("connect", http.StatusOK)
	p.reply(w,
		wire.Header{Type: wire.MsgConnectionResponse, ConnectionID: vid, QueryID: hdr.QueryID},
		wire.ConnectionResponse{
			ServerVersion: server.ServerVersion + " via quackbouncer",
			Platform:      server.Platform,
			QuackVersion:  server.QuackVersion,
		}.Encode(),
	)
}

func (p *proxy) handleDisconnect(w http.ResponseWriter, hdr wire.Header) {
	p.mu.Lock()
	up, ok := p.conns[hdr.ConnectionID]
	delete(p.conns, hdr.ConnectionID)
	p.mu.Unlock()
	if !ok {
		p.errorReply(w, "disconnect", "Connection does not exist / already disconnected")
		return
	}
	// The client's connection ends; the upstream one goes back to the pool.
	// Fetchable results die with the virtual connection either way: their
	// uuids were minted on this upstream and the client just forgot them.
	p.pool.put(up)
	p.count("disconnect", http.StatusOK)
	p.reply(w, wire.Header{Type: wire.MsgSuccess, QueryID: hdr.QueryID}, wire.EncodeEmptyBody())
}

// forward relays any other message over the mapped upstream connection,
// rewriting the connection id outbound and back on the return path. Bodies
// cross unread in both directions.
func (p *proxy) forward(w http.ResponseWriter, r *http.Request, hdr wire.Header, body []byte) {
	p.mu.Lock()
	up, ok := p.conns[hdr.ConnectionID]
	p.mu.Unlock()
	if !ok {
		p.errorReply(w, "forward", "Connection does not exist / already disconnected")
		return
	}
	outHdr := hdr
	outHdr.ConnectionID = up.ConnectionID()
	respHdr, respBody, err := up.RoundTripRaw(r.Context(), wire.EncodeEnvelope(outHdr, body))
	if err != nil {
		p.errorReply(w, "forward", "quackbouncer: upstream request failed: "+err.Error())
		return
	}
	if respHdr.ConnectionID == up.ConnectionID() {
		respHdr.ConnectionID = hdr.ConnectionID
	}
	p.count("forward", http.StatusOK)
	p.reply(w, respHdr, respBody)
}
