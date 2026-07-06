package driver

import (
	"database/sql/driver"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mehrabr/duckcall/codec"
)

// The Quack wire has no parameterized-query message as of protocol v1, so
// placeholders are interpolated client-side into literals. That is a
// documented limitation, not a design choice; the day the protocol grows
// bind parameters, this file gets deleted with pleasure.
//
// interpolate replaces each ? outside strings, quoted identifiers, and
// comments with the corresponding escaped argument.
func interpolate(query string, args []driver.NamedValue) (string, error) {
	for _, a := range args {
		if a.Name != "" {
			return "", fmt.Errorf("duckcall: named parameters are not supported, use ordinal ?")
		}
	}
	if len(args) == 0 && !strings.ContainsRune(query, '?') {
		return query, nil
	}
	var b strings.Builder
	b.Grow(len(query) + 16*len(args))
	arg := 0
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch ch {
		case '?':
			if arg >= len(args) {
				return "", fmt.Errorf("duckcall: query has more placeholders than the %d args given", len(args))
			}
			lit, err := literal(args[arg].Value)
			if err != nil {
				return "", err
			}
			b.WriteString(lit)
			arg++
			continue
		case '\'', '"':
			end := skipQuoted(query, i, ch)
			b.WriteString(query[i:end])
			i = end - 1
			continue
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				end := strings.IndexByte(query[i:], '\n')
				if end < 0 {
					end = len(query)
				} else {
					end += i
				}
				b.WriteString(query[i:end])
				i = end - 1
				continue
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				end := strings.Index(query[i+2:], "*/")
				if end < 0 {
					end = len(query)
				} else {
					end += i + 4
				}
				b.WriteString(query[i:end])
				i = end - 1
				continue
			}
		}
		b.WriteByte(ch)
	}
	if arg != len(args) {
		return "", fmt.Errorf("duckcall: %d args for %d placeholders", len(args), arg)
	}
	return b.String(), nil
}

// skipQuoted returns the index just past a quoted region starting at start.
// SQL escapes a quote by doubling it, and duckdb follows suit.
func skipQuoted(s string, start int, quote byte) int {
	for i := start + 1; i < len(s); i++ {
		if s[i] == quote {
			if i+1 < len(s) && s[i+1] == quote {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(s)
}

func literal(v driver.Value) (string, error) {
	switch x := v.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if x {
			return "TRUE", nil
		}
		return "FALSE", nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		switch {
		case math.IsNaN(x):
			return "'nan'::DOUBLE", nil
		case math.IsInf(x, 1):
			return "'infinity'::DOUBLE", nil
		case math.IsInf(x, -1):
			return "'-infinity'::DOUBLE", nil
		}
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'", nil
	case []byte:
		var b strings.Builder
		b.WriteByte('\'')
		for _, c := range x {
			fmt.Fprintf(&b, `\x%02X`, c)
		}
		b.WriteString("'::BLOB")
		return b.String(), nil
	case time.Time:
		return "TIMESTAMP '" + x.UTC().Format("2006-01-02 15:04:05.999999") + "'", nil
	case codec.Decimal:
		return x.String(), nil
	}
	return "", fmt.Errorf("duckcall: cannot interpolate %T", v)
}
