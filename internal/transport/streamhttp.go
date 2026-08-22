package transport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxMessageBytes bounds a single SSE event. MCP tool results carrying file
// contents are routinely large, so the limit is generous; without one, a
// server that never emits a newline would grow the buffer without bound.
const maxMessageBytes = 8 << 20 // 8 MiB

// httpTransport speaks the MCP Streamable HTTP transport.
//
// Every client message is an HTTP POST. The server answers in one of three
// ways: application/json with a single JSON-RPC message, text/event-stream
// with an SSE stream carrying one or more, or 202 Accepted with no body at
// all (the normal answer to a notification). Session affinity, when the
// server wants it, rides on the Mcp-Session-Id header.
type httpTransport struct {
	endpoint string
	headers  map[string]string
	client   *http.Client
	auth     TokenProvider

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
// a user who wrote one in the config means.
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

// NewHTTP creates a Streamable HTTP transport for the given MCP endpoint.
func NewHTTP(endpoint string, opts ...Option) (Transport, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("upstream URL is required")
	}
	t := &httpTransport{
		endpoint: endpoint,
		client:   defaultClient(),
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
func defaultClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
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

// Send posts one message and routes whatever the server sends back.
func (t *httpTransport) Send(data []byte) error {
	resp, err := t.post(data)
	if err != nil {
		return err
	}

	// A 401 usually means the access token expired. Drop it and try once with
	// a fresh one; a second 401 is a real authentication failure and the error
	// says so, because the fix (log in again) differs from a retry.
	if resp.StatusCode == http.StatusUnauthorized && t.auth != nil {
		drain(resp)
		t.auth.Invalidate()
		resp, err = t.post(data)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			body := readSnippet(resp)
			return fmt.Errorf("upstream rejected the credentials (HTTP 401) after refreshing the token: %s", body)
		}
	}

	return t.route(resp)
}

// post performs one HTTP POST with the configured authentication.
func (t *httpTransport) post(data []byte) (*http.Response, error) {
	t.mu.Lock()
	if t.isClosed {
		t.mu.Unlock()
		return nil, fmt.Errorf("transport is closed")
	}
	endpoint, sessionID := t.endpoint, t.sessionID
	t.mu.Unlock()

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
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

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	return resp, nil
}

// route dispatches a response by content type, after deciding whether an HTTP
// error status carries a usable JSON-RPC answer.
func (t *httpTransport) route(resp *http.Response) error {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	contentType := resp.Header.Get("Content-Type")

	// An HTTP error status is a transport-level failure unless the server also
	// answered at the JSON-RPC layer. Some servers do both — 400 with a proper
	// JSON-RPC error object — and forwarding that answer is more useful to the
	// client than replacing it with a synthesised one.
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes))
		resp.Body.Close()
		if strings.HasPrefix(contentType, "application/json") && isJSONRPCAnswer(body) {
			return t.deliver(body)
		}
		return fmt.Errorf("upstream returned HTTP %d: %s", resp.StatusCode, truncate(body, 200))
	}

	switch {
	case strings.HasPrefix(contentType, "application/json"):
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read upstream response: %w", err)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		return t.deliver(body)

	case strings.HasPrefix(contentType, "text/event-stream"):
		t.streams.Add(1)
		go t.consumeStream(resp.Body)
		return nil

	default:
		// 202 Accepted with an empty body is the normal answer to a
		// notification: there is nothing to deliver and nothing is wrong.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes))
		resp.Body.Close()
		if len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		return t.deliver(body)
	}
}

// isJSONRPCAnswer reports whether body looks like a JSON-RPC response the
// client can consume, rather than an HTML error page or a bare API error.
func isJSONRPCAnswer(body []byte) bool {
	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.JSONRPC == "2.0" && len(probe.ID) > 0
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
	if resp, err := t.client.Do(req); err == nil {
		drain(resp)
	}
	return nil
}

// consumeStream reads an SSE body and delivers each message event.
func (t *httpTransport) consumeStream(body io.ReadCloser) {
	defer t.streams.Done()
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)

	var eventType string
	var dataLines []string

	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		// Only "message" events carry JSON-RPC; "ping" and server-specific
		// event types are stream keep-alives and must not reach the client.
		if eventType == "" || eventType == "message" {
			t.dispatch(strings.Join(dataLines, "\n"))
		}
		eventType, dataLines = "", nil
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
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	// A stream that ends without a trailing blank line still had a complete
	// event pending.
	flush()
}

// dispatch delivers SSE payload data, unwrapping a batch array into its
// members so each reaches the client as its own message.
func (t *httpTransport) dispatch(data string) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return
	}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &batch) == nil {
			for _, msg := range batch {
				if err := t.deliver(msg); err != nil {
					return
				}
			}
			return
		}
	}
	_ = t.deliver([]byte(trimmed))
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
