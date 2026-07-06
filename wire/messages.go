package wire

import (
	"fmt"

	"github.com/mehrabr/duckcall/internal/qser"
)

// MessageType mirrors duckdb-quack's enum. The values are the wire contract.
type MessageType uint8

const (
	MsgInvalid            MessageType = 0
	MsgConnectionRequest  MessageType = 1
	MsgConnectionResponse MessageType = 2
	MsgPrepareRequest     MessageType = 3
	MsgPrepareResponse    MessageType = 4
	MsgFetchRequest       MessageType = 7
	MsgFetchResponse      MessageType = 8
	MsgAppendRequest      MessageType = 9
	MsgSuccess            MessageType = 10
	MsgDisconnect         MessageType = 11
	MsgError              MessageType = 100
)

func (t MessageType) String() string {
	switch t {
	case MsgConnectionRequest:
		return "CONNECTION_REQUEST"
	case MsgConnectionResponse:
		return "CONNECTION_RESPONSE"
	case MsgPrepareRequest:
		return "PREPARE_REQUEST"
	case MsgPrepareResponse:
		return "PREPARE_RESPONSE"
	case MsgFetchRequest:
		return "FETCH_REQUEST"
	case MsgFetchResponse:
		return "FETCH_RESPONSE"
	case MsgAppendRequest:
		return "APPEND_REQUEST"
	case MsgSuccess:
		return "SUCCESS_RESPONSE"
	case MsgDisconnect:
		return "DISCONNECT_MESSAGE"
	case MsgError:
		return "ERROR_RESPONSE"
	}
	return fmt.Sprintf("MESSAGE(%d)", uint8(t))
}

// Hugeint is a 128-bit value as duckdb serializes it; result uuids are one.
type Hugeint = qser.Hugeint

// QueryIDAbsent is the unset client_query_id (duckdb's empty optional_idx).
const QueryIDAbsent = qser.OptionalIdxAbsent

// Header is the first of the two serialized documents in every message.
type Header struct {
	Type MessageType

	// ConnectionID is empty on CONNECTION_REQUEST and on most responses.
	ConnectionID string

	// QueryID is an opaque client-chosen correlation id, echoed into server
	// logs. QueryIDAbsent when unset.
	QueryID uint64
}

// EncodeEnvelope frames a header and an already-encoded body document.
func EncodeEnvelope(h Header, body []byte) []byte {
	var w qser.Writer
	w.FieldUvarint(1, uint64(h.Type))
	if h.ConnectionID != "" {
		w.FieldString(2, h.ConnectionID)
	}
	w.FieldUvarint(3, h.QueryID)
	w.End()
	w.Raw(body)
	return w.Bytes()
}

// SplitEnvelope decodes the header document and returns the raw body
// document that follows it. The body stays opaque here: for result-bearing
// messages it is codec's input, and for everything else the typed decoders
// below take it.
func SplitEnvelope(buf []byte) (Header, []byte, error) {
	r := qser.NewReader(buf)
	var h Header
	if r.TryField(1) {
		h.Type = MessageType(r.Uvarint())
	} else {
		return h, nil, fmt.Errorf("wire: message header missing type")
	}
	if r.TryField(2) {
		h.ConnectionID = r.String()
	}
	h.QueryID = QueryIDAbsent
	if r.TryField(3) {
		h.QueryID = r.OptionalIdx()
	}
	r.End()
	if err := r.Err(); err != nil {
		return h, nil, fmt.Errorf("wire: bad message header: %w", err)
	}
	return h, r.Rest(), nil
}

// ConnectionRequest is the auth handshake payload.
type ConnectionRequest struct {
	AuthString    string
	ClientVersion string
	Platform      string
	MinVersion    uint64
	MaxVersion    uint64
}

func (m ConnectionRequest) Encode() []byte {
	var w qser.Writer
	if m.AuthString != "" {
		w.FieldString(1, m.AuthString)
	}
	if m.ClientVersion != "" {
		w.FieldString(2, m.ClientVersion)
	}
	if m.Platform != "" {
		w.FieldString(3, m.Platform)
	}
	w.FieldUvarint(4, m.MinVersion)
	w.FieldUvarint(5, m.MaxVersion)
	w.End()
	return w.Bytes()
}

func DecodeConnectionRequest(body []byte) (ConnectionRequest, error) {
	r := qser.NewReader(body)
	var m ConnectionRequest
	if r.TryField(1) {
		m.AuthString = r.String()
	}
	if r.TryField(2) {
		m.ClientVersion = r.String()
	}
	if r.TryField(3) {
		m.Platform = r.String()
	}
	if r.TryField(4) {
		m.MinVersion = r.Uvarint()
	}
	if r.TryField(5) {
		m.MaxVersion = r.Uvarint()
	}
	r.End()
	return m, wrapErr("connection request", r.Err())
}

// ConnectionResponse reports what the server is; the new connection id
// arrives in the header, not here.
type ConnectionResponse struct {
	ServerVersion string
	Platform      string
	QuackVersion  uint64
}

func (m ConnectionResponse) Encode() []byte {
	var w qser.Writer
	if m.ServerVersion != "" {
		w.FieldString(1, m.ServerVersion)
	}
	if m.Platform != "" {
		w.FieldString(2, m.Platform)
	}
	w.FieldUvarint(3, m.QuackVersion)
	w.End()
	return w.Bytes()
}

func DecodeConnectionResponse(body []byte) (ConnectionResponse, error) {
	r := qser.NewReader(body)
	var m ConnectionResponse
	if r.TryField(1) {
		m.ServerVersion = r.String()
	}
	if r.TryField(2) {
		m.Platform = r.String()
	}
	if r.TryField(3) {
		m.QuackVersion = r.Uvarint()
	}
	r.End()
	return m, wrapErr("connection response", r.Err())
}

// PrepareRequest carries SQL. There is no bind step in quack v1; despite
// the name, this executes.
type PrepareRequest struct {
	SQL string
}

func (m PrepareRequest) Encode() []byte {
	var w qser.Writer
	if m.SQL != "" {
		w.FieldString(1, m.SQL)
	}
	w.End()
	return w.Bytes()
}

func DecodePrepareRequest(body []byte) (PrepareRequest, error) {
	r := qser.NewReader(body)
	var m PrepareRequest
	if r.TryField(1) {
		m.SQL = r.String()
	}
	r.End()
	return m, wrapErr("prepare request", r.Err())
}

// FetchRequest asks for the next batch of a result.
type FetchRequest struct {
	UUID Hugeint
}

func (m FetchRequest) Encode() []byte {
	var w qser.Writer
	w.FieldHugeint(1, m.UUID)
	w.End()
	return w.Bytes()
}

func DecodeFetchRequest(body []byte) (FetchRequest, error) {
	r := qser.NewReader(body)
	var m FetchRequest
	if r.TryField(1) {
		m.UUID = r.Hugeint()
	}
	r.End()
	return m, wrapErr("fetch request", r.Err())
}

// EncodeEmptyBody is the payload of DISCONNECT_MESSAGE and SUCCESS_RESPONSE:
// an object with no fields.
func EncodeEmptyBody() []byte {
	var w qser.Writer
	w.End()
	return w.Bytes()
}

// ErrorMessage is the in-band failure payload; it rides in HTTP 200.
type ErrorMessage struct {
	Message string
}

func (m ErrorMessage) Encode() []byte {
	var w qser.Writer
	if m.Message != "" {
		w.FieldString(1, m.Message)
	}
	w.End()
	return w.Bytes()
}

func DecodeErrorMessage(body []byte) (ErrorMessage, error) {
	r := qser.NewReader(body)
	var m ErrorMessage
	if r.TryField(1) {
		m.Message = r.String()
	}
	r.End()
	return m, wrapErr("error response", r.Err())
}

func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("wire: bad %s: %w", what, err)
}
