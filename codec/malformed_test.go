package codec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/internal/qser"
)

const malformedDir = "../testdata/corpus/malformed"

// malformedCases is the negative corpus: intentionally invalid DataChunk
// payloads, each committed as a .bin under testdata/corpus/malformed and
// required to decode to a structured error — the right one, with no panic
// and no silently wrong values. The same files seed the fuzzer.
//
// Regenerate the files (after changing a builder) with
// REGEN_MALFORMED=1 go test ./codec -run TestMalformedCorpus.
var malformedCases = []struct {
	name    string
	wantErr string
	build   func() []byte
}{
	{"truncated-mid", "short", func() []byte {
		// Cut mid-column: the LIST column's child vector loses its tail.
		good := codectest.EncodeChunk(tier2Cols())
		return good[:len(good)*3/5]
	}},
	{"truncated-tail", "truncated", func() []byte {
		good := codectest.EncodeChunk(tier2Cols())
		return good[:len(good)-3]
	}},
	{"rowcount-implausible", "implausible row count", func() []byte {
		var w qser.Writer
		w.FieldUvarint(100, 1<<20)
		w.End()
		return w.Bytes()
	}},
	{"vector-count-mismatch", "columns but", func() []byte {
		// One declared type, two encoded vectors.
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, codectest.T(codec.TypeInteger))
		w.Field(102)
		w.Uvarint(2)
		for range 2 {
			w.FieldBool(100, false)
			w.FieldBytes(102, []byte{1, 0, 0, 0})
			w.End()
		}
		w.End()
		return w.Bytes()
	}},
	{"missing-vector", "columns but", func() []byte {
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, codectest.T(codec.TypeInteger))
		w.Field(102)
		w.Uvarint(0)
		w.End()
		return w.Bytes()
	}},
	{"short-data", "short", func() []byte {
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
		return w.Bytes()
	}},
	{"short-validity", "validity mask short", func() []byte {
		var w qser.Writer
		w.FieldUvarint(100, 100)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, codectest.T(codec.TypeInteger))
		w.Field(102)
		w.Uvarint(1)
		w.FieldBool(100, true)
		w.FieldBytes(101, []byte{0xff})
		w.FieldBytes(102, make([]byte, 400))
		w.End()
		w.End()
		return w.Bytes()
	}},
	{"sel-out-of-range", "dictionary index", func() []byte {
		// 1 row selecting entry 7 of a 2-entry dictionary.
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, codectest.T(codec.TypeVarchar))
		w.Field(102)
		w.Uvarint(1)
		w.FieldUvarint(90, 3) // DICTIONARY_VECTOR
		w.FieldBytes(91, []byte{7, 0, 0, 0})
		w.FieldUvarint(92, 2)
		w.FieldBool(100, false)
		w.Field(102)
		w.Uvarint(2)
		w.String("a")
		w.String("b")
		w.End()
		w.End()
		return w.Bytes()
	}},
	{"oversized-list-size", "implausible list size", func() []byte {
		intT := codectest.T(codec.TypeInteger)
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, codec.LogicalType{ID: codec.TypeList, Child: &intT})
		w.Field(102)
		w.Uvarint(1)
		w.FieldBool(100, false)
		w.FieldUvarint(104, 1<<30) // list_size far past any real payload
		w.End()
		w.End()
		return w.Bytes()
	}},
	{"list-entry-out-of-range", "outside child", func() []byte {
		// Entry claims [5,10) of a 3-element child.
		intT := codectest.T(codec.TypeInteger)
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, codec.LogicalType{ID: codec.TypeList, Child: &intT})
		w.Field(102)
		w.Uvarint(1)
		w.FieldBool(100, false)
		w.FieldUvarint(104, 3)
		w.Field(105)
		w.Uvarint(1)
		w.FieldUvarint(100, 5)
		w.FieldUvarint(101, 5)
		w.End()
		w.Field(106)
		w.FieldBool(100, false)
		w.FieldBytes(102, []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0})
		w.End()
		w.End()
		w.End()
		return w.Bytes()
	}},
	{"struct-child-mismatch", "children for", func() []byte {
		// Type declares two fields, vector carries one child.
		st := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
			{Name: "a", Type: codectest.T(codec.TypeInteger)},
			{Name: "b", Type: codectest.T(codec.TypeInteger)},
		}}
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, st)
		w.Field(102)
		w.Uvarint(1)
		w.FieldBool(100, false)
		w.Field(103)
		w.Uvarint(1)
		w.FieldBool(100, false)
		w.FieldBytes(102, []byte{1, 0, 0, 0})
		w.End()
		w.End()
		w.End()
		return w.Bytes()
	}},
	{"bit-bad-padding", "padding", func() []byte {
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.Field(101)
		w.Uvarint(1)
		codectest.WriteType(&w, codectest.T(codec.TypeBit))
		w.Field(102)
		w.Uvarint(1)
		w.FieldBool(100, false)
		w.Field(102)
		w.Uvarint(1)
		w.BytesVal([]byte{9, 0xff}) // 9 padding bits cannot fit one byte
		w.End()
		w.End()
		return w.Bytes()
	}},
	{"unknown-field", "missing types", func() []byte {
		// Schema-driven decoding cannot skip an unknown field; it reads as
		// the absence of the field the schema demanded next.
		var w qser.Writer
		w.FieldUvarint(100, 1)
		w.FieldUvarint(77, 9)
		w.End()
		return w.Bytes()
	}},
}

func TestMalformedCorpus(t *testing.T) {
	cd := mustCodec(t)
	regen := os.Getenv("REGEN_MALFORMED") != ""
	for _, tc := range malformedCases {
		path := filepath.Join(malformedDir, tc.name+".bin")
		if regen {
			if err := os.WriteFile(path, tc.build(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (REGEN_MALFORMED=1 to create)", tc.name, err)
		}
		ch, err := cd.DecodeChunk(data)
		if err == nil {
			// Value-level corruption surfaces at Value, not DecodeChunk.
			for c := 0; err == nil && ch != nil && c < ch.ColumnCount(); c++ {
				err = ch.Column(c).Err()
			}
		}
		if err == nil {
			t.Errorf("%s: decoded without error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.wantErr)
		}
	}
}
