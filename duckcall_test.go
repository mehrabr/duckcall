package duckcall_test

import (
	"context"
	"testing"

	"github.com/mehrabr/duckcall"
	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/internal/quacktest"
)

func TestNativeQuickstart(t *testing.T) {
	s := quacktest.New("tok")
	t.Cleanup(s.Close)
	s.AddResult("FROM sales", []codectest.Col{
		{Name: "product", Type: codectest.T(codec.TypeVarchar), Vals: []any{"anvil", "rocket skates", nil}},
		{Name: "total", Type: codectest.T(codec.TypeBigint), Vals: []any{int64(3), int64(7), int64(0)}},
	}, 2)

	ctx := context.Background()
	conn, err := duckcall.Dial(ctx, duckcall.Config{Endpoint: s.URL(), Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	res, err := conn.Query(ctx, "FROM sales")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close(ctx)

	sc := res.Schema()
	if len(sc.Columns) != 2 || sc.Columns[0].Name != "product" || sc.Columns[1].Type.ID != codec.TypeBigint {
		t.Fatalf("schema: %+v", sc)
	}

	var rows int
	for chunk, err := range res.Chunks(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		rows += chunk.RowCount()
	}
	if rows != 3 {
		t.Fatalf("streamed %d rows, want 3", rows)
	}
	if err := res.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if s.OpenConnections() != 0 {
		t.Fatal("connection not released")
	}
}

// TestFetchLoopStreamsEverything pushes a result well past the inline
// budget so rows arrive through concurrent fetches, and checks nothing is
// lost or duplicated.
func TestFetchLoopStreamsEverything(t *testing.T) {
	s := quacktest.New("tok")
	t.Cleanup(s.Close)
	const rows = 5000
	vals := make([]any, rows)
	for i := range vals {
		vals[i] = int64(i)
	}
	s.AddResult("FROM big", []codectest.Col{
		{Name: "i", Type: codectest.T(codec.TypeBigint), Vals: vals},
	}, 128)

	ctx := context.Background()
	conn, err := duckcall.Dial(ctx, duckcall.Config{Endpoint: s.URL(), Token: "tok", FetchWorkers: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	res, err := conn.Query(ctx, "FROM big")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close(ctx)

	seen := make([]bool, rows)
	n := 0
	for chunk, err := range res.Chunks(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		for r := range chunk.RowCount() {
			v, err := chunk.Value(0, r)
			if err != nil {
				t.Fatal(err)
			}
			i := v.(int64)
			if seen[i] {
				t.Fatalf("row %d delivered twice", i)
			}
			seen[i] = true
			n++
		}
	}
	if n != rows {
		t.Fatalf("streamed %d rows, want %d", n, rows)
	}
}

func TestDialRejectsUnknownQuackVersion(t *testing.T) {
	s := quacktest.New("tok")
	t.Cleanup(s.Close)
	s.QuackVersion = 2
	if _, err := duckcall.Dial(context.Background(), duckcall.Config{Endpoint: s.URL(), Token: "tok"}); err == nil {
		t.Fatal("dial succeeded against unsupported quack version")
	}
}
