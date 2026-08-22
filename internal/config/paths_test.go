package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathsHonourXDGConfigHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	got, err := DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "mcp-bridge", "config.json")
	if got != want {
		t.Errorf("DefaultConfigPath = %q, want %q", got, want)
	}
}

func TestResolvePathPrefersOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := ResolvePath("/somewhere/else.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/somewhere/else.json" {
		t.Errorf("ResolvePath ignored the override: %q", got)
	}
}

// A server name reaches the filesystem as a path element. Traversal must be
// impossible even if validation upstream is ever bypassed.
func TestStateDirRejectsTraversal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"..", "../escape", "a/b", ""} {
		if _, err := StateDir(name); err == nil {
			t.Errorf("StateDir(%q) was accepted", name)
		}
	}
}

func TestStateDirLayout(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	dir, err := StateDir("slack")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "mcp-bridge", "state", "slack"); dir != want {
		t.Errorf("StateDir = %q, want %q", dir, want)
	}

	tokens, err := TokensPath("slack")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(tokens, filepath.Join("state", "slack", "tokens.json")) {
		t.Errorf("TokensPath = %q", tokens)
	}

	discovery, err := DiscoveryPath("slack")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(discovery, filepath.Join("state", "slack", "discovery.json")) {
		t.Errorf("DiscoveryPath = %q", discovery)
	}
}

// The state directory holds OAuth tokens, so it must not be world-readable.
func TestEnsureStateDirIsPrivate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := EnsureStateDir("slack")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir mode = %o, want 700", perm)
	}
}
