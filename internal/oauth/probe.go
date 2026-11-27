package oauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nlink-jp/mcp"
	"github.com/nlink-jp/mcp/client"
	"github.com/nlink-jp/mcp/httpwire"
)

// defaultProbeTimeout bounds the wait for the probe's answer. It is the same as
// defaultHTTPClient's timeout, so the module's probe deadline adds no bound of
// its own (ADR-0009 §1).
const defaultProbeTimeout = 30 * time.Second

// probed is what the server's answer to an unauthenticated request said.
type probed struct {
	reached     bool // the server answered, whatever it said
	metadataURL string
	scopes      []string
}

// probeNotSentError is a probe that failed before any request went out: an
// error of mcp-bridge, not a server that could not be reached.
type probeNotSentError struct{ err error }

func (e *probeNotSentError) Error() string {
	return "the discovery probe could not be sent: " + e.err.Error()
}
func (e *probeNotSentError) Unwrap() error { return e.err }

// probeChallenge makes an unauthenticated request and reads the 401 challenge:
// where the server's metadata is, and which scopes it asks for.
//
// The request is the opening an ordinary client sends, built by nlink-jp/mcp:
// client.Open runs over a probeConn, which posts the first message and ends
// the opening there (ADR-0009). mcp-bridge writes no handshake of its own.
//
// An error from a server that answered is one more attempt to report, not the
// end of discovery: a server that answers without a challenge, or with one
// that names nothing, may still publish its metadata at a well-known location.
func probeChallenge(req discoveryRequest) (probed, error) {
	timeout := req.probeTimeout
	if timeout == 0 {
		timeout = defaultProbeTimeout
	}
	conn := newProbeConn(req.client, req.serverURL)
	// Open returns no Client on error — and over a probeConn it always errs —
	// so closing the Conn is what ends its read and answer loops.
	defer conn.Close()

	// The whole opening is bounded too, not only the module's probe: should a
	// later request ever be sent, it does not wait without end.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, openErr := client.Open(ctx, conn, client.Options{
		Client:       mcp.Implementation{Name: "mcp-bridge", Version: "discovery"},
		ProbeTimeout: timeout,
	})

	out, ok := conn.outcome()
	if !ok {
		if openErr == nil {
			openErr = errors.New("the opening sent nothing")
		}
		return probed{}, &probeNotSentError{err: openErr}
	}
	if out.err != nil {
		if out.notSent {
			return probed{}, &probeNotSentError{err: out.err}
		}
		// A redirect this client refused is still an answer from the server.
		var refused *redirectError
		return probed{reached: errors.As(out.err, &refused)}, out.err
	}

	if out.status != http.StatusUnauthorized {
		return probed{reached: true}, fmt.Errorf("expected an HTTP 401 challenge, got %d — the server may not require authorization", out.status)
	}
	p := probed{
		reached:     true,
		metadataURL: bearerParam(out.challenges, "resource_metadata"),
		scopes:      strings.Fields(bearerParam(out.challenges, "scope")),
	}
	if p.metadataURL == "" {
		return p, fmt.Errorf("the challenge names no resource_metadata: %s", orDash(strings.Join(out.challenges, ", ")))
	}
	return p, nil
}

// errProbeAnswered ends client.Open after the probe's one request, whatever
// came of it. One error for every outcome matters: Open reads a Send error
// that is a deadline as "no answer" and falls back to the legacy opening.
var errProbeAnswered = errors.New("discovery probe: the first request has been made")

// errProbeSpent refuses every Send after the first, without a request.
var errProbeSpent = errors.New("discovery probe: only one request is made")

// probeOutcome is what came of the probe's one request.
type probeOutcome struct {
	status     int
	challenges []string
	err        error // the request failed; status and challenges are unset
	notSent    bool  // err came before any request went out
}

// probeConn is a client.Conn that makes one unauthenticated POST and keeps
// its outcome. It delivers nothing to Recv: the probe reads the HTTP answer,
// not an MCP one.
type probeConn struct {
	client *http.Client
	url    string

	mu   sync.Mutex
	sent bool
	out  probeOutcome

	closeOnce sync.Once
	closed    chan struct{}
}

func newProbeConn(c *http.Client, url string) *probeConn {
	return &probeConn{client: c, url: url, closed: make(chan struct{})}
}

// Send posts msg once, with the headers the module derives from it and no
// credential, and records the outcome. It returns errProbeAnswered on the
// first call and errProbeSpent on every later one.
func (p *probeConn) Send(ctx context.Context, msg []byte) error {
	p.mu.Lock()
	if p.sent {
		p.mu.Unlock()
		return errProbeSpent
	}
	p.sent = true
	p.mu.Unlock()

	out := p.post(ctx, msg)

	p.mu.Lock()
	p.out = out
	p.mu.Unlock()
	return errProbeAnswered
}

func (p *probeConn) post(ctx context.Context, msg []byte) probeOutcome {
	headers, err := httpwire.RequestHeaders(msg, "", nil)
	if err != nil {
		return probeOutcome{err: err, notSent: true}
	}
	// A bytes.Reader lets a 307/308 resend the body.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(msg))
	if err != nil {
		return probeOutcome{err: err, notSent: true}
	}
	for name, values := range headers {
		httpReq.Header[name] = values
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return probeOutcome{err: err}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxMetadataBytes))
	resp.Body.Close()
	return probeOutcome{status: resp.StatusCode, challenges: resp.Header.Values("WWW-Authenticate")}
}

// outcome returns the recorded outcome, and false if no Send was made.
func (p *probeConn) outcome() (probeOutcome, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out, p.sent
}

// Recv blocks until Close, then reports the end.
func (p *probeConn) Recv() ([]byte, error) {
	<-p.closed
	return nil, io.EOF
}

func (p *probeConn) Close() error {
	p.closeOnce.Do(func() { close(p.closed) })
	return nil
}
