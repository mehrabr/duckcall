// Package qser reads and writes DuckDB's BinarySerializer framing, the
// encoding every Quack message rides in: little-endian uint16 field ids,
// LEB128 varints, length-prefixed strings, nested objects terminated by
// 0xFFFF, and fields omitted entirely when they hold their default value.
//
// There is no wire-type byte, so decoding is schema-driven: the caller must
// know each field's value encoding, and an unknown field cannot be skipped.
// That is upstream's contract (serialization compatibility is version-gated),
// and it is why every decoder built on this package fails loudly rather than
// guessing.
package qser

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// EndID terminates an object (BinarySerializer's MESSAGE_TERMINATOR_FIELD_ID).
const EndID uint16 = 0xFFFF

var (
	ErrTruncated = errors.New("qser: truncated input")
	ErrOverflow  = errors.New("qser: varint overflow")
)

// Hugeint is duckdb's hugeint_t as serialized: signed upper, unsigned lower.
type Hugeint struct {
	Upper int64
	Lower uint64
}

// OptionalIdxAbsent is how duckdb serializes an unset optional_idx.
const OptionalIdxAbsent = math.MaxUint64

// Reader consumes one serialized document. Errors are sticky: after the
// first failure every method returns zero values and Err() reports the
// cause, so decoders read straight through and check once. All reads are
// bounds-checked; a fuzzer drives these paths, so no read may assume
// well-formed input.
type Reader struct {
	buf []byte
	off int
	err error
}

func NewReader(buf []byte) *Reader { return &Reader{buf: buf} }

func (r *Reader) Err() error     { return r.err }
func (r *Reader) Remaining() int { return len(r.buf) - r.off }

// Rest returns the unconsumed tail without copying.
func (r *Reader) Rest() []byte { return r.buf[r.off:] }

func (r *Reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// PeekID returns the next field id without consuming it.
func (r *Reader) PeekID() uint16 {
	if r.err != nil {
		return EndID
	}
	if r.Remaining() < 2 {
		r.fail(ErrTruncated)
		return EndID
	}
	return binary.LittleEndian.Uint16(r.buf[r.off:])
}

// TryField consumes the next field id if it equals id. This is how optional
// (WritePropertyWithDefault) fields decode: absent means default.
func (r *Reader) TryField(id uint16) bool {
	if r.PeekID() != id || r.err != nil {
		return false
	}
	r.off += 2
	return true
}

// End consumes an object terminator; anything else is a framing error,
// usually an unknown field this decoder has no schema for.
func (r *Reader) End() {
	id := r.PeekID()
	if r.err != nil {
		return
	}
	if id != EndID {
		r.fail(fmt.Errorf("qser: unexpected field %d where object should end", id))
		return
	}
	r.off += 2
}

func (r *Reader) Take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.Remaining() < n {
		r.fail(ErrTruncated)
		return nil
	}
	b := r.buf[r.off : r.off+n]
	r.off += n
	return b
}

// Uvarint reads an unsigned LEB128.
func (r *Reader) Uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.buf[r.off:])
	if n == 0 {
		r.fail(ErrTruncated)
		return 0
	}
	if n < 0 {
		r.fail(ErrOverflow)
		return 0
	}
	r.off += n
	return v
}

// Svarint reads a signed (sign-extended, not zigzag) LEB128, the encoding
// duckdb uses for signed integer types.
func (r *Reader) Svarint() int64 {
	if r.err != nil {
		return 0
	}
	var v uint64
	var shift uint
	for i := 0; ; i++ {
		if r.Remaining() == 0 {
			r.fail(ErrTruncated)
			return 0
		}
		if i == 10 {
			r.fail(ErrOverflow)
			return 0
		}
		b := r.buf[r.off]
		r.off++
		if shift < 64 {
			v |= uint64(b&0x7f) << shift
		}
		shift += 7
		if b&0x80 == 0 {
			if shift < 64 && b&0x40 != 0 {
				v |= ^uint64(0) << shift
			}
			return int64(v)
		}
	}
}

// Bool is a raw byte; duckdb writes bools uncompressed.
func (r *Reader) Bool() bool {
	b := r.Take(1)
	return len(b) == 1 && b[0] != 0
}

// Bytes reads a uvarint length prefix and that many bytes, aliasing the
// input buffer.
func (r *Reader) Bytes() []byte {
	n := r.Uvarint()
	if r.err != nil {
		return nil
	}
	if n > uint64(r.Remaining()) {
		r.fail(ErrTruncated)
		return nil
	}
	return r.Take(int(n))
}

func (r *Reader) String() string { return string(r.Bytes()) }

// ListCount reads a list's element count, bounded by what could possibly
// fit in the remaining input.
func (r *Reader) ListCount() int {
	n := r.Uvarint()
	if r.err != nil {
		return 0
	}
	if n > uint64(r.Remaining()) {
		r.fail(ErrTruncated)
		return 0
	}
	return int(n)
}

// Present reads a nullable marker (OnNullableBegin's bool).
func (r *Reader) Present() bool { return r.Bool() }

func (r *Reader) Hugeint() Hugeint {
	return Hugeint{Upper: r.Svarint(), Lower: r.Uvarint()}
}

// OptionalIdx reads duckdb's optional_idx: a uvarint whose max value means
// absent. Callers compare against OptionalIdxAbsent.
func (r *Reader) OptionalIdx() uint64 { return r.Uvarint() }

// Writer builds documents in the same framing. Optional fields are the
// caller's concern: to match duckdb, skip the field entirely when the value
// is its default.
type Writer struct {
	buf []byte
}

func (w *Writer) Bytes() []byte { return w.buf }

func (w *Writer) Field(id uint16) { w.buf = binary.LittleEndian.AppendUint16(w.buf, id) }
func (w *Writer) End()            { w.Field(EndID) }

func (w *Writer) Uvarint(v uint64) { w.buf = binary.AppendUvarint(w.buf, v) }

func (w *Writer) Svarint(v int64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			w.buf = append(w.buf, b)
			return
		}
		w.buf = append(w.buf, b|0x80)
	}
}

func (w *Writer) Bool(b bool) {
	if b {
		w.buf = append(w.buf, 1)
	} else {
		w.buf = append(w.buf, 0)
	}
}

func (w *Writer) BytesVal(b []byte) {
	w.Uvarint(uint64(len(b)))
	w.buf = append(w.buf, b...)
}

func (w *Writer) String(s string) { w.BytesVal([]byte(s)) }

func (w *Writer) Raw(b []byte) { w.buf = append(w.buf, b...) }

func (w *Writer) Hugeint(h Hugeint) {
	w.Svarint(h.Upper)
	w.Uvarint(h.Lower)
}

// Field-plus-value shorthands for the common cases.

func (w *Writer) FieldUvarint(id uint16, v uint64) { w.Field(id); w.Uvarint(v) }
func (w *Writer) FieldSvarint(id uint16, v int64)  { w.Field(id); w.Svarint(v) }
func (w *Writer) FieldBool(id uint16, b bool)      { w.Field(id); w.Bool(b) }
func (w *Writer) FieldString(id uint16, s string)  { w.Field(id); w.String(s) }
func (w *Writer) FieldBytes(id uint16, b []byte)   { w.Field(id); w.BytesVal(b) }
func (w *Writer) FieldHugeint(id uint16, h Hugeint) {
	w.Field(id)
	w.Hugeint(h)
}
