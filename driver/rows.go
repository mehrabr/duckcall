package driver

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/mehrabr/duckcall"
	"github.com/mehrabr/duckcall/codec"
)

type rows struct {
	res    *duckcall.Result
	next   func() (*codec.Chunk, error, bool)
	stop   func()
	chunk  *codec.Chunk
	row    int
	closed bool
}

func newRows(ctx context.Context, res *duckcall.Result) *rows {
	next, stop := iter.Pull2(res.Chunks(ctx))
	return &rows{res: res, next: next, stop: stop}
}

func (r *rows) Columns() []string {
	cols := r.res.Schema().Columns
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return names
}

// ColumnTypeDatabaseTypeName lets callers see DECIMAL(18,2) and friends
// through sql.ColumnType.
func (r *rows) ColumnTypeDatabaseTypeName(index int) string {
	return r.res.Schema().Columns[index].Type.String()
}

func (r *rows) Next(dest []driver.Value) error {
	for r.chunk == nil || r.row >= r.chunk.RowCount() {
		chunk, err, ok := r.next()
		if !ok {
			return io.EOF
		}
		if err != nil {
			return err
		}
		r.chunk, r.row = chunk, 0
	}
	for i := range dest {
		v, err := r.chunk.Value(i, r.row)
		if err != nil {
			return err
		}
		dest[i], err = toDriverValue(r.chunk.Column(i).Type, v)
		if err != nil {
			return err
		}
	}
	r.row++
	return nil
}

func (r *rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.stop()
	// The protocol has no per-result release; this only stops any
	// still-running fetches.
	return r.res.Close(context.Background())
}

// toDriverValue narrows codec's Go types to the driver.Value set. Lossless
// or bust: anything that cannot cross exactly goes as a string. Nested
// values (LIST, STRUCT, MAP, ARRAY) cross as JSON — the one string form a
// database/sql caller can feed to a parser instead of regexing apart; the
// type tree rides along so element rendering stays type-exact.
func toDriverValue(t codec.LogicalType, v any) (driver.Value, error) {
	switch t.ID {
	case codec.TypeList, codec.TypeArray, codec.TypeStruct, codec.TypeMap:
		if v == nil {
			return nil, nil
		}
		b, err := appendJSON(nil, t, v)
		return string(b), err
	}
	switch x := v.(type) {
	case nil, bool, int64, float64, string, []byte:
		return x, nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint64:
		if x > math.MaxInt64 {
			return strconv.FormatUint(x, 10), nil
		}
		return int64(x), nil
	case float32:
		return float64(x), nil
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), nil
		}
		return x.String(), nil
	case codec.Decimal:
		return x.String(), nil
	case codec.Time:
		return x.String(), nil
	case codec.TimeNS:
		return x.String(), nil
	case codec.TimeTZ:
		return x.String(), nil
	case codec.Interval:
		return x.String(), nil
	case codec.UUID:
		return x.String(), nil
	case time.Time:
		return x, nil
	default:
		return nil, fmt.Errorf("duckcall: no driver.Value mapping for %T", v)
	}
}

// appendJSON renders a decoded nested value as JSON, walking the type tree
// in step with the value. Struct fields keep declaration order (and survive
// duplicate names, which encoding/json maps would not); map keys stringify
// through their scalar rendering, the same convention duckdb's to_json
// uses.
func appendJSON(b []byte, t codec.LogicalType, v any) ([]byte, error) {
	if v == nil {
		return append(b, "null"...), nil
	}
	switch t.ID {
	case codec.TypeList, codec.TypeArray:
		vals, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("duckcall: %s value is %T", t, v)
		}
		b = append(b, '[')
		var err error
		for i, e := range vals {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendJSON(b, *t.Child, e); err != nil {
				return nil, err
			}
		}
		return append(b, ']'), nil

	case codec.TypeStruct:
		s, ok := v.(codec.Struct)
		if !ok || len(s) != len(t.Fields) {
			return nil, fmt.Errorf("duckcall: %s value is %T", t, v)
		}
		b = append(b, '{')
		var err error
		for i, e := range s {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendJSONString(b, e.Name); err != nil {
				return nil, err
			}
			b = append(b, ':')
			if b, err = appendJSON(b, t.Fields[i].Type, e.Value); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil

	case codec.TypeMap:
		entries, ok := v.([]codec.MapEntry)
		if !ok || t.Child == nil || len(t.Child.Fields) != 2 {
			return nil, fmt.Errorf("duckcall: %s value is %T", t, v)
		}
		b = append(b, '{')
		for i, e := range entries {
			if i > 0 {
				b = append(b, ',')
			}
			key, err := toDriverValue(t.Child.Fields[0].Type, e.Key)
			if err != nil {
				return nil, err
			}
			if b, err = appendJSONString(b, keyString(key)); err != nil {
				return nil, err
			}
			b = append(b, ':')
			if b, err = appendJSON(b, t.Child.Fields[1].Type, e.Value); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil
	}

	// Scalar leaf. Numbers stay numbers — including HUGEINT and DECIMAL,
	// whose digit strings are valid JSON numbers — and everything temporal
	// or exotic goes through its duckdb-style rendering as a JSON string.
	dv, err := toDriverValue(t, v)
	if err != nil {
		return nil, err
	}
	switch x := dv.(type) {
	case bool:
		return strconv.AppendBool(b, x), nil
	case int64:
		return strconv.AppendInt(b, x, 10), nil
	case float64:
		return appendJSONFloat(b, x)
	case string:
		if jsonNumberSafe(t.ID) {
			return append(b, x...), nil
		}
		return appendJSONString(b, x)
	case []byte:
		return appendJSONString(b, string(x))
	case time.Time:
		return appendJSONString(b, timeString(t.ID, x))
	default:
		return nil, fmt.Errorf("duckcall: no JSON rendering for %T", dv)
	}
}

// jsonNumberSafe reports types whose string rendering is already a JSON
// number literal.
func jsonNumberSafe(id codec.TypeID) bool {
	switch id {
	case codec.TypeDecimal, codec.TypeHugeint, codec.TypeUHugeint, codec.TypeUBigint:
		return true
	}
	return false
}

func appendJSONFloat(b []byte, f float64) ([]byte, error) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		// JSON has no literals for these; duckdb's to_json emits strings.
		return appendJSONString(b, strconv.FormatFloat(f, 'g', -1, 64))
	}
	return strconv.AppendFloat(b, f, 'g', -1, 64), nil
}

func appendJSONString(b []byte, s string) ([]byte, error) {
	j, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append(b, j...), nil
}

// keyString flattens an already-converted map key to text.
func keyString(v driver.Value) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "null"
	default:
		return fmt.Sprint(x)
	}
}

// timeString renders DATE and the TIMESTAMP family the way duckdb casts
// them to VARCHAR; Go's .999 verbs drop trailing fraction zeros the same
// way duckdb does.
func timeString(id codec.TypeID, ts time.Time) string {
	switch id {
	case codec.TypeDate:
		return ts.Format("2006-01-02")
	case codec.TypeTimestampTZ:
		return ts.Format("2006-01-02 15:04:05.999999999") + "+00"
	default:
		return ts.Format("2006-01-02 15:04:05.999999999")
	}
}
