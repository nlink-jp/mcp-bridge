package jsonrpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageShapes(t *testing.T) {
	cases := []struct {
		name                            string
		raw                             string
		request, notification, response bool
	}{
		{"request", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, true, false, false},
		{"string id request", `{"jsonrpc":"2.0","id":"abc","method":"initialize"}`, true, false, false},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, false, true, false},
		{"result response", `{"jsonrpc":"2.0","id":1,"result":{}}`, false, false, true},
		{"error response", `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"x"}}`, false, false, true},
		// A null id is not a usable id: it cannot be correlated, and a message
		// carrying one is not a request awaiting a reply.
		{"null id with method", `{"jsonrpc":"2.0","id":null,"method":"x"}`, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if m.IsRequest() != tc.request {
				t.Errorf("IsRequest = %v, want %v", m.IsRequest(), tc.request)
			}
			if m.IsNotification() != tc.notification {
				t.Errorf("IsNotification = %v, want %v", m.IsNotification(), tc.notification)
			}
			if m.IsResponse() != tc.response {
				t.Errorf("IsResponse = %v, want %v", m.IsResponse(), tc.response)
			}
		})
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("garbage was accepted")
	}
}

// A relay must echo the id it was handed. Reformatting it — a large integer
// losing precision through float64, say — breaks the client's correlation.
func TestIDIsEchoedVerbatim(t *testing.T) {
	for _, id := range []string{`1`, `"abc"`, `9007199254740993`} {
		m, err := Parse([]byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp := NewErrorResponse(m.ID, CodeInternalError, "boom")
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(resp, &decoded); err != nil {
			t.Fatal(err)
		}
		if got := string(decoded["id"]); got != id {
			t.Errorf("id round-tripped as %s, want %s", got, id)
		}
	}
}

func TestIDKey(t *testing.T) {
	req, _ := Parse([]byte(`{"jsonrpc":"2.0","id":42,"method":"x"}`))
	resp, _ := Parse([]byte(`{"jsonrpc":"2.0","id":42,"result":{}}`))
	if req.IDKey() != resp.IDKey() {
		t.Errorf("request key %q does not match response key %q", req.IDKey(), resp.IDKey())
	}
	notification, _ := Parse([]byte(`{"jsonrpc":"2.0","method":"x"}`))
	if notification.IDKey() != "" {
		t.Errorf("notification produced a correlation key %q", notification.IDKey())
	}
}

func TestNewErrorResponseShape(t *testing.T) {
	raw := NewErrorResponse(json.RawMessage(`7`), CodeInvalidRequest, "upstream unreachable")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsResponse() {
		t.Error("built message is not a response")
	}
	if m.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", m.JSONRPC)
	}
	if m.Error == nil || m.Error.Code != CodeInvalidRequest {
		t.Fatalf("error member = %+v", m.Error)
	}
	if !strings.Contains(m.Error.Message, "upstream unreachable") {
		t.Errorf("message = %q", m.Error.Message)
	}
	if m.Result != nil {
		t.Error("an error response must not carry a result")
	}
}

func TestNewErrorResponseWithNoID(t *testing.T) {
	raw := NewErrorResponse(nil, CodeParseError, "bad json")
	if !strings.Contains(string(raw), `"id":null`) {
		t.Errorf("missing id was not encoded as null: %s", raw)
	}
}

func TestIsBatch(t *testing.T) {
	if !IsBatch([]byte(`  [{"jsonrpc":"2.0"}]`)) {
		t.Error("array not detected as a batch")
	}
	if IsBatch([]byte(`{"jsonrpc":"2.0"}`)) {
		t.Error("object detected as a batch")
	}
	if IsBatch(nil) {
		t.Error("empty input detected as a batch")
	}
}
