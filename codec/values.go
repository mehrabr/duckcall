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
func (d Decimal) BigInt() *big.Int {
	v := new(big.Int).SetUint64(d.lo)
	hi := new(big.Int).SetInt64(d.hi)
	hi.Lsh(hi, 64)
	return v.Add(v, hi)
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
	if us == 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d:%02d.%06d", h, m, s, us)
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
