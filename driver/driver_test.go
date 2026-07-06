package driver_test

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/mehrabr/duckcall/codec"
	"github.com/mehrabr/duckcall/codec/codectest"
	"github.com/mehrabr/duckcall/driver"
	"github.com/mehrabr/duckcall/internal/quacktest"
)

func startServer(t *testing.T) *quacktest.Server {
	t.Helper()
	s := quacktest.New("tok-abc")
	t.Cleanup(s.Close)
	return s
}

func openDB(t *testing.T, s *quacktest.Server) *sql.DB {
	t.Helper()
	t.Setenv("QUACK_TOKEN", "tok-abc")
	host := strings.TrimPrefix(s.URL(), "http://")
	db, err := sql.Open("duckcall", "quack://"+host+"?token=env:QUACK_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestQuickstart runs the README quickstart, verbatim SQL included.
func TestQuickstart(t *testing.T) {
	s := startServer(t)
	s.AddResult("SELECT product, sum(total) FROM sales GROUP BY 1", []codectest.Col{
		{Name: "product", Type: codectest.T(codec.TypeVarchar), Vals: []any{"anvil", "rocket skates"}},
		{Name: "sum(total)", Type: codectest.T(codec.TypeBigint), Vals: []any{int64(12), int64(34)}},
	}, 2048)
	db := openDB(t, s)

	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "SELECT product, sum(total) FROM sales GROUP BY 1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var product string
		var total int64
		if err := rows.Scan(&product, &total); err != nil {
			t.Fatal(err)
		}
		got = append(got, product)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "anvil" {
		t.Fatalf("rows: %v", got)
	}
	if s.OpenQueries() != 0 {
		t.Fatal("result leaked on server after rows.Close")
	}
}

func TestNullsAndTypes(t *testing.T) {
	s := startServer(t)
	when := time.Date(2026, 5, 12, 8, 0, 0, 123456000, time.UTC)
	s.AddResult("FROM t", []codectest.Col{
		{Name: "s", Type: codectest.T(codec.TypeVarchar), Vals: []any{"x", nil}},
		{Name: "f", Type: codectest.T(codec.TypeDouble), Vals: []any{2.5, nil}},
		{Name: "ts", Type: codectest.T(codec.TypeTimestamp), Vals: []any{when, nil}},
		{Name: "d", Type: codectest.DecimalT(18, 2), Vals: []any{codec.NewDecimal(18, 2, big.NewInt(-12345)), nil}},
	}, 2048)
	db := openDB(t, s)

	rows, err := db.Query("FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	types, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	if types[3].DatabaseTypeName() != "DECIMAL(18,2)" {
		t.Errorf("decimal type name: %s", types[3].DatabaseTypeName())
	}

	rows.Next()
	var (
		sv sql.NullString
		fv sql.NullFloat64
		tv sql.NullTime
		dv sql.NullString
	)
	if err := rows.Scan(&sv, &fv, &tv, &dv); err != nil {
		t.Fatal(err)
	}
	if sv.String != "x" || fv.Float64 != 2.5 || !tv.Time.Equal(when) || dv.String != "-123.45" {
		t.Fatalf("row 1: %v %v %v %v", sv, fv, tv, dv)
	}
	rows.Next()
	if err := rows.Scan(&sv, &fv, &tv, &dv); err != nil {
		t.Fatal(err)
	}
	if sv.Valid || fv.Valid || tv.Valid || dv.Valid {
		t.Fatalf("row 2 should be all NULL: %v %v %v %v", sv, fv, tv, dv)
	}
}

func TestInterpolation(t *testing.T) {
	s := startServer(t)
	want := "SELECT * FROM t WHERE name = 'o''brien' AND n = 42 AND ok = TRUE -- ? stays"
	s.AddResult(want, []codectest.Col{
		{Name: "n", Type: codectest.T(codec.TypeInteger), Vals: []any{int32(1)}},
	}, 2048)
	db := openDB(t, s)

	rows, err := db.Query("SELECT * FROM t WHERE name = ? AND n = ? AND ok = ? -- ? stays",
		"o'brien", 42, true)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()

	// Arg-count mismatches must fail before anything hits the wire.
	if _, err := db.Query("SELECT ?", 1, 2); err == nil {
		t.Fatal("extra args accepted")
	}
	if _, err := db.Query("SELECT ?, ?", 1); err == nil {
		t.Fatal("missing args accepted")
	}
}

func TestExecRejected(t *testing.T) {
	s := startServer(t)
	db := openDB(t, s)
	if _, err := db.Exec("DELETE FROM t"); !errors.Is(err, driver.ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
	if _, err := db.Begin(); err == nil {
		t.Fatal("transactions accepted on a read-only client")
	}
}

func TestQueryCancellation(t *testing.T) {
	s := startServer(t)
	s.ChunkDelay = 20 * time.Millisecond
	vals := make([]any, 500)
	for i := range vals {
		vals[i] = int32(i)
	}
	s.AddResult("FROM big", []codectest.Col{{Name: "i", Type: codectest.T(codec.TypeInteger), Vals: vals}}, 10)
	db := openDB(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows, err := db.QueryContext(ctx, "FROM big")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if n == 15 {
			cancel()
		}
	}
	if err := rows.Err(); err == nil {
		t.Fatal("cancellation did not surface through rows.Err")
	}
	if n >= 500 {
		t.Fatal("stream ran to completion despite cancellation")
	}
}

func TestDSNErrors(t *testing.T) {
	cases := []string{
		"postgres://h:1?token=x",
		"quack://h:1?token=env:DOES_NOT_EXIST_XYZ",
		"quack://h:1?tokn=x",
		"quack://user:pass@h:1?token=x",
		"quack://?token=x",
	}
	for _, dsn := range cases {
		if _, err := driver.ParseDSN(dsn); err == nil {
			t.Errorf("ParseDSN(%q) accepted", dsn)
		}
	}
	cfg, err := driver.ParseDSN("quacks://h:8888/base?fetch_workers=8&connect_timeout=3s&max_retries=5&token=abc")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://h:8888/base" || cfg.Token != "abc" || cfg.FetchWorkers != 8 ||
		cfg.ConnectTimeout != 3*time.Second || cfg.MaxRetries != 5 {
		t.Fatalf("cfg: %+v", cfg)
	}
}

// Compile-time proof the driver exposes the modern interfaces.
var (
	_ sqldriver.DriverContext = (*driver.Driver)(nil)
)
