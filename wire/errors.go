package wire

import (
	"errors"
	"fmt"
	"strings"
)

// ServerError is a failure reported by the server. Quack delivers protocol
// errors in-band (ERROR_RESPONSE inside HTTP 200), in which case Status is
// zero; a non-zero Status means the HTTP layer itself refused — a proxy,
// a wrong endpoint, not a Quack handler.
type ServerError struct {
	Status  int
	Message string
}

func (e *ServerError) Error() string {
	switch {
	case e.Status == 0:
		return "wire: server error: " + e.Message
	case e.Message == "":
		return fmt.Sprintf("wire: server returned HTTP %d", e.Status)
	default:
		return fmt.Sprintf("wire: server returned HTTP %d: %s", e.Status, e.Message)
	}
}

// ErrClosed is returned by operations on a closed client.
var ErrClosed = errors.New("wire: client is closed")

// ErrConnectionExpired matches via errors.Is when the server no longer
// knows this client's connection id — typically a server restart wiped its
// in-memory session table, or something disconnected the session out of
// band. The transport is healthy; only the logical session is gone, and any
// unfetched results went with it.
var ErrConnectionExpired = errors.New("wire: server no longer knows this connection id")

// connectionExpired recognizes a dead connection id in server error text.
// The protocol has no typed error codes, so this string match against
// quack_server.cpp's two literals ("Invalid connection id" for any normal
// message, the longer text for DISCONNECT of an unknown session) is the one
// place duckcall interprets error prose. Re-verify both on every supported
// release bump.
func connectionExpired(msg string) bool {
	return strings.Contains(msg, "Invalid connection id") ||
		strings.Contains(msg, "Connection does not exist / already disconnected")
}

// Is makes errors.Is(err, ErrConnectionExpired) work on in-band server
// errors without a wrapper type.
func (e *ServerError) Is(target error) bool {
	return target == ErrConnectionExpired && e.Status == 0 && connectionExpired(e.Message)
}

// redactToken scrubs the token from any string that might surface in logs.
// Tokens travel in headers, not URLs, but a server echoing its input back
// into an error message would otherwise leak straight through us.
func redactToken(token, s string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

func (c *Client) redactErr(err error) error {
	if err == nil {
		return nil
	}
	redacted := redactToken(c.cfg.Token, err.Error())
	if redacted == err.Error() {
		return err
	}
	return errors.New(redacted)
}
