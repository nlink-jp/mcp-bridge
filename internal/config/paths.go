package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// appDir is the directory that holds config.json and the per-server state.
//
// XDG_CONFIG_HOME is honoured when set — it is what tests use, and what a
// user who relocates their config expects. Otherwise ~/.config, on macOS as
// well as Linux: the org's tools keep their configuration there rather than
// in ~/Library/Application Support, so one habit covers every tool.
func appDir() (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "mcp-bridge"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "mcp-bridge"), nil
}

// DefaultConfigPath returns the path used when --config is not given.
func DefaultConfigPath() (string, error) {
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// ResolvePath returns override if it is non-empty, else the default path.
func ResolvePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return DefaultConfigPath()
}

// StateDir returns the directory holding one server's tokens and discovery
// results. The name is validated first: it becomes a path element, and an
// unchecked name would place token files outside the state tree.
func StateDir(serverName string) (string, error) {
	if err := validateName(serverName); err != nil {
		return "", err
	}
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state", serverName), nil
}

// EnsureStateDir creates a server's state directory if needed. It is created
// 0700 because it holds OAuth tokens.
func EnsureStateDir(serverName string) (string, error) {
	dir, err := StateDir(serverName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	return dir, nil
}

// TokensPath returns the file holding a server's OAuth tokens.
func TokensPath(serverName string) (string, error) {
	dir, err := StateDir(serverName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokens.json"), nil
}

// DiscoveryPath returns the file caching a server's discovered OAuth endpoints
// and dynamically registered client.
func DiscoveryPath(serverName string) (string, error) {
	dir, err := StateDir(serverName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "discovery.json"), nil
}
