package codec

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/mehrabr/duckcall/internal/qser"
)

// Schema is the decoded result header: column names and types. On the wire
// it arrives as two parallel lists in the PREPARE_RESPONSE.
type Schema struct {
	Columns []SchemaColumn
}

type SchemaColumn struct {
	Name string
	Type LogicalType
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

func (cl *Column) Err() error { return cl.err }

func (cl *Column) Value(row int) any { return cl.vals[row] }

// VectorType values a serialized vector can carry in field 90; absent means
// flat. Compressed forms appear because the server serializes at
// compatibility >= 5.
const (
	vecFlat       = 0
	vecFSST       = 1
	vecConstant   = 2
	vecDictionary = 3
	vecSequence   = 4
)

// A duckdb DataChunk holds at most STANDARD_VECTOR_SIZE rows; anything far
// beyond it is a corrupt or hostile length.
const maxChunkRows = 1 << 16

// maxVectorRows bounds any single vector's row count, including nested
// children, where list_size legitimately exceeds the chunk's row count. The
// cap matters because a child can serialize as a constant vector — a few
// bytes of input demanding list_size materialized values — so input length
// alone does not bound allocation. 16M child rows is orders of magnitude
// past any real batch payload.
const maxVectorRows = 1 << 24

// decodeChunkContents reads one DataChunk object's fields: 100 row count,
// 101 the column types, 102 the vectors. Chunks are self-describing — the
// types ride in every chunk — so decoding needs no schema.
func decodeChunkContents(r *qser.Reader) (*Chunk, error) {
	ch := &Chunk{}
	if !r.TryField(100) {
		return nil, fmt.Errorf("codec: chunk missing row count")
	}
	rows := r.Uvarint()
	if rows > maxChunkRows {
		return nil, fmt.Errorf("codec: implausible row count %d", rows)
	}
	ch.rows = int(rows)

	if !r.TryField(101) {
		return nil, fmt.Errorf("codec: chunk missing types")
	}
	ntypes := r.ListCount()
	types := make([]LogicalType, 0, ntypes)
	for range ntypes {
		t, err := decodeType(r, 0)
		if err != nil {
			return nil, err
		}
		types = append(types, t)
	}

	if !r.TryField(102) {
		return nil, fmt.Errorf("codec: chunk missing columns")
	}
	ncols := r.ListCount()
	if ncols != len(types) {
		return nil, fmt.Errorf("codec: chunk has %d columns but %d types", ncols, len(types))
	}
	ch.cols = make([]Column, 0, ncols)
	for _, t := range types {
		col := Column{Type: t}
		vals, err := decodeVector(r, t, ch.rows, 0)
		switch {
		case err == nil:
			col.vals = vals
		case isUnsupported(err):
			// The bytes parsed; only the values are beyond this codec.
			col.err = err
		default:
			return nil, fmt.Errorf("codec: %s column: %w", t, err)
		}
		r.End() // each vector is one list-element object
		if err := r.Err(); err != nil {
			return nil, fmt.Errorf("codec: %s column: %w", t, err)
		}
		ch.cols = append(ch.cols, col)
	}
	r.End()
	return ch, r.Err()
}

// decodeVector reads one serialized vector's fields (not its object
// terminator — lists and nested children share this) and returns the row
// values. A type this codec cannot produce values for still parses, so the
// stream stays in sync; it reports ErrUnsupportedType as the column error.
func decodeVector(r *qser.Reader, t LogicalType, count, depth int) ([]any, error) {
	if depth > maxTypeDepth {
		return nil, fmt.Errorf("codec: vector nesting deeper than %d", maxTypeDepth)
	}
	if count > maxVectorRows {
		return nil, fmt.Errorf("codec: implausible vector row count %d", count)
	}
	vtype := uint64(vecFlat)
	if r.TryField(90) {
		vtype = r.Uvarint()
	}
	switch vtype {
	case vecFlat:
		return decodeFlatVector(r, t, count, depth)

	case vecConstant:
		vals, err := decodeVector(r, t, 1, depth+1)
		if err != nil || len(vals) == 0 {
			return nil, err
		}
		out := make([]any, count)
		for i := range out {
			out[i] = vals[0]
		}
		return out, nil

	case vecDictionary:
		if !r.TryField(91) {
			return nil, fmt.Errorf("codec: dictionary vector missing selection")
		}
		sel := r.Bytes()
		if len(sel) != count*4 {
			return nil, fmt.Errorf("codec: dictionary selection is %d bytes for %d rows", len(sel), count)
		}
		if !r.TryField(92) {
			return nil, fmt.Errorf("codec: dictionary vector missing dict count")
		}
		dictCount := r.Uvarint()
		if dictCount > maxChunkRows {
			return nil, fmt.Errorf("codec: implausible dictionary size %d", dictCount)
		}
		dict, err := decodeVector(r, t, int(dictCount), depth+1)
		if err != nil || dict == nil {
			return nil, err
		}
		out := make([]any, count)
		for i := range out {
			idx := binary.LittleEndian.Uint32(sel[i*4:])
			if idx >= uint32(len(dict)) {
				return nil, fmt.Errorf("codec: dictionary index %d out of range %d", idx, len(dict))
			}
			out[i] = dict[idx]
		}
		return out, nil

	case vecSequence:
		if !r.TryField(91) {
			return nil, fmt.Errorf("codec: sequence vector missing start")
		}
		start := r.Svarint()
		if !r.TryField(92) {
			return nil, fmt.Errorf("codec: sequence vector missing increment")
		}
		inc := r.Svarint()
		out := make([]any, count)
		for i := range out {
			v, err := sequenceValue(t.ID, start+int64(i)*inc)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil

	default:
		// FSST and anything newer cannot be parsed past without a schema.
		return nil, fmt.Errorf("codec: unsupported vector encoding %d", vtype)
	}
}

func decodeFlatVector(r *qser.Reader, t LogicalType, count, depth int) ([]any, error) {
	unsupported := false
	if r.TryField(99) {
		r.Uvarint() // geometry storage format tag; the payload is WKB blobs
		unsupported = true
	}
	if !r.TryField(100) {
		return nil, fmt.Errorf("codec: vector missing validity flag")
	}
	hasNulls := r.Bool()
	var validity []byte
	if hasNulls {
		if !r.TryField(101) {
			return nil, fmt.Errorf("codec: vector claims nulls but has no validity mask")
		}
		validity = r.Bytes()
		if len(validity) < (count+7)/8 {
			return nil, fmt.Errorf("codec: validity mask short: %d bytes for %d rows", len(validity), count)
		}
	}

	switch t.ID {
	case TypeVarchar, TypeBlob, TypeBit, TypeGeometry:
		// Variable-width data is a list of length-prefixed strings; NULL
		// slots ride as empty entries.
		if t.ID == TypeGeometry {
			// WKB blobs parse fine; producing geometry values is out of
			// scope for this codec.
			unsupported = true
		}
		if !r.TryField(102) {
			return nil, fmt.Errorf("codec: %s vector missing data", t)
		}
		n := r.ListCount()
		if n != count {
			return nil, fmt.Errorf("codec: %s vector has %d entries for %d rows", t, n, count)
		}
		vals := make([]any, count)
		for i := range count {
			b := r.Bytes()
			if !valid(validity, i) || unsupported {
				continue
			}
			switch t.ID {
			case TypeVarchar:
				vals[i] = string(b)
			case TypeBit:
				s, err := bitString(b)
				if err != nil {
					return nil, err
				}
				vals[i] = s
			default:
				vals[i] = append([]byte(nil), b...)
			}
		}
		if unsupported {
			return nil, unsupportedButParsed(r, t)
		}
		return vals, r.Err()

	case TypeStruct, TypeUnion, TypeVariant:
		// All three store as one child vector per field, each with its own
		// validity. UNION (tag child first) and VARIANT (fixed internal
		// shape) share the storage; their fields ride in the type info, so
		// they parse structurally, but only plain STRUCT produces values.
		if !r.TryField(103) {
			return nil, fmt.Errorf("codec: %s vector missing children", t)
		}
		n := r.ListCount()
		if n != len(t.Fields) {
			return nil, fmt.Errorf("codec: %s vector has %d children for %d fields", t, n, len(t.Fields))
		}
		children := make([][]any, n)
		for i := range n {
			vals, err := decodeVector(r, t.Fields[i].Type, count, depth+1)
			switch {
			case err == nil:
				children[i] = vals
			case isUnsupported(err):
				unsupported = true
			default:
				return nil, err
			}
			r.End()
		}
		if unsupported || t.ID != TypeStruct {
			return nil, unsupportedButParsed(r, t)
		}
		vals := make([]any, count)
		for row := range count {
			if !valid(validity, row) {
				continue
			}
			s := make(Struct, n)
			for i := range n {
				s[i] = StructEntry{Name: t.Fields[i].Name, Value: children[i][row]}
			}
			vals[row] = s
		}
		return vals, r.Err()

	case TypeList, TypeMap:
		if !r.TryField(104) {
			return nil, fmt.Errorf("codec: list vector missing size")
		}
		listSize := r.Uvarint()
		if listSize > maxVectorRows {
			return nil, fmt.Errorf("codec: implausible list size %d", listSize)
		}
		if !r.TryField(105) {
			return nil, fmt.Errorf("codec: list vector missing entries")
		}
		n := r.ListCount()
		if n != count {
			return nil, fmt.Errorf("codec: %s vector has %d entries for %d rows", t, n, count)
		}
		offs := make([]uint64, n)
		lens := make([]uint64, n)
		for i := range n {
			if r.TryField(100) {
				offs[i] = r.Uvarint()
			}
			if r.TryField(101) {
				lens[i] = r.Uvarint()
			}
			r.End()
		}
		if !r.TryField(106) {
			return nil, fmt.Errorf("codec: list vector missing child")
		}
		if t.Child == nil {
			return nil, fmt.Errorf("codec: %s vector without child type", t)
		}
		childVals, childErr := decodeVector(r, *t.Child, int(listSize), depth+1)
		if childErr != nil && !isUnsupported(childErr) {
			return nil, childErr
		}
		r.End()
		if unsupported {
			return nil, unsupportedButParsed(r, t)
		}
		if childErr != nil {
			// Report the child's error so the message names the type the
			// codec actually cannot decode.
			if err := r.Err(); err != nil {
				return nil, err
			}
			return nil, childErr
		}
		vals := make([]any, count)
		for i := range count {
			if !valid(validity, i) {
				continue
			}
			off, ln := offs[i], lens[i]
			if ln > listSize || off > listSize-ln {
				return nil, fmt.Errorf("codec: list entry [%d,%d) outside child of %d", off, off+ln, listSize)
			}
			if t.ID == TypeMap {
				entries := make([]MapEntry, ln)
				for j := range entries {
					// The child of a MAP is STRUCT(key, value); rows are
					// never NULL on a real wire, so a nil stays a zero entry.
					if s, ok := childVals[off+uint64(j)].(Struct); ok && len(s) == 2 {
						entries[j] = MapEntry{Key: s[0].Value, Value: s[1].Value}
					}
				}
				vals[i] = entries
				continue
			}
			row := make([]any, ln)
			copy(row, childVals[off:off+ln])
			vals[i] = row
		}
		return vals, r.Err()

	case TypeArray:
		if !r.TryField(103) {
			return nil, fmt.Errorf("codec: array vector missing size")
		}
		size := r.Uvarint()
		if size > maxVectorRows || int(size)*count > maxVectorRows {
			return nil, fmt.Errorf("codec: implausible array size %d", size)
		}
		if !r.TryField(104) {
			return nil, fmt.Errorf("codec: array vector missing child")
		}
		if t.Child == nil {
			return nil, fmt.Errorf("codec: %s vector without child type", t)
		}
		childVals, childErr := decodeVector(r, *t.Child, int(size)*count, depth+1)
		if childErr != nil && !isUnsupported(childErr) {
			return nil, childErr
		}
		r.End()
		if unsupported {
			return nil, unsupportedButParsed(r, t)
		}
		if childErr != nil {
			if err := r.Err(); err != nil {
				return nil, err
			}
			return nil, childErr
		}
		vals := make([]any, count)
		for i := range count {
			if !valid(validity, i) {
				continue
			}
			row := make([]any, size)
			copy(row, childVals[i*int(size):])
			vals[i] = row
		}
		return vals, r.Err()

	default:
		// Everything else is constant-size storage: one raw blob.
		w := physicalWidth(t)
		if w == 0 {
			// No known width means no way to parse the blob's framing —
			// but the blob is length-prefixed, so consume it and degrade.
			if r.TryField(102) {
				r.Bytes()
			}
			return nil, unsupportedButParsed(r, t)
		}
		if !r.TryField(102) {
			return nil, fmt.Errorf("codec: %s vector missing data", t)
		}
		data := r.Bytes()
		if len(data) < count*w {
			return nil, fmt.Errorf("codec: %s vector data short: %d bytes for %d rows", t, len(data), count)
		}
		if unsupported || !supportedScalar(t) {
			return nil, unsupportedButParsed(r, t)
		}
		vals := make([]any, count)
		for i := range count {
			if !valid(validity, i) {
				continue
			}
			v, err := decodeCell(t, data[i*w:(i+1)*w])
			if err != nil {
				return nil, err
			}
			vals[i] = v
		}
		return vals, r.Err()
	}
}

// timeTZMaxOffset is dtime_tz_t's MAX_OFFSET: ±15:59:59 in seconds. Offsets
// are stored as MAX_OFFSET - offset so bigger offsets sort earlier.
const timeTZMaxOffset = 16*60*60 - 1

// bitString renders duckdb's BIT storage: one leading byte holding the
// count of padding bits at the front of the first data byte, then the bits
// MSB-first.
func bitString(b []byte) (string, error) {
	if len(b) == 0 {
		return "", fmt.Errorf("codec: BIT value with no padding byte")
	}
	padding := int(b[0])
	if padding > 7 || (len(b) == 1 && padding != 0) {
		return "", fmt.Errorf("codec: BIT value claims %d padding bits over %d bytes", padding, len(b)-1)
	}
	if len(b) == 1 {
		return "", nil
	}
	out := make([]byte, 0, (len(b)-1)*8-padding)
	for i, by := range b[1:] {
		start := 0
		if i == 0 {
			start = padding
		}
		for bit := start; bit < 8; bit++ {
			out = append(out, '0'+(by>>(7-bit))&1)
		}
	}
	return string(out), nil
}

// unsupportedButParsed reports a column whose bytes were consumed correctly
// but whose values this codec does not produce. The distinction matters:
// the chunk stays decodable, only this column errors.
func unsupportedButParsed(r *qser.Reader, t LogicalType) error {
	if err := r.Err(); err != nil {
		return err
	}
	return ErrUnsupportedType{Type: t}
}

func isUnsupported(err error) bool {
	_, ok := err.(ErrUnsupportedType)
	return ok
}

func valid(validity []byte, row int) bool {
	return validity == nil || validity[row/8]&(1<<(row%8)) != 0
}

// physicalWidth is the storage size of a constant-size type; 0 for types
// stored variably or unknown to this codec.
func physicalWidth(t LogicalType) int {
	switch t.ID {
	case TypeBoolean, TypeTinyint, TypeUTinyint:
		return 1
	case TypeSmallint, TypeUSmallint:
		return 2
	case TypeInteger, TypeUInteger, TypeFloat, TypeDate:
		return 4
	case TypeBigint, TypeUBigint, TypeDouble, TypeTime, TypeTimeTZ, TypeTimeNS,
		TypeTimestampSec, TypeTimestampMS, TypeTimestamp, TypeTimestampNS, TypeTimestampTZ:
		return 8
	case TypeInterval, TypeHugeint, TypeUHugeint, TypeUUID:
		return 16
	case TypeDecimal:
		switch {
		case t.Width <= 4:
			return 2
		case t.Width <= 9:
			return 4
		case t.Width <= 18:
			return 8
		default:
			return 16
		}
	case TypeEnum:
		return enumIndexWidth(len(t.Enum))
	}
	return 0
}

// supportedScalar reports whether decodeCell produces values for this type.
// Types outside this set still parse; they just error per column.
func supportedScalar(t LogicalType) bool {
	switch t.ID {
	case TypeBoolean, TypeTinyint, TypeUTinyint, TypeSmallint, TypeUSmallint,
		TypeInteger, TypeUInteger, TypeBigint, TypeUBigint,
		TypeFloat, TypeDouble, TypeDecimal, TypeDate, TypeTime,
		TypeTimestampSec, TypeTimestampMS, TypeTimestamp, TypeTimestampNS, TypeTimestampTZ,
		TypeEnum, TypeHugeint, TypeUHugeint, TypeUUID, TypeInterval, TypeTimeTZ, TypeTimeNS:
		return true
	}
	return false
}

func decodeCell(t LogicalType, cell []byte) (any, error) {
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
	case TypeTimestampSec, TypeTimestampMS, TypeTimestamp, TypeTimestampNS, TypeTimestampTZ:
		return timestampValue(t.ID, int64(binary.LittleEndian.Uint64(cell))), nil
	case TypeDecimal:
		return decodeDecimalCell(t, cell), nil
	case TypeHugeint:
		// hugeint_t stores lower first, upper second, little-endian halves.
		return int128Big(int64(binary.LittleEndian.Uint64(cell[8:])), binary.LittleEndian.Uint64(cell[:8])), nil
	case TypeUHugeint:
		return uint128Big(binary.LittleEndian.Uint64(cell[8:]), binary.LittleEndian.Uint64(cell[:8])), nil
	case TypeUUID:
		// Stored as a hugeint with the sign bit flipped so numeric order
		// matches UUID order; flip back and lay the halves out big-endian.
		var u UUID
		binary.BigEndian.PutUint64(u[:8], binary.LittleEndian.Uint64(cell[8:])^(1<<63))
		binary.BigEndian.PutUint64(u[8:], binary.LittleEndian.Uint64(cell[:8]))
		return u, nil
	case TypeInterval:
		return Interval{
			Months: int32(binary.LittleEndian.Uint32(cell)),
			Days:   int32(binary.LittleEndian.Uint32(cell[4:])),
			Micros: int64(binary.LittleEndian.Uint64(cell[8:])),
		}, nil
	case TypeTimeTZ:
		// dtime_tz_t packs micros into the top 40 bits and the UTC offset,
		// biased and reverse-ordered, into the low 24.
		bits := binary.LittleEndian.Uint64(cell)
		return TimeTZ{
			Micros: int64(bits >> 24),
			Offset: timeTZMaxOffset - int32(bits&0xFFFFFF),
		}, nil
	case TypeTimeNS:
		return TimeNS(binary.LittleEndian.Uint64(cell)), nil
	case TypeEnum:
		var idx int
		switch len(cell) {
		case 1:
			idx = int(cell[0])
		case 2:
			idx = int(binary.LittleEndian.Uint16(cell))
		default:
			idx = int(binary.LittleEndian.Uint32(cell))
		}
		if idx >= len(t.Enum) {
			return nil, fmt.Errorf("codec: enum index %d outside dictionary of %d", idx, len(t.Enum))
		}
		return t.Enum[idx], nil
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

// sequenceValue materializes one element of a SEQUENCE vector, which only
// integer types produce.
func sequenceValue(id TypeID, v int64) (any, error) {
	switch id {
	case TypeTinyint:
		return int8(v), nil
	case TypeSmallint:
		return int16(v), nil
	case TypeInteger:
		return int32(v), nil
	case TypeBigint:
		return v, nil
	case TypeUTinyint:
		return uint8(v), nil
	case TypeUSmallint:
		return uint16(v), nil
	case TypeUInteger:
		return uint32(v), nil
	case TypeUBigint:
		return uint64(v), nil
	}
	return nil, fmt.Errorf("codec: sequence vector of non-integer type %d", id)
}
