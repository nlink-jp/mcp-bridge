// Package bridge relays JSON-RPC messages between a stdio MCP client and an
// upstream transport.
//
// The relay passes the era the client opens through unchanged (ADR-0008 §2):
// it answers neither initialize nor server/discover, and message bodies keep
// their content. What the specification requires of an HTTP client — headers
// derived from the body, excluding tools with invalid x-mcp-header marks —
// happens in the transport. The messages mcp-bridge originates here are error
// responses for requests it could not forward or could not read.
package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/nlink-jp/mcp/wire"

	"github.com/nlink-jp/mcp-bridge/internal/jsonrpc"
	"github.com/nlink-jp/mcp-bridge/internal/transport"
)

// maxLineBytes bounds one message from the client. Tool arguments carrying
// file contents get large, so the limit is generous.
const maxLineBytes = 8 << 20 // 8 MiB

// Bridge pipes a stdio client to an upstream transport.
type Bridge struct {
	upstream transport.Transport
	in       io.Reader
	logs     io.Writer

	// out serialises stdout writes and writes each message as one line. Two
	// goroutines write there — the upstream reader and the error path of the
	// client reader — and an interleaved write would corrupt the newline
	// framing that the client parses.
	out *wire.Writer
}

// New creates a bridge. out receives JSON-RPC and nothing else; logs receives
// everything else. In production out is stdout and logs is stderr, and mixing
// them breaks every MCP client that connects.
func New(upstream transport.Transport, in io.Reader, out, logs io.Writer) *Bridge {
	return &Bridge{upstream: upstream, in: in, out: wire.NewWriter(out), logs: logs}
}

// Run relays until the client closes its input, then shuts the upstream down.
func (b *Bridge) Run() error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.pumpUpstream()
	}()

	err := b.pumpClient()

	// Closing the upstream unblocks the reader above.
	if closeErr := b.upstream.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	<-done
	return err
}

// pumpClient reads newline-delimited messages from the client and forwards
// them upstream.
func (b *Bridge) pumpClient() error {
	reader := wire.NewReader(b.in, maxLineBytes)

	for {
		line, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, wire.ErrLineTooLong) {
			// The rest of the line was skipped, so its id is unknown. Answer
			// with a null id and keep reading: one oversized message must not
			// end the relay (ADR-0008 §7).
			b.logf("skipping a client message longer than %d bytes", maxLineBytes)
			b.write(jsonrpc.NewErrorResponse(nil, jsonrpc.CodeParseError,
				fmt.Sprintf("message longer than %d bytes", maxLineBytes)))
			continue
		}
		if err != nil {
			return fmt.Errorf("read from client: %w", err)
		}
		// The reader reuses its buffer, and the message outlives this
		// iteration once it reaches the transport.
		msg := append([]byte(nil), line...)

		// A batch cannot be answered per-member without parsing it, and
		// mcp-bridge does not parse message bodies. Forward it untouched and
		// let the server answer.
		if jsonrpc.IsBatch(msg) {
			if err := b.upstream.Send(msg); err != nil {
				b.logf("forwarding a batch failed: %v", err)
			}
			continue
		}

		parsed, err := jsonrpc.Parse(msg)
		if err == nil && parsed.Method == "" && len(parsed.ID) == 0 && parsed.Result == nil && parsed.Error == nil {
			// JSON that decodes to no message at all — a bare null, an empty
			// object (ADR-0008 §11.6).
			err = errors.New("not a JSON-RPC message")
		}
		if err != nil {
			// Nothing to address a response to: the id is inside the message
			// that would not parse.
			b.logf("ignoring unparseable message from the client: %v", err)
			if json.Valid(msg) {
				// JSON, but not a JSON-RPC message (ADR-0008 §11.6).
				b.write(jsonrpc.NewErrorResponse(nil, jsonrpc.CodeInvalidRequest, "invalid JSON-RPC message"))
				continue
			}
			b.write(jsonrpc.NewErrorResponse(nil, jsonrpc.CodeParseError, "invalid JSON-RPC message"))
			continue
		}

		if err := b.upstream.Send(msg); err != nil {
			b.logf("forwarding %s failed: %v", describe(parsed), err)
			// The invariant: a request that could not be forwarded still gets
			// an answer. Silence would make the client block until its own
			// timeout with no clue what went wrong — and the usual cause, an
			// expired token, has a one-command fix the user never sees
			// suggested.
			if parsed.IsRequest() {
				b.write(jsonrpc.NewErrorResponse(parsed.ID, jsonrpc.CodeInternalError, err.Error()))
			}
		}
	}
}

// pumpUpstream forwards server messages to the client.
func (b *Bridge) pumpUpstream() {
	for {
		msg, ok := b.upstream.Receive()
		if !ok {
			return
		}
		if len(bytes.TrimSpace(msg)) == 0 {
			continue
		}
		b.write(msg)
	}
}

// write emits one message as a single line.
//
// wire.Writer compacts the payload first: stdio framing is newline-delimited,
// so a server that pretty-prints its JSON would otherwise split one message
// across several lines and desynchronise the client for the rest of the
// session. A payload that is not JSON is refused rather than written, because
// writing it would corrupt the stream for every later message. The transport
// answers a request whose 2xx body is not JSON itself (ADR-0008 §3), so what
// is dropped here is an unsolicited message.
func (b *Bridge) write(msg []byte) {
	err := b.out.Write(msg)
	if err == nil {
		return
	}
	// Only a payload that is not JSON is quoted: a server emitting non-JSON is
	// a bug worth seeing. A failed write to the client is an I/O error, and
	// the message — a tool result, say — has no business in the log.
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		b.logf("dropping a malformed upstream message (%v): %s", err, truncate(msg, 400))
		return
	}
	b.logf("write to the client failed: %v", err)
}

// logf writes a diagnostic. Everything that is not JSON-RPC goes here.
func (b *Bridge) logf(format string, args ...any) {
	if b.logs == nil {
		return
	}
	fmt.Fprintf(b.logs, "mcp-bridge: "+format+"\n", args...)
}

// describe names a message for a log line without quoting its contents, which
// may hold arguments the user would not want in a log.
func describe(m *jsonrpc.Message) string {
	switch {
	case m.IsRequest():
		return fmt.Sprintf("request %s (id %s)", m.Method, m.IDKey())
	case m.IsNotification():
		return fmt.Sprintf("notification %s", m.Method)
	default:
		return "message"
	}
}

func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
