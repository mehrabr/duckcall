// Package qser holds the low-level binary framing shared by codec's reader
// and codectest's writer: field-tagged objects in the style of DuckDB's
// BinarySerializer. Fields are (id uvarint, kind byte, payload); id 0 ends
// an object. The kind byte is what lets a decoder skip fields it does not
// know, which the protocol requires since extensions may add messages.
package qser

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Field payload kinds.
const (
	KindUvarint byte = 1 // unsigned LEB128
	KindVarint  byte = 2 // zigzag LEB128
	KindFixed4  byte = 3 // 4 bytes little-endian
	KindFixed8  byte = 4 // 8 bytes little-endian
	KindBytes   byte = 5 // uvarint length + raw bytes
	KindObject  byte = 6 // nested fields until id 0
	KindList    byte = 7 // uvarint count + that many objects
)

var (
	ErrTruncated = errors.New("qser: truncated input")
	ErrOverflow  = errors.New("qser: varint overflow")
)

// Reader consumes a single serialized buffer. All methods are bounds-checked;
// a fuzzer drives these paths, so no read may assume well-formed input.
type Reader struct {
	buf []byte
	off int
}

func NewReader(buf []byte) *Reader { return &Reader{buf: buf} }

func (r *Reader) Remaining() int { return len(r.buf) - r.off }

func (r *Reader) Uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.buf[r.off:])
	if n == 0 {
		return 0, ErrTruncated
	}
	if n < 0 {
		return 0, ErrOverflow
	}
	r.off += n
	return v, nil
}

func (r *Reader) Varint() (int64, error) {
	u, err := r.Uvarint()
	if err != nil {
		return 0, err
	}
	// zigzag
	return int64(u>>1) ^ -int64(u&1), nil
}

func (r *Reader) Fixed4() (uint32, error) {
	b, err := r.Take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (r *Reader) Fixed8() (uint64, error) {
	b, err := r.Take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// Take returns the next n bytes without copying. The returned slice aliases
// the input buffer.
func (r *Reader) Take(n int) ([]byte, error) {
	if n < 0 || r.Remaining() < n {
		return nil, ErrTruncated
	}
	b := r.buf[r.off : r.off+n]
	r.off += n
	return b, nil
}

func (r *Reader) Bytes() ([]byte, error) {
	n, err := r.Uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(r.Remaining()) {
		return nil, ErrTruncated
	}
	return r.Take(int(n))
}

// Field reads the next field header. id 0 means end-of-object and kind is
// meaningless.
func (r *Reader) Field() (id uint64, kind byte, err error) {
	id, err = r.Uvarint()
	if err != nil || id == 0 {
		return id, 0, err
	}
	k, err := r.Take(1)
	if err != nil {
		return 0, 0, err
	}
	return id, k[0], nil
}

// Skip discards a value of the given kind, recursing through objects and
// lists so unknown fields never desync the stream.
func (r *Reader) Skip(kind byte) error {
	switch kind {
	case KindUvarint, KindVarint:
		_, err := r.Uvarint()
		return err
	case KindFixed4:
		_, err := r.Take(4)
		return err
	case KindFixed8:
		_, err := r.Take(8)
		return err
	case KindBytes:
		_, err := r.Bytes()
		return err
	case KindObject:
		return r.SkipObject()
	case KindList:
		n, err := r.Uvarint()
		if err != nil {
			return err
		}
		if n > uint64(r.Remaining()) {
			return ErrTruncated
		}
		for i := uint64(0); i < n; i++ {
			if err := r.SkipObject(); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("qser: unknown field kind %d", kind)
	}
}

func (r *Reader) SkipObject() error {
	for {
		id, kind, err := r.Field()
		if err != nil {
			return err
		}
		if id == 0 {
			return nil
		}
		if err := r.Skip(kind); err != nil {
			return err
		}
	}
}

// Writer builds buffers in the same framing. It exists for codectest and the
// fuzz seeds; production duckcall never encodes result payloads.
type Writer struct {
	buf []byte
}

func (w *Writer) Bytes() []byte { return w.buf }

func (w *Writer) uvarint(v uint64) { w.buf = binary.AppendUvarint(w.buf, v) }

func (w *Writer) field(id uint64, kind byte) {
	w.uvarint(id)
	w.buf = append(w.buf, kind)
}

func (w *Writer) FieldUvarint(id, v uint64) {
	w.field(id, KindUvarint)
	w.uvarint(v)
}

func (w *Writer) FieldVarint(id uint64, v int64) {
	w.field(id, KindVarint)
	w.uvarint(uint64(v<<1) ^ uint64(v>>63))
}

func (w *Writer) FieldFixed4(id uint64, v uint32) {
	w.field(id, KindFixed4)
	w.buf = binary.LittleEndian.AppendUint32(w.buf, v)
}

func (w *Writer) FieldFixed8(id, v uint64) {
	w.field(id, KindFixed8)
	w.buf = binary.LittleEndian.AppendUint64(w.buf, v)
}

func (w *Writer) FieldBytes(id uint64, b []byte) {
	w.field(id, KindBytes)
	w.uvarint(uint64(len(b)))
	w.buf = append(w.buf, b...)
}

// FieldObject opens a nested object; the caller writes its fields and then
// calls End.
func (w *Writer) FieldObject(id uint64) { w.field(id, KindObject) }

// FieldList opens a list of count objects; each element must be written as
// fields followed by End.
func (w *Writer) FieldList(id, count uint64) {
	w.field(id, KindList)
	w.uvarint(count)
}

func (w *Writer) End() { w.uvarint(0) }

// Float bit helpers keep NaN payloads intact across the wire.
func Float32bits(f float32) uint32 { return math.Float32bits(f) }
func Float64bits(f float64) uint64 { return math.Float64bits(f) }
