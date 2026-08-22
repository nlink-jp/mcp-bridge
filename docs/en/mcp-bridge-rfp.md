# RFP: mcp-bridge

> Generated: 2026-08-23
> Status: Draft

## 1. Problem Statement

A bridge that lets stdio-only MCP clients (Claude Code, Claude Desktop, and the like) reach Streamable HTTP MCP servers that **do not support RFC 7591 Dynamic Client Registration and instead require a pre-registered OAuth client** (client_id + client_secret + a fixed redirect URI).

Claude Code already handles `type: "http"` MCP servers with DCR-based OAuth on its own, so this tool's scope is deliberately narrowed to the providers that a client's built-in OAuth cannot reach: the official Slack MCP server, GitHub Apps, Microsoft Entra ID, and similar.

The target user is an operator who can register an OAuth app in the provider's own admin console. Distribution to users without that administrative access is not a goal.

This project extracts only the bridging half of mcp-guardian into a new repository. Governance gates, receipt auditing, and telemetry are not carried over.

## 2. Functional Specification

### Commands / API Surface

The CLI is subcommand-based. mcp-guardian switched between seven modes through combinations of fourteen flags (`--tool` / `--outcome` / `--limit` were `--view`-only; `--callback-port` was `--login`-only, all implicitly), and that was the main source of its configuration confusion. Modes become explicit subcommands here.

| Command | Description |
|---|---|
| `mcp-bridge run <name>` | Start the bridge. The MCP client launches this as a child process |
| `mcp-bridge login <name>` | OAuth browser login |
| `mcp-bridge logout <name>` | Delete stored tokens |
| `mcp-bridge list` | List configured servers and their login state |
| `mcp-bridge inspect <name>` | Connect and print serverInfo and the tool list |
| `mcp-bridge version` | Print version |

Flags:

- `--config <path>` (global; overrides the config file path)
- `--callback-port <n>` (`login` only; overrides `callbackPort` from the config)

Two flags in total. `--version` is also accepted as an alias (org rule: every CLI answers `--version`).

### Input / Output

- **Downstream (agent side)**: stdio, fixed. Reads JSON-RPC 2.0, one message per line, from stdin and writes to stdout.
- **Upstream (MCP server side)**: Streamable HTTP only. JSON POST plus an SSE stream.
- **Logging**: stderr only. Writing anything but JSON-RPC to stdout breaks the MCP connection, so this separation is strict.
- **Relay policy**: JSON-RPC messages pass through unmodified. There is no special handling of `initialize`, `tools/list`, or `tools/call` (mcp-guardian did schema caching, meta-tool injection, and masking there; all are dropped).
- **When forwarding upstream fails, the client's request always receives a JSON-RPC error response** (inherited from mcp-guardian ADR-0002). Staying silent makes the client block until its own timeout, hiding the actual reason — such as an expired token that needs a fresh login.

### Configuration

A single file, `~/.config/mcp-bridge/config.json`. The two-tier "system global config + per-server profile" split from mcp-guardian is dropped: once telemetry is gone, nothing is left to put in the global tier.

```json
{
  "servers": {
    "slack": {
      "url": "https://mcp.slack.com/mcp",
      "oauth": {
        "authorizeUrl": "https://slack.com/oauth/v2_user/authorize",
        "tokenUrl": "https://slack.com/api/oauth.v2.user.access",
        "clientId": "<your-client-id>",
        "clientSecret": "<your-client-secret>",
        "scopes": ["chat:write", "channels:history"],
        "callbackPort": 7777,
        "callbackScheme": "https",
        "clientAuthMethod": "post"
      }
    },
    "atlassian": {
      "url": "https://mcp.example.com/v1/mcp",
      "oauth": {}
    },
    "plain": {
      "url": "https://mcp.example.com/mcp"
    },
    "static": {
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer <your-api-key>" }
    },
    "gcp": {
      "url": "https://mcp.example.com/mcp",
      "tokenCommand": { "command": "gcloud", "args": ["auth", "print-access-token"] }
    }
  }
}
```

About twelve configuration keys in total (mcp-guardian had 26 in profiles plus 16 in the global config).

Decisions:

- **JSON, keeping zero external dependencies.** The organization has a sectioned-TOML convention, but a TOML parser adds one external dependency. For a tool that handles OAuth client secrets and access tokens, `encoding/json` with `DisallowUnknownFields` wins. It also matches the MCP ecosystem's own formats (`.mcp.json`, `claude.json`).
- **Strict decoding everywhere.** mcp-guardian was strict for profiles but used a plain `json.Unmarshal` for the global config. Asymmetric behaviour across files of the same kind swallows typos; a single strictly-decoded file removes it.
- **No `transport` key.** mcp-guardian's `"sse"` was actually Streamable HTTP — a name that did not match its content. Upstream is HTTP only, so the key does not exist.
- **No `flow` key.** With client_credentials out of scope, authorization_code is the only option left.
- **`"oauth": {}` (an empty object) means "use DCR auto-discovery."** Omitting the key entirely means no authentication.

State lives in `~/.config/mcp-bridge/state/<name>/` and holds only `tokens.json` (mode 0600) and `discovery.json`. There is no `controller.json`, `authority.json`, or `receipts-*.jsonl`, and no fallback to a `.governance` directory in the working directory.

### External Dependencies

- Go standard library only. `go.mod` keeps zero require lines.
- At runtime, the only external dependencies are the target MCP server and its OAuth authorization server.
- `tokenCommand` additionally requires whatever command the user names (e.g. `gcloud`) to be on PATH.

Supported authentication methods:

| Method | Description |
|---|---|
| None | Omit `oauth`, `headers`, and `tokenCommand` |
| Static header | Put `Authorization` (or similar) directly in `headers` |
| `tokenCommand` | Run an external command to obtain a Bearer token (for short-lived tokens) |
| OAuth2 authorization_code | Always PKCE. Supports both pre-registered confidential clients and DCR |

**client_credentials (M2M) is out of scope** — it has no role in a stdio bridge launched by a local client.

## 3. Design Decisions

**Language and dependencies**

Go, zero external dependencies. The tool handles OAuth client secrets and access tokens, so no supply-chain surface is added. The same reasoning drives the choice of JSON for configuration.

**Fix the direction**

Downstream is always stdio; upstream is Streamable HTTP only. This is not a bidirectional bridge. The README will state plainly that the reverse direction — exposing a stdio MCP server over HTTP — is not supported (a point that was repeatedly misunderstood with mcp-guardian).

**Carried over from mcp-guardian**

- The Streamable HTTP client and OAuth implementation (transport layer)
- JSON-RPC message handling
- `login` / `discover` / `inspect` / self-signed certificate generation
- Three design decisions (ADR-0001 pre-registered OAuth client, ADR-0002 respond on upstream forward failure, ADR-0003 refresh-less tokens are non-expiring). These are re-issued in the mcp-bridge repository rather than referenced, because an ADR belongs in the repository whose scope it binds.

**Explicitly out of scope**

- The five governance gates (budget / schema / constraint / authority / convergence)
- Receipt auditing, the SHA-256 hash chain, and `--view` / `--verify` / `--explain` / `--receipts`
- Meta-tool injection (the five `governance_*` tools)
- Tool masking
- OTLP / Splunk HEC / webhook telemetry export
- The `enforcement` (strict/advisory) and `schema` (off/warn/strict) knobs
- The client_credentials flow
- Audit logging of any kind, including a lightweight JSONL variant

**Relationship to existing tools**

- **mcp-guardian**: the source of the extraction. mcp-bridge does not depend on it; the code is copied and simplified. Turning the bridge into a library that mcp-guardian consumes was rejected: under a zero-dependency policy it creates a permanent two-repository synchronisation cost and ties releases of the parts in use to the parts that are not. What happens to mcp-guardian itself (freeze / archive / keep) is out of scope for this RFP and will be decided separately.
- **slack-mcp-extender**: a different layer. mcp-bridge establishes the connection; the extender extends the tools. Chaining both is possible in principle.

**Observed confusion in mcp-guardian, and how it is resolved**

| Problem in mcp-guardian | Resolution in mcp-bridge |
|---|---|
| Fourteen flags switching between seven modes | Six subcommands plus two flags |
| The profile JSON mixed login-only keys with runtime keys (`authorizeUrl` and `extraParams` never reached `Config`) | One configuration struct; no split in when keys are read |
| `enforcement: "advisory"` still blocked at the budget and schema gates, contradicting the README | Governance removed entirely |
| `transport: "sse"` was actually Streamable HTTP | Key removed |
| The legacy `.governance` path survived in a fallback and in the shipped examples | No fallback is created |
| `mask` only took effect with `enforcement: strict` — two orthogonal concepts coupled | Feature removed entirely |
| Only the global config skipped strict decoding | Single file, strict throughout |
| Error messages pointed at flags that no longer existed | A test pins error messages to the actual flag definitions from the scaffold onward |
| `--inspect` and `--callback-port` appeared in neither README | The Phase 3 documentation check verifies that every subcommand and flag is documented |

## 4. Development Plan

### Phase 1: Core

- JSON config loader (strict decode, single file, `--config` override)
- stdio to Streamable HTTP relay (unmodified JSON-RPC passthrough, error response on upstream forward failure)
- No-auth and static-header authentication
- Subcommands `run`, `list`, `version`
- Unit tests for every package plus an end-to-end test against a mock MCP server

At completion the tool is usable against HTTP MCP servers that need no authentication. Independently reviewable.

### Phase 2: Features

- OAuth2 authorization_code (PKCE, pre-registered confidential client, `clientAuthMethod` post/basic/none)
- https loopback callback (ephemeral self-signed ECDSA P-256 certificate, in memory only)
- RFC 8414 metadata discovery
- RFC 7591 Dynamic Client Registration
- Refresh-less tokens treated as non-expiring (ADR-0003)
- Subcommands `login`, `logout`, `inspect`
- `tokenCommand`

At completion, a real-data end-to-end run against the official Slack MCP server passes. Independently reviewable.

### Phase 3: Release

- README.md and README.ja.md (verified to document every subcommand and flag)
- AGENTS.md (written fresh — never copied from another project)
- CHANGELOG.md
- Three-layer docs structure (`docs/{en,ja}/{adr,reference,history}/`), the three ADRs re-issued, and docs-mirror-check
- Makefile (`make build` into `dist/`, version from `git describe`)
- Code signing, notarization, and the Homebrew tap
- Submodule integration into the util-series umbrella repository
- Organization profile README update
- `check-org.sh` passing

Independently reviewable.

### Where real-data E2E lands

Phase 1 proves an unauthenticated server; Phase 2 proves Slack. The phase boundaries are drawn to coincide with what becomes verifiable.

## 5. Required API Scopes / Permissions

The tool itself requires no scopes of its own; each is defined per target server by the user.

However, **the user must pre-register an OAuth app in the target provider's admin console**:

- **Slack**: declare user token scopes (e.g. `chat:write`, `channels:history`, `channels:read`, `groups:history`, `groups:read`, `im:history`, `im:read`, `mpim:history`, `mpim:read`, `search:read`, `users:read`) and register `https://localhost:<port>/callback` as a Redirect URL
- **GitHub Apps / Microsoft Entra ID**: likewise, a pre-registered client_id, client_secret, and redirect URI

**Setup-burden check** (organizational lesson: whether a tool gets adopted depends on whether its intended user can complete the setup unaided and diagnose it when it breaks):

This tool requires per-user OAuth app registration — the same structure that led to gdrive-collector being cancelled after Phase 1. Here, though, the intended user is the operator themselves, and Slack app registration has already been done successfully. It is therefore not expected to be a drop-off point.

Conversely, **the tool cannot be handed as-is to users without administrative access**. That constraint goes at the top of the README so the intended audience is not misrepresented.

## 6. Series Placement

Series: **util-series**

Reason: a single-purpose CLI that composes with other tools, in the same family as its own source (mcp-guardian) and the other MCP-related tools such as data-toolbox-mcp, splunk-mcp, and chrome-pilot-mcp. It is not an interactive client for one external service (cli-series), not Slack ChatOps automation (chatops-series), and not a security tool (cybersecurity-series). It is intended for production use rather than experimentation, so lab-series does not fit either.

## 7. External Platform Constraints

**Official Slack MCP server**

1. Rejects `http://` loopback redirect URIs at app-registration time. `callbackScheme: "https"` and an ephemeral self-signed certificate are mandatory; the browser shows a one-time "not secure" warning that the user clicks through (this is expected behaviour). The certificate covers 127.0.0.1, ::1, and localhost in its SANs and never leaves memory.
2. The token endpoint is a non-standard path, `oauth.v2.user.access`, which RFC 8414 discovery does not surface. It must be configured by hand.
3. When token rotation is disabled, `refresh_token` comes back empty and no `expires_in` is returned. Such tokens are **treated as non-expiring** (ADR-0003). Inventing an artificial one-hour expiry forces an hourly re-login; actual revocation is detected through a 401 from upstream.
4. The redirect URI needs a fixed port, so `callbackPort` must be set explicitly.

**MCP protocol**

- There is no cancellation notification. The only way to interrupt is to kill the child process.
- A stdio MCP server breaks its connection if anything other than JSON-RPC reaches stdout. All logging goes to stderr.

**Streamable HTTP**

- JSON POST combined with an SSE stream. Sessions are identified by the `Mcp-Session-Id` header.

**Claude Code / MCP clients**

- Claude Code handles `type: "http"` MCP servers with DCR-based OAuth directly. DCR-capable providers are therefore not the primary target here (discovery is retained so they work, but this is not the recommended route). The README states that boundary rather than overstating the tool's reach.

---

## Discussion Log

**Origin** — The problem was raised that mcp-guardian's parameters and configuration had become hard to follow, together with the proposal that "since the governance gateway features are not being used effectively, it would be better to extract the MCP proxy portion into something clearer."

**Measured current state** — Investigation of the implementation and actual usage found:

- Fourteen CLI flags, of which three are used routinely; roughly 26 profile JSON keys, of which five are used; roughly 16 global config keys, of which none are used (the file does not exist)
- Only two profiles defined: `aws` and `slack`
- Not a single receipt file in the state directories — the audit trail is effectively never produced
- Exactly one MCP client wiring, in a throwaway test project; Slack has already been replaced by slack-mcp-extender

What actually delivers value in practice is one thing: connecting a stdio client to an HTTP MCP server that requires a pre-registered OAuth client.

**Root-cause analysis of the confusion** — Nine concrete defects were identified (see the table in §3). Four of them — the advisory-mode misbehaviour, the asymmetric strict decoding, error messages naming non-existent flags, and flags missing from the README — are defects regardless of which direction is chosen.

**Redefining what gets extracted** — What should be extracted is not a *proxy* (in the MITM sense) but a **transport bridge** (stdio to Streamable HTTP plus OAuth). Unless the name states the reality plainly, the same growth happens again. Measured by line count, the bridge side is about 2,600 lines and the discarded side about 2,550 — almost an even split. The heart of what is kept is the OAuth implementation, around 1,500 lines, and it is the most valuable asset in the repository.

**Narrowing the scope** — Because Claude Code handles `type: "http"` with DCR-based OAuth directly, defining this as "a bridge for HTTP MCP servers in general" leaves little value. Restricting it to providers that lack DCR and demand a pre-registered confidential client leaves a narrow but solid purpose.

**Choosing a direction** — Four options were considered:

- A: Reorganize mcp-guardian in place (subcommands, single-tier config, corrected terminology)
- B: New bridge, mcp-guardian frozen
- C: New bridge, with mcp-guardian rebuilt on top of it
- D: Fix the defects only, decide later

The decision was to **build the bridge as a new project**. A was rejected because fixing the nine defects still leaves 26 unused governance configuration keys — the root cause (the sheer volume of unused features) would survive. C was rejected because, under a zero-dependency policy, it creates a permanent synchronisation cost between two repositories.

**Applying past lessons** — mcp-guardian previously suffered rework: wrap/unwrap integration was removed right after it was built; deprecation code for `--server-config` was written and then deleted; inline flags were maintained and then dropped in favour of profile-only configuration. In every case, deciding the final shape first would have made the intermediate work unnecessary. This time the final shape is fixed in this RFP before implementation begins.

**Individual decisions**:

| Question | Decision | Rationale |
|---|---|---|
| Tool name | `mcp-bridge` | States the reality; fits the util-series `<domain>-<role>` convention. Same-named OSS projects exist, but distribution is through the org's own tap, so there is no practical conflict |
| Config format | JSON (zero dependencies preserved) | Handling OAuth secrets argues against adding supply-chain surface. This conflicts with the org's TOML convention; zero dependencies wins |
| Audit logging | None | Stay a pure bridge — not even a lightweight JSONL variant |
| Auth methods | authorization_code / none and static header / tokenCommand | client_credentials has no role in a stdio bridge |
| Discovery | Keep both RFC 8414 metadata and RFC 7591 DCR | So DCR-capable servers can still be managed in one place |
| CLI shape | Six subcommands plus two flags | Mode-switching via flag combinations was the main source of confusion |
| Config file layout | Single file | Once telemetry was dropped, nothing remained for a global tier |
| Fate of mcp-guardian | Out of scope for this RFP | To be decided separately; this project does not depend on it either way |
