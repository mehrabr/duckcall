package codec

import (
	"fmt"
	"math/big"
	"time"
)

// Decimal is a fixed-point value carried as a 128-bit two's-complement
// integer scaled by 10^Scale. Narrower physical widths widen losslessly on
// decode. There is no float accessor on purpose: if a caller wants lossy,
// they can do the division themselves.
type Decimal struct {
	Width, Scale uint8
	hi           int64
	lo           uint64
}

func newDecimal(width, scale uint8, hi int64, lo uint64) Decimal {
	return Decimal{Width: width, Scale: scale, hi: hi, lo: lo}
}

// NewDecimal builds a decimal from an unscaled integer, which must fit in
// 128 bits two's complement.
func NewDecimal(width, scale uint8, unscaled *big.Int) Decimal {
	b := new(big.Int).Set(unscaled)
	if b.Sign() < 0 {
		b.Add(b, new(big.Int).Lsh(big.NewInt(1), 128))
	}
	lo := b.Uint64()
	hi := int64(new(big.Int).Rsh(b, 64).Uint64())
	return newDecimal(width, scale, hi, lo)
}

// BigInt returns the unscaled integer.
func (d Decimal) BigInt() *big.Int { return int128Big(d.hi, d.lo) }

// int128Big widens a two's-complement 128-bit value. HUGEINT columns decode
// through this to *big.Int rather than a bespoke pair type: callers get
// arithmetic and printing for free, and a type that exists only to be
// converted would tax every consumer to save one allocation on a rare
// column.
func int128Big(hi int64, lo uint64) *big.Int {
	v := new(big.Int).SetUint64(lo)
	h := new(big.Int).SetInt64(hi)
	h.Lsh(h, 64)
	return v.Add(v, h)
}

// uint128Big widens an unsigned 128-bit value (UHUGEINT).
func uint128Big(hi, lo uint64) *big.Int {
	v := new(big.Int).SetUint64(lo)
	h := new(big.Int).SetUint64(hi)
	h.Lsh(h, 64)
	return v.Add(v, h)
}

// Rat returns the exact value as unscaled / 10^Scale.
func (d Decimal) Rat() *big.Rat {
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d.Scale)), nil)
	return new(big.Rat).SetFrac(d.BigInt(), den)
}

func (d Decimal) String() string {
	v := d.BigInt()
	neg := v.Sign() < 0
	if neg {
		v.Neg(v)
	}
	s := v.String()
	if int(d.Scale) >= len(s) {
		s = fmt.Sprintf("%0*s", int(d.Scale)+1, s)
	}
	if d.Scale > 0 {
		cut := len(s) - int(d.Scale)
		s = s[:cut] + "." + s[cut:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Time is a TIME value: microseconds since midnight, no date, no zone.
type Time int64

func (t Time) Duration() time.Duration { return time.Duration(t) * time.Microsecond }

func (t Time) String() string {
	us := int64(t)
	h, us := us/3_600_000_000, us%3_600_000_000
	m, us := us/60_000_000, us%60_000_000
	s, us := us/1_000_000, us%1_000_000
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s) + fracString(us, 6)
}

// fracString renders a fractional-seconds suffix the way duckdb's casts do:
// nothing for zero, otherwise a dot and the digits with trailing zeros
// dropped. Matching duckdb exactly is what lets the differential harness
// compare rendered values byte for byte.
func fracString(frac int64, digits int) string {
	if frac == 0 {
		return ""
	}
	s := fmt.Sprintf(".%0*d", digits, frac)
	for s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	return s
}

// TimeNS is a TIME_NS value: nanoseconds since midnight, no date, no zone.
type TimeNS int64

func (t TimeNS) Duration() time.Duration { return time.Duration(t) }

func (t TimeNS) String() string {
	ns := int64(t)
	h, ns := ns/3_600_000_000_000, ns%3_600_000_000_000
	m, ns := ns/60_000_000_000, ns%60_000_000_000
	s, ns := ns/1_000_000_000, ns%1_000_000_000
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s) + fracString(ns, 9)
}

// TimeTZ is a TIME WITH TIME ZONE value: a clock time plus a fixed UTC
// offset. On the wire it is duckdb's dtime_tz_t bit packing (micros in the
// high 40 bits, the offset biased into the low 24); decoded here so callers
// never see the packing.
type TimeTZ struct {
	Micros int64 // microseconds since midnight, in the offset's local clock
	Offset int32 // seconds east of UTC, -57599..57599 (±15:59:59)
}

// String renders as duckdb casts TIMETZ to VARCHAR: the time, a sign, hours,
// then minutes and seconds only when nonzero.
func (t TimeTZ) String() string {
	s := Time(t.Micros).String()
	off := t.Offset
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	s += fmt.Sprintf("%s%02d", sign, off/3600)
	if mm := off / 60 % 60; mm != 0 {
		s += fmt.Sprintf(":%02d", mm)
	}
	if ss := off % 60; ss != 0 {
		s += fmt.Sprintf(":%02d", ss)
	}
	return s
}

// Interval is duckdb's interval_t: months, days, and microseconds are
// independent components (a month is not a fixed number of days), so there
// is deliberately no Duration accessor.
type Interval struct {
	Months int32
	Days   int32
	Micros int64
}

// String matches duckdb's IntervalToStringCast: zero components are
// omitted, units are pluralized, and the sub-day remainder renders as a
// signed clock.
func (iv Interval) String() string {
	var s string
	part := func(v int32, unit string) {
		if v == 0 {
			return
		}
		if s != "" {
			s += " "
		}
		s += fmt.Sprintf("%d %s", v, unit)
		if v != 1 && v != -1 {
			s += "s"
		}
	}
	part(iv.Months/12, "year")
	part(iv.Months%12, "month")
	part(iv.Days, "day")
	if iv.Micros == 0 {
		if s == "" {
			return "00:00:00"
		}
		return s
	}
	if s != "" {
		s += " "
	}
	us := iv.Micros
	if us < 0 {
		s += "-"
		us = -us
	}
	h, us := us/3_600_000_000, us%3_600_000_000
	m, us := us/60_000_000, us%60_000_000
	sec, us := us/1_000_000, us%1_000_000
	return s + fmt.Sprintf("%02d:%02d:%02d", h, m, sec) + fracString(us, 6)
}

// UUID is a decoded UUID value in canonical byte order. The wire carries it
// as a hugeint with the top bit flipped (duckdb's ordering trick); the flip
// is undone on decode, so these bytes match what the value looked like in
// SQL.
type UUID [16]byte

func (u UUID) String() string {
	const hexdig = "0123456789abcdef"
	buf := make([]byte, 36)
	p := 0
	for i, b := range u {
		switch i {
		case 4, 6, 8, 10:
			buf[p] = '-'
			p++
		}
		buf[p] = hexdig[b>>4]
		buf[p+1] = hexdig[b&0xf]
		p += 2
	}
	return string(buf)
}

// Struct is a decoded STRUCT value. It is a slice, not a map, because
// duckdb struct fields are ordered and their names need not be unique;
// flattening to map[string]any would silently drop both properties.
type Struct []StructEntry

type StructEntry struct {
	Name  string
	Value any
}

// Field returns the first field with the given name.
func (s Struct) Field(name string) (any, bool) {
	for _, e := range s {
		if e.Name == name {
			return e.Value, true
		}
	}
	return nil, false
}

// MapEntry is one key/value pair of a decoded MAP value. A MAP decodes to
// []MapEntry rather than a Go map because duckdb map keys can be any type —
// including ones that are not valid Go map keys — and entry order is
// meaningful on the wire.
type MapEntry struct {
	Key, Value any
}

// duckdb epochs: DATE is days since 1970-01-01, timestamps are counts since
// the unix epoch in the unit named by the type.
func dateValue(days int32) time.Time {
	return time.Unix(int64(days)*86400, 0).UTC()
}

func timestampValue(id TypeID, v int64) time.Time {
	switch id {
	case TypeTimestampSec:
		return time.Unix(v, 0).UTC()
	case TypeTimestampMS:
		return time.UnixMilli(v).UTC()
	case TypeTimestampNS:
		return time.Unix(v/1_000_000_000, v%1_000_000_000).UTC()
	default: // TypeTimestamp, microseconds
		return time.UnixMicro(v).UTC()
	}
}
