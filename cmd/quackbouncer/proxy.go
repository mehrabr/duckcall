package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/mehrabr/duckcall/wire"
)

// proxy terminates TLS and client auth in front of a plain-HTTP quack_serve,
// multiplexing client sessions onto pooled upstream sessions. Payloads pass
// through untouched — the wire layer treats them as opaque bytes, and so
// does the proxy. That is the whole trick: nothing here decodes a result,
// so nothing here breaks when the serialization changes.
type proxy struct {
	tokens map[string]string // client token -> client name, for metrics
	pool   *pool
	m      *metrics

	mu       sync.Mutex
	sessions map[string]*wire.Client // bouncer session id -> upstream
}

func newProxy(tokens map[string]string, p *pool, m *metrics) *proxy {
	px := &proxy{tokens: tokens, pool: p, m: m, sessions: map[string]*wire.Client{}}
	m.gauge("quackbouncer_active_sessions", func() float64 {
		px.mu.Lock()
		defer px.mu.Unlock()
		return float64(len(px.sessions))
	})
	m.gauge("quackbouncer_pool_idle", func() float64 { return float64(p.idleCount()) })
	return px
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (p *proxy) count(route string, code int) {
	p.m.inc("quackbouncer_requests_total",
		fmt.Sprintf(`{route=%q,code=%q}`, route, strconv.Itoa(code)))
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/metrics":
		p.m.ServeHTTP(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/quack/v1/connect":
		p.handleConnect(w, r)
	case r.Method == http.MethodDelete && r.URL.Path == "/quack/v1/session":
		p.handleDisconnect(w, r)
	case strings.HasPrefix(r.URL.Path, "/quack/v1/"):
		p.forward(w, r)
	default:
		p.count("other", http.StatusNotFound)
		jsonError(w, http.StatusNotFound, "no such endpoint")
	}
}

func (p *proxy) clientName(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return "", false
	}
	name, ok := p.tokens[token]
	return name, ok
}

func (p *proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.clientName(r); !ok {
		p.count("connect", http.StatusUnauthorized)
		jsonError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	up, reused, err := p.pool.get(r.Context())
	if err != nil {
		p.count("connect", http.StatusBadGateway)
		jsonError(w, http.StatusBadGateway, "upstream connect failed: "+err.Error())
		return
	}
	if !reused {
		p.m.inc("quackbouncer_upstream_connects_total", "")
	}

	sid := rand.Text()
	p.mu.Lock()
	p.sessions[sid] = up
	p.mu.Unlock()

	hs := up.Handshake()
	p.count("connect", http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"session_id":            sid,
		"protocol_version":      hs.ProtocolVersion,
		"serialization_version": hs.SerializationVersion,
		"server_version":        hs.ServerVersion + " via quackbouncer",
	})
}

func (p *proxy) session(r *http.Request) (*wire.Client, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.sessions[r.Header.Get("X-Quack-Session")]
	return c, ok
}

func (p *proxy) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("X-Quack-Session")
	p.mu.Lock()
	up, ok := p.sessions[sid]
	delete(p.sessions, sid)
	p.mu.Unlock()
	if !ok {
		p.count("disconnect", http.StatusForbidden)
		jsonError(w, http.StatusForbidden, "unknown session")
		return
	}
	// The client's session ends; the upstream one goes back to the pool.
	p.pool.put(r.Context(), up)
	p.count("disconnect", http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}

// forward relays any other protocol request over the mapped upstream
// session. The upstream client re-stamps auth and session headers; body
// bytes cross unread in both directions.
func (p *proxy) forward(w http.ResponseWriter, r *http.Request) {
	up, ok := p.session(r)
	if !ok {
		p.count("forward", http.StatusForbidden)
		jsonError(w, http.StatusForbidden, "unknown session")
		return
	}
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	resp, err := up.Do(r.Context(), r.Method, path, r.Body, r.Header.Get("Content-Type"))
	if err != nil {
		p.count("forward", http.StatusBadGateway)
		jsonError(w, http.StatusBadGateway, "upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	p.count("forward", resp.StatusCode)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
