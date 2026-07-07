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

// TestTier2Values pins the driver-side story for the Tier 2 types: exotic
// scalars cross as their duckdb-style strings (or int64 when lossless),
// nested values cross as JSON.
func TestTier2Values(t *testing.T) {
	s := startServer(t)
	intT := codectest.T(codec.TypeInteger)
	strT := codectest.T(codec.TypeVarchar)
	kvT := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
		{Name: "key", Type: strT}, {Name: "value", Type: intT},
	}}
	ptT := codec.LogicalType{ID: codec.TypeStruct, Fields: []codec.StructField{
		{Name: "x", Type: intT}, {Name: "y", Type: strT},
	}}
	huge, _ := new(big.Int).SetString("170141183460469231731687303715884105727", 10)
	s.AddResult("FROM tier2", []codectest.Col{
		{Name: "h", Type: codectest.T(codec.TypeHugeint), Vals: []any{huge, nil}},
		{Name: "hs", Type: codectest.T(codec.TypeHugeint), Vals: []any{big.NewInt(-5), nil}},
		{Name: "u", Type: codectest.T(codec.TypeUUID), Vals: []any{
			codec.UUID{0xc8, 0x18, 0x5c, 0xa9, 0x1c, 0x05, 0x40, 0xc3, 0x8a, 0x22, 0x0f, 0x63, 0x28, 0x86, 0x28, 0x8c}, nil}},
		{Name: "iv", Type: codectest.T(codec.TypeInterval), Vals: []any{
			codec.Interval{Months: 1, Days: 2, Micros: 3_000_000}, nil}},
		{Name: "bits", Type: codectest.T(codec.TypeBit), Vals: []any{"10101", nil}},
		{Name: "li", Type: codec.LogicalType{ID: codec.TypeList, Child: &intT}, Vals: []any{
			[]any{int32(1), nil, int32(3)}, nil}},
		{Name: "st", Type: ptT, Vals: []any{
			codec.Struct{{Name: "x", Value: int32(7)}, {Name: "y", Value: "a\"b"}}, nil}},
		{Name: "m", Type: codec.LogicalType{ID: codec.TypeMap, Child: &kvT}, Vals: []any{
			[]codec.MapEntry{{Key: "a", Value: int32(1)}, {Key: "b", Value: nil}}, nil}},
	}, 2048)
	db := openDB(t, s)

	rows, err := db.Query("FROM tier2")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	rows.Next()
	var h, u, iv, bits, li, st, m string
	var hs int64
	if err := rows.Scan(&h, &hs, &u, &iv, &bits, &li, &st, &m); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][2]string{
		"h":    {h, "170141183460469231731687303715884105727"},
		"u":    {u, "c8185ca9-1c05-40c3-8a22-0f632886288c"},
		"iv":   {iv, "1 month 2 days 00:00:03"},
		"bits": {bits, "10101"},
		"li":   {li, "[1,null,3]"},
		"st":   {st, `{"x":7,"y":"a\"b"}`},
		"m":    {m, `{"a":1,"b":null}`},
	} {
		if got[0] != got[1] {
			t.Errorf("%s: got %q, want %q", name, got[0], got[1])
		}
	}
	if hs != -5 {
		t.Errorf("hs: got %d, want -5", hs)
	}

	rows.Next()
	nulls := make([]sql.NullString, 7)
	var hn sql.NullInt64
	if err := rows.Scan(&nulls[0], &hn, &nulls[1], &nulls[2], &nulls[3], &nulls[4], &nulls[5], &nulls[6]); err != nil {
		t.Fatal(err)
	}
	for i, n := range nulls {
		if n.Valid {
			t.Errorf("column %d: want NULL, got %q", i, n.String)
		}
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
	s.FetchDelay = 20 * time.Millisecond
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

// TestExpiredSessionRetries: a pooled connection whose server-side session
// died (restart, out-of-band disconnect) maps to driver.ErrBadConn at query
// submission, so database/sql retires it and retries on a fresh connection
// — invisible to the caller. Mid-result expiry stays an error; see
// TestExpiredSessionMidResultFails.
func TestExpiredSessionRetries(t *testing.T) {
	s := startServer(t)
	s.AddResult("SELECT 1", []codectest.Col{
		{Name: "one", Type: codectest.T(codec.TypeInteger), Vals: []any{int32(1)}},
	}, 2048)
	db := openDB(t, s)
	db.SetMaxOpenConns(1)

	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatal(err)
	}
	s.ExpireConnections()
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("query after expiry: %v (one=%d)", err, one)
	}
	if s.OpenConnections() != 1 {
		t.Fatalf("open connections: %d, want the one retried conn", s.OpenConnections())
	}
}

func TestExpiredSessionMidResultFails(t *testing.T) {
	s := startServer(t)
	vals := make([]any, 100)
	for i := range vals {
		vals[i] = int32(i)
	}
	s.AddResult("FROM big", []codectest.Col{{Name: "i", Type: codectest.T(codec.TypeInteger), Vals: vals}}, 10)
	db := openDB(t, s)

	rows, err := db.Query("FROM big")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no first row")
	}
	s.ExpireConnections()
	for rows.Next() {
	}
	// Losing the session mid-stream loses unfetched batches; that must be
	// an error, never a silently short result.
	if err := rows.Err(); err == nil {
		t.Fatal("mid-result expiry did not surface through rows.Err")
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
