// Package bridge relays JSON-RPC messages between a stdio MCP client and an
// upstream transport.
//
// The relay is transparent: no message is inspected for its MCP meaning,
// rewritten, filtered, or answered locally. The only messages mcp-bridge
// originates are error responses for requests it could not forward.
package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"

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
	out      io.Writer
	logs     io.Writer

	// outMu serialises stdout writes. Two goroutines write there — the
	// upstream reader and the error path of the client reader — and an
	// interleaved write would corrupt the newline framing that the client
	// parses.
	outMu sync.Mutex
}

// New creates a bridge. out receives JSON-RPC and nothing else; logs receives
// everything else. In production out is stdout and logs is stderr, and mixing
// them breaks every MCP client that connects.
func New(upstream transport.Transport, in io.Reader, out, logs io.Writer) *Bridge {
	return &Bridge{upstream: upstream, in: in, out: out, logs: logs}
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
	scanner := bufio.NewScanner(b.in)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// scanner reuses its buffer, and the message outlives this iteration
		// once it reaches the transport.
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
		if err != nil {
			// Nothing to address a response to: the id is inside the message
			// that would not parse.
			b.logf("ignoring unparseable message from the client: %v", err)
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

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read from client: %w", err)
	}
	return nil
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
// The payload is compacted first: stdio framing is newline-delimited, so a
// server that pretty-prints its JSON would otherwise split one message across
// several lines and desynchronise the client for the rest of the session.
func (b *Bridge) write(msg []byte) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, msg); err != nil {
		// Writing this would corrupt the stream for every later message, so
		// dropping it is the lesser harm. It is logged in full because a
		// server emitting non-JSON is a bug worth seeing.
		b.logf("dropping a malformed upstream message (%v): %s", err, truncate(msg, 400))
		return
	}
	buf.WriteByte('\n')

	b.outMu.Lock()
	defer b.outMu.Unlock()
	if _, err := b.out.Write(buf.Bytes()); err != nil {
		b.logf("write to the client failed: %v", err)
	}
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
