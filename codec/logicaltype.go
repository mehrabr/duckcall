package codec

import (
	"fmt"
	"strings"

	"github.com/mehrabr/duckcall/internal/qser"
)

// TypeID mirrors duckdb's LogicalTypeId for the ids the wire can carry.
type TypeID uint32

const (
	TypeInvalid      TypeID = 0
	TypeSQLNull      TypeID = 1
	TypeBoolean      TypeID = 10
	TypeTinyint      TypeID = 11
	TypeSmallint     TypeID = 12
	TypeInteger      TypeID = 13
	TypeBigint       TypeID = 14
	TypeDate         TypeID = 15
	TypeTime         TypeID = 16
	TypeTimestampSec TypeID = 17
	TypeTimestampMS  TypeID = 18
	TypeTimestamp    TypeID = 19 // microseconds, duckdb's default
	TypeTimestampNS  TypeID = 20
	TypeDecimal      TypeID = 21
	TypeFloat        TypeID = 22
	TypeDouble       TypeID = 23
	TypeVarchar      TypeID = 25
	TypeBlob         TypeID = 26
	TypeInterval     TypeID = 27
	TypeUTinyint     TypeID = 28
	TypeUSmallint    TypeID = 29
	TypeUInteger     TypeID = 30
	TypeUBigint      TypeID = 31
	TypeUHugeint     TypeID = 49
	TypeHugeint      TypeID = 50
	TypeUUID         TypeID = 54
	TypeList         TypeID = 100
	TypeStruct       TypeID = 101
	TypeMap          TypeID = 102
	TypeArray        TypeID = 103
	TypeEnum         TypeID = 104
	TypeUnion        TypeID = 107
)

// LogicalType is a decoded type tree. Nested kinds (list/struct/map) are
// decoded structurally even though their vectors are Tier 2, so an
// unsupported column can still be named and skipped cleanly.
type LogicalType struct {
	ID TypeID

	// Decimal only.
	Width, Scale uint8

	// List/array element, map entry.
	Child *LogicalType

	// Struct fields.
	Fields []StructField
}

type StructField struct {
	Name string
	Type LogicalType
}

func (t LogicalType) String() string {
	switch t.ID {
	case TypeDecimal:
		return fmt.Sprintf("DECIMAL(%d,%d)", t.Width, t.Scale)
	case TypeList:
		return t.Child.String() + "[]"
	case TypeStruct:
		var b strings.Builder
		b.WriteString("STRUCT(")
		for i, f := range t.Fields {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %s", f.Name, f.Type)
		}
		b.WriteString(")")
		return b.String()
	case TypeMap:
		return "MAP(" + t.Child.String() + ")"
	}
	if n, ok := typeNames[t.ID]; ok {
		return n
	}
	return fmt.Sprintf("TYPE(%d)", uint32(t.ID))
}

var typeNames = map[TypeID]string{
	TypeSQLNull: "NULL", TypeBoolean: "BOOLEAN",
	TypeTinyint: "TINYINT", TypeSmallint: "SMALLINT", TypeInteger: "INTEGER", TypeBigint: "BIGINT",
	TypeUTinyint: "UTINYINT", TypeUSmallint: "USMALLINT", TypeUInteger: "UINTEGER", TypeUBigint: "UBIGINT",
	TypeHugeint: "HUGEINT", TypeUHugeint: "UHUGEINT",
	TypeFloat: "FLOAT", TypeDouble: "DOUBLE",
	TypeVarchar: "VARCHAR", TypeBlob: "BLOB",
	TypeDate: "DATE", TypeTime: "TIME",
	TypeTimestampSec: "TIMESTAMP_S", TypeTimestampMS: "TIMESTAMP_MS",
	TypeTimestamp: "TIMESTAMP", TypeTimestampNS: "TIMESTAMP_NS",
	TypeInterval: "INTERVAL", TypeUUID: "UUID", TypeEnum: "ENUM",
	TypeArray: "ARRAY", TypeUnion: "UNION",
}

// Type object fields on the wire.
const (
	ftID     = 1
	ftWidth  = 2
	ftScale  = 3
	ftChild  = 4
	ftFields = 5
	ftName   = 1 // within a struct-field object
	ftType   = 2
)

// maxTypeDepth bounds recursion on hostile input.
const maxTypeDepth = 64

func decodeType(r *qser.Reader, depth int) (LogicalType, error) {
	if depth > maxTypeDepth {
		return LogicalType{}, fmt.Errorf("codec: type tree deeper than %d", maxTypeDepth)
	}
	var t LogicalType
	for {
		id, kind, err := r.Field()
		if err != nil {
			return t, err
		}
		if id == 0 {
			if t.ID == TypeInvalid {
				return t, fmt.Errorf("codec: type object missing id")
			}
			return t, nil
		}
		switch id {
		case ftID:
			v, err := r.Uvarint()
			if err != nil {
				return t, err
			}
			t.ID = TypeID(v)
		case ftWidth:
			v, err := r.Uvarint()
			if err != nil {
				return t, err
			}
			t.Width = uint8(v)
		case ftScale:
			v, err := r.Uvarint()
			if err != nil {
				return t, err
			}
			t.Scale = uint8(v)
		case ftChild:
			child, err := decodeType(r, depth+1)
			if err != nil {
				return t, err
			}
			t.Child = &child
		case ftFields:
			n, err := r.Uvarint()
			if err != nil {
				return t, err
			}
			if n > uint64(r.Remaining()) {
				return t, qser.ErrTruncated
			}
			t.Fields = make([]StructField, 0, n)
			for i := uint64(0); i < n; i++ {
				f, err := decodeStructField(r, depth+1)
				if err != nil {
					return t, err
				}
				t.Fields = append(t.Fields, f)
			}
		default:
			if err := r.Skip(kind); err != nil {
				return t, err
			}
		}
	}
}

func decodeStructField(r *qser.Reader, depth int) (StructField, error) {
	var f StructField
	for {
		id, kind, err := r.Field()
		if err != nil {
			return f, err
		}
		if id == 0 {
			return f, nil
		}
		switch id {
		case ftName:
			b, err := r.Bytes()
			if err != nil {
				return f, err
			}
			f.Name = string(b)
		case ftType:
			f.Type, err = decodeType(r, depth+1)
			if err != nil {
				return f, err
			}
		default:
			if err := r.Skip(kind); err != nil {
				return f, err
			}
		}
	}
}
