package codec_test

import (
	"fmt"
	"math/big"
	"reflect"
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

// tier2Cols is one column per Tier 2 type — nested kinds and the exotic
// scalars — with NULLs at every nesting level: whole rows, elements,
// struct fields, and inner lists.
func tier2Cols() []codectest.Col {
	intT := codectest.T(codec.TypeInteger)
	strT := codectest.T(codec.TypeVarchar)
	listInt := codec.LogicalType{ID: codec.TypeList, Child: &intT}
	pointT := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
		{Name: "x", Type: intT}, {Name: "y", Type: strT},
	}}
	nestT := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
		{Name: "id", Type: intT}, {Name: "pt", Type: pointT},
	}}
	kvT := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
		{Name: "key", Type: strT}, {Name: "value", Type: intT},
	}}
	ikvT := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
		{Name: "key", Type: intT}, {Name: "value", Type: strT},
	}}
	bigint := func(s string) *big.Int {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			panic(s)
		}
		return v
	}
	return []codectest.Col{
		{Name: "h", Type: codectest.T(codec.TypeHugeint),
			Vals: []any{bigint("-170141183460469231731687303715884105727"), nil, bigint("42")}},
		{Name: "uh", Type: codectest.T(codec.TypeUHugeint),
			Vals: []any{bigint("340282366920938463463374607431768211455"), nil, bigint("0")}},
		{Name: "uuid", Type: codectest.T(codec.TypeUUID),
			Vals: []any{codec.UUID{0xc8, 0x18, 0x5c, 0xa9, 0x1c, 0x05, 0x40, 0xc3, 0x8a, 0x22, 0x0f, 0x63, 0x28, 0x86, 0x28, 0x8c}, nil,
				codec.UUID{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}}},
		{Name: "iv", Type: codectest.T(codec.TypeInterval),
			Vals: []any{codec.Interval{Months: 14, Days: 3, Micros: 4_500_000}, nil,
				codec.Interval{Months: -1, Days: 0, Micros: -1}}},
		{Name: "ttz", Type: codectest.T(codec.TypeTimeTZ),
			Vals: []any{codec.TimeTZ{Micros: 45_015_000_000, Offset: 19800}, nil,
				codec.TimeTZ{Micros: 1, Offset: -3600}}},
		{Name: "tns", Type: codectest.T(codec.TypeTimeNS),
			Vals: []any{codec.TimeNS(45_015_123_456_789), nil, codec.TimeNS(1)}},
		{Name: "bits", Type: codectest.T(codec.TypeBit),
			Vals: []any{"010110110", nil, "1"}},
		{Name: "li", Type: listInt,
			Vals: []any{[]any{int32(1), nil, int32(3)}, nil, []any{}}},
		{Name: "ll", Type: codec.LogicalType{ID: codec.TypeList, Child: &listInt},
			Vals: []any{[]any{[]any{int32(1)}, nil}, nil, []any{[]any{}}}},
		{Name: "st", Type: nestT,
			Vals: []any{
				codec.Struct{{Name: "id", Value: int32(7)}, {Name: "pt", Value: codec.Struct{{Name: "x", Value: int32(1)}, {Name: "y", Value: "a"}}}},
				nil,
				codec.Struct{{Name: "id", Value: nil}, {Name: "pt", Value: nil}},
			}},
		{Name: "m", Type: codec.LogicalType{ID: codec.TypeMap, Child: &kvT},
			Vals: []any{
				[]codec.MapEntry{{Key: "a", Value: int32(1)}, {Key: "b", Value: nil}},
				nil,
				[]codec.MapEntry{},
			}},
		{Name: "mi", Type: codec.LogicalType{ID: codec.TypeMap, Child: &ikvT},
			Vals: []any{
				[]codec.MapEntry{{Key: int32(10), Value: "ten"}},
				[]codec.MapEntry{},
				nil,
			}},
		{Name: "arr", Type: codec.LogicalType{ID: codec.TypeArray, Child: &intT, ArraySize: 3},
			Vals: []any{[]any{int32(1), nil, int32(3)}, nil, []any{int32(0), int32(0), int32(0)}}},
	}
}

func TestTier2RoundTrip(t *testing.T) {
	cd := mustCodec(t)
	cols := tier2Cols()
	ch, err := cd.DecodeChunk(codectest.EncodeChunk(cols))
	if err != nil {
		t.Fatal(err)
	}
	for ci, col := range cols {
		for ri, want := range col.Vals {
			got, err := ch.Value(ci, ri)
			if err != nil {
				t.Fatalf("%s[%d]: %v", col.Name, ri, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s[%d]: got %#v, want %#v", col.Name, ri, got, want)
			}
		}
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
	// UNION stays beyond this codec: it must parse (struct-shaped storage,
	// tag child first) and error per column, leaving its neighbor decodable.
	union := codec.LogicalType{ID: codec.TypeUnion, Fields: []codec.StructField{
		{Name: "", Type: codectest.T(codec.TypeUTinyint)},
		{Name: "i", Type: codectest.T(codec.TypeInteger)},
	}}
	var w qser.Writer
	w.FieldUvarint(100, 2) // rows
	w.Field(101)
	w.Uvarint(2)
	codectest.WriteType(&w, union)
	codectest.WriteType(&w, codectest.T(codec.TypeInteger))
	w.Field(102)
	w.Uvarint(2)
	// union vector: no nulls, tag child then one member child
	w.FieldBool(100, false)
	w.Field(103)
	w.Uvarint(2)
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{0, 0})
	w.End()
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{1, 0, 0, 0, 2, 0, 0, 0})
	w.End()
	w.End() // union vector element
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
		t.Fatal("UNION column decoded; it should be unsupported")
	} else if _, ok := err.(codec.ErrUnsupportedType); !ok {
		t.Fatalf("want ErrUnsupportedType, got %v", err)
	}
	if v, err := ch.Value(1, 1); err != nil || v != int32(8) {
		t.Fatalf("neighbor column: %v, %v", v, err)
	}
}

// TestUnsupportedChildDegradesColumn: a LIST of UNION cannot produce values,
// but the bytes still parse and the error names the child type.
func TestUnsupportedChildDegradesColumn(t *testing.T) {
	cd := mustCodec(t)
	union := codec.LogicalType{ID: codec.TypeUnion, Fields: []codec.StructField{
		{Name: "", Type: codectest.T(codec.TypeUTinyint)},
		{Name: "i", Type: codectest.T(codec.TypeInteger)},
	}}
	var w qser.Writer
	w.FieldUvarint(100, 1)
	w.Field(101)
	w.Uvarint(2)
	codectest.WriteType(&w, codec.LogicalType{ID: codec.TypeList, Child: &union})
	codectest.WriteType(&w, codectest.T(codec.TypeInteger))
	w.Field(102)
	w.Uvarint(2)
	// list vector: 1 row of 2 union elements
	w.FieldBool(100, false)
	w.FieldUvarint(104, 2)
	w.Field(105)
	w.Uvarint(1)
	w.FieldUvarint(100, 0)
	w.FieldUvarint(101, 2)
	w.End()
	w.Field(106)
	w.FieldBool(100, false)
	w.Field(103)
	w.Uvarint(2)
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{0, 0})
	w.End()
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{1, 0, 0, 0, 2, 0, 0, 0})
	w.End()
	w.End() // child object
	w.End() // list vector element
	w.FieldBool(100, false)
	w.FieldBytes(102, []byte{7, 0, 0, 0})
	w.End()
	w.End()

	ch, err := cd.DecodeChunk(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	_, err = ch.Value(0, 0)
	ue, ok := err.(codec.ErrUnsupportedType)
	if !ok || ue.Type.ID != codec.TypeUnion {
		t.Fatalf("want ErrUnsupportedType naming UNION, got %v", err)
	}
	if v, err := ch.Value(1, 0); err != nil || v != int32(7) {
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
	// duckdb trims trailing zeros in fractional seconds; matching it keeps
	// rendered values diffable against the official client.
	if got := codec.Time(45015500000).String(); got != "12:30:15.5" {
		t.Errorf("Time = %q, want 12:30:15.5", got)
	}
}

// The String() renderings mirror duckdb's VARCHAR casts; the expectations
// here were confirmed against duckdb v1.5.4 output.
func TestTier2ValueStrings(t *testing.T) {
	for _, tc := range []struct {
		v    fmt.Stringer
		want string
	}{
		{codec.UUID{0xc8, 0x18, 0x5c, 0xa9, 0x1c, 0x05, 0x40, 0xc3, 0x8a, 0x22, 0x0f, 0x63, 0x28, 0x86, 0x28, 0x8c},
			"c8185ca9-1c05-40c3-8a22-0f632886288c"},
		{codec.Interval{}, "00:00:00"},
		{codec.Interval{Months: 14, Days: 3, Micros: 4_500_000}, "1 year 2 months 3 days 00:00:04.5"},
		{codec.Interval{Months: -1}, "-1 month"},
		{codec.Interval{Months: -26, Micros: -3_600_000_001}, "-2 years -2 months -01:00:00.000001"},
		{codec.Interval{Days: 2}, "2 days"},
		{codec.TimeTZ{Micros: 45_015_000_000, Offset: 19800}, "12:30:15+05:30"},
		{codec.TimeTZ{Micros: 1, Offset: -3600}, "00:00:00.000001-01"},
		// duckdb skips the minutes component when zero even if seconds
		// follow; the ambiguity is upstream's.
		{codec.TimeTZ{Micros: 0, Offset: 30}, "00:00:00+00:30"},
		{codec.TimeNS(45_015_123_456_789), "12:30:15.123456789"},
		{codec.TimeNS(45_015_500_000_000), "12:30:15.5"},
	} {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("%T %#v = %q, want %q", tc.v, tc.v, got, tc.want)
		}
	}
}
