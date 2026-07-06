// Package codectest encodes logical types and chunks in the wire
// serialization, for tests, fuzz seeds, and fake servers. duckcall itself
// never encodes result payloads — a read-only client has no reason to — so
// the encoder lives here, out of the production tree, and doubles as the
// round-trip oracle for codec alongside the captured corpus in testdata.
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

// Type info kinds, duplicated from codec's decode tables; codec keeps them
// unexported so its public surface stays decode-only.
const (
	infoDecimal = 2
	infoList    = 4
	infoStruct  = 5
	infoEnum    = 6
	infoArray   = 9
)

// WriteType appends one serialized LogicalType object (contents plus
// terminator) — the element form used inside type lists.
func WriteType(w *qser.Writer, t codec.LogicalType) {
	w.FieldUvarint(100, uint64(t.ID))
	switch t.ID {
	case codec.TypeDecimal:
		w.Field(101)
		w.Bool(true)
		w.FieldUvarint(100, infoDecimal)
		if t.Width != 0 {
			w.FieldUvarint(200, uint64(t.Width))
		}
		if t.Scale != 0 {
			w.FieldUvarint(201, uint64(t.Scale))
		}
		w.End()
	case codec.TypeList, codec.TypeMap:
		w.Field(101)
		w.Bool(true)
		w.FieldUvarint(100, infoList)
		w.Field(200)
		WriteType(w, *t.Child)
		w.End()
	case codec.TypeStruct:
		w.Field(101)
		w.Bool(true)
		w.FieldUvarint(100, infoStruct)
		w.Field(200)
		w.Uvarint(uint64(len(t.Fields)))
		for _, f := range t.Fields {
			w.FieldString(0, f.Name)
			w.Field(1)
			WriteType(w, f.Type)
			w.End()
		}
		w.End()
	case codec.TypeEnum:
		w.Field(101)
		w.Bool(true)
		w.FieldUvarint(100, infoEnum)
		w.FieldUvarint(200, uint64(len(t.Enum)))
		w.Field(201)
		w.Uvarint(uint64(len(t.Enum)))
		for _, v := range t.Enum {
			w.String(v)
		}
		w.End()
	case codec.TypeArray:
		w.Field(101)
		w.Bool(true)
		w.FieldUvarint(100, infoArray)
		w.Field(200)
		WriteType(w, *t.Child)
		if t.ArraySize != 0 {
			w.FieldUvarint(201, uint64(t.ArraySize))
		}
		w.End()
	}
	w.End()
}

// EncodeChunk builds one serialized DataChunk (contents plus terminator,
// the form living inside a wrapper's field 300). All columns must have the
// same number of values.
func EncodeChunk(cols []Col) []byte {
	var w qser.Writer
	WriteChunk(&w, cols)
	return w.Bytes()
}

// WriteChunk appends a serialized DataChunk to an existing writer.
func WriteChunk(w *qser.Writer, cols []Col) {
	rows := 0
	if len(cols) > 0 {
		rows = len(cols[0].Vals)
	}
	w.FieldUvarint(100, uint64(rows))
	w.Field(101)
	w.Uvarint(uint64(len(cols)))
	for _, c := range cols {
		WriteType(w, c.Type)
	}
	w.Field(102)
	w.Uvarint(uint64(len(cols)))
	for _, c := range cols {
		if len(c.Vals) != rows {
			panic(fmt.Sprintf("codectest: column %q has %d values, want %d", c.Name, len(c.Vals), rows))
		}
		writeVector(w, c.Type, c.Vals)
		w.End()
	}
	w.End()
}

// writeVector appends flat vector fields (no terminator; the caller ends
// the list element).
func writeVector(w *qser.Writer, t codec.LogicalType, vals []any) {
	validity := validityMask(vals)
	w.FieldBool(100, validity != nil)
	if validity != nil {
		w.FieldBytes(101, validity)
	}
	switch t.ID {
	case codec.TypeVarchar, codec.TypeBlob:
		w.Field(102)
		w.Uvarint(uint64(len(vals)))
		for _, v := range vals {
			switch s := v.(type) {
			case nil:
				w.String("")
			case string:
				w.String(s)
			case []byte:
				w.BytesVal(s)
			default:
				panic(fmt.Sprintf("codectest: string column got %T", v))
			}
		}
	default:
		var data []byte
		for _, v := range vals {
			data = appendCell(data, t, v)
		}
		w.FieldBytes(102, data)
	}
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
	// The server pads masks to 8-byte entries; match it so fixtures look
	// like the wire.
	mask := make([]byte, (len(vals)+63)/64*8)
	for i, v := range vals {
		if v != nil {
			mask[i/8] |= 1 << (i % 8)
		}
	}
	return mask
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
	case codec.TypeTimestampSec, codec.TypeTimestampMS, codec.TypeTimestamp,
		codec.TypeTimestampNS, codec.TypeTimestampTZ:
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
	case codec.TypeEnum:
		var idx int
		if v != nil {
			s := v.(string)
			idx = -1
			for i, e := range t.Enum {
				if e == s {
					idx = i
					break
				}
			}
			if idx < 0 {
				panic(fmt.Sprintf("codectest: %q not in enum dictionary", s))
			}
		}
		switch {
		case len(t.Enum) <= math.MaxUint8:
			return append(data, byte(idx))
		case len(t.Enum) <= math.MaxUint16:
			return le.AppendUint16(data, uint16(idx))
		default:
			return le.AppendUint32(data, uint32(idx))
		}
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

// EncodePrepareBody assembles a full PREPARE_RESPONSE body: schema from the
// column definitions, inline chunks, and the fetch handle. Fake servers
// compose messages from this plus wire's envelope helpers.
func EncodePrepareBody(cols []Col, inline [][]Col, needsMore bool, uuid qser.Hugeint) []byte {
	var w qser.Writer
	if len(cols) > 0 {
		w.Field(1)
		w.Uvarint(uint64(len(cols)))
		for _, c := range cols {
			WriteType(&w, c.Type)
		}
		w.Field(2)
		w.Uvarint(uint64(len(cols)))
		for _, c := range cols {
			w.String(c.Name)
		}
	}
	if needsMore {
		w.FieldBool(3, true)
	}
	if len(inline) > 0 {
		w.Field(4)
		writeChunkList(&w, inline)
	}
	w.FieldHugeint(5, uuid)
	w.End()
	return w.Bytes()
}

// EncodeFetchBody assembles a FETCH_RESPONSE body. Empty chunks means
// drained.
func EncodeFetchBody(chunks [][]Col, batchIndex uint64) []byte {
	var w qser.Writer
	if len(chunks) > 0 {
		w.Field(1)
		writeChunkList(&w, chunks)
	}
	w.FieldUvarint(2, batchIndex)
	w.End()
	return w.Bytes()
}

func writeChunkList(w *qser.Writer, chunks [][]Col) {
	w.Uvarint(uint64(len(chunks)))
	for _, cols := range chunks {
		w.Bool(true) // non-null wrapper pointer
		w.Field(300)
		WriteChunk(w, cols)
		w.End() // wrapper object
	}
}
