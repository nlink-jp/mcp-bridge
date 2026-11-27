package transport

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nlink-jp/mcp/httpwire"

	"github.com/nlink-jp/mcp-bridge/internal/jsonrpc"
)

// maxMessageBytes bounds one upstream answer: a JSON body, one SSE line, and
// one SSE event's joined data lines. MCP tool results carrying file contents
// are routinely large, so the limit is generous; without one, a server that
// never emits a newline would grow the buffer without bound.
const maxMessageBytes = 8 << 20 // 8 MiB

// codeInvalidParams answers a request the transport would not send: a marked
// argument that cannot be mirrored into a header, or a tools/call with no
// name (ADR-0008 §4).
const codeInvalidParams = -32602

// httpTransport speaks the MCP Streamable HTTP transport.
//
// Every client message is an HTTP POST. The server answers in one of three
// ways: application/json with a single JSON-RPC message, text/event-stream
// with an SSE stream carrying one or more, or 202 Accepted with no body at
// all (the normal answer to a notification). Session affinity, when a legacy
// server wants it, rides on the Mcp-Session-Id header.
//
// The era is the client's (ADR-0008 §2). What the specification requires of
// an HTTP client is added here, through nlink-jp/mcp/httpwire: the headers
// derived from each body (Tracker.Outgoing), the version and x-mcp-header
// marks learned from the answers (Tracker.Incoming), and the judgement of
// non-2xx answers (Classify).
type httpTransport struct {
	endpoint string
	headers  map[string]string
	client   *http.Client
	auth     TokenProvider
	logf     func(format string, args ...any)

	tracker *httpwire.Tracker

	incoming chan []byte
	closed   chan struct{}

	mu        sync.Mutex
	isClosed  bool
	sessionID string

	// streams counts in-flight SSE readers so Close can wait for them rather
	// than racing their sends against the closed channel.
	streams sync.WaitGroup
}

// Option configures an HTTP transport.
type Option func(*httpTransport)

// WithHeaders sends static headers with every request. They are applied after
// any token provider, so an explicit Authorization header wins — which is what
// a user who wrote one in the config means. The MCP protocol headers are
// applied after them; the config refuses those names (ADR-0008 §4).
func WithHeaders(headers map[string]string) Option {
	return func(t *httpTransport) { t.headers = headers }
}

// WithTokenProvider authenticates each request with a Bearer token.
func WithTokenProvider(auth TokenProvider) Option {
	return func(t *httpTransport) { t.auth = auth }
}

// WithHTTPClient replaces the HTTP client. Tests use it; production does not.
func WithHTTPClient(client *http.Client) Option {
	return func(t *httpTransport) { t.client = client }
}

// WithLogs sends the transport's diagnostics — a removed tool, a batch sent
// without protocol headers, a synthesized answer — to w. Without it they are
// discarded. Nothing here ever logs argument or result content.
func WithLogs(w io.Writer) Option {
	return func(t *httpTransport) {
		if w == nil {
			return
		}
		t.logf = func(format string, args ...any) {
			fmt.Fprintf(w, "mcp-bridge: "+format+"\n", args...)
		}
	}
}

// NewHTTP creates a Streamable HTTP transport for the given MCP endpoint.
func NewHTTP(endpoint string, opts ...Option) (Transport, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("upstream URL is required")
	}
	t := &httpTransport{
		endpoint: endpoint,
		client:   defaultClient(nil),
		logf:     func(string, ...any) {},
		tracker:  httpwire.NewTracker(),
		incoming: make(chan []byte, 64),
		closed:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

// defaultClient bounds connection setup and the wait for response headers
// without bounding the response body: an SSE stream stays open for the life of
// the session, so a whole-request timeout would sever a healthy connection.
// A nil tlsConfig means Go's usual certificate verification.
func defaultClient(tlsConfig *tls.Config) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:       tlsConfig,
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ExpectContinueTimeout: time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// outgoing is one message on its way upstream.
type outgoing struct {
	body []byte
	// id is the request's id; nil for a notification, a response and a batch.
	id json.RawMessage
	// method is the request's method; empty unless id is set.
	method string
	// header holds the MCP protocol headers derived from the body.
	header http.Header
}

// Send posts one message and routes whatever the server sends back.
func (t *httpTransport) Send(data []byte) error {
	out, err := t.prepare(data)
	if err != nil || out == nil {
		return err
	}
	return t.exchange(out)
}

// prepare derives the protocol headers. A nil outgoing with a nil error
// means the request was answered here and is not to be sent.
func (t *httpTransport) prepare(data []byte) (*outgoing, error) {
	out := &outgoing{body: data}
	batch := jsonrpc.IsBatch(data)
	if !batch {
		if m, err := jsonrpc.Parse(data); err == nil && m.IsRequest() {
			out.id, out.method = m.ID, m.Method
		}
	}
	header, err := t.tracker.Outgoing(data)
	if err != nil {
		if batch {
			// Batches belong to 2025-03-26 alone; the server decides what to
			// make of one. It goes as it always has.
			t.logf("forwarding a batch without MCP protocol headers")
			return out, nil
		}
		if out.id != nil {
			// The message would contradict its own headers, so it is not sent.
			// httpwire's error names the argument and the tool, never a value.
			return nil, t.deliver(jsonrpc.NewErrorResponse(out.id, codeInvalidParams, err.Error()))
		}
		return nil, fmt.Errorf("not sent: %w", err)
	}
	out.header = header
	return out, nil
}

// exchange posts the message, renewing the token once on a 401, and routes
// the answer.
func (t *httpTransport) exchange(out *outgoing) error {
	resp, err := t.post(out)
	if err != nil {
		return err
	}

	// A 401 usually means the access token expired. Drop it and try once with
	// a fresh one; a second 401 is a real authentication failure and the error
	// says so, because the fix (log in again) differs from a retry.
	//
	// Both failure paths keep the 401 attached. Without it the user sees only
	// the after-effect — "no access token" — and goes looking for an empty
	// token file instead of a rejected credential.
	if resp.StatusCode == http.StatusUnauthorized && t.auth != nil {
		drain(resp)
		t.auth.Invalidate()
		resp, err = t.post(out)
		if err != nil {
			return fmt.Errorf("upstream rejected the credentials (HTTP 401): %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			body := readSnippet(resp)
			return fmt.Errorf("upstream rejected the credentials (HTTP 401) again after renewing the token: %s", body)
		}
	}

	return t.route(resp, out)
}

// post performs one HTTP POST with the configured authentication and the
// message's protocol headers.
func (t *httpTransport) post(out *outgoing) (*http.Response, error) {
	t.mu.Lock()
	if t.isClosed {
		t.mu.Unlock()
		return nil, fmt.Errorf("transport is closed")
	}
	endpoint, sessionID := t.endpoint, t.sessionID
	t.mu.Unlock()

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(out.body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if t.auth != nil {
		token, err := t.auth.Token()
		if err != nil {
			return nil, fmt.Errorf("obtain upstream token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	// Last, so that nothing configured can contradict the body.
	for k, v := range out.header {
		req.Header[k] = v
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	return resp, nil
}

// route dispatches a response. httpwire.Classify decides what a non-2xx
// answer means; a 2xx JSON body and every SSE message pass through the
// Tracker before they are delivered.
func (t *httpTransport) route(resp *http.Response, out *outgoing) error {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	status := resp.StatusCode
	contentType := resp.Header.Get("Content-Type")
	ok2xx := status >= 200 && status < 300

	if ok2xx && mediaType(contentType) == "text/event-stream" {
		t.streams.Add(1)
		go t.consumeStream(resp.Body, out.id)
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read upstream response: %w", err)
	}
	if len(body) > maxMessageBytes {
		if ok2xx {
			return t.answerOwn(out.id, fmt.Sprintf("upstream response larger than %d bytes", maxMessageBytes))
		}
		// A non-2xx body this large is not a JSON-RPC error; Classify will
		// synthesize one from its first bytes.
		body = body[:maxMessageBytes]
	}

	outcome := httpwire.Classify(out.id, status, contentType, body)
	switch outcome.Kind {
	case httpwire.Accepted:
		return nil
	case httpwire.Drop:
		if !ok2xx {
			t.logf("upstream answered HTTP %d to a message without an id", status)
		}
		return nil
	case httpwire.Synthesized:
		t.logf("upstream answered HTTP %d without a JSON-RPC message; answering the request with -32603", status)
		return t.deliverMessage(outcome.Body)
	}

	// Deliver.
	if ok2xx && !json.Valid(bytes.TrimSpace(body)) {
		// wire.Writer refuses what is not JSON, so without an answer here the
		// request would wait for ever (ADR-0008 §3). The body is quoted in the
		// log only — a server emitting non-JSON is a bug worth seeing — and the
		// log is made printable by the caller.
		t.logf("upstream sent a body that is not JSON: %s", truncate(body, 200))
		return t.answerOwn(out.id, fmt.Sprintf("upstream answered HTTP %d with a body that is not JSON", status))
	}
	return t.deliverMessage(outcome.Body)
}

// answerOwn delivers a synthesized -32603 for the request with this id, or
// nothing when the message was not a request.
func (t *httpTransport) answerOwn(id json.RawMessage, message string) error {
	if id == nil {
		t.logf("%s (no request to answer)", message)
		return nil
	}
	t.logf("%s; answering the request with -32603", message)
	return t.deliverMessage(jsonrpc.NewErrorResponse(id, jsonrpc.CodeInternalError, message))
}

// deliverMessage passes one message through the Tracker, which learns the
// negotiated version and the tools' marks and removes tools whose marks are
// invalid (ADR-0008 §3, §5), and queues the result for Receive.
func (t *httpTransport) deliverMessage(msg []byte) error {
	out, rejected, err := t.tracker.Incoming(msg)
	if err != nil {
		// Not a message the Tracker can read; it is delivered as it came.
		out = msg
	}
	for _, r := range rejected {
		t.logf("removed tool %q from tools/list: %s", r.Tool, r.Reason)
	}
	return t.deliver(out)
}

// deliver queues one message for Receive.
func (t *httpTransport) deliver(msg []byte) error {
	select {
	case t.incoming <- msg:
		return nil
	case <-t.closed:
		return fmt.Errorf("transport is closed")
	}
}

// Receive returns the next message from the server.
func (t *httpTransport) Receive() ([]byte, bool) {
	select {
	case data := <-t.incoming:
		return data, true
	case <-t.closed:
		// Drain anything already queued before reporting the channel done, so
		// a response that arrived just before Close is not dropped.
		select {
		case data := <-t.incoming:
			return data, true
		default:
			return nil, false
		}
	}
}

// Close ends the session and stops accepting messages.
func (t *httpTransport) Close() error {
	t.mu.Lock()
	if t.isClosed {
		t.mu.Unlock()
		return nil
	}
	t.isClosed = true
	sessionID := t.sessionID
	close(t.closed)
	t.mu.Unlock()

	t.streams.Wait()

	if sessionID == "" {
		return nil
	}
	// Best effort: the session is ending either way, and a server that does
	// not implement DELETE is not a failure worth reporting to the user.
	req, err := http.NewRequest(http.MethodDelete, t.endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", sessionID)
	if t.auth != nil {
		if token, err := t.auth.Token(); err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if legacy := t.tracker.LegacyVersion(); legacy != "" {
		req.Header.Set(httpwire.HeaderProtocolVersion, legacy)
	}
	if resp, err := t.client.Do(req); err == nil {
		drain(resp)
	}
	return nil
}

// consumeStream reads an SSE body and delivers each message event. openID is
// the id of the request whose POST opened the stream, or nil.
//
// One line and one event's joined data lines are each bounded by
// maxMessageBytes. Past either bound the stream is abandoned, and if the
// request that opened it has not been answered, it is answered with -32603
// (ADR-0008 §5) — mcp-bridge has no per-request timeout, so silence would be
// for ever.
func (t *httpTransport) consumeStream(body io.ReadCloser, openID json.RawMessage) {
	defer t.streams.Done()
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)

	var (
		eventType string
		dataLines []string
		size      int
		answered  bool
		overflow  bool
	)

	flush := func() {
		if len(dataLines) > 0 && (eventType == "" || eventType == "message") {
			// Only "message" events carry JSON-RPC; "ping" and server-specific
			// event types are stream keep-alives and must not reach the client.
			if t.dispatch(strings.Join(dataLines, "\n"), openID) {
				answered = true
			}
		}
		eventType, dataLines, size = "", nil, 0
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive.
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			size += len(data) + 1
			if size > maxMessageBytes {
				overflow = true
			}
			dataLines = append(dataLines, data)
		}
		if overflow {
			break
		}
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		overflow = true
	}
	if overflow {
		if openID != nil && !answered {
			_ = t.answerOwn(openID, fmt.Sprintf("upstream response larger than %d bytes", maxMessageBytes))
		} else {
			t.logf("abandoning an upstream stream with a message larger than %d bytes", maxMessageBytes)
		}
		return
	}
	// A stream that ends without a trailing blank line still had a complete
	// event pending.
	flush()
}

// dispatch delivers SSE payload data, unwrapping a batch array into its
// members so each reaches the client as its own message. It reports whether
// one of them answered openID.
func (t *httpTransport) dispatch(data string, openID json.RawMessage) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	messages := []json.RawMessage{json.RawMessage(trimmed)}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &batch) == nil {
			messages = batch
		}
	}
	answered := false
	for _, msg := range messages {
		if openID != nil && answers(msg, openID) {
			answered = true
		}
		if err := t.deliverMessage(msg); err != nil {
			return answered
		}
	}
	return answered
}

// answers reports whether msg is a response to the request with this id.
func answers(msg, id json.RawMessage) bool {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method *string         `json:"method"`
	}
	if json.Unmarshal(msg, &m) != nil || m.Method != nil {
		return false
	}
	return sameID(m.ID, id)
}

// sameID compares two JSON-RPC ids by their compacted text.
func sameID(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	if json.Compact(&x, a) != nil || json.Compact(&y, b) != nil {
		return false
	}
	return x.String() == y.String()
}

// mediaType returns the lower-cased type of a Content-Type, without its
// parameters, or "" when it cannot be read.
func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}

// drain closes a response body after discarding it, so the connection can be
// reused instead of being torn down.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
}

// readSnippet reads a bounded prefix of the body for use in an error message.
func readSnippet(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	return truncate(body, 200)
}

func truncate(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
