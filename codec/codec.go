// Package codec decodes DuckDB's binary result serialization: schemas,
// logical type trees, and data chunks with validity, as carried by the Quack
// protocol. It has no knowledge of HTTP or the transport; bytes in, typed
// values out. Anything that speaks the framing can use it — a proxy, a CLI,
// another client's test oracle.
package codec

import "fmt"

// Codec decodes payloads for one negotiated protocol/serialization version
// pair. Codecs are stateless and safe for concurrent use.
type Codec struct {
	protocol      int
	serialization int
}

// For returns the codec for a negotiated version pair, or an error listing
// what this build supports.
func For(protocol, serialization int) (*Codec, error) {
	if c, ok := registry[versionKey{protocol, serialization}]; ok {
		return c, nil
	}
	supported := make([]versionKey, 0, len(registry))
	for k := range registry {
		supported = append(supported, k)
	}
	return nil, fmt.Errorf("codec: no decoder for protocol %d / serialization %d (supported: %v)",
		protocol, serialization, supported)
}

// Versions reports the pair this codec was selected for.
func (c *Codec) Versions() (protocol, serialization int) {
	return c.protocol, c.serialization
}
