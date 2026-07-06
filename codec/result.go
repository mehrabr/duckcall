package codec

import (
	"fmt"

	"github.com/mehrabr/duckcall/internal/qser"
)

// Hugeint is a 128-bit wire value; result uuids are one. It matches
// wire.Hugeint by construction — both alias the same type.
type Hugeint = qser.Hugeint

// BatchIndexAbsent marks a FETCH_RESPONSE that carried no batch index.
const BatchIndexAbsent = qser.OptionalIdxAbsent

// PrepareResult is a decoded PREPARE_RESPONSE body: the schema, whatever
// chunks the server inlined, and — when NeedsMoreFetch — the uuid the rest
// of the result is fetched under.
type PrepareResult struct {
	Schema         *Schema
	Chunks         []*Chunk
	NeedsMoreFetch bool
	ResultUUID     Hugeint
}

// FetchResult is a decoded FETCH_RESPONSE body. Empty Chunks means the
// result is drained. BatchIndex orders batches for consumers that care;
// batches arrive in whatever order competing fetchers get served.
type FetchResult struct {
	Chunks     []*Chunk
	BatchIndex uint64
}

// DecodePrepare decodes a PREPARE_RESPONSE body.
func (c *Codec) DecodePrepare(body []byte) (*PrepareResult, error) {
	r := qser.NewReader(body)
	res := &PrepareResult{Schema: &Schema{}}

	var types []LogicalType
	if r.TryField(1) {
		n := r.ListCount()
		types = make([]LogicalType, 0, n)
		for range n {
			t, err := decodeType(r, 0)
			if err != nil {
				return nil, err
			}
			types = append(types, t)
		}
	}
	var names []string
	if r.TryField(2) {
		n := r.ListCount()
		names = make([]string, 0, n)
		for range n {
			names = append(names, r.String())
		}
	}
	if len(names) != len(types) {
		return nil, fmt.Errorf("codec: prepare response has %d names for %d types", len(names), len(types))
	}
	res.Schema.Columns = make([]SchemaColumn, len(types))
	for i := range types {
		res.Schema.Columns[i] = SchemaColumn{Name: names[i], Type: types[i]}
	}

	if r.TryField(3) {
		res.NeedsMoreFetch = r.Bool()
	}
	if r.TryField(4) {
		chunks, err := decodeChunkList(r)
		if err != nil {
			return nil, err
		}
		res.Chunks = chunks
	}
	if !r.TryField(5) {
		return nil, fmt.Errorf("codec: prepare response missing result uuid")
	}
	res.ResultUUID = r.Hugeint()
	r.End()
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("codec: bad prepare response: %w", err)
	}
	return res, nil
}

// DecodeFetch decodes a FETCH_RESPONSE body.
func (c *Codec) DecodeFetch(body []byte) (*FetchResult, error) {
	r := qser.NewReader(body)
	res := &FetchResult{BatchIndex: BatchIndexAbsent}
	if r.TryField(1) {
		chunks, err := decodeChunkList(r)
		if err != nil {
			return nil, err
		}
		res.Chunks = chunks
	}
	if r.TryField(2) {
		res.BatchIndex = r.OptionalIdx()
	}
	r.End()
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("codec: bad fetch response: %w", err)
	}
	return res, nil
}

// decodeChunkList reads a vector<unique_ptr<DataChunkWrapper>>: per element
// a nullable marker, then the wrapper object holding the chunk at field 300.
func decodeChunkList(r *qser.Reader) ([]*Chunk, error) {
	n := r.ListCount()
	chunks := make([]*Chunk, 0, n)
	for range n {
		if !r.Present() {
			// A null chunk pointer is just its absence marker; nothing
			// observed emits one, but the framing allows it.
			continue
		}
		if !r.TryField(300) {
			return nil, fmt.Errorf("codec: chunk wrapper missing chunk object")
		}
		ch, err := decodeChunkContents(r)
		if err != nil {
			return nil, err
		}
		r.End() // wrapper object
		chunks = append(chunks, ch)
	}
	return chunks, r.Err()
}

// DecodeChunk decodes one standalone serialized DataChunk (the contents of
// a wrapper's field 300). Fixtures and fuzzing enter here; the wire always
// arrives via DecodePrepare/DecodeFetch.
func (c *Codec) DecodeChunk(buf []byte) (*Chunk, error) {
	r := qser.NewReader(buf)
	ch, err := decodeChunkContents(r)
	if err != nil {
		return nil, err
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	return ch, nil
}
