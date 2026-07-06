package driver

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"iter"
	"math"
	"time"

	"github.com/mehrabr/duckcall"
	"github.com/mehrabr/duckcall/codec"
)

// releaseTimeout bounds the DELETE that frees a server-side result when the
// original query context is already gone.
const releaseTimeout = 5 * time.Second

type rows struct {
	res    *duckcall.Result
	ctx    context.Context
	next   func() (*codec.Chunk, error, bool)
	stop   func()
	chunk  *codec.Chunk
	row    int
	closed bool
}

func newRows(ctx context.Context, res *duckcall.Result) *rows {
	next, stop := iter.Pull2(res.Chunks(ctx))
	return &rows{res: res, ctx: ctx, next: next, stop: stop}
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
	// Release the server-side result even if the caller abandoned the
	// stream early; the query context may already be dead, so use a fresh
	// bounded one.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), releaseTimeout)
	defer cancel()
	return r.res.Close(ctx)
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
			return fmt.Sprintf("%d", x), nil
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
