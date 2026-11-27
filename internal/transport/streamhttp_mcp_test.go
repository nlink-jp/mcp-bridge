package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests pin ADR-0008 §3–§5: the protocol headers derived from each
// body, what the Tracker learns from the answers, and the answers mcp-bridge
// synthesizes. Expected values come from the MCP 2026-07-28 streamable-http
// specification and the ADR, not from nlink-jp/mcp's code.

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"t","version":"0"}}`

// toolsWithOwnerMark is a modern tools/list result: get_repo mirrors its
// owner argument into Mcp-Param-Owner.
const toolsWithOwnerMark = `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","ttlMs":1000,"cacheScope":"private","tools":[` +
	`{"name":"get_repo","inputSchema":{"type":"object","properties":{"owner":{"type":"string","x-mcp-header":"Owner"},"repo":{"type":"string"}}}}]}}`

// seen is one request the fake upstream received.
type seen struct {
	method string
	header http.Header
	body   string
}

// recorder is an upstream that records what it receives and answers with
// answer(body).
type recorder struct {
	mu   sync.Mutex
	reqs []seen
}

func (rec *recorder) server(t *testing.T, answer func(w http.ResponseWriter, body string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, seen{method: r.Method, header: r.Header.Clone(), body: string(b)})
		rec.mu.Unlock()
		answer(w, string(b))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (rec *recorder) last() seen {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.reqs[len(rec.reqs)-1]
}

func (rec *recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.reqs)
}

// idOf reads a request body's id, for echoing it back.
func idOf(body string) string {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &m)
	return string(m.ID)
}

func jsonAnswer(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, s)
}

func TestLegacyRequestsCarryTheNegotiatedVersion(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, body string) {
		if strings.Contains(body, `"initialize"`) {
			jsonAnswer(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`)
			return
		}
		jsonAnswer(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, idOf(body)))
	})
	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, tr, time.Second)
	if v := rec.last().header.Get("MCP-Protocol-Version"); v != "" {
		t.Errorf("initialize itself carried MCP-Protocol-Version %q", v)
	}
	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, tr, time.Second)
	if v := rec.last().header.Get("MCP-Protocol-Version"); v != "2025-06-18" {
		t.Errorf("a legacy request after initialize carried MCP-Protocol-Version %q, want 2025-06-18", v)
	}
	if v := rec.last().header.Get("Mcp-Method"); v != "" {
		t.Errorf("a legacy request carried Mcp-Method %q", v)
	}
}

func TestModernToolsCallCarriesTheStandardAndParamHeaders(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, body string) {
		if strings.Contains(body, `"tools/list"`) {
			jsonAnswer(w, fmt.Sprintf(toolsWithOwnerMark, idOf(body)))
			return
		}
		jsonAnswer(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","content":[]}}`, idOf(body)))
	})
	tr, _ := NewHTTP(srv.URL, WithHeaders(map[string]string{"Mcp-Method": "configured"}))
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`)); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, tr, time.Second)
	if got := rec.last().header; got.Get("Mcp-Method") != "tools/list" || got.Get("MCP-Protocol-Version") != "2026-07-28" {
		t.Errorf("modern tools/list headers: %v", got)
	}

	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_repo","arguments":{"owner":"nlink-jp","repo":"x"},` + modernMeta + `}}`
	if err := tr.Send([]byte(call)); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, tr, time.Second)
	h := rec.last().header
	for name, want := range map[string]string{
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "get_repo",
		"Mcp-Param-Owner":      "nlink-jp",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q (a configured header must not win)", name, got, want)
		}
	}
	if len(h.Values("Mcp-Method")) != 1 {
		t.Errorf("Mcp-Method sent %d times", len(h.Values("Mcp-Method")))
	}

	// An argument with no value gets no header.
	noOwner := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_repo","arguments":{"repo":"x"},` + modernMeta + `}}`
	if err := tr.Send([]byte(noOwner)); err != nil {
		t.Fatal(err)
	}
	receiveWithin(t, tr, time.Second)
	if v, ok := rec.last().header["Mcp-Param-Owner"]; ok {
		t.Errorf("an absent argument was mirrored: %v", v)
	}
}

// Marks learned from an SSE answer count as much as from a JSON one.
func TestMarksAreLearnedFromAnSSEAnswer(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, body string) {
		if strings.Contains(body, `"tools/list"`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: "+toolsWithOwnerMark+"\n\n", idOf(body))
			return
		}
		jsonAnswer(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","content":[]}}`, idOf(body)))
	})
	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`))
	receiveWithin(t, tr, time.Second)
	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_repo","arguments":{"owner":"o"},` + modernMeta + `}}`))
	receiveWithin(t, tr, time.Second)
	if got := rec.last().header.Get("Mcp-Param-Owner"); got != "o" {
		t.Errorf("Mcp-Param-Owner = %q after an SSE tools/list, want o", got)
	}
}

// A tool whose mark is invalid is removed from a modern tools/list result
// (a MUST of the specification); the other tools and keys stay.
func TestToolWithAnInvalidMarkIsRemoved(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, body string) {
		jsonAnswer(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","ttlMs":1000,"cacheScope":"private","nextCursor":"c2","tools":[`+
			`{"name":"good","inputSchema":{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"A"}}}},`+
			`{"name":"bad","inputSchema":{"type":"object","properties":{"a":{"type":"object","x-mcp-header":"A"}}}}]}}`, idOf(body)))
	})
	var logs syncBuffer
	tr, _ := NewHTTP(srv.URL, WithLogs(&logs))
	defer tr.Close()

	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`))
	got := receiveWithin(t, tr, time.Second)
	if strings.Contains(got, `"bad"`) || !strings.Contains(got, `"good"`) {
		t.Errorf("tools/list after filtering: %s", got)
	}
	for _, key := range []string{`"nextCursor":"c2"`, `"ttlMs":1000`, `"cacheScope":"private"`, `"resultType":"complete"`} {
		if !strings.Contains(got, key) {
			t.Errorf("filtering lost %s: %s", key, got)
		}
	}
	if !strings.Contains(logs.String(), `removed tool "bad"`) {
		t.Errorf("the removal was not logged: %q", logs.String())
	}
}

// Classify's table, through the transport.
func TestNonSuccessAnswersAreAnsweredUnderTheRequestsID(t *testing.T) {
	cases := []struct {
		name   string
		answer func(w http.ResponseWriter)
		want   []string
	}{
		{"empty 400", func(w http.ResponseWriter) { w.WriteHeader(400) }, []string{`"id":9`, "-32603", "400"}},
		{"400 with id null", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"no session"}}`)
		}, []string{`"id":9`, "-32600", "no session"}},
		{"2xx that is not JSON", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "<html>oops</html>")
		}, []string{`"id":9`, "-32603", "not JSON"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec recorder
			srv := rec.server(t, func(w http.ResponseWriter, _ string) { c.answer(w) })
			tr, _ := NewHTTP(srv.URL)
			defer tr.Close()
			if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)); err != nil {
				t.Fatal(err)
			}
			got := receiveWithin(t, tr, time.Second)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("answer %q lacks %s", got, w)
				}
			}
		})
	}
}

func TestOversizedAnswersAreAnswered(t *testing.T) {
	big := strings.Repeat("a", maxMessageBytes)
	cases := []struct {
		name   string
		answer func(w http.ResponseWriter)
	}{
		{"JSON body", func(w http.ResponseWriter) {
			jsonAnswer(w, `{"jsonrpc":"2.0","id":5,"result":{"x":"`+big+`"}}`)
		}},
		{"SSE line", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+big+"\n\n")
		}},
		{"SSE event", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			half := big[:maxMessageBytes/2+16]
			fmt.Fprint(w, "data: "+half+"\ndata: "+half+"\n\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec recorder
			srv := rec.server(t, func(w http.ResponseWriter, _ string) { c.answer(w) })
			tr, _ := NewHTTP(srv.URL)
			defer tr.Close()
			if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"x"}}`)); err != nil {
				t.Fatal(err)
			}
			got := receiveWithin(t, tr, 5*time.Second)
			if !strings.Contains(got, `"id":5`) || !strings.Contains(got, "-32603") || !strings.Contains(got, "larger than") {
				t.Errorf("answer %q", got[:min(len(got), 300)])
			}
		})
	}
}

func TestABatchIsSentWithoutProtocolHeaders(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, _ string) { w.WriteHeader(202) })
	var logs syncBuffer
	tr, _ := NewHTTP(srv.URL, WithLogs(&logs))
	defer tr.Close()

	if err := tr.Send([]byte(`[{"jsonrpc":"2.0","id":1,"method":"a","params":{` + modernMeta + `}}]`)); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 {
		t.Fatalf("the batch was not sent")
	}
	h := rec.last().header
	if h.Get("Mcp-Method") != "" || h.Get("MCP-Protocol-Version") != "" {
		t.Errorf("a batch carried protocol headers: %v", h)
	}
	if !strings.Contains(logs.String(), "batch") {
		t.Errorf("the batch was not logged: %q", logs.String())
	}
}

// A message whose headers cannot be derived is not sent: its request gets
// -32602, and no value appears in the answer.
func TestUnsendableRequestsAreAnsweredWithInvalidParams(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, body string) {
		jsonAnswer(w, fmt.Sprintf(toolsWithOwnerMark, idOf(body)))
	})
	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()
	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`))
	receiveWithin(t, tr, time.Second)
	before := rec.count()

	for _, c := range []struct{ name, body string }{
		{"unmirrorable", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_repo","arguments":{"owner":{"secret":"s3cr3t"}},` + modernMeta + `}}`},
		{"no name", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"arguments":{},` + modernMeta + `}}`},
	} {
		if err := tr.Send([]byte(c.body)); err != nil {
			t.Fatalf("%s: Send: %v", c.name, err)
		}
		got := receiveWithin(t, tr, time.Second)
		if !strings.Contains(got, `"id":2`) || !strings.Contains(got, "-32602") {
			t.Errorf("%s: answer %q", c.name, got)
		}
		if strings.Contains(got, "s3cr3t") {
			t.Errorf("%s: the value reached the answer: %q", c.name, got)
		}
	}
	if rec.count() != before {
		t.Errorf("an unsendable request reached the upstream")
	}
}

// A legacy session's DELETE carries the negotiated version too.
func TestCloseCarriesTheNegotiatedVersion(t *testing.T) {
	deleted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted <- r.Header.Get("MCP-Protocol-Version")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Mcp-Session-Id", "s1")
		jsonAnswer(w, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`)
	}))
	defer srv.Close()

	tr, _ := NewHTTP(srv.URL)
	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`))
	receiveWithin(t, tr, time.Second)
	_ = tr.Close()
	select {
	case v := <-deleted:
		if v != "2025-11-25" {
			t.Errorf("DELETE carried MCP-Protocol-Version %q", v)
		}
	case <-time.After(time.Second):
		t.Fatal("no DELETE")
	}
}

// A legacy upstream refusing the era probe is the normal fallback, and the
// log says so instead of reporting a failure.
func TestALegacyAnswerToTheEraProbeIsLoggedAsTheFallback(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "JSON RPC not handled")
	})
	var logs syncBuffer
	tr, _ := NewHTTP(srv.URL, WithLogs(&logs))
	defer tr.Close()

	_ = tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + modernMeta + `}}`))
	if got := receiveWithin(t, tr, time.Second); !strings.Contains(got, "-32603") {
		t.Errorf("answer %s", got)
	}
	if !strings.Contains(logs.String(), "a legacy server") || strings.Contains(logs.String(), "answering the request") {
		t.Errorf("log %q", logs.String())
	}
}
