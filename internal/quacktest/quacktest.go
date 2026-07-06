// Package quacktest is an in-process fake quack_serve speaking just enough
// of the protocol for duckcall's own tests: token auth, sessions, canned
// query results served as chunks. It is not a server implementation and
// never will be — once a captured corpus from real quack_serve exists, the
// differential harness supersedes most of what this fake is for.
package quacktest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mehrabr/duckcall/codec/codectest"
)

type result struct {
	schema []byte
	chunks [][]byte
}

// Server is the fake. Configure behavior fields before the first request.
type Server struct {
	HTTP *httptest.Server

	// Token is the only accepted bearer token.
	Token string

	// ProtocolVersion lets tests simulate a server this build cannot speak.
	ProtocolVersion int

	// FailFirst makes each chunk fetch fail with 503 this many times before
	// succeeding, to exercise retry.
	FailFirst int

	// ChunkDelay is a per-chunk-fetch sleep, for cancellation tests.
	ChunkDelay time.Duration

	mu          sync.Mutex
	results     map[string]*result // by SQL text
	sessions    map[string]bool
	queries     map[string]*result
	chunkFails  map[string]int
	nextID      int
	fetchCount  int
	activeSess  int
	activeQuery int
}

// New starts a fake server with one valid token.
func New(token string) *Server {
	s := &Server{
		Token:           token,
		ProtocolVersion: 1,
		results:         map[string]*result{},
		sessions:        map[string]bool{},
		queries:         map[string]*result{},
		chunkFails:      map[string]int{},
	}
	s.HTTP = httptest.NewServer(s)
	return s
}

func (s *Server) Close()      { s.HTTP.Close() }
func (s *Server) URL() string { return s.HTTP.URL }

// AddResult registers the reply for an exact SQL string, split into chunks
// of chunkRows.
func (s *Server) AddResult(sql string, cols []codectest.Col, chunkRows int) {
	rows := 0
	if len(cols) > 0 {
		rows = len(cols[0].Vals)
	}
	res := &result{schema: codectest.EncodeSchema(cols)}
	for at := 0; at < rows; at += chunkRows {
		end := min(at+chunkRows, rows)
		part := make([]codectest.Col, len(cols))
		for i, c := range cols {
			part[i] = codectest.Col{Name: c.Name, Type: c.Type, Vals: c.Vals[at:end]}
		}
		res.chunks = append(res.chunks, codectest.EncodeChunk(part))
	}
	s.mu.Lock()
	s.results[sql] = res
	s.mu.Unlock()
}

// FetchCount reports how many chunk fetches the server has seen, including
// the ones it failed on purpose.
func (s *Server) FetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetchCount
}

// OpenSessions reports sessions connected and not yet closed.
func (s *Server) OpenSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeSess
}

// OpenQueries reports results not yet released.
func (s *Server) OpenQueries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeQuery
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+s.Token {
		// Echoing the presented credential back is exactly the kind of
		// server behavior the client must redact.
		jsonError(w, http.StatusUnauthorized, fmt.Sprintf("invalid token %q", strings.TrimPrefix(auth, "Bearer ")))
		return
	}

	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/quack/v1/connect":
		s.handleConnect(w)
	case r.Method == http.MethodDelete && path == "/quack/v1/session":
		s.withSession(w, r, s.handleSessionClose)
	case r.Method == http.MethodPost && path == "/quack/v1/query":
		s.withSession(w, r, func(w http.ResponseWriter, r *http.Request) { s.handleQuery(w, r) })
	case strings.HasPrefix(path, "/quack/v1/query/"):
		s.withSession(w, r, func(w http.ResponseWriter, r *http.Request) { s.handleQuerySub(w, r) })
	default:
		jsonError(w, http.StatusNotFound, "no such endpoint")
	}
}

func (s *Server) withSession(w http.ResponseWriter, r *http.Request, h http.HandlerFunc) {
	sid := r.Header.Get("X-Quack-Session")
	s.mu.Lock()
	ok := s.sessions[sid]
	s.mu.Unlock()
	if !ok {
		jsonError(w, http.StatusForbidden, "unknown session")
		return
	}
	h(w, r)
}

func (s *Server) handleConnect(w http.ResponseWriter) {
	s.mu.Lock()
	s.nextID++
	sid := "s-" + strconv.Itoa(s.nextID)
	s.sessions[sid] = true
	s.activeSess++
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{
		"session_id":            sid,
		"protocol_version":      s.ProtocolVersion,
		"serialization_version": 1,
		"server_version":        "quack_serve/1.5.3 (quacktest)",
	})
}

func (s *Server) handleSessionClose(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("X-Quack-Session")
	s.mu.Lock()
	delete(s.sessions, sid)
	s.activeSess--
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request body")
		return
	}
	s.mu.Lock()
	res, ok := s.results[body.SQL]
	if !ok {
		s.mu.Unlock()
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("Parser Error: no canned result for %q", body.SQL))
		return
	}
	s.nextID++
	qid := "q-" + strconv.Itoa(s.nextID)
	s.queries[qid] = res
	s.activeQuery++
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{
		"query_id":    qid,
		"chunk_count": len(res.chunks),
		"schema":      res.schema,
	})
}

func (s *Server) handleQuerySub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/quack/v1/query/")
	parts := strings.Split(rest, "/")
	s.mu.Lock()
	res, ok := s.queries[parts[0]]
	s.mu.Unlock()
	if !ok {
		jsonError(w, http.StatusNotFound, "unknown query")
		return
	}
	switch {
	case r.Method == http.MethodDelete && len(parts) == 1:
		s.mu.Lock()
		delete(s.queries, parts[0])
		s.activeQuery--
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "chunk":
		idx, err := strconv.Atoi(parts[2])
		if err != nil || idx < 0 || idx >= len(res.chunks) {
			jsonError(w, http.StatusNotFound, "chunk index out of range")
			return
		}
		if s.ChunkDelay > 0 {
			time.Sleep(s.ChunkDelay)
		}
		key := parts[0] + "/" + parts[2]
		s.mu.Lock()
		s.fetchCount++
		fails := s.chunkFails[key]
		if fails < s.FailFirst {
			s.chunkFails[key] = fails + 1
			s.mu.Unlock()
			jsonError(w, http.StatusServiceUnavailable, "transient")
			return
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(res.chunks[idx])
	default:
		jsonError(w, http.StatusNotFound, "no such endpoint")
	}
}
