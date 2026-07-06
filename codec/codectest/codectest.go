// Package codectest encodes schemas and chunks in the wire serialization,
// for tests, fuzz seeds, and fake servers. duckcall itself never encodes
// result payloads — a read-only client has no reason to — so the encoder
// lives here, out of the production tree, and doubles as the round-trip
// oracle for codec until a captured corpus from a real quack_serve replaces
// synthetic fixtures.
package codectest

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/internal/qser"
)

// Col describes one column for encoding: a name, a type, and its values.
// A nil value is SQL NULL. Value Go types must match what codec decodes to
// (int32 for INTEGER, string for VARCHAR, time.Time for DATE/TIMESTAMP...).
type Col struct {
	Name string
	Type codec.LogicalType
	Vals []any
}

// T is shorthand for a scalar logical type.
func T(id codec.TypeID) codec.LogicalType { return codec.LogicalType{ID: id} }

// DecimalT builds a decimal type.
func DecimalT(width, scale uint8) codec.LogicalType {
	return codec.LogicalType{ID: codec.TypeDecimal, Width: width, Scale: scale}
}

// Field ids duplicated from codec's decode tables; codec keeps them
// unexported so its public surface stays decode-only.
const (
	ftID     = 1
	ftWidth  = 2
	ftScale  = 3
	ftChild  = 4
	ftFields = 5
	ftName   = 1
	ftType   = 2

	fsColumns = 1

	fcRows    = 1
	fcColumns = 2

	fvType     = 1
	fvValidity = 2
	fvData     = 3
	fvHeap     = 4
)

func writeType(w *qser.Writer, id uint64, t codec.LogicalType) {
	w.FieldObject(id)
	w.FieldUvarint(ftID, uint64(t.ID))
	if t.ID == codec.TypeDecimal {
		w.FieldUvarint(ftWidth, uint64(t.Width))
		w.FieldUvarint(ftScale, uint64(t.Scale))
	}
	if t.Child != nil {
		writeType(w, ftChild, *t.Child)
	}
	if len(t.Fields) > 0 {
		w.FieldList(ftFields, uint64(len(t.Fields)))
		for _, f := range t.Fields {
			w.FieldBytes(ftName, []byte(f.Name))
			writeType(w, ftType, f.Type)
			w.End()
		}
	}
	w.End()
}

// EncodeSchema builds a schema payload from column names and types.
func EncodeSchema(cols []Col) []byte {
	var w qser.Writer
	w.FieldList(fsColumns, uint64(len(cols)))
	for _, c := range cols {
		w.FieldBytes(ftName, []byte(c.Name))
		writeType(&w, ftType, c.Type)
		w.End()
	}
	w.End()
	return w.Bytes()
}

// EncodeChunk builds a DataChunk payload. All columns must have the same
// number of values.
func EncodeChunk(cols []Col) []byte {
	rows := 0
	if len(cols) > 0 {
		rows = len(cols[0].Vals)
	}
	var w qser.Writer
	w.FieldUvarint(fcRows, uint64(rows))
	w.FieldList(fcColumns, uint64(len(cols)))
	for _, c := range cols {
		if len(c.Vals) != rows {
			panic(fmt.Sprintf("codectest: column %q has %d values, want %d", c.Name, len(c.Vals), rows))
		}
		writeType(&w, fvType, c.Type)
		if validity := validityMask(c.Vals); validity != nil {
			w.FieldBytes(fvValidity, validity)
		}
		data, heap := encodeVector(c.Type, c.Vals)
		w.FieldBytes(fvData, data)
		if heap != nil {
			w.FieldBytes(fvHeap, heap)
		}
		w.End()
	}
	w.End()
	return w.Bytes()
}

func validityMask(vals []any) []byte {
	hasNull := false
	for _, v := range vals {
		if v == nil {
			hasNull = true
			break
		}
	}
	if !hasNull {
		return nil
	}
	mask := make([]byte, (len(vals)+7)/8)
	for i, v := range vals {
		if v != nil {
			mask[i/8] |= 1 << (i % 8)
		}
	}
	return mask
}

func encodeVector(t codec.LogicalType, vals []any) (data, heap []byte) {
	switch t.ID {
	case codec.TypeVarchar, codec.TypeBlob:
		return encodeStrings(vals)
	}
	for _, v := range vals {
		data = appendCell(data, t, v)
	}
	return data, nil
}

func appendCell(data []byte, t codec.LogicalType, v any) []byte {
	le := binary.LittleEndian
	switch t.ID {
	case codec.TypeBoolean:
		b := byte(0)
		if v != nil && v.(bool) {
			b = 1
		}
		return append(data, b)
	case codec.TypeTinyint:
		return append(data, byte(nz[int8](v)))
	case codec.TypeUTinyint:
		return append(data, nz[uint8](v))
	case codec.TypeSmallint:
		return le.AppendUint16(data, uint16(nz[int16](v)))
	case codec.TypeUSmallint:
		return le.AppendUint16(data, nz[uint16](v))
	case codec.TypeInteger:
		return le.AppendUint32(data, uint32(nz[int32](v)))
	case codec.TypeUInteger:
		return le.AppendUint32(data, nz[uint32](v))
	case codec.TypeBigint:
		return le.AppendUint64(data, uint64(nz[int64](v)))
	case codec.TypeUBigint:
		return le.AppendUint64(data, nz[uint64](v))
	case codec.TypeFloat:
		return le.AppendUint32(data, math.Float32bits(nz[float32](v)))
	case codec.TypeDouble:
		return le.AppendUint64(data, math.Float64bits(nz[float64](v)))
	case codec.TypeDate:
		var days int64
		if v != nil {
			days = v.(time.Time).Unix() / 86400
		}
		return le.AppendUint32(data, uint32(int32(days)))
	case codec.TypeTime:
		return le.AppendUint64(data, uint64(nz[codec.Time](v)))
	case codec.TypeTimestampSec, codec.TypeTimestampMS, codec.TypeTimestamp, codec.TypeTimestampNS:
		var n int64
		if v != nil {
			ts := v.(time.Time)
			switch t.ID {
			case codec.TypeTimestampSec:
				n = ts.Unix()
			case codec.TypeTimestampMS:
				n = ts.UnixMilli()
			case codec.TypeTimestampNS:
				n = ts.UnixNano()
			default:
				n = ts.UnixMicro()
			}
		}
		return le.AppendUint64(data, uint64(n))
	case codec.TypeDecimal:
		return appendDecimal(data, t, v)
	}
	panic(fmt.Sprintf("codectest: cannot encode type %s", t))
}

// nz unwraps v or returns the zero value for NULL slots, which occupy their
// fixed width in the vector regardless.
func nz[T any](v any) T {
	if v == nil {
		var zero T
		return zero
	}
	return v.(T)
}

func appendDecimal(data []byte, t codec.LogicalType, v any) []byte {
	le := binary.LittleEndian
	var hi int64
	var lo uint64
	if v != nil {
		hi, lo = int128parts(v.(codec.Decimal).BigInt())
	}
	switch {
	case t.Width <= 4:
		return le.AppendUint16(data, uint16(lo))
	case t.Width <= 9:
		return le.AppendUint32(data, uint32(lo))
	case t.Width <= 18:
		return le.AppendUint64(data, lo)
	default:
		data = le.AppendUint64(data, lo)
		return le.AppendUint64(data, uint64(hi))
	}
}

// int128parts splits an integer into its 128-bit two's-complement halves.
func int128parts(v *big.Int) (hi int64, lo uint64) {
	b := new(big.Int).Set(v)
	if b.Sign() < 0 {
		b.Add(b, new(big.Int).Lsh(big.NewInt(1), 128))
	}
	return int64(new(big.Int).Rsh(b, 64).Uint64()), b.Uint64()
}

func encodeStrings(vals []any) (data, heap []byte) {
	le := binary.LittleEndian
	for _, v := range vals {
		var b []byte
		switch s := v.(type) {
		case nil:
		case string:
			b = []byte(s)
		case []byte:
			b = s
		default:
			panic(fmt.Sprintf("codectest: string column got %T", v))
		}
		entry := make([]byte, 16)
		le.PutUint32(entry[:4], uint32(len(b)))
		if len(b) <= 12 {
			copy(entry[4:], b)
		} else {
			copy(entry[4:8], b[:4])
			le.PutUint64(entry[8:16], uint64(len(heap)))
			heap = append(heap, b...)
		}
		data = append(data, entry...)
	}
	return data, heap
}
