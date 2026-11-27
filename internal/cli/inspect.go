package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/nlink-jp/mcp"
	"github.com/nlink-jp/mcp/client"

	"github.com/nlink-jp/mcp-bridge/internal/config"
	"github.com/nlink-jp/mcp-bridge/internal/transport"
)

// inspectTimeout bounds the whole of an inspection: opening the server and
// listing its tools.
const inspectTimeout = 30 * time.Second

// probeTimeout bounds the server/discover probe. An HTTP server answers it at
// once, modern or legacy; the default 5 s of nlink-jp/mcp/client is meant for
// a local process and is short for a first POST that renews a token.
const probeTimeout = 15 * time.Second

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
// servers answer without a credential, and what they say confirms the URL, the
// protocol and the tool list regardless of whether the login has happened yet.
//
// The server is opened with nlink-jp/mcp/client, in the modern era when the
// server allows it and in the legacy era otherwise (ADR-0008 §9).
func Inspect(opts InspectOptions) error {
	// Everything printed below the URL is what the server says about itself.
	opts.Out = printable(opts.Out)

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

	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()
	c, err := openServer(ctx, upstream, "inspect")
	if err != nil {
		if !cred.presented && cred.mode != config.AuthNone {
			// The server did want a credential, and there is none to send.
			return fmt.Errorf("%w\n(the server requires authentication; run \"mcp-bridge login %s\")", err, opts.Server)
		}
		return err
	}
	defer c.Close()

	info := c.Server()
	fmt.Fprintf(opts.Out, "%s\n", srv.URL)
	fmt.Fprintf(opts.Out, "  server:     %s %s\n", orUnknown(info.Name), info.Version)
	fmt.Fprintf(opts.Out, "  protocol:   %s (%s)\n", orUnknown(c.Version()), c.Era())
	for _, line := range authLines(srv, cred) {
		fmt.Fprintln(opts.Out, line)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}

	fmt.Fprintf(opts.Out, "\n%d tool(s):\n", len(tools.Tools))
	w := tabwriter.NewWriter(opts.Out, 0, 0, 2, ' ', 0)
	for _, tool := range tools.Tools {
		fmt.Fprintf(w, "  %s\t%s\n", tool.Name, firstLine(tool.Description))
	}
	return w.Flush()
}

// openServer opens the server behind upstream with nlink-jp/mcp/client: the
// server/discover probe, then initialize if the server is legacy.
func openServer(ctx context.Context, upstream transport.Transport, role string) (*client.Client, error) {
	c, err := client.Open(ctx, transport.NewConn(upstream), client.Options{
		Client:       mcp.Implementation{Name: "mcp-bridge", Version: role},
		ProbeTimeout: probeTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("open the server: %w", err)
	}
	return c, nil
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
func authLines(srv *config.Server, cred credential) []string {
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

	case serverAnswersUnauthenticated(srv):
		return []string{
			fmt.Sprintf("%s%s (credential presented)", label, cred.mode),
			indent + "the server answers these calls without a credential too,",
			indent + "so this does not confirm the credential works",
		}

	default:
		return []string{fmt.Sprintf("%s%s (credential accepted)", label, cred.mode)}
	}
}

// serverAnswersUnauthenticated reports whether the server opens — in either
// era — with no credential at all. A failure to probe answers false: the point is to
// avoid overclaiming, and an inconclusive probe is not evidence.
func serverAnswersUnauthenticated(srv *config.Server) bool {
	bare, err := transport.NewHTTP(srv.URL, connectionOptions(srv)...)
	if err != nil {
		return false
	}
	defer bare.Close()

	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()
	c, err := openServer(ctx, bare, "inspect-probe")
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
