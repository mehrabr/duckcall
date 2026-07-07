package duckcall_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mehrabr/duckcall"
	"github.com/mehrabr/duckcall/codec"
	_ "github.com/mehrabr/duckcall/driver"
)

// The live suite runs the full type matrix against a real quack_serve and
// diffs duckcall's decoding against the official client (quack_query),
// cell by cell. It is gated behind QUACK_LIVE=1 because it spawns duckdb —
// which must be on PATH with the quack extension installable — and is the
// spec's Phase 5 conformance harness.

const liveToken = "live-test-token"

// liveSQL sets up every table the suite queries. tier2, listlist, mapint,
// and uni deliberately match testdata/corpus/frames/README.md, so the same
// expectations pin both the captured frames and the live server.
const liveSQL = `
INSTALL quack;
LOAD quack;
CREATE TYPE mood AS ENUM ('happy', 'sad', 'ok');
CREATE TABLE tier1 AS SELECT * FROM (VALUES
  (1, true, (-8)::TINYINT, 200::UTINYINT, (-300)::SMALLINT, 60000::USMALLINT,
   (-70000)::INTEGER, 4000000000::UINTEGER, (-1099511627776)::BIGINT,
   18446744073709551615::UBIGINT, 1.5::FLOAT, 2.75::DOUBLE,
   123.4::DECIMAL(4,1), 1234.56::DECIMAL(9,2), 123456789.123::DECIMAL(18,3),
   1234567890123456.789012::DECIMAL(38,6), 'o''brien &日本', '\x01A'::BLOB,
   DATE '2026-07-04', TIME '12:30:15.123456', TIMESTAMP '2026-07-04 12:30:15.5',
   TIMESTAMP_S '2026-07-04 12:30:15', TIMESTAMP_MS '2026-07-04 12:30:15.123',
   TIMESTAMP_NS '2026-07-04 12:30:15.123456789',
   TIMESTAMPTZ '2026-07-04 12:30:15.5+00', 'sad'::mood, 3.4028235e38::FLOAT),
  (2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
   NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
   NULL, NULL)
) t(id, b, i8, u8, i16, u16, i32, u32, i64, u64, f32, f64, d4, d9, d18, d38,
    s, bl, d, tm, ts, ts_s, ts_ms, ts_ns, tstz, mood, f32x);
CREATE TABLE tier2 AS SELECT * FROM (VALUES
  (1, [1, NULL, 3]::INTEGER[], {'id': 7, 'pt': {'x': 1, 'y': 'a'}},
   MAP {'a': 1, 'b': NULL}, [1, NULL, 3]::INTEGER[3],
   170141183460469231731687303715884105727::HUGEINT,
   340282366920938463463374607431768211455::UHUGEINT,
   'c8185ca9-1c05-40c3-8a22-0f632886288c'::UUID,
   INTERVAL '1 year 2 months 3 days 4 seconds 500 milliseconds',
   TIMETZ '12:30:15+05:30', '12:30:15.123456789'::TIME_NS, '010110110'::BIT),
  (2, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
  (3, []::INTEGER[], {'id': NULL, 'pt': NULL}, MAP {}, [0, 0, 0]::INTEGER[3],
   (-170141183460469231731687303715884105727)::HUGEINT, 0::UHUGEINT,
   '00000000-0000-0000-0000-000000000000'::UUID, INTERVAL '-1 month',
   TIMETZ '00:00:00.000001-01', '00:00:00.000000001'::TIME_NS, '1'::BIT)
) t(id, li, st, m, arr, h, uh, u, iv, ttz, tns, bits);
CREATE TABLE listlist AS SELECT * FROM (VALUES
  (1, [[1], NULL]::INTEGER[][]), (2, NULL), (3, [[]]::INTEGER[][])) t(id, ll);
CREATE TABLE mapint AS SELECT * FROM (VALUES
  (1, MAP {10: 'ten'}), (2, NULL)) t(id, mi);
CREATE TABLE uni AS SELECT * FROM (VALUES
  (1, union_value(num := 2::INTEGER), 'neighbor')) t(id, uv, s);
CREATE TABLE medium AS SELECT
  lpad(range::VARCHAR, 8, '0') AS k, range::BIGINT AS v,
  's' || (range % 13)::VARCHAR AS s,
  CASE WHEN range % 10 = 0 THEN NULL ELSE range * 0.5 END AS f
  FROM range(30000);
CREATE TABLE big AS SELECT range::BIGINT AS v FROM range(500000);
`

// startLiveServer launches duckdb with quack_serve on a free port and
// returns the HTTP endpoint.
func startLiveServer(t *testing.T) string {
	t.Helper()
	if os.Getenv("QUACK_LIVE") == "" {
		t.Skip("set QUACK_LIVE=1 to run against a live quack_serve")
	}
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Fatal("QUACK_LIVE=1 but no duckdb on PATH")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	dir := t.TempDir()
	sql := liveSQL + fmt.Sprintf(
		"CALL quack_serve('quack://127.0.0.1:%d', token := '%s', disable_ssl := true);\n",
		port, liveToken)
	if err := os.WriteFile(filepath.Join(dir, "serve.sql"), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("duckdb", "live.duckdb", "-cmd", ".read serve.sql")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe() // held open: the CLI exits on EOF
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.Create(filepath.Join(dir, "serve.log"))
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(endpoint + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return endpoint
			}
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(dir, "serve.log"))
			t.Fatalf("quack_serve did not come up:\n%s", log)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func dialLive(t *testing.T, endpoint string) *duckcall.Conn {
	t.Helper()
	conn, err := duckcall.Dial(context.Background(), duckcall.Config{Endpoint: endpoint, Token: liveToken})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// liveRows runs a query over the native API and flattens the result; cell
// errors (unsupported columns) surface as the error value.
func liveRows(t *testing.T, conn *duckcall.Conn, sql string) (*codec.Schema, [][]any) {
	t.Helper()
	res, err := conn.Query(context.Background(), sql)
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	for ch, err := range res.Chunks(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		for r := range ch.RowCount() {
			row := make([]any, ch.ColumnCount())
			for c := range row {
				v, err := ch.Value(c, r)
				if err != nil {
					row[c] = err
				} else {
					row[c] = v
				}
			}
			rows = append(rows, row)
		}
	}
	return res.Schema(), rows
}

// TestLiveTier2Matrix asserts exact decoded values for the Tier 2 matrix
// over the native API, then the driver renderings over database/sql.
func TestLiveTier2Matrix(t *testing.T) {
	endpoint := startLiveServer(t)
	conn := dialLive(t, endpoint)

	_, rows := liveRows(t, conn, "SELECT * FROM tier2 ORDER BY id")
	huge, _ := new(big.Int).SetString("170141183460469231731687303715884105727", 10)
	uhuge, _ := new(big.Int).SetString("340282366920938463463374607431768211455", 10)
	want := [][]any{
		{
			int32(1),
			[]any{int32(1), nil, int32(3)},
			codec.Struct{
				{Name: "id", Value: int32(7)},
				{Name: "pt", Value: codec.Struct{{Name: "x", Value: int32(1)}, {Name: "y", Value: "a"}}},
			},
			[]codec.MapEntry{{Key: "a", Value: int32(1)}, {Key: "b", Value: nil}},
			[]any{int32(1), nil, int32(3)},
			huge,
			uhuge,
			codec.UUID{0xc8, 0x18, 0x5c, 0xa9, 0x1c, 0x05, 0x40, 0xc3, 0x8a, 0x22, 0x0f, 0x63, 0x28, 0x86, 0x28, 0x8c},
			codec.Interval{Months: 14, Days: 3, Micros: 4_500_000},
			codec.TimeTZ{Micros: 45_015_000_000, Offset: 19800},
			codec.TimeNS(45_015_123_456_789),
			"010110110",
		},
		{int32(2), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil},
		{
			int32(3),
			[]any{},
			codec.Struct{{Name: "id", Value: nil}, {Name: "pt", Value: nil}},
			[]codec.MapEntry{},
			[]any{int32(0), int32(0), int32(0)},
			new(big.Int).Neg(huge),
			big.NewInt(0),
			codec.UUID{},
			codec.Interval{Months: -1},
			codec.TimeTZ{Micros: 1, Offset: -3600},
			codec.TimeNS(1),
			"1",
		},
	}
	if len(rows) != len(want) {
		t.Fatalf("native: %d rows, want %d", len(rows), len(want))
	}
	for r := range want {
		for c := range want[r] {
			if !reflect.DeepEqual(rows[r][c], want[r][c]) {
				t.Errorf("native [%d][%d]: got %#v, want %#v", r, c, rows[r][c], want[r][c])
			}
		}
	}

	// UNION degrades per column against the real server too.
	_, urows := liveRows(t, conn, "SELECT * FROM uni ORDER BY id")
	if ue, ok := urows[0][1].(codec.ErrUnsupportedType); !ok || ue.Type.ID != codec.TypeUnion {
		t.Errorf("live UNION column: got %#v, want ErrUnsupportedType", urows[0][1])
	}
	if urows[0][2] != "neighbor" {
		t.Errorf("live UNION neighbor: %#v", urows[0][2])
	}

	// database/sql: exotic scalars as strings, nested as JSON.
	host := strings.TrimPrefix(endpoint, "http://")
	db, err := sql.Open("duckcall", "quack://"+host+"?token="+liveToken)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var li, st, m, h, u, iv, ttz, tns, bits string
	err = db.QueryRow("SELECT li, st, m, h, u, iv, ttz, tns, bits FROM tier2 WHERE id = 1").
		Scan(&li, &st, &m, &h, &u, &iv, &ttz, &tns, &bits)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][2]string{
		"li":   {li, "[1,null,3]"},
		"st":   {st, `{"id":7,"pt":{"x":1,"y":"a"}}`},
		"m":    {m, `{"a":1,"b":null}`},
		"h":    {h, "170141183460469231731687303715884105727"},
		"u":    {u, "c8185ca9-1c05-40c3-8a22-0f632886288c"},
		"iv":   {iv, "1 year 2 months 3 days 00:00:04.5"},
		"ttz":  {ttz, "12:30:15+05:30"},
		"tns":  {tns, "12:30:15.123456789"},
		"bits": {bits, "010110110"},
	} {
		if got[0] != got[1] {
			t.Errorf("sql %s: got %q, want %q", name, got[0], got[1])
		}
	}
}

// TestLiveDifferential runs each query through duckcall and through the
// official client on the same server, normalizes both to JSON, and diffs
// cell by cell.
func TestLiveDifferential(t *testing.T) {
	endpoint := startLiveServer(t)
	conn := dialLive(t, endpoint)

	for _, q := range []struct{ name, sql string }{
		{"tier1", "SELECT * FROM tier1 ORDER BY id"},
		{"tier2", "SELECT * FROM tier2 ORDER BY id"},
		{"listlist", "SELECT * FROM listlist ORDER BY id"},
		{"mapint", "SELECT * FROM mapint ORDER BY id"},
		// Crosses the inline budget, so rows arrive via concurrent fetches.
		{"medium", "SELECT * FROM medium"},
	} {
		t.Run(q.name, func(t *testing.T) {
			schema, rows := liveRows(t, conn, q.sql)
			ours := make([]string, len(rows))
			for i, row := range rows {
				var b []byte
				b = append(b, '{')
				for c, cell := range row {
					if c > 0 {
						b = append(b, ',')
					}
					col := schema.Columns[c]
					b = appendQuoted(b, col.Name)
					b = append(b, ':')
					jb, err := appendToJSON(b, col.Type, cell)
					if err != nil {
						t.Fatalf("row %d %s: %v", i, col.Name, err)
					}
					b = jb
				}
				ours[i] = string(append(b, '}'))
			}
			theirs := quackQueryJSON(t, endpoint, q.sql)
			if len(ours) != len(theirs) {
				t.Fatalf("duckcall returned %d rows, quack_query %d", len(ours), len(theirs))
			}
			// Fetched batches arrive in completion order; compare as sorted
			// sets keyed by the whole row (first column is unique per table).
			sort.Strings(ours)
			sort.Strings(theirs)
			for i := range ours {
				a, b := canonJSON(t, ours[i]), canonJSON(t, theirs[i])
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("row %d differs:\nduckcall:    %s\nquack_query: %s", i, ours[i], theirs[i])
				}
			}
		})
	}
}

// TestLiveParallelFetch500k checksums a 500k-row result streamed through
// the parallel fetch pool.
func TestLiveParallelFetch500k(t *testing.T) {
	endpoint := startLiveServer(t)
	conn := dialLive(t, endpoint)
	res, err := conn.Query(context.Background(), "SELECT v FROM big")
	if err != nil {
		t.Fatal(err)
	}
	var n, sum int64
	for ch, err := range res.Chunks(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		for r := range ch.RowCount() {
			v, err := ch.Value(0, r)
			if err != nil {
				t.Fatal(err)
			}
			n++
			sum += v.(int64)
		}
	}
	if n != 500_000 || sum != 124_999_750_000 {
		t.Fatalf("got %d rows summing %d, want 500000 summing 124999750000", n, sum)
	}
}

// quackQueryJSON runs sql on the same server through the official client
// and returns each row as JSON text (to_json of the row struct, computed
// client-side by the duckdb CLI after its own Quack decode).
func quackQueryJSON(t *testing.T, endpoint, sql string) []string {
	t.Helper()
	url := "quack://" + strings.TrimPrefix(endpoint, "http://")
	cmd := exec.Command("duckdb", "-json", "-c", fmt.Sprintf(
		"SET TimeZone='UTC'; LOAD quack; SELECT to_json(q)::VARCHAR AS row FROM "+
			"(SELECT * FROM quack_query('%s', $liveq$%s$liveq$, token := '%s', disable_ssl := true)) q;",
		url, sql, liveToken))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quack_query: %v\n%s", err, out)
	}
	var wrapped []struct{ Row string }
	if err := json.Unmarshal(out, &wrapped); err != nil {
		t.Fatalf("parsing CLI output: %v\n%s", err, out)
	}
	rows := make([]string, len(wrapped))
	for i, w := range wrapped {
		rows[i] = w.Row
	}
	return rows
}

// appendToJSON renders a decoded native value the way duckdb's to_json
// does, so the differential comparison is byte-honest about values and
// structure while canonJSON absorbs key-order and number-spelling noise.
func appendToJSON(b []byte, t codec.LogicalType, v any) ([]byte, error) {
	if v == nil {
		return append(b, "null"...), nil
	}
	switch t.ID {
	case codec.TypeList, codec.TypeArray:
		vals, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("%s value is %T", t, v)
		}
		b = append(b, '[')
		var err error
		for i, e := range vals {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendToJSON(b, *t.Child, e); err != nil {
				return nil, err
			}
		}
		return append(b, ']'), nil
	case codec.TypeStruct:
		s, ok := v.(codec.Struct)
		if !ok {
			return nil, fmt.Errorf("%s value is %T", t, v)
		}
		b = append(b, '{')
		var err error
		for i, e := range s {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendQuoted(b, e.Name)
			b = append(b, ':')
			if b, err = appendToJSON(b, t.Fields[i].Type, e.Value); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil
	case codec.TypeMap:
		entries, ok := v.([]codec.MapEntry)
		if !ok {
			return nil, fmt.Errorf("%s value is %T", t, v)
		}
		b = append(b, '{')
		for i, e := range entries {
			if i > 0 {
				b = append(b, ',')
			}
			kb, err := appendToJSON(nil, t.Child.Fields[0].Type, e.Key)
			if err != nil {
				return nil, err
			}
			key := string(kb)
			if strings.HasPrefix(key, `"`) {
				b = append(b, key...)
			} else {
				b = appendQuoted(b, key)
			}
			b = append(b, ':')
			if b, err = appendToJSON(b, t.Child.Fields[1].Type, e.Value); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil
	}
	switch x := v.(type) {
	case bool:
		return strconv.AppendBool(b, x), nil
	case int8:
		return strconv.AppendInt(b, int64(x), 10), nil
	case int16:
		return strconv.AppendInt(b, int64(x), 10), nil
	case int32:
		return strconv.AppendInt(b, int64(x), 10), nil
	case int64:
		return strconv.AppendInt(b, x, 10), nil
	case uint8:
		return strconv.AppendUint(b, uint64(x), 10), nil
	case uint16:
		return strconv.AppendUint(b, uint64(x), 10), nil
	case uint32:
		return strconv.AppendUint(b, uint64(x), 10), nil
	case uint64:
		return strconv.AppendUint(b, x, 10), nil
	case float32:
		// to_json widens FLOAT to DOUBLE before printing shortest-round-trip
		// (0.1f renders as 0.10000000149011612), so bitSize is 64 here too.
		return strconv.AppendFloat(b, float64(x), 'g', -1, 64), nil
	case float64:
		return strconv.AppendFloat(b, x, 'g', -1, 64), nil
	case *big.Int:
		return append(b, x.String()...), nil
	case codec.Decimal:
		return append(b, x.String()...), nil
	case string:
		return appendQuoted(b, x), nil
	case []byte:
		return appendQuoted(b, blobLiteral(x)), nil
	case codec.Time:
		return appendQuoted(b, x.String()), nil
	case codec.TimeNS:
		return appendQuoted(b, x.String()), nil
	case codec.TimeTZ:
		return appendQuoted(b, x.String()), nil
	case codec.Interval:
		return appendQuoted(b, x.String()), nil
	case codec.UUID:
		return appendQuoted(b, x.String()), nil
	case time.Time:
		switch t.ID {
		case codec.TypeDate:
			return appendQuoted(b, x.Format("2006-01-02")), nil
		case codec.TypeTimestampTZ:
			return appendQuoted(b, x.Format("2006-01-02 15:04:05.999999999")+"+00"), nil
		default:
			return appendQuoted(b, x.Format("2006-01-02 15:04:05.999999999")), nil
		}
	default:
		return nil, fmt.Errorf("no to_json rendering for %T", v)
	}
}

func appendQuoted(b []byte, s string) []byte {
	j, _ := json.Marshal(s)
	return append(b, j...)
}

// blobLiteral renders bytes the way duckdb casts BLOB to text: printable
// ASCII stays, everything else becomes \xHH.
func blobLiteral(data []byte) string {
	var sb strings.Builder
	for _, c := range data {
		if c >= 0x20 && c <= 0x7e && c != '\\' && c != '\'' {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, `\x%02X`, c)
		}
	}
	return sb.String()
}

// canonJSON parses JSON with numbers preserved, then normalizes every
// number through big.Float so 1e+20 and 100000000000000000000 compare
// equal without float64 truncation of 128-bit integers.
func canonJSON(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("bad JSON %q: %v", s, err)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case json.Number:
			f, _, err := big.ParseFloat(x.String(), 10, 256, big.ToNearestEven)
			if err != nil {
				return x.String()
			}
			return f.Text('e', 50)
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
			return x
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
			return x
		}
		return v
	}
	return walk(v)
}
