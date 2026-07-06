package wire

import (
	"errors"
	"fmt"
	"strings"
)

// ServerError is a non-2xx response from the server, message included when
// the server sent one.
type ServerError struct {
	Status  int
	Message string
}

func (e *ServerError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("wire: server returned %d", e.Status)
	}
	return fmt.Sprintf("wire: server returned %d: %s", e.Status, e.Message)
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
