package transport

import (
	"context"
	"fmt"
	"io"

	"github.com/nlink-jp/mcp/client"
)

// conn adapts a Transport to nlink-jp/mcp's client.Conn, so that inspect
// opens a server with the module's era rules instead of a handshake of its
// own (ADR-0008 §9). The transport already derives the headers
// (Tracker.Outgoing), judges non-2xx answers (Classify) and passes every
// answer through Tracker.Incoming; the adapter only bounds the wait.
type conn struct {
	t Transport
}

// NewConn wraps t. Closing the Conn closes t.
func NewConn(t Transport) client.Conn {
	return &conn{t: t}
}

// Send posts msg. A transport's Send returns once the answer is queued, so it
// runs beside ctx: past the deadline Send returns, and the POST may still
// have reached the server.
func (c *conn) Send(ctx context.Context, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", client.ErrNotSent, err)
	}
	done := make(chan error, 1)
	go func() { done <- c.t.Send(msg) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Recv returns the next message, or io.EOF once the transport is closed and
// drained.
func (c *conn) Recv() ([]byte, error) {
	msg, ok := c.t.Receive()
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

func (c *conn) Close() error { return c.t.Close() }
