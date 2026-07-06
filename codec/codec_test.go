package codec_test

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/internal/qser"
)

func mustCodec(t *testing.T) *codec.Codec {
	t.Helper()
	cd, err := codec.For(1)
	if err != nil {
		t.Fatal(err)
	}
	return cd
}

func TestVersionRegistry(t *testing.T) {
	if _, err := codec.For(1); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.For(99); err == nil {
		t.Fatal("codec.For(99) succeeded; nothing speaks quack 99 yet")
	}
}

func dec(width, scale uint8, unscaled int64) codec.Decimal {
	return codec.NewDecimal(width, scale, big.NewInt(unscaled))
}

// tier1Cols is one column per shipped type, nulls sprinkled in, so a single
// round trip covers the whole Tier 1 matrix.
func tier1Cols() []codectest.Col {
	ts := time.Date(2026, 7, 4, 12, 30, 15, 0, time.UTC)
	day := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	return []codectest.Col{
		{Name: "b", Type: codectest.T(codec.TypeBoolean), Vals: []any{true, nil, false}},
		{Name: "i8", Type: codectest.T(codec.TypeTinyint), Vals: []any{int8(-8), int8(7), nil}},
		{Name: "u8", Type: codectest.T(codec.TypeUTinyint), Vals: []any{uint8(200), nil, uint8(0)}},
		{Name: "i16", Type: codectest.T(codec.TypeSmallint), Vals: []any{int16(-300), nil, int16(300)}},
		{Name: "u16", Type: codectest.T(codec.TypeUSmallint), Vals: []any{uint16(60000), nil, uint16(1)}},
		{Name: "i32", Type: codectest.T(codec.TypeInteger), Vals: []any{int32(-70000), int32(1), nil}},
		{Name: "u32", Type: codectest.T(codec.TypeUInteger), Vals: []any{uint32(4e9), nil, uint32(2)}},
		{Name: "i64", Type: codectest.T(codec.TypeBigint), Vals: []any{int64(-1 << 40), nil, int64(9)}},
		{Name: "u64", Type: codectest.T(codec.TypeUBigint), Vals: []any{uint64(1 << 63), nil, uint64(3)}},
		{Name: "f32", Type: codectest.T(codec.TypeFloat), Vals: []any{float32(1.5), nil, float32(-0.25)}},
		{Name: "f64", Type: codectest.T(codec.TypeDouble), Vals: []any{2.75, nil, -13.5}},
		{Name: "d4", Type: codectest.DecimalT(4, 1), Vals: []any{dec(4, 1, 123), nil, dec(4, 1, -999)}},
		{Name: "d9", Type: codectest.DecimalT(9, 2), Vals: []any{dec(9, 2, 123456), nil, dec(9, 2, -1)}},
		{Name: "d18", Type: codectest.DecimalT(18, 3), Vals: []any{dec(18, 3, 1e15), nil, dec(18, 3, -5)}},
		{Name: "d38", Type: codectest.DecimalT(38, 2), Vals: []any{dec(38, 2, 8900), nil, dec(38, 2, -42)}},
		{Name: "s", Type: codectest.T(codec.TypeVarchar), Vals: []any{"anvil", nil, strings.Repeat("long", 10)}},
		{Name: "raw", Type: codectest.T(codec.TypeBlob), Vals: []any{[]byte{1, 2, 3}, nil, []byte{}}},
		{Name: "day", Type: codectest.T(codec.TypeDate), Vals: []any{day, nil, day.AddDate(0, 0, -400)}},
		{Name: "clock", Type: codectest.T(codec.TypeTime), Vals: []any{codec.Time(45015000000), nil, codec.Time(1)}},
		{Name: "ts", Type: codectest.T(codec.TypeTimestamp), Vals: []any{ts, nil, ts.Add(-time.Hour)}},
		{Name: "ts_s", Type: codectest.T(codec.TypeTimestampSec), Vals: []any{ts, nil, ts.Add(time.Minute)}},
		{Name: "ts_ms", Type: codectest.T(codec.TypeTimestampMS), Vals: []any{ts, nil, ts}},
		{Name: "ts_ns", Type: codectest.T(codec.TypeTimestampNS), Vals: []any{ts, nil, ts}},
		{Name: "tstz", Type: codectest.T(codec.TypeTimestampTZ), Vals: []any{ts, nil, ts}},
		{Name: "mood", Type: codec.LogicalType{ID: codec.TypeEnum, Enum: []string{"happy", "sad", "ok"}},
			Vals: []any{"sad", nil, "happy"}},
	}
}

func TestChunkRoundTrip(t *testing.T) {
	cd := mustCodec(t)
	cols := tier1Cols()
	ch, err := cd.DecodeChunk(codectest.EncodeChunk(cols))
	if err != nil {
		t.Fatal(err)
	}
	if ch.RowCount() != 3 || ch.ColumnCount() != len(cols) {
		t.Fatalf("decoded %dx%d, want 3x%d", ch.RowCount(), ch.ColumnCount(), len(cols))
	}
	for ci, col := range cols {
		for ri, want := range col.Vals {
			got, err := ch.Value(ci, ri)
			if err != nil {
				t.Fatalf("%s[%d]: %v", col.Name, ri, err)
			}
			if want == nil {
				if got != nil {
					t.Fatalf("%s[%d]: want NULL, got %v", col.Name, ri, got)
				}
				continue
			}
			switch w := want.(type) {
			case []byte:
				g, ok := got.([]byte)
				if !ok || string(g) != string(w) {
					t.Fatalf("%s[%d]: got %v (%T), want %v", col.Name, ri, got, got, want)
				}
			case time.Time:
				g, ok := got.(time.Time)
				if !ok || !g.Equal(truncateFor(col.Type.ID, w)) {
					t.Fatalf("%s[%d]: got %v, want %v", col.Name, ri, got, w)
				}
			case codec.Decimal:
				g, ok := got.(codec.Decimal)
				if !ok || g.String() != w.String() {
					t.Fatalf("%s[%d]: got %v, want %v", col.Name, ri, got, w)
				}
			default:
				if got != want {
					t.Fatalf("%s[%d]: got %v (%T), want %v (%T)", col.Name, ri, got, got, want, want)
				}
			}
		}
	}
}

// truncateFor drops the precision a narrower timestamp type cannot carry.
func truncateFor(id codec.TypeID, ts time.Time) time.Time {
	switch id {
	case codec.TypeTimestampSec:
		return ts.Truncate(time.Second)
	case codec.TypeTimestampMS:
		return ts.Truncate(time.Millisecond)
	default:
		return ts
	}
}

func TestPrepareBodyRoundTrip(t *testing.T) {
	cd := mustCodec(t)
	cols := []codectest.Col{
		{Name: "product", Type: codectest.T(codec.TypeVarchar), Vals: []any{"anvil", "rocket"}},
		{Name: "total", Type: codectest.DecimalT(10, 2), Vals: []any{dec(10, 2, 1999), nil}},
	}
	uuid := codec.Hugeint{Upper: -3, Lower: 12345}
	body := codectest.EncodePrepareBody(cols, [][]codectest.Col{cols}, true, uuid)
	pr, err := cd.DecodePrepare(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Schema.Columns) != 2 ||
		pr.Schema.Columns[0].Name != "product" || pr.Schema.Columns[0].Type.ID != codec.TypeVarchar ||
		pr.Schema.Columns[1].Type.String() != "DECIMAL(10,2)" {
		t.Fatalf("schema: %+v", pr.Schema)
	}
	if !pr.NeedsMoreFetch || pr.ResultUUID != uuid {
		t.Fatalf("handle: needs=%v uuid=%+v", pr.NeedsMoreFetch, pr.ResultUUID)
	}
	if len(pr.Chunks) != 1 || pr.Chunks[0].RowCount() != 2 {
		t.Fatalf("inline chunks: %+v", pr.Chunks)
	}
}

func TestFetchBodyRoundTrip(t *testing.T) {
	cd := mustCodec(t)
	cols := []codectest.Col{{Name: "i", Type: codectest.T(codec.TypeInteger), Vals: []any{int32(1)}}}
	fr, err := cd.DecodeFetch(codectest.EncodeFetchBody([][]codectest.Col{cols, cols}, 7))
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.Chunks) != 2 || fr.BatchIndex != 7 {
		t.Fatalf("fetch: %d chunks, batch %d", len(fr.Chunks), fr.BatchIndex)
	}
	// A drained response: no chunks, batch index absent.
	fr, err = cd.DecodeFetch(codectest.EncodeFetchBody(nil, codec.BatchIndexAbsent))
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.Chunks) != 0 || fr.BatchIndex != codec.BatchIndexAbsent {
		t.Fatalf("drained fetch: %+v", fr)
	}
}

func TestUnsupportedColumnDoesNotPoisonChunk(t *testing.T) {
	cd := mustCodec(t)
	// A LIST column is Tier 2: it must parse (the stream stays in sync)
	// and error per column, leaving its neighbor decodable.
	inner := codectest.T(codec.TypeInteger)
	var w qser.Writer
	w.FieldUvarint(100, 2) // rows
	w.Field(101)
	w.Uvarint(2)
	codectest.WriteType(&w, codec.LogicalType{ID: codec.TypeList, Child: &inner})
	codectest.WriteType(&w, codectest.T(codec.TypeInteger))
	w.Field(102)
	w.Uvarint(2)
	// list vector: no nulls, 2 entries, child with 3 ints
	w.FieldBool(100, false)
	w.FieldUvarint(104, 3) // list_size
	w.Field(105)
	w.Uvarint(2)
	for _, e := range [][2]uint64{{0, 2}, {2, 1}} {
		w.FieldUvarint(100, e[0])
		w.FieldUvarint(101, e[1])
		w.End()
	}
	w.Field(106) // child vector
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0})
	w.End() // child object
	w.End() // list vector element
	// plain integer neighbor
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{9, 0, 0, 0, 8, 0, 0, 0})
	w.End() // vector element
	w.End() // chunk

	ch, err := cd.DecodeChunk(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Value(0, 0); err == nil {
		t.Fatal("LIST column decoded; it should be unsupported")
	} else if _, ok := err.(codec.ErrUnsupportedType); !ok {
		t.Fatalf("want ErrUnsupportedType, got %v", err)
	}
	if v, err := ch.Value(1, 1); err != nil || v != int32(8) {
		t.Fatalf("neighbor column: %v, %v", v, err)
	}
}

func TestCompressedVectors(t *testing.T) {
	cd := mustCodec(t)
	intType := codectest.T(codec.TypeInteger)

	// CONSTANT: field 90, then a one-row flat vector broadcast to all rows.
	var w qser.Writer
	w.FieldUvarint(100, 4)
	w.Field(101)
	w.Uvarint(1)
	codectest.WriteType(&w, intType)
	w.Field(102)
	w.Uvarint(1)
	w.FieldUvarint(90, 2) // CONSTANT_VECTOR
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{42, 0, 0, 0})
	w.End()
	w.End()
	ch, err := cd.DecodeChunk(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for r := range 4 {
		if v, _ := ch.Value(0, r); v != int32(42) {
			t.Fatalf("constant row %d: %v", r, v)
		}
	}

	// SEQUENCE: start 5, increment 3.
	w = qser.Writer{}
	w.FieldUvarint(100, 3)
	w.Field(101)
	w.Uvarint(1)
	codectest.WriteType(&w, codectest.T(codec.TypeBigint))
	w.Field(102)
	w.Uvarint(1)
	w.FieldUvarint(90, 4) // SEQUENCE_VECTOR
	w.FieldSvarint(91, 5)
	w.FieldSvarint(92, 3)
	w.End()
	w.End()
	ch, err = cd.DecodeChunk(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for r, want := range []int64{5, 8, 11} {
		if v, _ := ch.Value(0, r); v != want {
			t.Fatalf("sequence row %d: %v want %d", r, v, want)
		}
	}

	// DICTIONARY: 4 rows selecting from a 2-entry dict.
	w = qser.Writer{}
	w.FieldUvarint(100, 4)
	w.Field(101)
	w.Uvarint(1)
	codectest.WriteType(&w, codectest.T(codec.TypeVarchar))
	w.Field(102)
	w.Uvarint(1)
	w.FieldUvarint(90, 3)                                                    // DICTIONARY_VECTOR
	w.FieldBytes(91, []byte{0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}) // sel 0,1,1,0
	w.FieldUvarint(92, 2)
	w.FieldBool(100, false)
	w.Field(102)
	w.Uvarint(2)
	w.String("yes")
	w.String("no")
	w.End()
	w.End()
	ch, err = cd.DecodeChunk(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for r, want := range []string{"yes", "no", "no", "yes"} {
		if v, _ := ch.Value(0, r); v != want {
			t.Fatalf("dictionary row %d: %v want %q", r, v, want)
		}
	}
}

func TestUnknownFieldsFailLoudly(t *testing.T) {
	cd := mustCodec(t)
	// Schema-driven decoding cannot skip fields it has no schema for; a
	// message from a future serialization must error, not desync.
	var w qser.Writer
	w.FieldUvarint(100, 1)
	w.FieldUvarint(77, 9) // no such field in a chunk
	w.End()
	if _, err := cd.DecodeChunk(w.Bytes()); err == nil {
		t.Fatal("unknown field decoded silently")
	}
}

func TestCorruptInputsError(t *testing.T) {
	cd := mustCodec(t)
	cols := []codectest.Col{{Name: "i", Type: codectest.T(codec.TypeInteger), Vals: []any{int32(1), int32(2)}}}
	good := codectest.EncodeChunk(cols)
	for name, buf := range map[string][]byte{
		"empty":     {},
		"truncated": good[:len(good)-3],
		"garbage":   {0xde, 0xad, 0xbe, 0xef},
	} {
		if _, err := cd.DecodeChunk(buf); err == nil {
			t.Fatalf("%s input decoded without error", name)
		}
	}
	// Short vector data: claim 2 rows, supply 1 int.
	var w qser.Writer
	w.FieldUvarint(100, 2)
	w.Field(101)
	w.Uvarint(1)
	codectest.WriteType(&w, codectest.T(codec.TypeInteger))
	w.Field(102)
	w.Uvarint(1)
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{1, 0, 0, 0})
	w.End()
	w.End()
	if _, err := cd.DecodeChunk(w.Bytes()); err == nil || !strings.Contains(err.Error(), "short") {
		t.Fatalf("short data: %v", err)
	}
}

func TestDecimalString(t *testing.T) {
	for _, tc := range []struct {
		unscaled int64
		width    uint8
		scale    uint8
		want     string
	}{
		{1999, 10, 2, "19.99"},
		{-1999, 10, 2, "-19.99"},
		{5, 10, 2, "0.05"},
		{-5, 10, 2, "-0.05"},
		{0, 10, 2, "0.00"},
		{42, 4, 0, "42"},
	} {
		d := codec.NewDecimal(tc.width, tc.scale, big.NewInt(tc.unscaled))
		if got := d.String(); got != tc.want {
			t.Errorf("Decimal(%d, scale %d) = %q, want %q", tc.unscaled, tc.scale, got, tc.want)
		}
	}
}

func TestTimeString(t *testing.T) {
	if got := codec.Time(45015000000).String(); got != "12:30:15" {
		t.Errorf("Time = %q, want 12:30:15", got)
	}
	if got := codec.Time(45015123456).String(); got != "12:30:15.123456" {
		t.Errorf("Time = %q, want 12:30:15.123456", got)
	}
}
