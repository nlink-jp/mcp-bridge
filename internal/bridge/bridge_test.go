package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeTransport stands in for the upstream server.
type fakeTransport struct {
	mu       sync.Mutex
	sent     [][]byte
	sendErr  error
	incoming chan []byte
	closed   bool
}

func newFake() *fakeTransport {
	return &fakeTransport{incoming: make(chan []byte, 16)}
}

func (f *fakeTransport) Send(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, append([]byte(nil), data...))
	return nil
}

func (f *fakeTransport) Receive() ([]byte, bool) {
	data, ok := <-f.incoming
	return data, ok
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.incoming)
	}
	return nil
}

func (f *fakeTransport) sentMessages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, string(m))
	}
	return out
}

// syncWriter makes concurrent writes from the bridge safe to inspect.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// run drives a bridge over the given client input and returns stdout, stderr.
func run(t *testing.T, up *fakeTransport, input string, before func()) (string, string) {
	t.Helper()
	var out, logs syncWriter
	b := New(up, strings.NewReader(input), &out, &logs)
	if before != nil {
		before()
	}
	if err := b.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String(), logs.String()
}

func TestClientMessagesReachUpstream(t *testing.T) {
	up := newFake()
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	run(t, up, input, nil)

	sent := up.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("upstream saw %d messages, want 2: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "initialize") || !strings.Contains(sent[1], "notifications/initialized") {
		t.Errorf("messages arrived altered or out of order: %v", sent)
	}
}

// The relay is transparent: what the client sent is byte-for-byte what the
// server receives.
func TestMessagesAreForwardedUnmodified(t *testing.T) {
	up := newFake()
	original := `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"x","arguments":{"deep":{"nested":[1,2,3]}}}}`
	run(t, up, original+"\n", nil)

	sent := up.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("upstream saw %d messages", len(sent))
	}
	if sent[0] != original {
		t.Errorf("message was altered in transit:\n sent: %s\n want: %s", sent[0], original)
	}
}

func TestUpstreamMessagesReachTheClient(t *testing.T) {
	up := newFake()
	up.incoming <- []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	out, _ := run(t, up, "", nil)

	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("stdout = %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("message was not newline-terminated")
	}
}

// stdio framing is newline-delimited. A server that pretty-prints its JSON
// would split one message across several lines and desynchronise the client
// for the rest of the session.
func TestPrettyPrintedUpstreamJSONIsCompacted(t *testing.T) {
	up := newFake()
	up.incoming <- []byte("{\n  \"jsonrpc\": \"2.0\",\n  \"id\": 1,\n  \"result\": {\n    \"ok\": true\n  }\n}")
	out, _ := run(t, up, "", nil)

	if strings.Count(out, "\n") != 1 {
		t.Errorf("message spans %d lines: %q", strings.Count(out, "\n"), out)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &decoded); err != nil {
		t.Fatalf("compacted output is not valid JSON: %v", err)
	}
}

// Writing a malformed message would corrupt the stream for everything after
// it, so it is dropped and logged rather than forwarded.
func TestMalformedUpstreamMessageIsDroppedNotForwarded(t *testing.T) {
	up := newFake()
	up.incoming <- []byte("this is not json")
	up.incoming <- []byte(`{"jsonrpc":"2.0","id":2,"result":{}}`)
	out, logs := run(t, up, "", nil)

	if strings.Contains(out, "not json") {
		t.Errorf("malformed message reached stdout: %q", out)
	}
	if !strings.Contains(logs, "malformed") {
		t.Errorf("the drop was not logged: %q", logs)
	}
	if !strings.Contains(out, `"id":2`) {
		t.Errorf("the following valid message was lost: %q", out)
	}
}

// The invariant: a request that could not be forwarded still gets an answer.
// Silence makes the client block until its own timeout with no diagnosis.
func TestUnforwardableRequestGetsAnErrorResponse(t *testing.T) {
	up := newFake()
	up.sendErr = fmt.Errorf("upstream rejected the credentials (HTTP 401)")
	out, _ := run(t, up, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n", nil)

	var resp struct {
		ID    json.RawMessage `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("no usable response was written (%v): %q", err, out)
	}
	if string(resp.ID) != "7" {
		t.Errorf("response id = %s, want 7", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("response carries no error member")
	}
	if !strings.Contains(resp.Error.Message, "401") {
		t.Errorf("the error does not carry the cause: %q", resp.Error.Message)
	}
}

// A notification has no id, so there is nothing to answer. It must be logged,
// not answered — a response to a notification is a protocol violation.
func TestUnforwardableNotificationIsNotAnswered(t *testing.T) {
	up := newFake()
	up.sendErr = fmt.Errorf("connection refused")
	out, logs := run(t, up, `{"jsonrpc":"2.0","method":"notifications/cancelled"}`+"\n", nil)

	if strings.TrimSpace(out) != "" {
		t.Errorf("a notification was answered: %q", out)
	}
	if !strings.Contains(logs, "connection refused") {
		t.Errorf("the failure was not logged: %q", logs)
	}
}

func TestUnparseableClientMessageGetsParseError(t *testing.T) {
	up := newFake()
	out, logs := run(t, up, "not json at all\n", nil)

	if !strings.Contains(out, "-32700") {
		t.Errorf("no parse error was returned: %q", out)
	}
	if !strings.Contains(out, `"id":null`) {
		t.Errorf("parse error should carry a null id: %q", out)
	}
	if len(up.sentMessages()) != 0 {
		t.Error("unparseable input was forwarded upstream")
	}
	if !strings.Contains(logs, "unparseable") {
		t.Errorf("the rejection was not logged: %q", logs)
	}
}

// A batch cannot be answered per-member without parsing bodies, which the
// relay does not do. It is forwarded untouched.
func TestBatchIsForwardedUntouched(t *testing.T) {
	up := newFake()
	batch := `[{"jsonrpc":"2.0","id":1,"method":"a"},{"jsonrpc":"2.0","id":2,"method":"b"}]`
	run(t, up, batch+"\n", nil)

	sent := up.sentMessages()
	if len(sent) != 1 || sent[0] != batch {
		t.Errorf("batch was not forwarded verbatim: %v", sent)
	}
}

func TestBlankLinesAreIgnored(t *testing.T) {
	up := newFake()
	run(t, up, "\n\n  \n"+`{"jsonrpc":"2.0","id":1,"method":"x"}`+"\n\n", nil)
	if got := len(up.sentMessages()); got != 1 {
		t.Errorf("upstream saw %d messages, want 1", got)
	}
}

func TestClientEOFClosesUpstream(t *testing.T) {
	up := newFake()
	run(t, up, "", nil)
	up.mu.Lock()
	defer up.mu.Unlock()
	if !up.closed {
		t.Error("upstream was left open after the client disconnected")
	}
}

// Diagnostics must never reach stdout: the client parses every line there as
// JSON-RPC.
func TestDiagnosticsNeverReachStdout(t *testing.T) {
	up := newFake()
	up.sendErr = fmt.Errorf("boom")
	up.incoming <- []byte("not json")
	out, logs := run(t, up, "garbage\n"+`{"jsonrpc":"2.0","method":"n"}`+"\n", nil)

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Errorf("non-JSON line on stdout: %q", line)
		}
	}
	if logs == "" {
		t.Error("nothing was logged despite three failures")
	}
}
