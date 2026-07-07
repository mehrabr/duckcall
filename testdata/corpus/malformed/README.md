# Malformed DataChunk corpus

Intentionally invalid chunk payloads (the standalone `DecodeChunk` form: the
contents of a wrapper's field 300). Every file must decode to a structured
error — the specific one asserted in `codec/malformed_test.go` — with no
panic and no silently wrong values. The same files seed `FuzzDecodeChunk`.

The builders live next to the assertions in `malformed_test.go`; regenerate
after changing one with:

```
REGEN_MALFORMED=1 go test ./codec -run TestMalformedCorpus
```

| file | corruption |
|---|---|
| `truncated-mid.bin` | Tier 2 chunk cut mid-column |
| `truncated-tail.bin` | Tier 2 chunk missing its last bytes |
| `rowcount-implausible.bin` | row count far past STANDARD_VECTOR_SIZE |
| `vector-count-mismatch.bin` | one declared type, two encoded vectors |
| `missing-vector.bin` | one declared type, zero encoded vectors |
| `short-data.bin` | fixed-width data blob shorter than rows demand |
| `short-validity.bin` | validity mask shorter than the row count |
| `sel-out-of-range.bin` | dictionary selection index past the dictionary |
| `oversized-list-size.bin` | list_size claiming 2^30 child rows |
| `list-entry-out-of-range.bin` | list entry window outside the child vector |
| `struct-child-mismatch.bin` | struct vector with fewer children than fields |
| `bit-bad-padding.bin` | BIT value claiming 9 padding bits |
| `unknown-field.bin` | field id no chunk schema knows |
