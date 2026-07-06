package codec

// Version assumptions live here and nowhere else. The protocol is beta
// until DuckDB 2.0; decoders are registered per negotiated version so 1.5.x
// and 2.0 can coexist in one binary.
//
// Written against duckdb-quack as shipped with DuckDB 1.5.3/1.5.4. The type
// id enum tracks duckdb's LogicalTypeId; re-verify it against
// src/common/types.hpp on every supported release bump.

type versionKey struct {
	Protocol      int
	Serialization int
}

// registry maps a negotiated (protocol, serialization) pair to a decoder.
// Adding 2.0 support means adding an entry, not rewriting the tree.
var registry = map[versionKey]*Codec{
	{Protocol: 1, Serialization: 1}: {protocol: 1, serialization: 1},
}
