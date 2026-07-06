// Package codec decodes DuckDB's binary result serialization: logical type
// trees and data chunks with validity, as carried inside Quack's
// PREPARE_RESPONSE and FETCH_RESPONSE messages. It has no knowledge of HTTP
// or the transport; bytes in, typed values out. Anything that has the bytes
// can use it — a proxy, a CLI, another client's test oracle.
package codec

import (
	"fmt"
	"maps"
	"slices"
)

// Codec decodes payloads for one negotiated quack protocol version. Codecs
// are stateless and safe for concurrent use.
type Codec struct {
	version uint64
}

// For returns the codec for a negotiated quack version, or an error listing
// what this build supports.
func For(version uint64) (*Codec, error) {
	if c, ok := registry[version]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("codec: no decoder for quack version %d (supported: %v)",
		version, slices.Sorted(maps.Keys(registry)))
}

func (c *Codec) Version() uint64 { return c.version }
