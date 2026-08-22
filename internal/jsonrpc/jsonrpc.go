// Package jsonrpc provides the minimum JSON-RPC 2.0 handling a transparent
// relay needs: enough to tell a request from a notification from a response,
// and enough to synthesise an error response addressed to a specific request.
//
// It deliberately does not model MCP itself. mcp-bridge forwards messages
// unmodified, so nothing here parses methods, tools, or schemas.
package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Standard JSON-RPC 2.0 error codes (§5.1) plus the range mcp-bridge uses.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeInternalError  = -32603
)

// Message is a JSON-RPC 2.0 message in any of its three shapes.
//
// ID is kept raw because the specification allows a string, a number, or null,
// and a relay must echo back exactly what it was given — reformatting an id
// (3 becoming 3.0, say) breaks the client's own request correlation.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the error member of a response.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Parse decodes one JSON-RPC message.
func Parse(data []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC message: %w", err)
	}
	return &m, nil
}

// IsBatch reports whether the payload is a JSON array. Batches cannot be
// answered per-request by a relay that does not parse them, so callers forward
// them untouched rather than trying to attribute an error to one member.
func IsBatch(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '['
}

// hasID reports whether the message carries an id that is not JSON null.
func (m *Message) hasID() bool {
	if len(m.ID) == 0 {
		return false
	}
	return !bytes.Equal(bytes.TrimSpace(m.ID), []byte("null"))
}

// IsRequest reports whether the message expects a response.
func (m *Message) IsRequest() bool { return m.Method != "" && m.hasID() }

// IsNotification reports whether the message is a method call with no reply.
func (m *Message) IsNotification() bool { return m.Method != "" && !m.hasID() }

// IsResponse reports whether the message answers an earlier request.
func (m *Message) IsResponse() bool { return m.Method == "" && m.hasID() }

// IDKey returns a stable map key for correlating a response with its request.
// Requests with no usable id return "", which callers must not correlate on.
func (m *Message) IDKey() string {
	if !m.hasID() {
		return ""
	}
	return string(bytes.TrimSpace(m.ID))
}

// NewErrorResponse builds a response carrying an error, addressed to id.
//
// This exists to keep one invariant: a client request that cannot be forwarded
// still gets an answer. Staying silent makes the client block until its own
// timeout and hides the real cause — usually an expired token, where the fix
// is a single login command the user never sees suggested.
func NewErrorResponse(id json.RawMessage, code int, message string) []byte {
	if len(bytes.TrimSpace(id)) == 0 {
		id = json.RawMessage("null")
	}
	resp := Message{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}
	// Marshalling a struct of plain fields cannot fail; a raw id that is not
	// valid JSON is the only risk, and it came from a decoded message.
	encoded, err := json.Marshal(resp)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	return encoded
}
