# Captured PREPARE_RESPONSE frames

Each `.bin` is one complete response message — header document plus body —
captured from DuckDB v1.5.4's `quack_serve` answering the official client
(`quack_query`) through a logging reverse proxy, 2026-07-06. `corpus_test.go`
at the repo root decodes every frame and asserts the cell values, so these
pin the codec to bytes a real server produced rather than to codectest's
idea of them.

| frame | query | pins |
|---|---|---|
| `prepare-tier2.bin` | `SELECT * FROM tier2 ORDER BY id` | LIST, STRUCT (nested), MAP, ARRAY, HUGEINT, UHUGEINT, UUID, INTERVAL, TIMETZ, TIME_NS, BIT — one row of values, one all-NULL, one of edge values |
| `prepare-listlist.bin` | `SELECT * FROM listlist ORDER BY id` | LIST of LIST with inner NULL and inner empty |
| `prepare-mapint.bin` | `SELECT * FROM mapint ORDER BY id` | MAP with non-VARCHAR keys |
| `prepare-union.bin` | `SELECT * FROM uni ORDER BY id` | UNION parses past and errors per column; neighbors decode |

Regenerate per the recipe in the spec §7: serve the tables below over
`quack_serve`, put any body-dumping proxy in front, run the queries through
`quack_query`, and keep the responses whose header type is 4:

```sql
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
```
