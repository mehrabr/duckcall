package duckcall_test

import (
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/wire"
)

// These tests decode PREPARE_RESPONSE frames captured from a live DuckDB
// v1.5.4 quack_serve (testdata/corpus/frames/README.md has the provenance
// and the exact queries) and assert cell values. They are the ground truth
// for the Tier 2 decoders: codectest round trips prove self-consistency,
// these prove the wire.

func decodeFrame(t *testing.T, name string) *codec.PrepareResult {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "corpus", "frames", name))
	if err != nil {
		t.Fatal(err)
	}
	hdr, body, err := wire.SplitEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Type != wire.MsgPrepareResponse {
		t.Fatalf("frame %s is a %s, want prepare response", name, hdr.Type)
	}
	cd, err := codec.For(1)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := cd.DecodePrepare(body)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

// rows flattens a prepare result's inline chunks to per-row value slices;
// cell errors surface as the error value itself so tests can assert on it.
func frameRows(pr *codec.PrepareResult) [][]any {
	var out [][]any
	for _, ch := range pr.Chunks {
		for r := range ch.RowCount() {
			row := make([]any, ch.ColumnCount())
			for c := range row {
				v, err := ch.Value(c, r)
				if err != nil {
					row[c] = err
				} else {
					row[c] = v
				}
			}
			out = append(out, row)
		}
	}
	return out
}

func TestCorpusTier2Frame(t *testing.T) {
	pr := decodeFrame(t, "prepare-tier2.bin")

	wantCols := []struct{ name, typ string }{
		{"id", "INTEGER"},
		{"li", "INTEGER[]"},
		{"st", "STRUCT(id INTEGER, pt STRUCT(x INTEGER, y VARCHAR))"},
		{"m", "MAP(VARCHAR, INTEGER)"},
		{"arr", "INTEGER[3]"},
		{"h", "HUGEINT"},
		{"uh", "UHUGEINT"},
		{"u", "UUID"},
		{"iv", "INTERVAL"},
		{"ttz", "TIMETZ"},
		{"tns", "TIME_NS"},
		{"bits", "BIT"},
	}
	if len(pr.Schema.Columns) != len(wantCols) {
		t.Fatalf("schema has %d columns, want %d", len(pr.Schema.Columns), len(wantCols))
	}
	for i, w := range wantCols {
		c := pr.Schema.Columns[i]
		if c.Name != w.name || c.Type.String() != w.typ {
			t.Errorf("column %d: %s %s, want %s %s", i, c.Name, c.Type, w.name, w.typ)
		}
	}

	huge, _ := new(big.Int).SetString("170141183460469231731687303715884105727", 10)
	negHuge := new(big.Int).Neg(huge)
	uhuge, _ := new(big.Int).SetString("340282366920938463463374607431768211455", 10)
	want := [][]any{
		{
			int32(1),
			[]any{int32(1), nil, int32(3)},
			codec.Struct{
				{Name: "id", Value: int32(7)},
				{Name: "pt", Value: codec.Struct{{Name: "x", Value: int32(1)}, {Name: "y", Value: "a"}}},
			},
			[]codec.MapEntry{{Key: "a", Value: int32(1)}, {Key: "b", Value: nil}},
			[]any{int32(1), nil, int32(3)},
			huge,
			uhuge,
			codec.UUID{0xc8, 0x18, 0x5c, 0xa9, 0x1c, 0x05, 0x40, 0xc3, 0x8a, 0x22, 0x0f, 0x63, 0x28, 0x86, 0x28, 0x8c},
			codec.Interval{Months: 14, Days: 3, Micros: 4_500_000},
			codec.TimeTZ{Micros: 45_015_000_000, Offset: 19800},
			codec.TimeNS(45_015_123_456_789),
			"010110110",
		},
		{int32(2), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil},
		{
			int32(3),
			[]any{},
			codec.Struct{{Name: "id", Value: nil}, {Name: "pt", Value: nil}},
			[]codec.MapEntry{},
			[]any{int32(0), int32(0), int32(0)},
			negHuge,
			big.NewInt(0),
			codec.UUID{},
			codec.Interval{Months: -1},
			codec.TimeTZ{Micros: 1, Offset: -3600},
			codec.TimeNS(1),
			"1",
		},
	}
	got := frameRows(pr)
	if len(got) != len(want) {
		t.Fatalf("decoded %d rows, want %d", len(got), len(want))
	}
	for r := range want {
		for c := range want[r] {
			if !reflect.DeepEqual(got[r][c], want[r][c]) {
				t.Errorf("%s[%d]: got %#v, want %#v", wantCols[c].name, r, got[r][c], want[r][c])
			}
		}
	}
}

func TestCorpusListListFrame(t *testing.T) {
	pr := decodeFrame(t, "prepare-listlist.bin")
	want := [][]any{
		{int32(1), []any{[]any{int32(1)}, nil}},
		{int32(2), nil},
		{int32(3), []any{[]any{}}},
	}
	got := frameRows(pr)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestCorpusMapIntFrame(t *testing.T) {
	pr := decodeFrame(t, "prepare-mapint.bin")
	if typ := pr.Schema.Columns[1].Type.String(); typ != "MAP(INTEGER, VARCHAR)" {
		t.Fatalf("mi type: %s", typ)
	}
	want := [][]any{
		{int32(1), []codec.MapEntry{{Key: int32(10), Value: "ten"}}},
		{int32(2), nil},
	}
	got := frameRows(pr)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestCorpusUnionFrame(t *testing.T) {
	pr := decodeFrame(t, "prepare-union.bin")
	got := frameRows(pr)
	if len(got) != 1 {
		t.Fatalf("decoded %d rows, want 1", len(got))
	}
	if got[0][0] != int32(1) || got[0][2] != "neighbor" {
		t.Fatalf("neighbors of the UNION column: %#v", got[0])
	}
	err, ok := got[0][1].(codec.ErrUnsupportedType)
	if !ok || err.Type.ID != codec.TypeUnion {
		t.Fatalf("UNION column: got %#v, want ErrUnsupportedType", got[0][1])
	}
}
