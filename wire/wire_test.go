package wire_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/internal/quacktest"
	"github.com/mehrabr/duckcall/wire"
)

const testToken = "sekrit-token-42"

func startServer(t *testing.T) *quacktest.Server {
	t.Helper()
	s := quacktest.New(testToken)
	t.Cleanup(s.Close)
	return s
}

func connect(t *testing.T, s *quacktest.Server, mutate func(*wire.Config)) *wire.Client {
	t.Helper()
	cfg := wire.Config{Endpoint: s.URL(), Token: testToken}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := wire.Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

// intCol builds one INTEGER column with n sequential rows, so chunk order is
// checkable after parallel fetch.
func intCol(n int) []codectest.Col {
	vals := make([]any, n)
	for i := range vals {
		vals[i] = int32(i)
	}
	return []codectest.Col{{Name: "i", Type: codectest.T(codec.TypeInteger), Vals: vals}}
}

func TestConnectAndHandshake(t *testing.T) {
	s := startServer(t)
	c := connect(t, s, nil)
	hs := c.Handshake()
	if hs.ProtocolVersion != 1 || hs.SessionID == "" {
		t.Fatalf("handshake: %+v", hs)
	}
	if s.OpenSessions() != 1 {
		t.Fatalf("server sees %d sessions", s.OpenSessions())
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.OpenSessions() != 0 {
		t.Fatal("session not released on server")
	}
	if _, err := c.Execute(context.Background(), "SELECT 1"); !errors.Is(err, wire.ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}

func TestBadTokenRedacted(t *testing.T) {
	s := startServer(t)
	_, err := wire.Connect(context.Background(), wire.Config{Endpoint: s.URL(), Token: "wrong-token-99"})
	if err == nil {
		t.Fatal("connect succeeded with bad token")
	}
	var se *wire.ServerError
	if !errors.As(err, &se) || se.Status != 401 {
		t.Fatalf("want 401 ServerError, got %v", err)
	}
	// quacktest echoes the presented token; the client must have scrubbed it.
	if strings.Contains(err.Error(), "wrong-token-99") {
		t.Fatalf("token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("expected redaction marker: %v", err)
	}
}

func TestProtocolVersionMismatch(t *testing.T) {
	s := startServer(t)
	s.ProtocolVersion = 99
	_, err := wire.Connect(context.Background(), wire.Config{Endpoint: s.URL(), Token: testToken})
	if err == nil || !strings.Contains(err.Error(), "protocol 99") {
		t.Fatalf("want version error, got %v", err)
	}
}

func TestStreamChunksOrdered(t *testing.T) {
	s := startServer(t)
	const rows, chunkRows = 1000, 32
	s.AddResult("SELECT i FROM seq", intCol(rows), chunkRows)
	c := connect(t, s, func(cfg *wire.Config) { cfg.FetchWorkers = 8 })

	res, err := c.Execute(context.Background(), "SELECT i FROM seq")
	if err != nil {
		t.Fatal(err)
	}
	wantChunks := (rows + chunkRows - 1) / chunkRows
	if res.ChunkCount != wantChunks {
		t.Fatalf("chunk count %d, want %d", res.ChunkCount, wantChunks)
	}

	cd, _ := codec.For(1, 1)
	next := 0
	for payload, err := range c.StreamChunks(context.Background(), res) {
		if err != nil {
			t.Fatal(err)
		}
		ch, err := cd.DecodeChunk(payload)
		if err != nil {
			t.Fatal(err)
		}
		for r := 0; r < ch.RowCount(); r++ {
			v, err := ch.Value(0, r)
			if err != nil {
				t.Fatal(err)
			}
			if v != int32(next) {
				t.Fatalf("row %d out of order: got %v", next, v)
			}
			next++
		}
	}
	if next != rows {
		t.Fatalf("streamed %d rows, want %d", next, rows)
	}
	if err := c.CloseQuery(context.Background(), res.QueryID); err != nil {
		t.Fatal(err)
	}
	if s.OpenQueries() != 0 {
		t.Fatal("query not released on server")
	}
}

func TestFetchRetriesTransientFailures(t *testing.T) {
	s := startServer(t)
	s.FailFirst = 2
	s.AddResult("SELECT i FROM seq", intCol(10), 5)
	c := connect(t, s, func(cfg *wire.Config) { cfg.RetryBaseDelay = time.Millisecond })

	res, err := c.Execute(context.Background(), "SELECT i FROM seq")
	if err != nil {
		t.Fatal(err)
	}
	chunks := 0
	for payload, err := range c.StreamChunks(context.Background(), res) {
		if err != nil {
			t.Fatal(err)
		}
		if len(payload) == 0 {
			t.Fatal("empty chunk payload")
		}
		chunks++
	}
	if chunks != 2 {
		t.Fatalf("streamed %d chunks, want 2", chunks)
	}
	// 2 chunks, each failed twice then fetched: 6 server-side fetches.
	if s.FetchCount() != 6 {
		t.Fatalf("fetch count %d, want 6", s.FetchCount())
	}
}

func TestRetryBudgetExhausted(t *testing.T) {
	s := startServer(t)
	s.FailFirst = 100
	s.AddResult("SELECT i FROM seq", intCol(4), 4)
	c := connect(t, s, func(cfg *wire.Config) {
		cfg.RetryBaseDelay = time.Millisecond
		cfg.MaxRetries = 2
	})
	res, err := c.Execute(context.Background(), "SELECT i FROM seq")
	if err != nil {
		t.Fatal(err)
	}
	var se *wire.ServerError
	for _, err := range c.StreamChunks(context.Background(), res) {
		if err == nil {
			t.Fatal("stream succeeded against a permanently failing chunk")
		}
		if !errors.As(err, &se) || se.Status != 503 {
			t.Fatalf("want 503, got %v", err)
		}
		break
	}
	if s.FetchCount() != 3 { // initial try + 2 retries
		t.Fatalf("fetch count %d, want 3", s.FetchCount())
	}
}

func TestStreamCancellation(t *testing.T) {
	s := startServer(t)
	s.ChunkDelay = 20 * time.Millisecond
	s.AddResult("SELECT i FROM seq", intCol(1000), 10)
	c := connect(t, s, nil)

	res, err := c.Execute(context.Background(), "SELECT i FROM seq")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := 0
	for _, err := range c.StreamChunks(ctx, res) {
		if err != nil {
			if seen == 0 {
				t.Fatal("no chunks before cancellation")
			}
			return // cancellation surfaced as an error, as it should
		}
		seen++
		if seen == 3 {
			cancel()
		}
	}
	t.Fatal("stream ended without surfacing cancellation")
}

func TestQueryError(t *testing.T) {
	s := startServer(t)
	c := connect(t, s, nil)
	_, err := c.Execute(context.Background(), "SELEC typo")
	var se *wire.ServerError
	if !errors.As(err, &se) || se.Status != 400 || !strings.Contains(se.Message, "Parser Error") {
		t.Fatalf("want parser error, got %v", err)
	}
}
