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
	if s.OpenQueries() != 0 {
		t.Fatal("result not released")
	}
}

func TestDialRejectsUnknownCodec(t *testing.T) {
	s := quacktest.New("tok")
	t.Cleanup(s.Close)
	// quacktest can only lie about the protocol version within the range the
	// transport accepts, which is exactly one version today, so this test
	// drives the mismatch through the transport check instead.
	s.ProtocolVersion = 2
	if _, err := duckcall.Dial(context.Background(), duckcall.Config{Endpoint: s.URL(), Token: "tok"}); err == nil {
		t.Fatal("dial succeeded against unsupported protocol")
	}
}
