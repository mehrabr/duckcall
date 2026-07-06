package codec_test

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/internal/qser"
)

func mustCodec(t *testing.T) *codec.Codec {
	t.Helper()
	c, err := codec.For(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVersionRegistry(t *testing.T) {
	if _, err := codec.For(1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.For(9, 9); err == nil {
		t.Fatal("expected error for unregistered version pair")
	}
}

func ts(s string) time.Time {
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return v.UTC()
}

// tier1Cols is the shared round-trip fixture: every Tier 1 type, with nulls
// mixed in, strings on both sides of the 12-byte inline threshold.
func tier1Cols() []codectest.Col {
	bigDec := codec.NewDecimal(30, 4, mustBig("-123456789012345678901234"))
	return []codectest.Col{
		{Name: "b", Type: codectest.T(codec.TypeBoolean), Vals: []any{true, nil, false}},
		{Name: "i8", Type: codectest.T(codec.TypeTinyint), Vals: []any{int8(-8), int8(127), nil}},
		{Name: "u8", Type: codectest.T(codec.TypeUTinyint), Vals: []any{uint8(0), uint8(255), nil}},
		{Name: "i16", Type: codectest.T(codec.TypeSmallint), Vals: []any{int16(-300), nil, int16(300)}},
		{Name: "u16", Type: codectest.T(codec.TypeUSmallint), Vals: []any{uint16(65535), nil, uint16(1)}},
		{Name: "i32", Type: codectest.T(codec.TypeInteger), Vals: []any{int32(-70000), int32(70000), nil}},
		{Name: "u32", Type: codectest.T(codec.TypeUInteger), Vals: []any{uint32(4000000000), nil, uint32(7)}},
		{Name: "i64", Type: codectest.T(codec.TypeBigint), Vals: []any{int64(math.MinInt64), int64(math.MaxInt64), nil}},
		{Name: "u64", Type: codectest.T(codec.TypeUBigint), Vals: []any{uint64(math.MaxUint64), nil, uint64(0)}},
		{Name: "f32", Type: codectest.T(codec.TypeFloat), Vals: []any{float32(1.5), nil, float32(-0.25)}},
		{Name: "f64", Type: codectest.T(codec.TypeDouble), Vals: []any{3.14159, nil, -2.5e300}},
		{Name: "s", Type: codectest.T(codec.TypeVarchar), Vals: []any{"short", nil, "definitely longer than twelve bytes"}},
		{Name: "bl", Type: codectest.T(codec.TypeBlob), Vals: []any{[]byte{0, 1, 2}, nil, []byte("also longer than twelve bytes here")}},
		{Name: "d", Type: codectest.T(codec.TypeDate), Vals: []any{ts("2026-07-01T00:00:00Z"), nil, ts("1969-12-31T00:00:00Z")}},
		{Name: "t", Type: codectest.T(codec.TypeTime), Vals: []any{codec.Time(13*3600e6 + 45*60e6 + 30e6 + 123456), nil, codec.Time(0)}},
		{Name: "ts_s", Type: codectest.T(codec.TypeTimestampSec), Vals: []any{ts("2026-05-12T08:00:00Z"), nil, ts("1970-01-01T00:00:01Z")}},
		{Name: "ts_ms", Type: codectest.T(codec.TypeTimestampMS), Vals: []any{ts("2026-05-12T08:00:00.123Z"), nil, ts("1970-01-01T00:00:00.001Z")}},
		{Name: "ts_us", Type: codectest.T(codec.TypeTimestamp), Vals: []any{ts("2026-05-12T08:00:00.123456Z"), nil, ts("1969-07-20T20:17:40Z")}},
		{Name: "ts_ns", Type: codectest.T(codec.TypeTimestampNS), Vals: []any{ts("2026-05-12T08:00:00.123456789Z"), nil, ts("1970-01-01T00:00:00Z")}},
		{Name: "dec4", Type: codectest.DecimalT(4, 1), Vals: []any{codec.NewDecimal(4, 1, big.NewInt(-99)), nil, codec.NewDecimal(4, 1, big.NewInt(5))}},
		{Name: "dec9", Type: codectest.DecimalT(9, 2), Vals: []any{codec.NewDecimal(9, 2, big.NewInt(123456789)), nil, codec.NewDecimal(9, 2, big.NewInt(-1))}},
		{Name: "dec18", Type: codectest.DecimalT(18, 6), Vals: []any{codec.NewDecimal(18, 6, big.NewInt(-987654321012345678)), nil, codec.NewDecimal(18, 6, big.NewInt(42))}},
		{Name: "dec38", Type: codectest.DecimalT(30, 4), Vals: []any{bigDec, nil, codec.NewDecimal(30, 4, big.NewInt(1))}},
	}
}

func mustBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return v
}

func TestChunkRoundTrip(t *testing.T) {
	c := mustCodec(t)
	cols := tier1Cols()
	ch, err := c.DecodeChunk(codectest.EncodeChunk(cols))
	if err != nil {
		t.Fatal(err)
	}
	if ch.RowCount() != 3 || ch.ColumnCount() != len(cols) {
		t.Fatalf("got %d rows, %d cols", ch.RowCount(), ch.ColumnCount())
	}
	for ci, col := range cols {
		for ri, want := range col.Vals {
			got, err := ch.Value(ci, ri)
			if err != nil {
				t.Fatalf("%s[%d]: %v", col.Name, ri, err)
			}
			if want == nil {
				if got != nil {
					t.Errorf("%s[%d]: want NULL, got %#v", col.Name, ri, got)
				}
				continue
			}
			switch w := want.(type) {
			case []byte:
				g, ok := got.([]byte)
				if !ok || string(g) != string(w) {
					t.Errorf("%s[%d]: want %v, got %#v", col.Name, ri, w, got)
				}
			case codec.Decimal:
				g, ok := got.(codec.Decimal)
				if !ok || g.String() != w.String() {
					t.Errorf("%s[%d]: want %s, got %#v", col.Name, ri, w, got)
				}
			case time.Time:
				g, ok := got.(time.Time)
				if !ok || !g.Equal(w) {
					t.Errorf("%s[%d]: want %s, got %#v", col.Name, ri, w, got)
				}
			default:
				if got != want {
					t.Errorf("%s[%d]: want %#v, got %#v", col.Name, ri, want, got)
				}
			}
		}
	}
}

func TestSchemaRoundTrip(t *testing.T) {
	c := mustCodec(t)
	nested := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
		{Name: "x", Type: codectest.T(codec.TypeInteger)},
		{Name: "tags", Type: codec.LogicalType{ID: codec.TypeList, Child: &codec.LogicalType{ID: codec.TypeVarchar}}},
	}}
	cols := []codectest.Col{
		{Name: "id", Type: codectest.T(codec.TypeBigint)},
		{Name: "price", Type: codectest.DecimalT(18, 2)},
		{Name: "meta", Type: nested},
	}
	s, err := c.DecodeSchema(codectest.EncodeSchema(cols))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Columns) != 3 {
		t.Fatalf("got %d columns", len(s.Columns))
	}
	if s.Columns[1].Type.String() != "DECIMAL(18,2)" {
		t.Errorf("price type: %s", s.Columns[1].Type)
	}
	if got := s.Columns[2].Type.String(); got != "STRUCT(x INTEGER, tags VARCHAR[])" {
		t.Errorf("meta type: %s", got)
	}
}

func TestUnsupportedColumnDoesNotPoisonChunk(t *testing.T) {
	c := mustCodec(t)
	// HUGEINT is Tier 2: 16 bytes per row on the wire, undecodable today.
	// codectest refuses to encode it, so the chunk is built by hand.
	ch, err := c.DecodeChunk(encodeWithRawColumn())
	if err != nil {
		t.Fatal(err)
	}
	var unsup codec.ErrUnsupportedType
	if _, err := ch.Value(1, 0); !errors.As(err, &unsup) {
		t.Fatalf("want ErrUnsupportedType, got %v", err)
	}
	if unsup.Type.ID != codec.TypeHugeint {
		t.Errorf("error names %s", unsup.Type)
	}
	if v, err := ch.Value(0, 1); err != nil || v != int32(2) {
		t.Errorf("neighbor column: %v, %v", v, err)
	}
	if v, err := ch.Value(2, 0); err != nil || v != "a" {
		t.Errorf("neighbor column: %v, %v", v, err)
	}
}

// encodeWithRawColumn builds a 3-column chunk whose middle column is a
// hugeint vector encoded by hand.
func encodeWithRawColumn() []byte {
	var w qser.Writer
	w.FieldUvarint(1, 2) // rows
	w.FieldList(2, 3)    // columns
	// col 0
	writeVector(&w, codec.TypeInteger, []byte{1, 0, 0, 0, 2, 0, 0, 0}, nil)
	// col 1: hugeint, 2 rows * 16 bytes
	writeVector(&w, codec.TypeHugeint, make([]byte, 32), nil)
	// col 2: varchar "a", "b"
	sdata := make([]byte, 32)
	sdata[0] = 1
	sdata[4] = 'a'
	sdata[16] = 1
	sdata[20] = 'b'
	writeVector(&w, codec.TypeVarchar, sdata, nil)
	w.End()
	return w.Bytes()
}

func writeVector(w *qser.Writer, id codec.TypeID, data, heap []byte) {
	w.FieldObject(1)
	w.FieldUvarint(1, uint64(id))
	w.End()
	w.FieldBytes(3, data)
	if heap != nil {
		w.FieldBytes(4, heap)
	}
	w.End()
}

func TestUnknownFieldsAreSkipped(t *testing.T) {
	// Extensions may add protocol fields; decoders must not desync. Splice
	// unknown fields of every kind into a vector object and decode.
	c := mustCodec(t)
	var w qser.Writer
	w.FieldUvarint(1, 1)
	w.FieldUvarint(99, 7) // unknown chunk-level field
	w.FieldList(2, 1)
	w.FieldObject(1)
	w.FieldUvarint(1, uint64(codec.TypeInteger))
	w.End()
	w.FieldBytes(98, []byte("future")) // unknown vector-level field
	w.FieldObject(97)                  // unknown nested object
	w.FieldFixed8(1, 42)
	w.End()
	w.FieldBytes(3, []byte{7, 0, 0, 0})
	w.End()
	w.End()
	ch, err := c.DecodeChunk(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if v, err := ch.Value(0, 0); err != nil || v != int32(7) {
		t.Fatalf("got %v, %v", v, err)
	}
}

func TestCorruptInputsError(t *testing.T) {
	c := mustCodec(t)
	good := codectest.EncodeChunk(tier1Cols())
	cases := map[string][]byte{
		"empty":     {},
		"truncated": good[:len(good)/2],
		"garbage":   {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	}
	for name, buf := range cases {
		if _, err := c.DecodeChunk(buf); err == nil {
			t.Errorf("%s: decode succeeded on corrupt input", name)
		}
	}

	// Heap offset past the end must error, not read out of bounds.
	var w qser.Writer
	w.FieldUvarint(1, 1)
	w.FieldList(2, 1)
	w.FieldObject(1)
	w.FieldUvarint(1, uint64(codec.TypeVarchar))
	w.End()
	entry := make([]byte, 16)
	entry[0] = 100 // length 100, way past the 4-byte heap
	entry[8] = 0
	w.FieldBytes(3, entry)
	w.FieldBytes(4, []byte("heap"))
	w.End()
	w.End()
	if _, err := c.DecodeChunk(w.Bytes()); err == nil {
		t.Error("out-of-range heap reference decoded without error")
	}
}

func TestDecimalString(t *testing.T) {
	cases := []struct {
		unscaled string
		scale    uint8
		want     string
	}{
		{"12345", 2, "123.45"},
		{"-12345", 2, "-123.45"},
		{"5", 3, "0.005"},
		{"-5", 3, "-0.005"},
		{"7", 0, "7"},
		{"0", 2, "0.00"},
		{"-123456789012345678901234", 4, "-12345678901234567890.1234"},
	}
	for _, tc := range cases {
		d := codec.NewDecimal(38, tc.scale, mustBig(tc.unscaled))
		if got := d.String(); got != tc.want {
			t.Errorf("(%s, scale %d): got %s, want %s", tc.unscaled, tc.scale, got, tc.want)
		}
	}
}

func TestTimeString(t *testing.T) {
	if got := codec.Time(13*3600e6 + 45*60e6 + 30e6 + 123456).String(); got != "13:45:30.123456" {
		t.Errorf("got %s", got)
	}
	if got := codec.Time(0).String(); got != "00:00:00" {
		t.Errorf("got %s", got)
	}
}
