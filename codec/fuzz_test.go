package codec_test

import (
	"testing"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
)

// A hand-rolled binary decoder without fuzzing is a CVE with a release date.
// All entry points take attacker-adjacent bytes (anything on the network
// path can mangle a payload), so the only requirements are: no panic, no
// out-of-bounds, errors instead of garbage.

func FuzzDecodeChunk(f *testing.F) {
	f.Add(codectest.EncodeChunk(tier1Cols()))
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{0x64, 0x00, 0x01, 0xff, 0xff})
	c, err := codec.For(1)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		ch, err := c.DecodeChunk(data)
		if err != nil {
			return
		}
		// A successful decode must be internally consistent and re-readable.
		for col := range ch.ColumnCount() {
			for row := range ch.RowCount() {
				ch.Value(col, row)
			}
		}
		ch2, err := c.DecodeChunk(data)
		if err != nil || ch2.RowCount() != ch.RowCount() || ch2.ColumnCount() != ch.ColumnCount() {
			t.Fatalf("decode is not deterministic: %v", err)
		}
	})
}

func FuzzDecodePrepare(f *testing.F) {
	cols := tier1Cols()
	f.Add(codectest.EncodePrepareBody(cols, [][]codectest.Col{cols}, true, codec.Hugeint{Upper: 1, Lower: 2}))
	f.Add(codectest.EncodePrepareBody(nil, nil, false, codec.Hugeint{}))
	f.Add([]byte{})
	c, err := codec.For(1)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pr, err := c.DecodePrepare(data)
		if err != nil {
			return
		}
		for _, col := range pr.Schema.Columns {
			_ = col.Type.String()
		}
		for _, ch := range pr.Chunks {
			for col := range ch.ColumnCount() {
				for row := range ch.RowCount() {
					ch.Value(col, row)
				}
			}
		}
	})
}

func FuzzDecodeFetch(f *testing.F) {
	cols := tier1Cols()
	f.Add(codectest.EncodeFetchBody([][]codectest.Col{cols}, 3))
	f.Add(codectest.EncodeFetchBody(nil, codec.BatchIndexAbsent))
	f.Add([]byte{})
	c, err := codec.For(1)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, err := c.DecodeFetch(data)
		if err != nil {
			return
		}
		for _, ch := range fr.Chunks {
			for col := range ch.ColumnCount() {
				for row := range ch.RowCount() {
					ch.Value(col, row)
				}
			}
		}
	})
}
