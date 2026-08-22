package main

import (
	"bytes"
	"errors"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// call runs the CLI with args and returns (stdout, stderr, err).
func call(args ...string) (string, string, error) {
	var out, errOut bytes.Buffer
	err := run(args, &out, &errOut)
	return out.String(), errOut.String(), err
}

// A Homebrew formula's `brew test` runs `--version`, while humans reach for the
// subcommand. The org convention requires both to exist and to agree, so this
// test pins the pair rather than either one alone.
func TestVersionFlagAndSubcommandAgree(t *testing.T) {
	sub, _, err := call("version")
	if err != nil {
		t.Fatalf("version subcommand: %v", err)
	}
	for _, alias := range []string{"--version", "-version"} {
		got, _, err := call(alias)
		if err != nil {
			t.Fatalf("%s: %v", alias, err)
		}
		if got != sub {
			t.Errorf("%s printed %q, version subcommand printed %q", alias, got, sub)
		}
	}
	if !strings.HasPrefix(sub, "mcp-bridge ") {
		t.Errorf("version output %q does not name the binary", sub)
	}
}

func TestNoArgsPrintsUsageToStderr(t *testing.T) {
	stdout, stderr, err := call()
	if !errors.Is(err, errUsage) {
		t.Fatalf("want errUsage, got %v", err)
	}
	if stdout != "" {
		t.Errorf("usage must not go to stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr does not contain usage: %q", stderr)
	}
}

func TestUnknownCommand(t *testing.T) {
	_, stderr, err := call("frobnicate")
	if !errors.Is(err, errUsage) {
		t.Fatalf("want errUsage, got %v", err)
	}
	if !strings.Contains(stderr, "frobnicate") {
		t.Errorf("stderr does not name the unknown command: %q", stderr)
	}
}

// help is the one place usage belongs on stdout: the user asked for it.
func TestHelpGoesToStdout(t *testing.T) {
	stdout, _, err := call("help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(stdout, "usage:") {
		t.Errorf("help did not print usage: %q", stdout)
	}
}

// mcp-guardian shipped --inspect and --callback-port that appeared in neither
// README, and error messages naming flags that had been deleted. Both are drift
// between what the code accepts and what the text claims. These two tests pin
// the usage text to the dispatcher so the drift cannot open silently.

func TestEveryDocumentedCommandIsDispatchable(t *testing.T) {
	for _, name := range documentedCommands(t) {
		_, stderr, err := call(name, "--help")
		// --help inside a FlagSet returns flag.ErrHelp, which the command
		// bodies map to errUsage. What must not happen is the dispatcher
		// rejecting the name outright.
		if errors.Is(err, errUsage) && strings.Contains(stderr, "unknown command") {
			t.Errorf("command %q is documented in usage but not dispatched", name)
		}
	}
}

func TestEveryDispatchedCommandIsDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, name := range documentedCommands(t) {
		documented[name] = true
	}
	for _, name := range dispatchedCommands {
		if !documented[name] {
			t.Errorf("command %q is dispatched but missing from the usage text", name)
		}
	}
}

// dispatchedCommands mirrors the switch in run(). Adding a case without adding
// it here, or without documenting it, fails the tests above.
var dispatchedCommands = []string{"run", "login", "logout", "list", "inspect", "version"}

// documentedCommands extracts the command names from the usage text.
func documentedCommands(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^  mcp-bridge ([a-z-]+)`)
	matches := re.FindAllStringSubmatch(usageText, -1)
	if len(matches) == 0 {
		t.Fatal("usage text lists no commands — the extraction regexp is stale")
	}
	var names []string
	for _, m := range matches {
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}

// Every documented flag must be registered on at least one command, and the
// error text a command emits must not name a flag that does not exist.
func TestDocumentedFlagsAreRegistered(t *testing.T) {
	for _, tc := range []struct{ command, flag string }{
		{"run", "-config"},
		{"login", "-config"},
		{"login", "-callback-port"},
		{"logout", "-config"},
		{"list", "-config"},
		{"inspect", "-config"},
	} {
		_, stderr, err := call(tc.command, tc.flag+"=x", "dummy")
		// An unregistered flag makes FlagSet report "flag provided but not
		// defined"; anything else means the flag exists.
		if err != nil && strings.Contains(stderr, "not defined") {
			t.Errorf("%s does not accept %s", tc.command, tc.flag)
		}
	}
}

func TestServerNameIsRequired(t *testing.T) {
	for _, name := range []string{"run", "login", "logout", "inspect"} {
		_, _, err := call(name)
		if err == nil {
			t.Errorf("%s without a server name should fail", name)
			continue
		}
		if !strings.Contains(err.Error(), "server name is required") {
			t.Errorf("%s: unhelpful error %v", name, err)
		}
	}
}

func TestListTakesNoArguments(t *testing.T) {
	_, _, err := call("list", "extra")
	if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("list with an argument should be rejected, got %v", err)
	}
}
