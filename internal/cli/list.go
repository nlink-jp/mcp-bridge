package cli

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/nlink-jp/mcp-bridge/internal/config"
)

// ListOptions describes a listing.
type ListOptions struct {
	ConfigPath string
	Out        io.Writer
}

// List prints the configured servers with their authentication mode and, for
// the OAuth ones, whether a token has been stored.
func List(opts ListOptions) error {
	path, err := config.ResolvePath(opts.ConfigPath)
	if err != nil {
		return err
	}
	file, err := config.Load(path)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(opts.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tAUTH\tSTATE\tURL")
	for _, name := range file.Names() {
		srv := file.Servers[name]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, srv.AuthMode(), loginState(srv, name), srv.URL)
	}
	return w.Flush()
}

// loginState reports whether a server needs a login and whether it has had
// one. Only the OAuth modes have a login to do.
func loginState(srv *config.Server, name string) string {
	switch srv.AuthMode() {
	case config.AuthOAuthConfigured, config.AuthOAuthDiscover:
	default:
		return "-"
	}
	tokens, err := config.TokensPath(name)
	if err != nil {
		return "unknown"
	}
	if _, err := os.Stat(tokens); err != nil {
		return "not logged in"
	}
	return "logged in"
}

// fileExists reports whether a path is present.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
