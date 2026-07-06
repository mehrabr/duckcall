package codec

import "fmt"

// ErrUnsupportedType marks a column this codec version cannot decode yet.
// The contract: the column reports this error, the rest of the chunk still
// decodes. Never a panic, never a silently wrong value.
type ErrUnsupportedType struct {
	Type LogicalType
}

func (e ErrUnsupportedType) Error() string {
	return fmt.Sprintf("codec: unsupported type %s", e.Type)
}
