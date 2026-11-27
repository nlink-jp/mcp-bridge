package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeTarget is an MCP endpoint that records every request and answers with
// the given handler.
type probeTarget struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
}

func newProbeTarget(t *testing.T, answer http.HandlerFunc) *probeTarget {
	t.Helper()
	pt := &probeTarget{}
	pt.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		pt.mu.Lock()
		pt.requests = append(pt.requests, r)
		pt.bodies = append(pt.bodies, body)
		pt.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(pt.Close)
	return pt
}

func (pt *probeTarget) count() int {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return len(pt.requests)
}

func (pt *probeTarget) request() discoveryRequest {
	return discoveryRequest{serverURL: pt.URL + "/mcp", client: pt.Client(), out: &bytes.Buffer{}}
}

// The probe is the opening the module builds — server/discover with its
// headers — sent once, without a credential; the challenge is read as before
// (ADR-0009 §1).
func TestProbeSendsTheModulesOpeningOnce(t *testing.T) {
	pt := newProbeTarget(t, challengeWith(`Bearer resource_metadata="https://mcp.example.com/prm", scope="a b"`))

	got, err := probeChallenge(pt.request())
	if err != nil {
		t.Fatal(err)
	}
	want := probed{reached: true, metadataURL: "https://mcp.example.com/prm", scopes: []string{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("probed = %+v, want %+v", got, want)
	}
	if n := pt.count(); n != 1 {
		t.Fatalf("%d requests, want exactly one", n)
	}

	r := pt.requests[0]
	if r.Method != http.MethodPost || r.URL.Path != "/mcp" {
		t.Errorf("request = %s %s", r.Method, r.URL.Path)
	}
	for name, want := range map[string]string{
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "server/discover",
		"Content-Type":         "application/json",
		"Accept":               "application/json, text/event-stream",
		"Authorization":        "",
	} {
		if got := r.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	var body struct {
		Method string `json:"method"`
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(pt.bodies[0], &body); err != nil {
		t.Fatalf("body %s: %v", pt.bodies[0], err)
	}
	if body.Method != "server/discover" {
		t.Errorf("method = %q, want server/discover", body.Method)
	}
	if v := string(body.Params.Meta["io.modelcontextprotocol/protocolVersion"]); v != `"2026-07-28"` {
		t.Errorf("protocolVersion = %s", v)
	}
	var info struct{ Name, Version string }
	_ = json.Unmarshal(body.Params.Meta["io.modelcontextprotocol/clientInfo"], &info)
	if info.Name != "mcp-bridge" || info.Version != "discovery" {
		t.Errorf("clientInfo = %+v", info)
	}
}

// A server that answers the opening instead of challenging it still gets one
// request; discovery is told, and goes on to the well-known locations.
func TestProbeAnsweredWithoutAChallengeIsOneRequest(t *testing.T) {
	pt := newProbeTarget(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`)
	})

	got, err := probeChallenge(pt.request())
	if err == nil || !strings.Contains(err.Error(), "expected an HTTP 401 challenge, got 200") {
		t.Fatalf("err = %v", err)
	}
	if !got.reached {
		t.Error("a server that answered was not counted as reached")
	}
	if n := pt.count(); n != 1 {
		t.Errorf("%d requests, want exactly one", n)
	}
}

// A server that does not answer in time gets one request too: the module
// reads a deadline as "no answer" and would fall back to its legacy opening,
// which the probe must not send. The HTTP client's timeout fires well inside
// the probe's, while the module's context is still live: were Send to return
// that deadline error and the guard against a second Send both gone, Open
// would fall back and this test would see two requests. Each defence alone is
// pinned by TestProbeConnSendsOnce and TestProbeConnFailedRequestEndsTheOpening.
func TestProbeThatTimesOutIsOneRequest(t *testing.T) {
	release := make(chan struct{})
	pt := newProbeTarget(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) }) // runs before the server closes

	req := pt.request()
	short := *pt.Client()
	short.Timeout = 100 * time.Millisecond
	req.client = &short
	req.probeTimeout = 10 * time.Second
	got, err := probeChallenge(req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the timeout", err)
	}
	if got.reached {
		t.Error("a server that never answered was counted as reached")
	}
	if n := pt.count(); n != 1 {
		t.Errorf("%d requests, want exactly one", n)
	}
}

// A probe that cannot be sent is mcp-bridge's error, not an unreachable server.
func TestProbeThatCannotBeSentIsNotUnreachable(t *testing.T) {
	req := discoveryRequest{serverURL: "http://[::1", client: defaultHTTPClient(), out: &bytes.Buffer{}}
	_, err := findAuthServer(req)
	var notSent *probeNotSentError
	if !errors.As(err, &notSent) {
		t.Fatalf("err = %v, want a probe that was not sent", err)
	}
	if strings.Contains(err.Error(), "could not reach") {
		t.Errorf("reported as unreachable: %v", err)
	}
}

// Only the first Send makes a request.
func TestProbeConnSendsOnce(t *testing.T) {
	pt := newProbeTarget(t, challengeWith(`Bearer resource_metadata="https://x/prm"`))
	conn := newProbeConn(pt.Client(), pt.URL+"/mcp")
	defer conn.Close()

	msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`)
	if err := conn.Send(context.Background(), msg); !errors.Is(err, errProbeAnswered) {
		t.Fatalf("first Send = %v", err)
	}
	if err := conn.Send(context.Background(), msg); !errors.Is(err, errProbeSpent) {
		t.Fatalf("second Send = %v", err)
	}
	if n := pt.count(); n != 1 {
		t.Errorf("%d requests, want exactly one", n)
	}
}

// A request that fails still ends the opening with errProbeAnswered — not the
// failure itself, which client.Open would read, for a deadline, as "no
// answer" and fall back on — and the failure is kept in the outcome.
func TestProbeConnFailedRequestEndsTheOpening(t *testing.T) {
	release := make(chan struct{})
	pt := newProbeTarget(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	short := *pt.Client()
	short.Timeout = 50 * time.Millisecond
	conn := newProbeConn(&short, pt.URL+"/mcp")
	defer conn.Close()

	msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`)
	if err := conn.Send(context.Background(), msg); !errors.Is(err, errProbeAnswered) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send = %v, want errProbeAnswered and not the deadline", err)
	}
	out, ok := conn.outcome()
	if !ok || !errors.Is(out.err, context.DeadlineExceeded) {
		t.Errorf("outcome = %+v, %v; want the timeout kept", out, ok)
	}
}

// Recv ends at Close, whichever comes first.
func TestProbeConnRecvEndsAtClose(t *testing.T) {
	closedFirst := newProbeConn(http.DefaultClient, "http://unused")
	closedFirst.Close()
	if _, err := closedFirst.Recv(); err != io.EOF {
		t.Errorf("Recv after Close = %v, want io.EOF", err)
	}

	waiting := newProbeConn(http.DefaultClient, "http://unused")
	done := make(chan error, 1)
	go func() { _, err := waiting.Recv(); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("Recv returned before Close: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	waiting.Close()
	waiting.Close() // a second Close is harmless
	select {
	case err := <-done:
		if err != io.EOF {
			t.Errorf("Recv = %v, want io.EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not end at Close")
	}
}

// The probe leaves none of client.Open's goroutines running.
func TestProbeLeavesNoGoroutine(t *testing.T) {
	pt := newProbeTarget(t, challengeWith(`Bearer resource_metadata="https://x/prm"`))
	req := pt.request()
	settle := func() {
		pt.Client().CloseIdleConnections()
		pt.CloseClientConnections()
	}
	if _, err := probeChallenge(req); err != nil { // warm the server up
		t.Fatal(err)
	}
	settle()
	before := waitGoroutines(-1)

	for range 20 {
		if _, err := probeChallenge(req); err != nil {
			t.Fatal(err)
		}
	}
	settle()
	if after := waitGoroutines(before); after > before {
		t.Errorf("goroutines: %d before 20 probes, %d after", before, after)
	}
}

// waitGoroutines returns the goroutine count once it is at most limit, or
// after two seconds; a negative limit waits briefly for the count to settle.
func waitGoroutines(limit int) int {
	deadline := time.Now().Add(2 * time.Second)
	if limit < 0 {
		deadline = time.Now().Add(100 * time.Millisecond)
	}
	for {
		n := runtime.NumGoroutine()
		if (limit >= 0 && n <= limit) || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}
