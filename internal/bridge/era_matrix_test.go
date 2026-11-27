package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/mcp/probe"

	"github.com/nlink-jp/mcp-bridge/internal/transport"
)

// The probe matrix of ADR-0008's test design: nlink-jp/mcp's three probe
// clients (legacy only, dual-era, modern only) through mcp-bridge's stdio, to
// three Streamable HTTP upstreams (legacy only, dual-era, modern only). The
// relay passes the era through, so each pair must end as the specification's
// compatibility matrix says for that client and that server (probe.Expect).
//
// Each upstream runs nlink-jp/mcp's stdio probe server for its MCP behaviour
// and adds the HTTP rules of the 2026-07-28 streamable-http text in front of
// it: a modern request's MCP-Protocol-Version and Mcp-Method must match the
// body (400 with -32020 otherwise), and a legacy request after initialize must
// carry the negotiated MCP-Protocol-Version (required since 2025-06-18).

// httpProbe is one upstream.
type httpProbe struct {
	mu     sync.Mutex
	in     io.WriteCloser
	lines  chan string
	legacy string // the version a legacy initialize negotiated
}

func newHTTPProbe(t *testing.T, kind probe.ServerKind) *httptest.Server {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = probe.Serve(ctx, kind, inR, outW, probe.ServerOptions{}); outW.Close() }()
	p := &httpProbe{in: inW, lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(func() { srv.Close(); cancel(); inW.Close() })
	return srv
}

func (p *httpProbe) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &msg)
	var version string
	_ = json.Unmarshal(msg.Params.Meta["io.modelcontextprotocol/protocolVersion"], &version)

	p.mu.Lock()
	defer p.mu.Unlock()
	reject := func(code int, text string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, msg.ID, code, text)
	}
	switch {
	case version != "":
		if r.Header.Get("MCP-Protocol-Version") != version || r.Header.Get("Mcp-Method") != msg.Method {
			reject(-32020, "headers and body disagree")
			return
		}
	case msg.Method != "initialize" && p.legacy != "":
		if r.Header.Get("MCP-Protocol-Version") != p.legacy {
			reject(-32600, "MCP-Protocol-Version "+p.legacy+" is required")
			return
		}
	}

	if _, err := p.in.Write(append(body, '\n')); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if len(msg.ID) == 0 || msg.Method == "" {
		w.WriteHeader(http.StatusAccepted) // a notification or a response
		return
	}
	select {
	case line, ok := <-p.lines:
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if msg.Method == "initialize" {
			var res struct {
				Result struct {
					ProtocolVersion string `json:"protocolVersion"`
				} `json:"result"`
			}
			if json.Unmarshal([]byte(line), &res) == nil && res.Result.ProtocolVersion != "" {
				p.legacy = res.Result.ProtocolVersion
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(line, `"error"`) {
			w.WriteHeader(http.StatusBadRequest)
		}
		fmt.Fprint(w, line)
	case <-time.After(5 * time.Second):
		w.WriteHeader(http.StatusGatewayTimeout)
	}
}

func TestEraMatrixThroughTheBridge(t *testing.T) {
	clients := []probe.ClientKind{probe.ClientLegacyOnly, probe.ClientDual, probe.ClientModernOnly}
	servers := []probe.ServerKind{probe.ServerLegacy, probe.ServerDual, probe.ServerModernOnly}
	for _, ck := range clients {
		for _, sk := range servers {
			t.Run(ck.String()+"/"+sk.String(), func(t *testing.T) {
				srv := newHTTPProbe(t, sk)
				up, err := transport.NewHTTP(srv.URL)
				if err != nil {
					t.Fatal(err)
				}
				toBridgeR, toBridgeW := io.Pipe()
				fromBridgeR, fromBridgeW := io.Pipe()
				done := make(chan error, 1)
				go func() {
					done <- New(up, toBridgeR, fromBridgeW, io.Discard).Run()
					fromBridgeW.Close()
				}()

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				rep := probe.Run(ctx, ck, fromBridgeR, toBridgeW, probe.ClientOptions{})
				toBridgeW.Close()
				<-done

				if want := probe.Expect(ck, sk); rep.Outcome != want {
					t.Errorf("outcome %q, want %q (error %q)\nsteps: %+v", rep.Outcome, want, rep.Error, rep.Steps)
				}
				if len(rep.Violations) > 0 {
					t.Errorf("violations: %v", rep.Violations)
				}
			})
		}
	}
}
