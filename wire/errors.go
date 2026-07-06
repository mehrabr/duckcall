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
