package driver

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"iter"
	"math"
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
		dest[i], err = toDriverValue(v)
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
// or bust: anything that cannot cross exactly goes as a string.
func toDriverValue(v any) (driver.Value, error) {
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
	case codec.Decimal:
		return x.String(), nil
	case codec.Time:
		return x.String(), nil
	case time.Time:
		return x, nil
	default:
		return nil, fmt.Errorf("duckcall: no driver.Value mapping for %T", v)
	}
}
