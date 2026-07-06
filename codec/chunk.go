package codec

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/mehrabr/duckcall/internal/qser"
)

// Schema is the decoded result header: column names and types.
type Schema struct {
	Columns []SchemaColumn
}

type SchemaColumn struct {
	Name string
	Type LogicalType
}

// Wire field ids for schema, chunk, and vector objects.
const (
	fsColumns = 1

	fcRows    = 1
	fcColumns = 2

	fvType     = 1
	fvValidity = 2
	fvData     = 3
	fvHeap     = 4
)

// DecodeSchema decodes a result schema payload.
func (c *Codec) DecodeSchema(buf []byte) (*Schema, error) {
	r := qser.NewReader(buf)
	s := &Schema{}
	for {
		id, kind, err := r.Field()
		if err != nil {
			return nil, err
		}
		if id == 0 {
			return s, nil
		}
		switch id {
		case fsColumns:
			n, err := r.Uvarint()
			if err != nil {
				return nil, err
			}
			if n > uint64(r.Remaining()) {
				return nil, qser.ErrTruncated
			}
			s.Columns = make([]SchemaColumn, 0, n)
			for i := uint64(0); i < n; i++ {
				f, err := decodeStructField(r, 0)
				if err != nil {
					return nil, err
				}
				s.Columns = append(s.Columns, SchemaColumn{Name: f.Name, Type: f.Type})
			}
		default:
			if err := r.Skip(kind); err != nil {
				return nil, err
			}
		}
	}
}

// Chunk is one decoded DataChunk. Columns that this codec version cannot
// decode carry a per-column error; the others are still usable.
type Chunk struct {
	rows int
	cols []Column
}

type Column struct {
	Type LogicalType

	// vals holds one Go value per row, nil for NULL. Empty when err is set.
	vals []any
	err  error
}

func (c *Chunk) RowCount() int    { return c.rows }
func (c *Chunk) ColumnCount() int { return len(c.cols) }

// Column exposes the decoded column; useful for callers that iterate
// column-wise rather than row-wise.
func (c *Chunk) Column(i int) *Column { return &c.cols[i] }

// Value returns the value at (col, row); nil means SQL NULL. An unsupported
// column returns its ErrUnsupportedType for every row.
func (c *Chunk) Value(col, row int) (any, error) {
	if col < 0 || col >= len(c.cols) || row < 0 || row >= c.rows {
		return nil, fmt.Errorf("codec: value index (%d,%d) out of range", col, row)
	}
	cl := &c.cols[col]
	if cl.err != nil {
		return nil, cl.err
	}
	return cl.vals[row], nil
}

// Err returns the column's decode error, if any.
func (cl *Column) Err() error { return cl.err }

func (cl *Column) Value(row int) any { return cl.vals[row] }

// DecodeChunk decodes one DataChunk payload. Chunks are self-describing:
// each vector carries its own type, so a chunk can be decoded without the
// schema (the schema adds names).
func (c *Codec) DecodeChunk(buf []byte) (*Chunk, error) {
	r := qser.NewReader(buf)
	ch := &Chunk{rows: -1}
	for {
		id, kind, err := r.Field()
		if err != nil {
			return nil, err
		}
		if id == 0 {
			if ch.rows < 0 {
				return nil, fmt.Errorf("codec: chunk missing row count")
			}
			return ch, nil
		}
		switch id {
		case fcRows:
			n, err := r.Uvarint()
			if err != nil {
				return nil, err
			}
			// A duckdb vector is at most 2048 rows; anything bigger is a
			// corrupt or hostile length.
			if n > 1<<16 {
				return nil, fmt.Errorf("codec: implausible row count %d", n)
			}
			ch.rows = int(n)
		case fcColumns:
			if ch.rows < 0 {
				return nil, fmt.Errorf("codec: columns before row count")
			}
			n, err := r.Uvarint()
			if err != nil {
				return nil, err
			}
			if n > uint64(r.Remaining()) {
				return nil, qser.ErrTruncated
			}
			ch.cols = make([]Column, 0, n)
			for i := uint64(0); i < n; i++ {
				col, err := decodeVector(r, ch.rows)
				if err != nil {
					return nil, err
				}
				ch.cols = append(ch.cols, col)
			}
		default:
			if err := r.Skip(kind); err != nil {
				return nil, err
			}
		}
	}
}

func decodeVector(r *qser.Reader, rows int) (Column, error) {
	var (
		col      Column
		validity []byte
		data     []byte
		heap     []byte
		hasType  bool
	)
	for {
		id, kind, err := r.Field()
		if err != nil {
			return col, err
		}
		if id == 0 {
			break
		}
		switch id {
		case fvType:
			col.Type, err = decodeType(r, 0)
			if err != nil {
				return col, err
			}
			hasType = true
		case fvValidity:
			if validity, err = r.Bytes(); err != nil {
				return col, err
			}
		case fvData:
			if data, err = r.Bytes(); err != nil {
				return col, err
			}
		case fvHeap:
			if heap, err = r.Bytes(); err != nil {
				return col, err
			}
		default:
			if err := r.Skip(kind); err != nil {
				return col, err
			}
		}
	}
	if !hasType {
		return col, fmt.Errorf("codec: vector missing type")
	}
	if validity != nil && len(validity) < (rows+7)/8 {
		return col, fmt.Errorf("codec: validity mask short: %d bytes for %d rows", len(validity), rows)
	}
	col.vals, col.err = decodeValues(col.Type, rows, validity, data, heap)
	// A malformed vector is a decode error for the whole chunk; an
	// unsupported type is only an error for this column.
	if col.err != nil {
		if _, ok := col.err.(ErrUnsupportedType); !ok {
			return col, col.err
		}
	}
	return col, nil
}

func valid(validity []byte, row int) bool {
	return validity == nil || validity[row/8]&(1<<(row%8)) != 0
}

func fixedWidth(id TypeID, width uint8) int {
	switch id {
	case TypeBoolean, TypeTinyint, TypeUTinyint:
		return 1
	case TypeSmallint, TypeUSmallint:
		return 2
	case TypeInteger, TypeUInteger, TypeFloat, TypeDate:
		return 4
	case TypeBigint, TypeUBigint, TypeDouble, TypeTime,
		TypeTimestampSec, TypeTimestampMS, TypeTimestamp, TypeTimestampNS:
		return 8
	case TypeDecimal:
		switch {
		case width <= 4:
			return 2
		case width <= 9:
			return 4
		case width <= 18:
			return 8
		default:
			return 16
		}
	case TypeVarchar, TypeBlob:
		return 16 // string_t entries; payload may continue in the heap
	}
	return 0
}

func decodeValues(t LogicalType, rows int, validity, data, heap []byte) ([]any, error) {
	w := fixedWidth(t.ID, t.Width)
	if w == 0 {
		return nil, ErrUnsupportedType{Type: t}
	}
	if len(data) < rows*w {
		return nil, fmt.Errorf("codec: %s vector data short: %d bytes for %d rows", t, len(data), rows)
	}
	vals := make([]any, rows)
	for i := 0; i < rows; i++ {
		if !valid(validity, i) {
			continue
		}
		cell := data[i*w : (i+1)*w]
		v, err := decodeCell(t, cell, heap)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

func decodeCell(t LogicalType, cell, heap []byte) (any, error) {
	switch t.ID {
	case TypeBoolean:
		return cell[0] != 0, nil
	case TypeTinyint:
		return int8(cell[0]), nil
	case TypeUTinyint:
		return uint8(cell[0]), nil
	case TypeSmallint:
		return int16(binary.LittleEndian.Uint16(cell)), nil
	case TypeUSmallint:
		return binary.LittleEndian.Uint16(cell), nil
	case TypeInteger:
		return int32(binary.LittleEndian.Uint32(cell)), nil
	case TypeUInteger:
		return binary.LittleEndian.Uint32(cell), nil
	case TypeBigint:
		return int64(binary.LittleEndian.Uint64(cell)), nil
	case TypeUBigint:
		return binary.LittleEndian.Uint64(cell), nil
	case TypeFloat:
		return math.Float32frombits(binary.LittleEndian.Uint32(cell)), nil
	case TypeDouble:
		return math.Float64frombits(binary.LittleEndian.Uint64(cell)), nil
	case TypeDate:
		return dateValue(int32(binary.LittleEndian.Uint32(cell))), nil
	case TypeTime:
		return Time(binary.LittleEndian.Uint64(cell)), nil
	case TypeTimestampSec, TypeTimestampMS, TypeTimestamp, TypeTimestampNS:
		return timestampValue(t.ID, int64(binary.LittleEndian.Uint64(cell))), nil
	case TypeDecimal:
		return decodeDecimalCell(t, cell), nil
	case TypeVarchar:
		b, err := stringCell(cell, heap)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case TypeBlob:
		b, err := stringCell(cell, heap)
		if err != nil {
			return nil, err
		}
		out := make([]byte, len(b))
		copy(out, b)
		return out, nil
	}
	return nil, ErrUnsupportedType{Type: t}
}

func decodeDecimalCell(t LogicalType, cell []byte) Decimal {
	switch len(cell) {
	case 2:
		v := int64(int16(binary.LittleEndian.Uint16(cell)))
		return newDecimal(t.Width, t.Scale, v>>63, uint64(v))
	case 4:
		v := int64(int32(binary.LittleEndian.Uint32(cell)))
		return newDecimal(t.Width, t.Scale, v>>63, uint64(v))
	case 8:
		v := int64(binary.LittleEndian.Uint64(cell))
		return newDecimal(t.Width, t.Scale, v>>63, uint64(v))
	default: // 16: lo then hi, little-endian throughout
		lo := binary.LittleEndian.Uint64(cell[:8])
		hi := int64(binary.LittleEndian.Uint64(cell[8:]))
		return newDecimal(t.Width, t.Scale, hi, lo)
	}
}

// stringCell reads one 16-byte string_t entry: uint32 length, then either
// the content inlined (length <= 12) or a 4-byte prefix and a uint64 offset
// into the heap. The prefix is duplicated from the content; a mismatch means
// the payload is corrupt.
func stringCell(cell, heap []byte) ([]byte, error) {
	n := binary.LittleEndian.Uint32(cell[:4])
	if n <= 12 {
		return cell[4 : 4+n], nil
	}
	off := binary.LittleEndian.Uint64(cell[8:16])
	if off > uint64(len(heap)) || uint64(n) > uint64(len(heap))-off {
		return nil, fmt.Errorf("codec: string heap reference out of range (off=%d len=%d heap=%d)", off, n, len(heap))
	}
	b := heap[off : off+uint64(n)]
	if string(cell[4:8]) != string(b[:4]) {
		return nil, fmt.Errorf("codec: string prefix mismatch, corrupt heap")
	}
	return b, nil
}
