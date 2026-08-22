// Package cli implements the bodies of mcp-bridge's subcommands. Flag parsing
// and dispatch stay in main.go, where tests pin them to the usage text.
package cli

import (
	"fmt"
	"io"

	"github.com/nlink-jp/mcp-bridge/internal/bridge"
	"github.com/nlink-jp/mcp-bridge/internal/config"
	"github.com/nlink-jp/mcp-bridge/internal/transport"
)

// RunOptions describes one bridge session.
type RunOptions struct {
	ConfigPath string    // empty means the default location
	Server     string    // configured server name
	In         io.Reader // client input (stdin)
	Out        io.Writer // client output (stdout) — JSON-RPC only
	Logs       io.Writer // diagnostics (stderr)
}

// Run bridges the client on In/Out to the configured upstream server, and
// returns when the client closes its input.
func Run(opts RunOptions) error {
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

	fmt.Fprintf(opts.Logs, "mcp-bridge: connected to %s (server %q, auth %s)\n",
		srv.URL, opts.Server, srv.AuthMode())

	return bridge.New(upstream, opts.In, opts.Out, opts.Logs).Run()
}

// loadServer resolves the config path and looks up one server.
func loadServer(configPath, name string) (*config.Server, error) {
	path, err := config.ResolvePath(configPath)
	if err != nil {
		return nil, err
	}
	file, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return file.Server(name)
}
