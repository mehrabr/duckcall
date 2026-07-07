// Package driver is duckcall's database/sql driver.
//
//	import _ "github.com/mehrabr/duckcall/driver"
//
//	db, err := sql.Open("duckcall", "quack://analytics.internal:8888?token=env:QUACK_TOKEN")
//
// The driver is read-only, like the client under it: Query works, Exec
// returns an error. Placeholders are interpolated client-side because the
// wire has no bind message yet; see interpolate.go.
package driver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"github.com/mehrabr/duckcall"
	"github.com/mehrabr/duckcall/wire"
)

func init() {
	sql.Register("duckcall", &Driver{})
}

// ErrReadOnly is returned for Exec and anything else that implies writes.
var ErrReadOnly = errors.New("duckcall: read-only client, Exec is not supported")

type Driver struct{}

func (d *Driver) Open(dsn string) (driver.Conn, error) {
	c, err := d.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return c.Connect(context.Background())
}

func (d *Driver) OpenConnector(dsn string) (driver.Connector, error) {
	cfg, err := ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	return &connector{cfg: cfg, drv: d}, nil
}

type connector struct {
	cfg duckcall.Config
	drv *Driver
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	dc, err := duckcall.Dial(ctx, c.cfg)
	if err != nil {
		return nil, err
	}
	return &conn{dc: dc}, nil
}

func (c *connector) Driver() driver.Driver { return c.drv }

type conn struct {
	dc *duckcall.Conn
}

var (
	_ driver.QueryerContext = (*conn)(nil)
	_ driver.ExecerContext  = (*conn)(nil)
)

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	sql, err := interpolate(query, args)
	if err != nil {
		return nil, err
	}
	res, err := c.dc.Query(ctx, sql)
	if err != nil {
		// An expired session at submission means the query never ran:
		// ErrBadConn is safe here and lets database/sql retire this conn
		// and retry on a fresh one. It is NOT safe mid-result — a lost
		// connection loses fetched-but-undelivered batches, and rows.Next
		// must surface that as the error it is, never as a silent retry.
		if errors.Is(err, wire.ErrConnectionExpired) {
			return nil, driver.ErrBadConn
		}
		return nil, err
	}
	return newRows(ctx, res), nil
}

func (c *conn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, ErrReadOnly
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return &stmt{conn: c, query: query}, nil
}

func (c *conn) Close() error {
	// database/sql retires conns outside any caller context; bound the
	// goodbye so a dead server cannot wedge the pool.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.dc.Close(ctx)
	if errors.Is(err, wire.ErrConnectionExpired) {
		// Disconnecting a session the server already forgot is the outcome
		// Close wanted.
		return nil
	}
	return err
}

func (c *conn) Begin() (driver.Tx, error) {
	return nil, errors.New("duckcall: transactions are not supported (read-only client)")
}

type stmt struct {
	conn  *conn
	query string
}

func (s *stmt) Close() error  { return nil }
func (s *stmt) NumInput() int { return -1 }

func (s *stmt) Exec([]driver.Value) (driver.Result, error) { return nil, ErrReadOnly }

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, a := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: a}
	}
	return s.conn.QueryContext(context.Background(), s.query, named)
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.QueryContext(ctx, s.query, args)
}
