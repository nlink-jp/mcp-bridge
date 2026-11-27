package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nlink-jp/mcp/httpwire"

	"github.com/nlink-jp/mcp-bridge/internal/jsonrpc"
)

// A HeaderMismatch (-32020) on a modern tools/call means the upstream's
// x-mcp-header marks differ from the ones mcp-bridge holds. The specification
// says the client SHOULD call tools/list and retry; a stdio client cannot fix
// headers, so mcp-bridge does it, once per request (ADR-0008 §6).

// refreshTimeout bounds one refresh, all its pages together. Past it the
// refresh has failed, and the client gets the first -32020 (ADR-0008 §11.4).
// A variable so that tests can shorten it.
var refreshTimeout = 60 * time.Second

const (
	codeHeaderMismatch = -32020

	// refreshPages bounds how many tools/list pages one refresh reads.
	refreshPages = 10

	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
)

// isHeaderMismatch reports whether body answers a modern tools/call, sent for
// the first time, with -32020. Modern means Tracker.Outgoing gave it an
// Mcp-Method header. Classify's Modern flag is not used: it covers several
// codes at once.
func (t *httpTransport) isHeaderMismatch(out *outgoing, body []byte) bool {
	if out.retried || out.method != "tools/call" || out.header.Get(httpwire.HeaderMethod) == "" {
		return false
	}
	var answer struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &answer) != nil || answer.Error == nil {
		return false
	}
	return answer.Error.Code == codeHeaderMismatch
}

// retryAfterMismatch refreshes the marks and sends the request once more. If
// the refresh fails, the request is not resent and first is delivered.
func (t *httpTransport) retryAfterMismatch(out *outgoing, first []byte) error {
	t.refreshMu.Lock()
	err := t.refreshMarks(out.body)
	t.refreshMu.Unlock()
	if err != nil {
		t.logf("tools/list refresh after a header mismatch failed (%v); delivering the mismatch", err)
		return t.deliverMessage(first)
	}

	again, err := t.prepare(out.body)
	if err != nil || again == nil {
		// Answered here (the fresh marks make an argument unmirrorable), or
		// not sendable at all.
		return err
	}
	again.retried = true
	t.logf("resending a tools/call after refreshing the tools' header marks")
	return t.exchange(again)
}

// refreshMarks reads tools/list, following nextCursor for up to refreshPages
// pages, so that the Tracker learns the upstream's current marks. The answers
// are not delivered to the client.
func (t *httpTransport) refreshMarks(original []byte) error {
	meta, err := refreshMeta(original)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	cursor := ""
	for page := 0; page < refreshPages; page++ {
		id := fmt.Sprintf("mcp-bridge-refresh-%d", t.refreshSeq.Add(1))
		params := map[string]any{"_meta": meta}
		if cursor != "" {
			params["cursor"] = cursor
		}
		body, err := jsonrpc.MarshalPlain(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/list", "params": params})
		if err != nil {
			return err
		}
		answer, err := t.fetch(ctx, body, id)
		if err != nil {
			return err
		}
		if _, _, err := t.tracker.Incoming(answer); err != nil {
			return fmt.Errorf("read the tools/list answer: %w", err)
		}
		next, err := nextCursor(answer)
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
	return fmt.Errorf("tools/list has more than %d pages", refreshPages)
}

// refreshMeta copies the version, capabilities and identity from the
// original request's _meta, so that the refresh speaks as the client did.
func refreshMeta(original []byte) (map[string]json.RawMessage, error) {
	var msg struct {
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(original, &msg); err != nil {
		return nil, fmt.Errorf("read the original request: %w", err)
	}
	meta := map[string]json.RawMessage{}
	for _, k := range []string{metaProtocolVersion, metaClientCapabilities, metaClientInfo} {
		if v, ok := msg.Params.Meta[k]; ok {
			meta[k] = v
		}
	}
	if _, ok := meta[metaProtocolVersion]; !ok {
		return nil, errors.New("the original request carries no protocol version")
	}
	return meta, nil
}

// fetch posts one refresh request and returns the answer carrying id, read
// synchronously from a JSON body or an SSE stream.
func (t *httpTransport) fetch(ctx context.Context, body []byte, id string) ([]byte, error) {
	out, err := t.prepare(body)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("the refresh request could not be sent")
	}
	out.ctx = ctx

	resp, err := t.postRenewing(out)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream answered HTTP %d", resp.StatusCode)
	}
	if mediaType(resp.Header.Get("Content-Type")) == "text/event-stream" {
		return readAnswerFromStream(resp.Body, id)
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(answer) > maxMessageBytes {
		return nil, fmt.Errorf("the answer is larger than %d bytes", maxMessageBytes)
	}
	return answer, nil
}

// readAnswerFromStream reads SSE events until one carries the answer to id.
// Other messages on the stream are not the client's — the client never sent
// this request — and are dropped.
func readAnswerFromStream(body io.Reader, id string) ([]byte, error) {
	want, _ := json.Marshal(id)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)
	var data []string
	size := 0
	check := func() []byte {
		defer func() { data, size = nil, 0 }()
		if len(data) == 0 {
			return nil
		}
		msg := []byte(strings.TrimSpace(strings.Join(data, "\n")))
		if answers(msg, want) {
			return msg
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if msg := check(); msg != nil {
				return msg, nil
			}
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if len(data) > 0 {
				size++
			}
			size += len(d)
			if size > maxMessageBytes {
				return nil, fmt.Errorf("an event is larger than %d bytes", maxMessageBytes)
			}
			data = append(data, d)
		}
	}
	if msg := check(); msg != nil {
		return msg, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("the stream ended without the answer")
}

// nextCursor reads result.nextCursor from a tools/list answer. An error
// answer is a failed refresh.
func nextCursor(answer []byte) (string, error) {
	var msg struct {
		Result *struct {
			NextCursor *string `json:"nextCursor"`
		} `json:"result"`
		Error *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(answer, &msg); err != nil {
		return "", fmt.Errorf("read the tools/list answer: %w", err)
	}
	if msg.Error != nil || msg.Result == nil {
		return "", errors.New("tools/list answered with an error")
	}
	if msg.Result.NextCursor == nil {
		return "", nil
	}
	return *msg.Result.NextCursor, nil
}
