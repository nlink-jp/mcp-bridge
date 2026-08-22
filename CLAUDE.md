# CLAUDE.md — mcp-bridge

Project-specific rules. The organization rules at
<https://github.com/nlink-jp/.github/blob/main/CONVENTIONS.md> apply on top of
these and take precedence where they overlap.

## What this project is

A bridge from stdio MCP clients to Streamable HTTP MCP servers that require a
pre-registered OAuth client. It is deliberately narrow. Before adding anything,
read `docs/ja/mcp-bridge-rfp.ja.md` — it fixes the final shape and lists what is
out of scope.

## Non-negotiable rules

- **Zero external dependencies.** `go.mod` must have zero require lines. This
  tool handles OAuth client secrets and access tokens; adding a third-party
  module is a supply-chain decision, not a convenience decision. It is why the
  config format is JSON and not TOML, against the org-wide TOML convention.
- **stdout carries JSON-RPC and nothing else.** Every log line, warning, and
  diagnostic goes to stderr. A stray `fmt.Println` breaks every MCP client that
  connects.
- **Never widen the scope silently.** The following are out of scope by
  decision, not by omission: governance gates, audit receipts, hash chains,
  meta-tool injection, tool masking, OTLP / Splunk / webhook telemetry, the
  client_credentials flow, and any reverse (HTTP-to-stdio-server) direction. If
  one of these looks necessary, raise it before writing code.
- **The config is one file with one shape.** `~/.config/mcp-bridge/config.json`,
  strictly decoded. Do not add a second tier, a per-server file, or a key that
  is read at one time by one command and ignored elsewhere — that combination is
  exactly what made the predecessor unreadable.
- **JSON-RPC passes through unmodified.** Do not add special handling for
  `initialize`, `tools/list`, or `tools/call`.
- **On upstream forward failure, always answer the client** with a JSON-RPC
  error. Silence makes the client block until its own timeout and hides the real
  cause (usually an expired token).

## Documentation rules

- `README.md` and `README.ja.md` change in the same commit as behaviour.
- Every subcommand and every flag must appear in both READMEs. `make check`
  runs the tests that pin the CLI surface to the usage text; keep them passing
  rather than deleting them.
- `docs/en/` and `docs/ja/` are full structural mirrors, enforced by
  `scripts/docs-mirror-check.sh`.
- ADRs live in this repository, not in the predecessor's. An ADR belongs where
  its scope binds.

## Predecessor

mcp-bridge is extracted from `nlink-jp/mcp-guardian` but **does not depend on
it**. Code is copied and simplified. Do not edit mcp-guardian from this project;
its fate (freeze / archive / keep) is an open decision held by the operator.
