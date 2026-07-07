// Package quacktest is an in-process fake quack_serve speaking just enough
// of the real protocol for duckcall's own tests: token auth, connections,
// canned query results served inline and over the fetch loop. It is not a
// server implementation and never will be — the captured corpus in
// testdata and the differential harness against real quack_serve are the
// conformance story; this fake exists for fast, hermetic unit tests.
package quacktest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"

	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/internal/qser"
	"github.com/mehrabr/duckcall/wire"
)

type result struct {
	cols    []codectest.Col
	batches [][][]codectest.Col // fetchable batches, each a set of chunks
}

type liveResult struct {
	res  *result
	next int // next batch index to hand out
}

// Server is the fake. Configure behavior fields before the first request.
type Server struct {
	HTTP *httptest.Server

	// Token is the only accepted auth string. The failure echoes the
	// presented credential, which is exactly the server behavior the
	// client's redaction exists for.
	Token string

	// QuackVersion lets tests simulate a server this build cannot speak.
	QuackVersion uint64

	// BatchChunks is how many chunks ride per batch: the inline budget of a
	// prepare response and the size of each fetch response. Defaults to 2.
	BatchChunks int

	// ConnectFails makes this many connection attempts fail with HTTP 503
	// before succeeding, to exercise the connect retry.
	ConnectFails int

	// FetchFails makes this many fetch requests fail with HTTP 503. Fetches
	// must not retry, so one is enough to kill a stream.
	FetchFails int

	// FetchDelay is a per-fetch sleep, for cancellation tests.
	FetchDelay time.Duration

	mu         sync.Mutex
	results    map[string]*result // by SQL text
	conns      map[string]bool
	live       map[qser.Hugeint]*liveResult
	nextID     int
	fetchCount int
	activeConn int
}

// New starts a fake server with one valid token.
func New(token string) *Server {
	s := &Server{
		Token:        token,
		QuackVersion: 1,
		BatchChunks:  2,
		results:      map[string]*result{},
		conns:        map[string]bool{},
		live:         map[qser.Hugeint]*liveResult{},
	}
	s.HTTP = httptest.NewServer(s)
	return s
}

func (s *Server) Close()      { s.HTTP.Close() }
func (s *Server) URL() string { return s.HTTP.URL }

// AddResult registers the reply for an exact SQL string, split into chunks
// of chunkRows and batches of BatchChunks. The first batch rides inline in
// the prepare response; the rest are fetched.
func (s *Server) AddResult(sql string, cols []codectest.Col, chunkRows int) {
	rows := 0
	if len(cols) > 0 {
		rows = len(cols[0].Vals)
	}
	var chunks [][]codectest.Col
	for at := 0; at < rows; at += chunkRows {
		end := min(at+chunkRows, rows)
		part := make([]codectest.Col, len(cols))
		for i, c := range cols {
			part[i] = codectest.Col{Name: c.Name, Type: c.Type, Vals: c.Vals[at:end]}
		}
		chunks = append(chunks, part)
	}
	res := &result{cols: cols}
	per := s.BatchChunks
	for at := 0; at < len(chunks); at += per {
		res.batches = append(res.batches, chunks[at:min(at+per, len(chunks))])
	}
	s.mu.Lock()
	s.results[sql] = res
	s.mu.Unlock()
}

// FetchCount reports how many fetch requests the server has seen, including
// the ones it failed on purpose.
func (s *Server) FetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetchCount
}

// OpenConnections reports connections opened and not yet disconnected.
func (s *Server) OpenConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeConn
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/quack" {
		http.Error(w, "This is a DuckDB Quack RPC endpoint.", http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	hdr, body, err := wire.SplitEnvelope(raw)
	if err != nil {
		s.reply(w, wire.Header{Type: wire.MsgError}, wire.ErrorMessage{Message: err.Error()}.Encode())
		return
	}
	switch hdr.Type {
	case wire.MsgConnectionRequest:
		s.handleConnect(w, body)
	case wire.MsgPrepareRequest:
		s.withConn(w, hdr, func() { s.handlePrepare(w, hdr, body) })
	case wire.MsgFetchRequest:
		s.withConn(w, hdr, func() { s.handleFetch(w, body) })
	case wire.MsgDisconnect:
		s.handleDisconnect(w, hdr)
	default:
		s.errorReply(w, "unhandled message type "+hdr.Type.String())
	}
}

func (s *Server) reply(w http.ResponseWriter, hdr wire.Header, body []byte) {
	if hdr.QueryID == 0 {
		hdr.QueryID = wire.QueryIDAbsent
	}
	w.Header().Set("Content-Type", "application/vnd.duckdb")
	w.Write(wire.EncodeEnvelope(hdr, body))
}

func (s *Server) errorReply(w http.ResponseWriter, msg string) {
	s.reply(w, wire.Header{Type: wire.MsgError}, wire.ErrorMessage{Message: msg}.Encode())
}

func (s *Server) withConn(w http.ResponseWriter, hdr wire.Header, h func()) {
	s.mu.Lock()
	ok := s.conns[hdr.ConnectionID]
	s.mu.Unlock()
	if !ok {
		// The real server's text for an unknown connection id on any normal
		// message; DISCONNECT alone answers with the longer variant below.
		s.errorReply(w, "Invalid connection id")
		return
	}
	h()
}

// ExpireConnections forgets every live connection, as a server restart
// would; subsequent requests on old ids get the real server's error text.
func (s *Server) ExpireConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.conns)
	s.activeConn = 0
}

func (s *Server) handleConnect(w http.ResponseWriter, body []byte) {
	req, err := wire.DecodeConnectionRequest(body)
	if err != nil {
		s.errorReply(w, err.Error())
		return
	}
	s.mu.Lock()
	if s.ConnectFails > 0 {
		s.ConnectFails--
		s.mu.Unlock()
		http.Error(w, "transient", http.StatusServiceUnavailable)
		return
	}
	s.mu.Unlock()
	if req.MinVersion > 1 {
		s.errorReply(w, "Unsupported Quack version - server only supports version 1 of quack")
		return
	}
	if req.AuthString != s.Token {
		// Echo the credential like a badly-behaved server would.
		s.errorReply(w, "Authentication failed for token "+req.AuthString)
		return
	}
	s.mu.Lock()
	s.nextID++
	id := "TESTCONN" + strconv.Itoa(s.nextID)
	s.conns[id] = true
	s.activeConn++
	s.mu.Unlock()
	s.reply(w,
		wire.Header{Type: wire.MsgConnectionResponse, ConnectionID: id},
		wire.ConnectionResponse{
			ServerVersion: "v1.5.4-quacktest",
			Platform:      "test",
			QuackVersion:  s.QuackVersion,
		}.Encode(),
	)
}

func (s *Server) handleDisconnect(w http.ResponseWriter, hdr wire.Header) {
	s.mu.Lock()
	ok := s.conns[hdr.ConnectionID]
	if ok {
		delete(s.conns, hdr.ConnectionID)
		s.activeConn--
	}
	s.mu.Unlock()
	if !ok {
		s.errorReply(w, "Connection does not exist / already disconnected")
		return
	}
	s.reply(w, wire.Header{Type: wire.MsgSuccess}, wire.EncodeEmptyBody())
}

func (s *Server) handlePrepare(w http.ResponseWriter, hdr wire.Header, body []byte) {
	req, err := wire.DecodePrepareRequest(body)
	if err != nil {
		s.errorReply(w, err.Error())
		return
	}
	s.mu.Lock()
	res, ok := s.results[req.SQL]
	if !ok {
		s.mu.Unlock()
		s.errorReply(w, "Parser Error: no canned result for "+strconv.Quote(req.SQL))
		return
	}
	s.nextID++
	uuid := qser.Hugeint{Upper: 0x7e57, Lower: uint64(s.nextID)}
	var inline [][]codectest.Col
	if len(res.batches) > 0 {
		inline = res.batches[0]
	}
	needsMore := len(res.batches) > 1
	if needsMore {
		s.live[uuid] = &liveResult{res: res, next: 1}
	}
	s.mu.Unlock()
	s.reply(w,
		wire.Header{Type: wire.MsgPrepareResponse, QueryID: hdr.QueryID},
		codectest.EncodePrepareBody(res.cols, inline, needsMore, uuid),
	)
}

func (s *Server) handleFetch(w http.ResponseWriter, body []byte) {
	req, err := wire.DecodeFetchRequest(body)
	if err != nil {
		s.errorReply(w, err.Error())
		return
	}
	if s.FetchDelay > 0 {
		time.Sleep(s.FetchDelay)
	}
	s.mu.Lock()
	s.fetchCount++
	if s.FetchFails > 0 {
		s.FetchFails--
		s.mu.Unlock()
		http.Error(w, "transient", http.StatusServiceUnavailable)
		return
	}
	lr := s.live[req.UUID]
	var batch [][]codectest.Col
	idx := uint64(qser.OptionalIdxAbsent)
	if lr != nil && lr.next < len(lr.res.batches) {
		batch = lr.res.batches[lr.next]
		idx = uint64(lr.next)
		lr.next++
	}
	s.mu.Unlock()
	s.reply(w,
		wire.Header{Type: wire.MsgFetchResponse},
		codectest.EncodeFetchBody(batch, idx),
	)
}
