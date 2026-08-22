// Package transport speaks to the upstream MCP server.
//
// mcp-bridge has exactly one upstream transport: Streamable HTTP. The
// predecessor also had a stdio-child-process transport and called its HTTP
// one "sse", a name that had not matched the protocol since the Streamable
// HTTP revision. Neither survives here.
package transport

// Transport is a bidirectional JSON-RPC message channel.
type Transport interface {
	// Send delivers one JSON-RPC message upstream. Any responses the server
	// produces arrive through Receive.
	Send(data []byte) error

	// Receive returns the next message from the server. The second return
	// value is false once the channel is closed and drained.
	Receive() ([]byte, bool)

	// Close releases the connection and, where the protocol has one, ends the
	// server-side session.
	Close() error
}

// TokenProvider supplies a Bearer token for the upstream request.
//
// Invalidate is called when the server answers 401, so the next Token call
// produces a fresh credential rather than replaying the rejected one.
type TokenProvider interface {
	Token() (string, error)
	Invalidate()
}
