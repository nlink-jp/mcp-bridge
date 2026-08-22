package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

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
// This is the command that answers "is my configuration right?" without
// wiring the bridge into an MCP client first, so its failures are the useful
// output: an authentication problem surfaces here with the login command
// attached rather than as a client that silently shows no tools.
func Inspect(opts InspectOptions) error {
	srv, err := loadServer(opts.ConfigPath, opts.Server)
	if err != nil {
		return err
	}
	transportOpts, err := transportOptions(srv, opts.Server, opts.Logs)
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
	fmt.Fprintf(opts.Out, "  auth:       %s\n", srv.AuthMode())

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
