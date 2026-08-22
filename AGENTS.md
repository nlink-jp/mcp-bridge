# AGENTS.md — mcp-bridge

## Summary

`mcp-bridge` connects stdio-only MCP clients to Streamable HTTP MCP servers that
require a pre-registered OAuth client (client_id + client_secret + fixed
redirect URI). Its scope is deliberately limited to providers that do not
support RFC 7591 Dynamic Client Registration — Slack's official MCP server,
GitHub Apps, Microsoft Entra ID — because MCP clients handle DCR-capable servers
natively.

Downstream is always stdio; upstream is always Streamable HTTP. The reverse
direction is not supported.

- Module path: `github.com/nlink-jp/mcp-bridge`
- Series: util-series
- Language: Go, standard library only (`go.mod` has zero require lines)
- Status: scaffolded, not implemented. Commands return `not implemented yet`.

## Build and test

```bash
make build        # -> dist/mcp-bridge
make build-all    # cross-compiled binaries -> dist/
make test         # go test ./...
make lint         # go vet + gofmt check
make check        # lint + test + docs-mirror-check
make clean        # rm -rf dist/
```

Never run `go build` directly — it drops a binary in the project root.

## Structure

```
main.go                      Entry point: subcommand dispatch, version, usage
main_test.go                 Pins the CLI surface (see Gotchas)
internal/                    Private packages, added as implementation lands:
                               config/     single-file JSON config, strict decode
                               jsonrpc/    JSON-RPC 2.0 message handling
                               transport/  Streamable HTTP client, OAuth
                               cli/        login / discover / inspect
scripts/docs-mirror-check.sh Verifies docs/en and docs/ja are structural mirrors
docs/en/, docs/ja/           Three-layer docs (adr / reference / history);
                             currently holds the RFP
```

`internal/` does not exist yet — the subdirectories above are the planned layout,
created as each package is written.

## Runtime layout

- Config: `~/.config/mcp-bridge/config.json` (single file, strictly decoded)
- State: `~/.config/mcp-bridge/state/<server>/` holding `tokens.json` (mode
  0600) and `discovery.json`

## Gotchas

- **stdout is reserved for JSON-RPC.** Any other write breaks the MCP
  connection. All diagnostics go to stderr. `run()` takes injected writers so
  tests never touch the real streams.
- **`--version` and the `version` subcommand must produce identical output.**
  A Homebrew formula's `brew test` runs `--version`. `TestVersionFlagAndSubcommandAgree`
  pins the pair.
- **The usage text is pinned to the dispatcher by tests.** Adding a `case` to
  the switch in `run()` without documenting it — or documenting a command that
  is not dispatched — fails `TestEveryDispatchedCommandIsDocumented` and
  `TestEveryDocumentedCommandIsDispatchable`. `dispatchedCommands` in
  `main_test.go` must be updated alongside the switch. This is deliberate: the
  predecessor shipped `--inspect` and `--callback-port` documented nowhere.
- **Zero dependencies is a hard constraint**, not a preference. It is why config
  is JSON rather than the org-standard sectioned TOML.
- **Scope is fixed by the RFP** in `docs/ja/mcp-bridge-rfp.ja.md`. Governance
  gates, audit receipts, telemetry export, tool masking, and the
  client_credentials flow are out of scope by decision.

## Relationship to mcp-guardian

Extracted from `nlink-jp/mcp-guardian`, with the governance, audit, and
telemetry halves dropped (roughly half the source). No code dependency exists in
either direction. Do not modify mcp-guardian from this repository.
