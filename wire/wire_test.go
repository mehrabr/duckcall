package wire_test

import (
	"context"
	"encoding/hex"
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

// intCol builds one INTEGER column with n sequential rows.
func intCol(n int) []codectest.Col {
	vals := make([]any, n)
	for i := range vals {
		vals[i] = int32(i)
	}
	return []codectest.Col{{Name: "i", Type: codectest.T(codec.TypeInteger), Vals: vals}}
}

// The 50-byte CONNECTION_REQUEST DuckDB v1.5.4's CLI sent to a live
// quack_serve, from testdata/corpus/quack-v1.5.4-capture.txt. The envelope
// codec must read the real thing, not just its own output.
const capturedConnect = "010001030004ffff01000a64656d6f2d746f6b656e02000676312e352e34" +
	"0300096f73785f61726d3634040001050001ffff"

func TestDecodeCapturedConnectionRequest(t *testing.T) {
	raw, err := hex.DecodeString(strings.ReplaceAll(capturedConnect, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	hdr, body, err := wire.SplitEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Type != wire.MsgConnectionRequest || hdr.ConnectionID != "" || hdr.QueryID != 4 {
		t.Fatalf("header: %+v", hdr)
	}
	req, err := wire.DecodeConnectionRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	want := wire.ConnectionRequest{
		AuthString:    "demo-token",
		ClientVersion: "v1.5.4",
		Platform:      "osx_arm64",
		MinVersion:    1,
		MaxVersion:    1,
	}
	if req != want {
		t.Fatalf("decoded %+v, want %+v", req, want)
	}
	// And the encoder must reproduce the capture byte for byte.
	reencoded := wire.EncodeEnvelope(hdr, req.Encode())
	if !strings.EqualFold(hex.EncodeToString(reencoded), strings.ReplaceAll(capturedConnect, " ", "")) {
		t.Fatalf("re-encoding diverges from capture:\n got %x\nwant %s", reencoded, capturedConnect)
	}
}

func TestConnectAndDisconnect(t *testing.T) {
	s := startServer(t)
	c := connect(t, s, nil)
	if v := c.Server().QuackVersion; v != 1 {
		t.Fatalf("negotiated quack version %d", v)
	}
	if c.ConnectionID() == "" {
		t.Fatal("no connection id")
	}
	if s.OpenConnections() != 1 {
		t.Fatalf("server sees %d connections", s.OpenConnections())
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.OpenConnections() != 0 {
		t.Fatal("connection not released on server")
	}
	if _, err := c.Prepare(context.Background(), "SELECT 1"); !errors.Is(err, wire.ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}

func TestConnectRetriesTransientFailures(t *testing.T) {
	s := startServer(t)
	s.ConnectFails = 2
	c := connect(t, s, func(cfg *wire.Config) { cfg.RetryBaseDelay = time.Millisecond })
	if c.ConnectionID() == "" {
		t.Fatal("no connection id after retried connect")
	}
}

func TestBadTokenRedacted(t *testing.T) {
	s := startServer(t)
	_, err := wire.Connect(context.Background(), wire.Config{Endpoint: s.URL(), Token: "wrong-token-99"})
	if err == nil {
		t.Fatal("connect succeeded with bad token")
	}
	var se *wire.ServerError
	if !errors.As(err, &se) || se.Status != 0 {
		t.Fatalf("want in-band ServerError, got %v", err)
	}
	// quacktest echoes the presented token; the client must have scrubbed it.
	if strings.Contains(err.Error(), "wrong-token-99") {
		t.Fatalf("token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("expected redaction marker: %v", err)
	}
}

func TestQuackVersionMismatch(t *testing.T) {
	s := startServer(t)
	s.QuackVersion = 99
	_, err := wire.Connect(context.Background(), wire.Config{Endpoint: s.URL(), Token: testToken})
	if err == nil || !strings.Contains(err.Error(), "quack version 99") {
		t.Fatalf("want version error, got %v", err)
	}
}

func TestPrepareAndFetchLoop(t *testing.T) {
	s := startServer(t)
	const rows, chunkRows = 1000, 32
	s.AddResult("SELECT i FROM seq", intCol(rows), chunkRows)
	c := connect(t, s, nil)

	body, err := c.Prepare(context.Background(), "SELECT i FROM seq")
	if err != nil {
		t.Fatal(err)
	}
	cd, _ := codec.For(1)
	pr, err := cd.DecodePrepare(body)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.NeedsMoreFetch {
		t.Fatal("1000 rows fit inline?")
	}
	next := 0
	countRows := func(chunks []*codec.Chunk) {
		for _, ch := range chunks {
			for r := range ch.RowCount() {
				v, err := ch.Value(0, r)
				if err != nil {
					t.Fatal(err)
				}
				if v != int32(next) {
					t.Fatalf("row %d: got %v", next, v)
				}
				next++
			}
		}
	}
	countRows(pr.Chunks)
	for {
		body, err := c.Fetch(context.Background(), pr.ResultUUID)
		if err != nil {
			t.Fatal(err)
		}
		fr, err := cd.DecodeFetch(body)
		if err != nil {
			t.Fatal(err)
		}
		if len(fr.Chunks) == 0 {
			break
		}
		countRows(fr.Chunks)
	}
	if next != rows {
		t.Fatalf("fetched %d rows, want %d", next, rows)
	}
}

func TestFetchDoesNotRetry(t *testing.T) {
	s := startServer(t)
	s.AddResult("SELECT i FROM seq", intCol(100), 10)
	c := connect(t, s, func(cfg *wire.Config) { cfg.RetryBaseDelay = time.Millisecond })

	body, err := c.Prepare(context.Background(), "SELECT i FROM seq")
	if err != nil {
		t.Fatal(err)
	}
	cd, _ := codec.For(1)
	pr, err := cd.DecodePrepare(body)
	if err != nil {
		t.Fatal(err)
	}

	before := s.FetchCount()
	s.FetchFails = 1
	_, err = c.Fetch(context.Background(), pr.ResultUUID)
	var se *wire.ServerError
	if !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("want 503 ServerError, got %v", err)
	}
	// A destructive read gets exactly one attempt.
	if got := s.FetchCount() - before; got != 1 {
		t.Fatalf("server saw %d fetches, want 1", got)
	}
}

func TestQueryError(t *testing.T) {
	s := startServer(t)
	c := connect(t, s, nil)
	_, err := c.Prepare(context.Background(), "SELEC typo")
	var se *wire.ServerError
	if !errors.As(err, &se) || se.Status != 0 || !strings.Contains(se.Message, "Parser Error") {
		t.Fatalf("want parser error, got %v", err)
	}
	if errors.Is(err, wire.ErrConnectionExpired) {
		t.Fatal("a parser error is not an expired connection")
	}
}

func TestConnectionExpiredIsTyped(t *testing.T) {
	s := startServer(t)
	c := connect(t, s, nil)
	s.ExpireConnections()
	_, err := c.Prepare(context.Background(), "SELECT 1")
	if !errors.Is(err, wire.ErrConnectionExpired) {
		t.Fatalf("want ErrConnectionExpired via errors.Is, got %v", err)
	}
	// The DISCONNECT variant of the text must match too.
	if err := c.Close(context.Background()); !errors.Is(err, wire.ErrConnectionExpired) {
		t.Fatalf("disconnect on a dead session: want ErrConnectionExpired, got %v", err)
	}
}
