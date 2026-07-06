package codec

// Version assumptions live here and nowhere else. The protocol is beta
// until DuckDB 2.0; decoders are registered per negotiated quack version so
// 1.5.x and 2.0 can coexist in one binary.
//
// Written against duckdb-quack as shipped with DuckDB 1.5.3/1.5.4, which
// serializes at compatibility index 7 and announces quack version 1. The
// type id enum tracks duckdb's LogicalTypeId; re-verify it against
// src/include/duckdb/common/types.hpp on every supported release bump.

// registry maps a negotiated quack version to a decoder. Adding 2.0
// support means adding an entry, not rewriting the tree.
var registry = map[uint64]*Codec{
	1: {version: 1},
}
