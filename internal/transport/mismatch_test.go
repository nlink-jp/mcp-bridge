package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ADR-0008 §6: a HeaderMismatch on a modern tools/call is retried once after
// refreshing the marks from tools/list. The upstream here demands
// Mcp-Param-Owner on get_repo, as the specification's server validation does.

const mismatch = `{"jsonrpc":"2.0","id":%s,"error":{"code":-32020,"message":"Mcp-Param-Owner header is required"}}`

func modernCall(id int) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"get_repo","arguments":{"owner":"o"},%s}}`, id, modernMeta))
}

// strictUpstream answers tools/list with lists(cursor) and tools/call with
// call(hasParamHeader).
func strictUpstream(t *testing.T, rec *recorder, lists func(cursor string) string, call func(w http.ResponseWriter, id string, hasParam bool)) string {
	return rec.server(t, func(w http.ResponseWriter, body string) {
		var msg struct {
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		_ = json.Unmarshal([]byte(body), &msg)
		id := idOf(body)
		switch msg.Method {
		case "tools/list":
			jsonAnswer(w, fmt.Sprintf(lists(msg.Params.Cursor), id))
		default:
			call(w, id, rec.last().header.Get("Mcp-Param-Owner") != "")
		}
	}).URL
}

func rejectWithoutParam(w http.ResponseWriter, id string, hasParam bool) {
	if !hasParam {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, mismatch, id)
		return
	}
	jsonAnswer(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","content":[{"type":"text","text":"ok"}]}}`, id))
}

func alwaysReject(w http.ResponseWriter, id string, _ bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, mismatch, id)
}

func onePage(string) string { return toolsWithOwnerMark }

// noMore asserts nothing else is delivered.
func noMore(t *testing.T, tr Transport) {
	t.Helper()
	ch := make(chan []byte, 1)
	go func() {
		if m, ok := tr.Receive(); ok {
			ch <- m
		}
	}()
	select {
	case m := <-ch:
		t.Errorf("an extra message reached the client: %s", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMismatchIsRetriedOnceAfterARefresh(t *testing.T) {
	var rec recorder
	url := strictUpstream(t, &rec, onePage, rejectWithoutParam)
	tr, _ := NewHTTP(url)
	defer tr.Close()

	if err := tr.Send(modernCall(4)); err != nil {
		t.Fatal(err)
	}
	got := receiveWithin(t, tr, 2*time.Second)
	if !strings.Contains(got, `"id":4`) || !strings.Contains(got, `"ok"`) {
		t.Errorf("the resend's answer was not delivered: %s", got)
	}
	noMore(t, tr) // the refresh's tools/list answer stays inside the transport
	if rec.count() != 3 {
		t.Errorf("upstream saw %d requests, want call, refresh, resend", rec.count())
	}

	// The refresh spoke as the client did.
	rec.mu.Lock()
	refresh := rec.reqs[1].body
	rec.mu.Unlock()
	for _, want := range []string{`"method":"tools/list"`, `"mcp-bridge-refresh-`, `"io.modelcontextprotocol/protocolVersion":"2026-07-28"`,
		`"io.modelcontextprotocol/clientCapabilities":{}`, `"io.modelcontextprotocol/clientInfo":{"name":"t","version":"0"}`} {
		if !strings.Contains(refresh, want) {
			t.Errorf("refresh %s lacks %s", refresh, want)
		}
	}
}

func TestASecondMismatchIsDelivered(t *testing.T) {
	var rec recorder
	url := strictUpstream(t, &rec, onePage, alwaysReject)
	tr, _ := NewHTTP(url)
	defer tr.Close()

	_ = tr.Send(modernCall(5))
	got := receiveWithin(t, tr, 2*time.Second)
	if !strings.Contains(got, "-32020") || !strings.Contains(got, `"id":5`) {
		t.Errorf("answer %s", got)
	}
	noMore(t, tr)
	if rec.count() != 3 {
		t.Errorf("upstream saw %d requests, want exactly one retry", rec.count())
	}
}

func TestAFailedRefreshDeliversTheFirstMismatch(t *testing.T) {
	cases := map[string]func(string) string{
		// An error answer to tools/list.
		"error": func(string) string {
			return `{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"down"}}`
		},
		// More pages than a refresh reads.
		"page limit": func(cursor string) string {
			return `{"jsonrpc":"2.0","id":%s,"result":{"tools":[],"nextCursor":"more` + cursor + `"}}`
		},
	}
	for name, lists := range cases {
		t.Run(name, func(t *testing.T) {
			var rec recorder
			url := strictUpstream(t, &rec, lists, rejectWithoutParam)
			tr, _ := NewHTTP(url)
			defer tr.Close()

			_ = tr.Send(modernCall(6))
			got := receiveWithin(t, tr, 2*time.Second)
			if !strings.Contains(got, "-32020") || !strings.Contains(got, `"id":6`) {
				t.Errorf("answer %s", got)
			}
			noMore(t, tr)
			rec.mu.Lock()
			last := rec.reqs[len(rec.reqs)-1].body
			rec.mu.Unlock()
			if strings.Contains(last, `"tools/call"`) {
				t.Errorf("the request was resent after a failed refresh")
			}
		})
	}
}

// The marks may sit on a later page.
func TestRefreshFollowsTheCursor(t *testing.T) {
	var rec recorder
	lists := func(cursor string) string {
		if cursor == "" {
			return `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"other","inputSchema":{"type":"object"}}],"nextCursor":"p2"}}`
		}
		return toolsWithOwnerMark
	}
	url := strictUpstream(t, &rec, lists, rejectWithoutParam)
	tr, _ := NewHTTP(url)
	defer tr.Close()

	_ = tr.Send(modernCall(7))
	if got := receiveWithin(t, tr, 2*time.Second); !strings.Contains(got, `"ok"`) {
		t.Errorf("answer %s", got)
	}
}

func TestOnlyAModernToolsCallMismatchIsRetried(t *testing.T) {
	cases := map[string][]byte{
		"legacy tools/call": []byte(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"get_repo","arguments":{"owner":"o"}}}`),
		"modern tools/list": []byte(`{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{` + modernMeta + `}}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var rec recorder
			srv := rec.server(t, func(w http.ResponseWriter, b string) { alwaysReject(w, idOf(b), false) })
			tr, _ := NewHTTP(srv.URL)
			defer tr.Close()
			_ = tr.Send(body)
			if got := receiveWithin(t, tr, 2*time.Second); !strings.Contains(got, "-32020") {
				t.Errorf("answer %s", got)
			}
			if rec.count() != 1 {
				t.Errorf("upstream saw %d requests, want no retry", rec.count())
			}
		})
	}

	t.Run("another modern error", func(t *testing.T) {
		var rec recorder
		srv := rec.server(t, func(w http.ResponseWriter, b string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32022,"message":"Unsupported protocol version"}}`, idOf(b))
		})
		tr, _ := NewHTTP(srv.URL)
		defer tr.Close()
		_ = tr.Send(modernCall(9))
		if got := receiveWithin(t, tr, 2*time.Second); !strings.Contains(got, "-32022") {
			t.Errorf("answer %s", got)
		}
		if rec.count() != 1 {
			t.Errorf("upstream saw %d requests, want no retry", rec.count())
		}
	})
}

// A refresh answered over SSE works the same.
func TestRefreshReadsAnSSEAnswer(t *testing.T) {
	var rec recorder
	srv := rec.server(t, func(w http.ResponseWriter, body string) {
		id := idOf(body)
		if strings.Contains(body, `"tools/list"`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "event: message\ndata: "+toolsWithOwnerMark+"\n\n", id)
			return
		}
		rejectWithoutParam(w, id, rec.last().header.Get("Mcp-Param-Owner") != "")
	})
	tr, _ := NewHTTP(srv.URL)
	defer tr.Close()

	_ = tr.Send(modernCall(10))
	if got := receiveWithin(t, tr, 2*time.Second); !strings.Contains(got, `"ok"`) {
		t.Errorf("answer %s", got)
	}
	noMore(t, tr) // the refresh stream's notification is not the client's
}
