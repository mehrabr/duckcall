package codec

import (
	"fmt"
	"math"
	"strings"

	"github.com/mehrabr/duckcall/internal/qser"
)

// TypeID mirrors duckdb's LogicalTypeId for the ids the wire can carry.
// Verified against src/include/duckdb/common/types.hpp at v1.5.4.
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
	TypeTimestampTZ  TypeID = 32
	TypeTimeTZ       TypeID = 34
	TypeTimeNS       TypeID = 35
	TypeBit          TypeID = 36
	TypeUHugeint     TypeID = 49
	TypeHugeint      TypeID = 50
	TypeUUID         TypeID = 54
	TypeGeometry     TypeID = 60
	TypeStruct       TypeID = 100
	TypeList         TypeID = 101
	TypeMap          TypeID = 102
	TypeEnum         TypeID = 104
	TypeUnion        TypeID = 107
	TypeArray        TypeID = 108
)

// LogicalType is a decoded type tree. Nested kinds (list/struct/map/array)
// are decoded structurally even though their vectors are Tier 2, so an
// unsupported column can still be named and its bytes parsed past cleanly.
type LogicalType struct {
	ID TypeID

	// Decimal only.
	Width, Scale uint8

	// List/array element; for MAP the child is the key/value struct.
	Child *LogicalType

	// Struct fields.
	Fields []StructField

	// Enum dictionary, index-ordered.
	Enum []string

	// Array only.
	ArraySize uint32
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
	case TypeArray:
		return fmt.Sprintf("%s[%d]", t.Child.String(), t.ArraySize)
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
		if t.Child != nil && len(t.Child.Fields) == 2 {
			return fmt.Sprintf("MAP(%s, %s)", t.Child.Fields[0].Type, t.Child.Fields[1].Type)
		}
		return "MAP"
	case TypeEnum:
		return fmt.Sprintf("ENUM(%d values)", len(t.Enum))
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
	TypeVarchar: "VARCHAR", TypeBlob: "BLOB", TypeBit: "BIT",
	TypeDate: "DATE", TypeTime: "TIME", TypeTimeTZ: "TIMETZ", TypeTimeNS: "TIME_NS",
	TypeTimestampSec: "TIMESTAMP_S", TypeTimestampMS: "TIMESTAMP_MS",
	TypeTimestamp: "TIMESTAMP", TypeTimestampNS: "TIMESTAMP_NS", TypeTimestampTZ: "TIMESTAMPTZ",
	TypeInterval: "INTERVAL", TypeUUID: "UUID", TypeGeometry: "GEOMETRY", TypeUnion: "UNION",
}

// ExtraTypeInfoType mirrors duckdb's enum; the wire tags each type-info
// object with one of these.
const (
	infoInvalid = 0
	infoGeneric = 1
	infoDecimal = 2
	infoString  = 3
	infoList    = 4
	infoStruct  = 5
	infoEnum    = 6
	infoArray   = 9
)

// maxTypeDepth bounds recursion on hostile input.
const maxTypeDepth = 64

// decodeType reads one serialized LogicalType object: field 100 the id,
// optional field 101 a nullable ExtraTypeInfo object carrying the
// parameters of parameterized types. The caller consumes neither the
// leading field id (lists carry none) nor writes one; this reads contents
// plus the object terminator.
func decodeType(r *qser.Reader, depth int) (LogicalType, error) {
	if depth > maxTypeDepth {
		return LogicalType{}, fmt.Errorf("codec: type tree deeper than %d", maxTypeDepth)
	}
	var t LogicalType
	if !r.TryField(100) {
		return t, fmt.Errorf("codec: type object missing id")
	}
	t.ID = TypeID(r.Uvarint())
	if r.TryField(101) {
		if r.Present() {
			if err := decodeTypeInfo(r, &t, depth); err != nil {
				return t, err
			}
		}
	}
	r.End()
	return t, r.Err()
}

func decodeTypeInfo(r *qser.Reader, t *LogicalType, depth int) error {
	if !r.TryField(100) {
		return fmt.Errorf("codec: type info missing kind")
	}
	kind := r.Uvarint()
	if r.TryField(101) {
		_ = r.String() // alias; carried but unused
	}
	if r.TryField(103) {
		// extension_info holds arbitrary Values; nothing Tier 1 carries it
		// and its encoding cannot be skipped without a schema for it.
		if r.Present() {
			return fmt.Errorf("codec: type %s carries extension info this codec cannot parse", t)
		}
	}
	switch kind {
	case infoGeneric:
	case infoDecimal:
		if r.TryField(200) {
			t.Width = uint8(r.Uvarint())
		}
		if r.TryField(201) {
			t.Scale = uint8(r.Uvarint())
		}
	case infoString:
		if r.TryField(200) {
			_ = r.String() // collation; irrelevant to decoding
		}
	case infoList:
		if !r.TryField(200) {
			return fmt.Errorf("codec: list type missing child")
		}
		child, err := decodeType(r, depth+1)
		if err != nil {
			return err
		}
		t.Child = &child
	case infoStruct:
		if r.TryField(200) {
			n := r.ListCount()
			t.Fields = make([]StructField, 0, n)
			for range n {
				// child_types is a vector of pairs; pairs serialize as
				// objects with fields 0 (first) and 1 (second).
				var f StructField
				if r.TryField(0) {
					f.Name = r.String()
				}
				if !r.TryField(1) {
					return fmt.Errorf("codec: struct field %q missing type", f.Name)
				}
				ft, err := decodeType(r, depth+1)
				if err != nil {
					return err
				}
				f.Type = ft
				r.End()
				t.Fields = append(t.Fields, f)
			}
		}
	case infoEnum:
		if !r.TryField(200) {
			return fmt.Errorf("codec: enum type missing values count")
		}
		n := r.Uvarint()
		if r.TryField(201) {
			count := r.ListCount()
			t.Enum = make([]string, 0, count)
			for range count {
				t.Enum = append(t.Enum, r.String())
			}
		}
		if uint64(len(t.Enum)) != n {
			return fmt.Errorf("codec: enum dictionary has %d values, header says %d", len(t.Enum), n)
		}
	case infoArray:
		if !r.TryField(200) {
			return fmt.Errorf("codec: array type missing child")
		}
		child, err := decodeType(r, depth+1)
		if err != nil {
			return err
		}
		t.Child = &child
		if r.TryField(201) {
			t.ArraySize = uint32(r.Uvarint())
		}
	default:
		// Unknown type info cannot be skipped (no wire kinds); refusing the
		// whole payload beats desyncing the stream.
		return fmt.Errorf("codec: type %s carries type info kind %d this codec does not know", t, kind)
	}
	r.End()
	return r.Err()
}

// enumIndexWidth is duckdb's EnumTypeInfo::DictType: the physical width of
// an enum vector follows its dictionary size.
func enumIndexWidth(dictSize int) int {
	switch {
	case dictSize <= math.MaxUint8:
		return 1
	case dictSize <= math.MaxUint16:
		return 2
	default:
		return 4
	}
}
