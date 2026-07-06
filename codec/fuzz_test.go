package codec_test

import (
	"testing"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
)

// A hand-rolled binary decoder without fuzzing is a CVE with a release date.
// Both entry points take attacker-adjacent bytes (anything on the network
// path can mangle a payload), so the only requirements are: no panic, no
// out-of-bounds, errors instead of garbage.

func FuzzDecodeChunk(f *testing.F) {
	f.Add(codectest.EncodeChunk(tier1Cols()))
	f.Add(codectest.EncodeChunk(nil))
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 1, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01})
	c, err := codec.For(1, 1)
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

func FuzzDecodeSchema(f *testing.F) {
	f.Add(codectest.EncodeSchema(tier1Cols()))
	f.Add(codectest.EncodeSchema(nil))
	f.Add([]byte{})
	c, err := codec.For(1, 1)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := c.DecodeSchema(data)
		if err != nil {
			return
		}
		for _, col := range s.Columns {
			_ = col.Type.String()
		}
	})
}
