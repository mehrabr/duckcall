package wire

// Version assumptions live here and nowhere else. Everything below tracks
// duckdb-quack as shipped with DuckDB 1.5.3/1.5.4 (quack wire version 1);
// the protocol is beta until DuckDB 2.0, so expect this file to change with
// upstream.

// Quack protocol versions this transport can negotiate. Sent in the
// connection request; the server refuses if it cannot serve the minimum.
const (
	quackVersionMin = 1
	quackVersionMax = 1
)

// The one endpoint. Every message is a POST here; GET / is only a banner.
const pathRPC = "/quack"

// What we announce ourselves as. The server passes these through to its
// auth hook and logs; they carry no protocol meaning.
const clientVersion = "duckcall/0.4"

const contentType = "application/octet-stream"
