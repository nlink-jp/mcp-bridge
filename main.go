// Command mcp-bridge connects stdio-only MCP clients to Streamable HTTP MCP
// servers that require a pre-registered OAuth client.
//
// The downstream side is always stdio: mcp-bridge is launched as a child
// process by the MCP client and speaks JSON-RPC 2.0 over stdin/stdout. The
// upstream side is always Streamable HTTP. The reverse direction — exposing a
// stdio MCP server over HTTP — is not supported.
//
// Everything that is not JSON-RPC goes to stderr. Writing anything else to
// stdout breaks the MCP connection.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nlink-jp/mcp-bridge/internal/cli"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

const usageText = `mcp-bridge — bridge stdio MCP clients to Streamable HTTP MCP servers

usage:
  mcp-bridge run <name>       Start the bridge (launched by the MCP client)
  mcp-bridge login <name>     OAuth browser login
  mcp-bridge logout <name>    Delete stored tokens
  mcp-bridge list             List configured servers and their login state
  mcp-bridge inspect <name>   Connect and print serverInfo and the tool list
  mcp-bridge version          Print version

flags:
  --config <path>             Config file (default ~/.config/mcp-bridge/config.json)
  --callback-port <n>         Fixed loopback port for the OAuth callback (login only)

Run "mcp-bridge <command> --help" for the flags of a single command.
`

// errUsage signals that usage has already been written; main exits non-zero
// without printing a second diagnostic.
var errUsage = errors.New("usage")

// errNotImplemented marks the commands the OAuth phase still has to fill in.
var errNotImplemented = errors.New("not implemented yet")

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintf(os.Stderr, "mcp-bridge: %v\n", err)
		}
		os.Exit(1)
	}
}

// run dispatches a command line. I/O is injected so the whole surface is
// testable without touching the real stdout/stderr.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return errUsage
	}

	switch args[0] {
	case "version", "--version", "-version":
		// The org convention requires --version and the version subcommand to
		// produce identical output; a test pins both.
		fmt.Fprintf(stdout, "mcp-bridge %s\n", version)
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return nil
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "login":
		return cmdLogin(args[1:], stdout, stderr)
	case "logout":
		return cmdLogout(args[1:], stdout, stderr)
	case "list":
		return cmdList(args[1:], stdout, stderr)
	case "inspect":
		return cmdInspect(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usageText)
		return errUsage
	}
}

// newFlagSet builds a FlagSet that reports errors through the injected writer
// and registers --config, which every command accepts.
func newFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "Config file path (default ~/.config/mcp-bridge/config.json)")
	return fs, configPath
}

// serverArg parses a flag set that takes exactly one server name.
func serverArg(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", errUsage
	}
	switch fs.NArg() {
	case 1:
		return fs.Arg(0), nil
	case 0:
		return "", fmt.Errorf("%s: a server name is required", fs.Name())
	default:
		return "", fmt.Errorf("%s: expected exactly one server name, got %d", fs.Name(), fs.NArg())
	}
}

func cmdRun(args []string, stdout, stderr io.Writer) error {
	fs, configPath := newFlagSet("run", stderr)
	name, err := serverArg(fs, args)
	if err != nil {
		return err
	}
	// stdin is read directly rather than through the injected reader: run is
	// the one command whose input is the live client connection.
	return cli.Run(cli.RunOptions{
		ConfigPath: *configPath,
		Server:     name,
		In:         os.Stdin,
		Out:        stdout,
		Logs:       stderr,
	})
}

func cmdLogin(args []string, stdout, stderr io.Writer) error {
	fs, configPath := newFlagSet("login", stderr)
	callbackPort := fs.Int("callback-port", 0, "Fixed loopback port for the OAuth callback (overrides the config)")
	name, err := serverArg(fs, args)
	if err != nil {
		return err
	}
	_, _ = configPath, callbackPort
	return fmt.Errorf("login %s: %w", name, errNotImplemented)
}

func cmdLogout(args []string, stdout, stderr io.Writer) error {
	fs, configPath := newFlagSet("logout", stderr)
	name, err := serverArg(fs, args)
	if err != nil {
		return err
	}
	_ = configPath
	return fmt.Errorf("logout %s: %w", name, errNotImplemented)
}

func cmdList(args []string, stdout, stderr io.Writer) error {
	fs, configPath := newFlagSet("list", stderr)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("list: takes no arguments, got %d", fs.NArg())
	}
	return cli.List(cli.ListOptions{ConfigPath: *configPath, Out: stdout})
}

func cmdInspect(args []string, stdout, stderr io.Writer) error {
	fs, configPath := newFlagSet("inspect", stderr)
	name, err := serverArg(fs, args)
	if err != nil {
		return err
	}
	_ = configPath
	return fmt.Errorf("inspect %s: %w", name, errNotImplemented)
}
