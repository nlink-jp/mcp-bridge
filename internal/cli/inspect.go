package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/nlink-jp/mcp-bridge/internal/config"
	"github.com/nlink-jp/mcp-bridge/internal/jsonrpc"
	"github.com/nlink-jp/mcp-bridge/internal/transport"
)

// inspectTimeout bounds the wait for each reply while inspecting.
const inspectTimeout = 30 * time.Second

// InspectOptions describes which server to interrogate.
type InspectOptions struct {
	ConfigPath string
	Server     string
	Out        io.Writer
	Logs       io.Writer
}

// Inspect connects to a server and prints what it says it is and what tools it
// offers.
//
// This is the command that answers "is my configuration right?" without wiring
// the bridge into an MCP client first, and before the first login is exactly
// when that gets asked — so a missing OAuth login does not stop it. Many MCP
// servers answer initialize and tools/list without a credential, and what they
// say confirms the URL, the protocol and the tool list regardless of whether
// the login has happened yet.
func Inspect(opts InspectOptions) error {
	srv, err := loadServer(opts.ConfigPath, opts.Server)
	if err != nil {
		return err
	}
	transportOpts, cred, err := buildTransportOptions(srv, opts.Server, opts.Logs, true)
	if err != nil {
		return err
	}
	upstream, err := transport.NewHTTP(srv.URL, transportOpts...)
	if err != nil {
		return err
	}
	defer upstream.Close()

	initResult, err := call(upstream, 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mcp-bridge", "version": "inspect"},
	})
	if err != nil {
		if !cred.presented && cred.mode != config.AuthNone {
			// The server did want a credential, and there is none to send.
			return fmt.Errorf("%w\n(the server requires authentication; run \"mcp-bridge login %s\")", err, opts.Server)
		}
		return err
	}

	var info struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(initResult, &info)

	fmt.Fprintf(opts.Out, "%s\n", srv.URL)
	fmt.Fprintf(opts.Out, "  server:     %s %s\n", orUnknown(info.ServerInfo.Name), info.ServerInfo.Version)
	fmt.Fprintf(opts.Out, "  protocol:   %s\n", orUnknown(info.ProtocolVersion))
	for _, line := range authLines(srv.URL, cred) {
		fmt.Fprintln(opts.Out, line)
	}

	// The specification requires this notification after initialize; some
	// servers reject later requests without it.
	if err := upstream.Send([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}

	toolsResult, err := call(upstream, 2, "tools/list", map[string]any{})
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	var tools struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(toolsResult, &tools); err != nil {
		return fmt.Errorf("parse the tool list: %w", err)
	}

	fmt.Fprintf(opts.Out, "\n%d tool(s):\n", len(tools.Tools))
	w := tabwriter.NewWriter(opts.Out, 0, 0, 2, ' ', 0)
	for _, tool := range tools.Tools {
		fmt.Fprintf(w, "  %s\t%s\n", tool.Name, firstLine(tool.Description))
	}
	return w.Flush()
}

// call sends one request and waits for the matching response.
//
// Inspection is strictly sequential, so correlation is a comparison rather
// than a pending-request table: anything that arrives with a different id is a
// notification or a stray message and is skipped.
func call(upstream transport.Transport, id int, method string, params any) (json.RawMessage, error) {
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}
	if err := upstream.Send(request); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}

	type received struct {
		msg *jsonrpc.Message
		ok  bool
	}
	replies := make(chan received, 1)
	go func() {
		for {
			raw, ok := upstream.Receive()
			if !ok {
				replies <- received{nil, false}
				return
			}
			msg, err := jsonrpc.Parse(raw)
			if err != nil {
				continue
			}
			if msg.IsResponse() && msg.IDKey() == fmt.Sprint(id) {
				replies <- received{msg, true}
				return
			}
		}
	}()

	select {
	case r := <-replies:
		if !r.ok {
			return nil, fmt.Errorf("%s: the connection closed before a reply arrived", method)
		}
		if r.msg.Error != nil {
			return nil, fmt.Errorf("%s: the server returned error %d: %s", method, r.msg.Error.Code, r.msg.Error.Message)
		}
		return r.msg.Result, nil
	case <-time.After(inspectTimeout):
		return nil, fmt.Errorf("%s: no reply within %s", method, inspectTimeout)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(not reported)"
	}
	return s
}

// firstLine keeps a listing to one row per tool; descriptions are often
// several paragraphs.
func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// authLines describes how the connection was authenticated.
//
// A successful inspect with a credential attached does not, on its own, mean
// the credential works: a server that answers initialize to anyone would have
// produced the same output with no token at all. One unauthenticated probe
// settles which of the two happened, and saying so is the difference between
// "your login works" and "nothing here tested your login".
func authLines(url string, cred credential) []string {
	const label = "  auth:       "
	const indent = "              "

	switch {
	case cred.mode == config.AuthNone:
		return []string{label + string(cred.mode)}

	case !cred.presented:
		return []string{
			fmt.Sprintf("%s%s (%s)", label, cred.mode, cred.why),
			indent + "showing what the server returns without a credential",
		}

	case serverAnswersUnauthenticated(url):
		return []string{
			fmt.Sprintf("%s%s (credential presented)", label, cred.mode),
			indent + "the server answers these calls without a credential too,",
			indent + "so this does not confirm the credential works",
		}

	default:
		return []string{fmt.Sprintf("%s%s (credential accepted)", label, cred.mode)}
	}
}

// serverAnswersUnauthenticated reports whether the server completes initialize
// with no credential at all. A failure to probe answers false: the point is to
// avoid overclaiming, and an inconclusive probe is not evidence.
func serverAnswersUnauthenticated(url string) bool {
	bare, err := transport.NewHTTP(url)
	if err != nil {
		return false
	}
	defer bare.Close()

	_, err = call(bare, 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mcp-bridge", "version": "inspect-probe"},
	})
	return err == nil
}
