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
- Language: Go; the standard library plus `github.com/nlink-jp/mcp` (the
  shared MCP protocol layer, itself standard library only; ADR-0008)
- Status: released. Verified against the live Slack MCP server (pre-registered
  OAuth) and the live GitHub MCP server (token command). The pre-registered
  OAuth route for GitHub Apps is untested against a live endpoint. Entra ID is
  not the same shape as Slack (ADR-0006). A user ran the Entra ID route against
  a Microsoft Agent 365 server in their tenant (login, tool listing, token
  renewal); the maintainers have no tenant, so that run cannot be repeated
  here. What the server answered is in section 6 of
  `docs/en/history/v0.3.0-design.md`.

## Build and test

```bash
make build        # -> dist/mcp-bridge
make build-all    # cross-compiled binaries -> dist/
make test         # go test ./...
make test-linux   # same suite on Linux (container)
make lint         # go vet + gofmt check
make check        # lint + test + docs-mirror-check
make clean        # rm -rf dist/
make vuln            # govulncheck over the build; `make package` runs it first and stops on a finding
make verify-release  # gate: notarized, fresh, runs at this version, clean linux archives (run before upload)
```

Never run `go build` directly — it drops a binary in the project root.

## Structure

```
main.go                      Entry point: subcommand dispatch, version, usage
main_test.go                 Pins the CLI surface (see Gotchas)
internal/config/             Single-file JSON config, strict decode, path resolution
internal/jsonrpc/            JSON-RPC 2.0 shapes and error-response building
internal/transport/          Streamable HTTP client (OAuth token provider hook;
                             MCP headers, Tracker, Classify, HeaderMismatch
                             retry; a client.Conn for inspect)
internal/bridge/             stdio relay: the client's era passes through
                             (lines through nlink-jp/mcp/wire)
internal/oauth/              authorization_code + PKCE, discovery, DCR, token store
internal/tokencmd/           Bearer tokens from an external command
internal/cli/                Subcommand bodies (run, list, login, logout, inspect)
scripts/docs-mirror-check.sh Verifies docs/en and docs/ja are structural mirrors
docs/en/, docs/ja/           Three-layer docs (adr / reference / history);
                             holds the RFP, the ADRs, the Slack, GitHub and
                             Entra ID setup guides under reference/, and
                             the design notes of releases under history/
```

## Runtime layout

- Config: `~/.config/mcp-bridge/config.json` (single file, strictly decoded).
  `XDG_CONFIG_HOME` overrides the base directory, which is how tests stay off
  the developer's real configuration and token files.
- State: `~/.config/mcp-bridge/state/<server>/` holding `tokens.json` (mode
  0600) and `discovery.json`

## Gotchas

- **`inspect` tolerates a missing login; `run` does not.** The split is
  deliberate: `inspect` exists to check a configuration before the login has
  happened, while `run` is launched by an MCP client that cannot show a
  per-request failure legibly. `buildTransportOptions`'s `allowMissingLogin`
  flag is what separates them.
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
- **`tlsCertSha256` replaces verification, it does not add to it.** The
  `InsecureSkipVerify` in `internal/transport/certpin.go` is paired with a
  `VerifyConnection` that requires an exact match; never set one without the
  other. Anything that opens a connection to the MCP endpoint must take
  `connectionOptions(srv)`, including `inspect`'s unauthenticated probe
  (`TestInspectProbeUsesThePin`).
- **`headers` is additive, not an alternative.** Only `oauth` and
  `tokenCommand` are mutually exclusive; `headers` accompanies either and is
  applied *after* the token provider, so a header named `Authorization`
  overrides it. Servers that take options as request headers depend on this —
  the GitHub MCP server's `X-MCP-Toolsets` / `X-MCP-Exclude-Tools` reach an
  authenticated connection that way. `TestHeadersAccompanyATokenCommand` pins
  it; the authentication table in the README reads as exclusive at a glance,
  which is why the note is there.
- **An `oauth` block has three shapes**, told apart by keys and not by a mode:
  empty (discover and register), `clientId` with both URLs (nothing
  discovered), `clientId` with neither URL (endpoints and scopes discovered,
  nothing registered, no `clientSecret` allowed). `AuthMode` shows the last
  two alike, as `oauth`; `OAuth.DiscoversEndpoints` tells them apart.
- **Three things the MCP specification asks for are left out on purpose**
  (ADR-0006): the RFC 8707 `resource` parameter, refusing an authorization
  server that does not advertise PKCE support, and checking the metadata's
  `issuer`. Each would stop Microsoft Entra ID from connecting or cannot be
  tested without a tenant. The login that worked against Agent 365 was made
  with all three left out, and there is no tenant here to try a change on, so
  do not "fix" them toward the specification.
  `TestLoginWithAPreRegisteredClientAndDiscoveredEndpoints` and the
  `entraMetadata` fixture pin the Entra-shaped case built from published
  documents; `TestLoginAgainstWhatAgent365Publishes` pins what the real server
  was observed to answer.
- **Discovery trusts nothing the server hands it by default.** A discovered URL
  must be `https` unless the configured `url` is `http`
  (`checkDiscoveredURL`); protected-resource metadata must describe the
  configured `url` (`resourceMatches`); the OAuth HTTP client refuses a
  redirect off `https` (`refuseDowngrade`). A new request in the login or the
  refresh goes through `defaultHTTPClient` or a client with the same rule.
- **The discovery probe writes no MCP message** (ADR-0009). `probeChallenge`
  runs `client.Open` over `probeConn` (`internal/oauth/probe.go`), which makes
  one POST and returns `errProbeAnswered` whatever came of it. One error for
  every outcome matters: `client.Open` reads a deadline error from `Send` as
  "no answer" and falls back to `initialize`. Later `Send`s are refused;
  `probeChallenge` closes the Conn, because `Open` returns no `Client` on error.
  Do not put a hand-written body back.
- **Text from a server reaches the terminal only through `cli.Printable`.**
  `login` and `inspect` wrap their writer with `printable`, and `main` filters
  the final error. A command that starts printing what a server said wraps its
  writer the same way; do not escape at each `Fprintf`.
- **`WWW-Authenticate` is parsed, not searched.** `bearerParam` reads the
  header by its grammar (`internal/oauth/challenge.go`). Looking for
  `scope="` in the string finds it inside another parameter's quoted text.
- **No third-party dependency is a hard constraint**, not a preference. It is
  why config is JSON rather than the org-standard sectioned TOML. The one
  require line is nlink-jp/mcp, an organization module under the same rule.
- **The MCP protocol headers come from the body, never from the config.**
  `httpwire.Tracker.Outgoing` derives `MCP-Protocol-Version`, `Mcp-Method`,
  `Mcp-Name` and `Mcp-Param-*` for a modern request, and the negotiated
  `MCP-Protocol-Version` for every legacy message after `initialize` (and the
  session's DELETE), after the static headers; the
  config refuses those names (ADR-0008 §4). A request whose headers cannot be
  derived is answered with -32602 and not sent; a batch goes without them.
- **Every answer passes through the Tracker before delivery** — a JSON body and
  each SSE message — or the negotiated version and the tools' marks are not
  learned and the next request goes out without its headers.
- **Ids are compared by value, and written without HTML escaping**
  (`jsonrpc.CanonicalID`, `jsonrpc.MarshalPlain`). `json.Marshal` escapes
  `<` `>` `&`, so an id compared by its bytes, or re-encoded with it, stops
  matching its request.
- **The HeaderMismatch retry is the only request mcp-bridge originates**
  (`internal/transport/mismatch.go`): once per request, refreshes serialized,
  up to 10 pages, answers kept from the client. Do not add another.
- **Answers that would leave a request waiting are answered**: a 2xx body that
  is not JSON, and a JSON body, SSE line or SSE event over 8 MiB, get -32603
  under the request's id. mcp-bridge has no per-request timeout.
- **Scope is fixed by the RFP** in `docs/ja/mcp-bridge-rfp.ja.md`. Governance
  gates, audit receipts, telemetry export, tool masking, and the
  client_credentials flow are out of scope by decision. Removing the tools the
  MCP specification says must be excluded (invalid `x-mcp-header` marks) is
  not masking.
- **An expiry of zero means "no known expiry", not "expired at the epoch".**
  A token with no refresh token is used as stored whatever its expiry says;
  the server is the judge, via a 401. Treating zero as expired makes Slack
  demand a fresh login every hour.
- **`tokens.json` is the authoritative copy; a `Provider` holds a copy of it**
  (ADR-0007). Several `run` processes for one server share the file. Whenever
  the copy will not do, `decideUnderLock` reads the file again under
  `tokens.json.lock` and decides from it — never renew from memory without
  going through it, or a second process presents a spent refresh token and a
  server with reuse detection kills the login. The one exception is
  `unsavedFrom` (this process's renewal could not be saved). The lock file is
  never deleted. A lock held past `lockWait` fails the request instead of
  renewing unlocked. The tests stand several `Provider`s in for processes
  (separate opens exclude each other), and `TestRealProcessesRenewOnce` re-runs
  the test binary as children through `TestMain`; replacing the lock with a
  no-op makes it and `TestSixProcessesRenewOnce` fail, which is how they were
  checked.
- **Outgoing stdio messages are compacted.** A server that pretty-prints its
  JSON would otherwise split one message across several lines and
  desynchronise the client for the rest of the session.

## Relationship to mcp-guardian

Extracted from `nlink-jp/mcp-guardian`, with the governance, audit, and
telemetry halves dropped (roughly half the source). No code dependency exists in
either direction. Do not modify mcp-guardian from this repository.
