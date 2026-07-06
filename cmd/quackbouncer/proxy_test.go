package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mehrabr/duckcall"
	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/internal/quacktest"
	"github.com/mehrabr/duckcall/wire"
)

const upstreamToken = "upstream-secret"

func startProxy(t *testing.T) (*quacktest.Server, *httptest.Server, *pool) {
	t.Helper()
	up := quacktest.New(upstreamToken)
	t.Cleanup(up.Close)

	m := newMetrics()
	pl := newPool(wire.Config{Endpoint: up.URL(), Token: upstreamToken}, 4)
	px := newProxy(map[string]string{"alice-token": "alice"}, pl, m)
	front := httptest.NewServer(px)
	t.Cleanup(front.Close)
	return up, front, pl
}

// TestEndToEndThroughProxy runs a real duckcall client through the bouncer:
// if payload pass-through mangled a single byte, the codec would notice.
func TestEndToEndThroughProxy(t *testing.T) {
	up, front, _ := startProxy(t)
	up.AddResult("FROM sales", []codectest.Col{
		{Name: "product", Type: codectest.T(codec.TypeVarchar), Vals: []any{"anvil", "a much longer product name than twelve bytes"}},
	}, 1)

	ctx := context.Background()
	conn, err := duckcall.Dial(ctx, duckcall.Config{Endpoint: front.URL, Token: "alice-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if !strings.Contains(conn.ServerVersion(), "via quackbouncer") {
		t.Fatalf("server version: %s", conn.ServerVersion())
	}

	res, err := conn.Query(ctx, "FROM sales")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close(ctx)
	var got []string
	for chunk, err := range res.Chunks(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		for r := range chunk.RowCount() {
			v, err := chunk.Value(0, r)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, v.(string))
		}
	}
	if len(got) != 2 || got[1] != "a much longer product name than twelve bytes" {
		t.Fatalf("rows: %q", got)
	}
}

func TestClientAuthRequired(t *testing.T) {
	_, front, _ := startProxy(t)
	_, err := duckcall.Dial(context.Background(), duckcall.Config{Endpoint: front.URL, Token: "not-a-client"})
	if err == nil {
		t.Fatal("proxy accepted an unknown client token")
	}
	// The upstream token must never be needed, or visible, client-side.
	if strings.Contains(err.Error(), upstreamToken) {
		t.Fatalf("upstream token leaked: %v", err)
	}
}

func TestSessionPooling(t *testing.T) {
	up, front, pl := startProxy(t)
	ctx := context.Background()

	c1, err := duckcall.Dial(ctx, duckcall.Config{Endpoint: front.URL, Token: "alice-token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c1.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if up.OpenSessions() != 1 {
		t.Fatalf("upstream sessions after close: %d (want 1 pooled)", up.OpenSessions())
	}
	if pl.idleCount() != 1 {
		t.Fatalf("pool idle: %d", pl.idleCount())
	}

	// The next client must reuse the pooled upstream session.
	c2, err := duckcall.Dial(ctx, duckcall.Config{Endpoint: front.URL, Token: "alice-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close(ctx)
	if up.OpenSessions() != 1 {
		t.Fatalf("upstream sessions after reuse: %d", up.OpenSessions())
	}
}

func TestMetricsEndpoint(t *testing.T) {
	_, front, _ := startProxy(t)
	conn, err := duckcall.Dial(context.Background(), duckcall.Config{Endpoint: front.URL, Token: "alice-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())

	resp, err := http.Get(front.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		`quackbouncer_requests_total{route="connect",code="200"} 1`,
		"quackbouncer_active_sessions 1",
		"quackbouncer_upstream_connects_total 1",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics missing %q in:\n%s", want, body)
		}
	}
}
