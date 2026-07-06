package wire

// Version assumptions live here and nowhere else. The endpoint shapes below
// track duckdb-quack as shipped with DuckDB 1.5.3/1.5.4; the protocol is
// beta until DuckDB 2.0, so expect this file to change with upstream.

// Protocol versions this transport can negotiate.
const (
	protocolVersionMin = 1
	protocolVersionMax = 1
)

// Endpoint paths, relative to the configured base URL.
const (
	pathConnect = "/quack/v1/connect"
	pathSession = "/quack/v1/session"
	pathQuery   = "/quack/v1/query"
)

// sessionHeader carries the session id on every request after connect.
const sessionHeader = "X-Quack-Session"
