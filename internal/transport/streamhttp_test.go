package transport

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// receiveWithin returns the next message, failing if none arrives in time.
func receiveWithin(t *testing.T, tr Transport, d time.Duration) string {
	t.Helper()
	type result struct {
		data []byte
		ok   bool
	}
	ch := make(chan result, 1)
	go func() {
		data, ok := tr.Receive()
		ch <- result{data, ok}
	}()
	select {
	case r := <-ch:
		if !r.ok {
			t.Fatal("transport closed before delivering a message")
		}
		return string(r.data)
	case <-time.After(d):
		t.Fatal("no message arrived")
		return ""
	}
}

func TestJSONResponseIsDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	}))
	defer srv.Close()

	tr, err := NewHTTP(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	if got := receiveWithin(t, tr, time.Second); !strings.Contains(got, `"ok":true`) {
		t.Errorf("delivered %q", got)
	}
}

func TestSSEStreamIsDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		// A comment keep-alive and a non-message event must both be ignored.
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "event: ping\ndata: {\"not\":\"jsonrpc\"}\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"first\":true}}\n\n")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"second\":true}}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got := receiveWithin(t, tr, time.Second); !strings.Contains(got, `"first":true`) {
		t.Errorf("first message = %q", got)
	}
	if got := receiveWithin(t, tr, time.Second); !strings.Contains(got, `"second":true`) {
		t.Errorf("second message = %q (a ping event may have leaked through)", got)
	}
}

// An SSE stream that ends without a trailing blank line still had a complete
// event pending.
func TestSSEStreamWithoutTrailingBlankLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}")
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()
	if err := tr.Send([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := receiveWithin(t, tr, time.Second); !strings.Contains(got, `"id":1`) {
		t.Errorf("delivered %q", got)
	}
}

// 202 Accepted with no body is the normal answer to a notification. It must
// not be reported as an error and must not deliver a phantom message.
func TestAcceptedWithEmptyBodyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil {
		t.Fatalf("202 with an empty body was reported as an error: %v", err)
	}
}

func TestSessionIDIsEchoedBack(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Mcp-Session-Id"))
		mu.Unlock()
		w.Header().Set("Mcp-Session-Id", "session-abc")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	receiveWithin(t, tr, time.Second)
	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	receiveWithin(t, tr, time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected two requests, saw %d", len(seen))
	}
	if seen[0] != "" {
		t.Errorf("first request already carried a session id: %q", seen[0])
	}
	if seen[1] != "session-abc" {
		t.Errorf("second request session id = %q, want session-abc", seen[1])
	}
}

func TestStaticHeadersAreSent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL, WithHeaders(map[string]string{"Authorization": "Bearer static"}))
	defer tr.Close()
	_ = tr.Send([]byte(`{}`))
	receiveWithin(t, tr, time.Second)

	if got != "Bearer static" {
		t.Errorf("Authorization = %q", got)
	}
}

// stubProvider records how often the token was fetched and invalidated.
type stubProvider struct {
	mu          sync.Mutex
	token       string
	fetches     int
	invalidated int
}

func (p *stubProvider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetches++
	return p.token, nil
}

func (p *stubProvider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidated++
	p.token = "refreshed"
}

func TestUnauthorizedInvalidatesAndRetriesOnce(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Header.Get("Authorization") != "Bearer refreshed" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	provider := &stubProvider{token: "stale"}
	tr, _ := NewHTTP(srv.URL, WithTokenProvider(provider))
	defer tr.Close()

	if err := tr.Send([]byte(`{}`)); err != nil {
		t.Fatalf("retry after 401 failed: %v", err)
	}
	receiveWithin(t, tr, time.Second)

	if attempts != 2 {
		t.Errorf("server saw %d attempts, want 2", attempts)
	}
	if provider.invalidated != 1 {
		t.Errorf("Invalidate called %d times, want 1", provider.invalidated)
	}
}

// A second 401 is a real authentication failure. The error must say so,
// because the fix is to log in again rather than to retry.
func TestPersistentUnauthorizedIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "token_revoked")
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL, WithTokenProvider(&stubProvider{token: "x"}))
	defer tr.Close()

	err := tr.Send([]byte(`{}`))
	if err == nil {
		t.Fatal("a persistent 401 was not reported")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "token_revoked") {
		t.Errorf("error lacks the diagnosis: %v", err)
	}
}

// An HTTP error status carrying a real JSON-RPC error is the server answering
// at the JSON-RPC layer; forwarding its answer beats synthesising one.
func TestErrorStatusWithJSONRPCBodyIsForwarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad request"}}`)
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	if err := tr.Send([]byte(`{}`)); err != nil {
		t.Fatalf("a JSON-RPC error body was turned into a transport error: %v", err)
	}
	if got := receiveWithin(t, tr, time.Second); !strings.Contains(got, "bad request") {
		t.Errorf("delivered %q", got)
	}
}

// An HTTP error that is not a JSON-RPC answer must surface as a transport
// error rather than being handed to the client as a message.
func TestErrorStatusWithHTMLBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "<html>502 Bad Gateway</html>")
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	err := tr.Send([]byte(`{}`))
	if err == nil {
		t.Fatal("HTTP 502 was not reported as an error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error does not name the status: %v", err)
	}
}

func TestCloseEndsTheSession(t *testing.T) {
	deleted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted <- r.Header.Get("Mcp-Session-Id")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Mcp-Session-Id", "session-xyz")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	_ = tr.Send([]byte(`{}`))
	receiveWithin(t, tr, time.Second)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case sid := <-deleted:
		if sid != "session-xyz" {
			t.Errorf("DELETE carried session id %q", sid)
		}
	case <-time.After(time.Second):
		t.Error("Close did not terminate the session")
	}
}

func TestSendAfterCloseFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	_ = tr.Close()
	if err := tr.Send([]byte(`{}`)); err == nil {
		t.Error("Send succeeded on a closed transport")
	}
	if _, ok := tr.Receive(); ok {
		t.Error("Receive returned a message from a closed transport")
	}
}

func TestNewHTTPRequiresEndpoint(t *testing.T) {
	if _, err := NewHTTP(""); err == nil {
		t.Error("an empty endpoint was accepted")
	}
}

// Close must not race the SSE reader: a message already queued when Close runs
// is still delivered, and the reader goroutine is finished before Close
// returns.
func TestCloseWaitsForStreamReaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	_ = tr.Send([]byte(`{}`))
	got := receiveWithin(t, tr, time.Second)
	if !strings.Contains(got, `"id":1`) {
		t.Errorf("delivered %q", got)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
}

// deadProvider models a stored login that cannot be renewed: the first token
// works, and after Invalidate there is nothing left to hand out.
type deadProvider struct {
	mu       sync.Mutex
	rejected bool
}

func (p *deadProvider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rejected {
		return "", fmt.Errorf("the stored login was rejected and cannot be renewed: run \"mcp-bridge login x\"")
	}
	return "revoked-token", nil
}

func (p *deadProvider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rejected = true
}

// When the retry cannot obtain a replacement token, the 401 that caused it
// must survive into the error. Reporting only the provider's message leaves
// the user hunting for an empty token file rather than a rejected credential.
func TestUnauthorizedKeepsItsCauseWhenNoTokenRemains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL, WithTokenProvider(&deadProvider{}))
	defer tr.Close()

	err := tr.Send([]byte(`{}`))
	if err == nil {
		t.Fatal("a rejected credential was not reported")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the cause was lost: %v", err)
	}
	if !strings.Contains(err.Error(), "mcp-bridge login x") {
		t.Errorf("the fix was lost: %v", err)
	}
}
