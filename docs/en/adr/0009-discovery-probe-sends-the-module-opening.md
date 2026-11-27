# ADR-0009: The OAuth discovery probe sends the opening nlink-jp/mcp builds, not a hand-written `initialize`

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-27 |
| Binds | mcp-bridge |
| Supersedes | ADR-0008 §10 |
| Decision makers | nlink-jp maintainers |
| Triggered by | Organization ADR-030 §6: no repository outside nlink-jp/mcp handles `initialize` or `server/discover` itself. A survey on 2026-11-27 found the discovery probe's body to be the only hand-written `initialize` left outside test code in the organization |

## Context

Discovery (ADR-0006) starts with an unauthenticated POST to the configured server and reads the 401's
`WWW-Authenticate` for `resource_metadata` and `scope` (`probeChallenge` in `internal/oauth/discovery.go`). The
body is written by hand:

```json
{"jsonrpc":"2.0","id":"discovery","method":"initialize","params":{}}
```

It carries no MCP protocol headers and is not even a valid `initialize` (no `protocolVersion`,
`capabilities` or `clientInfo`). ADR-0008 §10 kept it on the ground that it never reaches MCP processing, and
left it to `.github` to exempt it when ADR-030 §6's check is written. That leaves a hand-written handshake in
the one repository whose rule (CLAUDE.md, ADR-0008 §2) is that it writes none, and asks the organization owner
for an exemption that, as measured below, is not needed.

### Measurement (2026-11-27)

Each registered upstream, and Slack's MCP server, got the same five unauthenticated requests. Nothing else was
sent: no `Authorization`, no configured static header.

| Upstream | `initialize` as today | `ping`, no MCP headers | POST, no body | GET `text/event-stream` | the opening `client.Open` sends |
|---|---|---|---|---|---|
| GitHub hosted MCP | 401, `resource_metadata` | same | same | same | same |
| Slack MCP | 401, `resource_metadata` | same | same | same | same |
| Obsidian (local) | 401, no challenge | 401 | 401 | **405** | 401 |
| NanoKVM (LAN) | 401, no challenge | 401 | 401 | 401 | 401 |

- On GitHub and Slack the `WWW-Authenticate` value was byte-for-byte the same for all five requests. Neither
  sends `scope` in it today.
- All four authenticate before they look at the body. Obsidian routes on the HTTP method before it
  authenticates.
- Obsidian and NanoKVM are configured with a static `Authorization` header, not OAuth; discovery does not run
  against them. They are in the table because they are registered upstreams.
- The opening `client.Open` (nlink-jp/mcp v0.2.0) sent was one `server/discover` with
  `_meta.io.modelcontextprotocol/protocolVersion` `2026-07-28`, `clientInfo` and `clientCapabilities`, and the
  headers `MCP-Protocol-Version: 2026-07-28` and `Mcp-Method: server/discover` from
  `httpwire.RequestHeaders`. A `Send` that fails ends `Open` without another message (measured; it is what
  `client.open` does with a non-deadline error).
- The measuring program named itself `discovery-probe` (not `mcp-bridge`/`discovery`) and used the module's
  default 5 s `ProbeTimeout`; neither matters to a server that answers 401 before reading the body.

## Decision

### 1. The probe is the opening of an ordinary client, built by nlink-jp/mcp

`probeChallenge` no longer writes a body. It runs `client.Open` over a one-shot `client.Conn` (`probeConn`, in
`internal/oauth`) and reads the answer to the first message the module sends. This is what the MCP
authorization specification describes — the client makes its ordinary request without a token and the server
answers 401 — and what mcp-bridge's other client, `inspect`, already does (ADR-0008 §9).

`probeConn`:

- **`Send` POSTs the message once and keeps the outcome.** The request is built with
  `http.NewRequestWithContext(ctx, …)` over a `bytes.Reader` (so a 307/308 to https is still followed and
  resent, as today). It carries `Content-Type: application/json`, `Accept: application/json, text/event-stream`
  and the headers `httpwire.RequestHeaders(msg, "", nil)` derives, and goes through discovery's own
  `http.Client` (the downgrade refusal, the redirect limit and the 30 s timeout stay; discovery has no
  certificate pin — the config refuses `tlsCertSha256` with `oauth`, ADR-0005). No credential and no static
  header is sent, as today. On an answer it reads at most `maxMetadataBytes` of the body, discards it, and
  records the status and the `WWW-Authenticate` values. On a transport error it records the error. Either way
  `Send` returns `errProbeAnswered`, so `Open` ends there.
- **`probeChallenge` reads the record, not `Open`'s error.** `reached` and the error come from what the first
  `Send` recorded: an answer is `reached`; a transport error is not, except a redirect this client refused
  (as today). A failure before any request goes out (a `RequestHeaders` error, a request that cannot be built)
  is an error of mcp-bridge, reported as such, not as "could not reach the server".
- **Only one `Send` makes a request.** A later `Send` returns an error without a request. This is not only a
  guard for a later module version: `client.Open` treats a `Send` error that is a deadline
  (`errors.Is(err, context.DeadlineExceeded)`, which `http.Client`'s timeout satisfies) as "no answer" and
  falls back to `initialize`. Returning `errProbeAnswered` for every outcome avoids that path, and the refusal
  holds the one-POST rule if it is ever taken.
- **`Recv` blocks until `Close`, then returns `io.EOF`** — at once if `Close` came first. `Open` starts its
  read and answer loops and, on error, returns no `Client` to close, so **`probeChallenge` closes the
  `probeConn` itself** (`defer`) and the loops end.
- `Open` gets `Client: {Name: "mcp-bridge", Version: "discovery"}` (the role in the version, as `inspect`
  does) and a `ProbeTimeout` of 30 s, the same as discovery's `http.Client`, so the wait is bounded as today.
  `Open` as a whole runs under a context with the same bound, so a later request, were one ever sent, would
  not wait without end either.

What the answer means is unchanged: a 401 with `resource_metadata` leads to the protected resource metadata;
any other status, or a 401 without it, is one attempt to report, and discovery goes on to the well-known
locations (ADR-0006).

### 2. ADR-0008 §10 is withdrawn

No exemption is asked of ADR-030 §6 for production code: no package of mcp-bridge writes `initialize` or
`server/discover`. mcp-bridge's tests hold upstreams that stand in for a peer and answer `initialize` (the
`inspect`, certificate-pin and era-matrix tests) or check a `server/discover` (this record's tests); what the
check lists for test code is `.github`'s to decide when the check is written (ADR-025), not this record's.

### 3. What does not change

The order of discovery, the scopes, the URL rules, the redirect rule, the config, the CLI and `go.mod` (the
module, `client` and `httpwire` included, is already required at v0.2.0).

## Alternatives considered

- **`ping`.** Not a handshake, so ADR-030 §6 would not catch it, but still a hand-written JSON-RPC request;
  in the modern era it needs the headers too, and in the legacy era it is the one request allowed before
  `initialize`, which says nothing about what a server checks before authenticating. Rejected: it moves the
  hand-writing rather than removing it.
- **A POST with no body.** No MCP text at all, and 401 on all four. Rejected: it is not an MCP request, and a
  server that validates the request before authenticating answers 400 where an ordinary client's request
  would reach its authentication.
- **GET.** Obsidian answered 405 before authenticating, and a modern-era server keeps no GET stream.
  Rejected on the measurement.
- **Keep the body and ask for an exemption** (ADR-0008 §10). Rejected: the organization owner would be asked
  to exempt something the module already covers.

## Consequences

- No hand-written handshake remains in mcp-bridge.
- A modern-only server that checks the protocol headers before authenticating now gets a valid request; today's
  body carries none. Whether such a server exists is **not measured** — none of the four does.
- A server that authorizes per method — some answer `initialize` and `tools/list` without a token (the Google
  Workspace servers, `internal/cli/auth.go`) — may answer `server/discover` without a 401 where it answered
  today's `initialize` with one, or the reverse. Microsoft Agent 365, the other OAuth upstream on record
  (answering `initialize` with 401 and `resource_metadata`, `docs/en/history/v0.3.0-design.md` §6), has
  **not been measured** with `server/discover`, and no tenant is at hand to measure it. When the 401 is
  missing, discovery goes on to the well-known locations, as for any answer without a challenge. For all three
  OAuth servers on record the first candidate there (RFC 9728's path-inserted location) is the very URL their
  challenge names, so they would still be found; what would be lost is the challenge's `scope`, which none of
  them sends. No handling is added for this.
- A legacy-only server that reads the method before authenticating may answer `server/discover` with an error
  rather than 401, with the same result. Today's `initialize` with empty `params` is no more valid to such a
  server. **Not measured.**
- The unauthenticated server learns the client's name (`mcp-bridge`) and the role (`discovery`). Today it
  learns nothing. Neither carries anything about the user.

## Verification

What decides the expected results: the module's opening contract (`client.Open` sends `server/discover`
first; a `Send` error that is not a deadline ends it), and the MCP authorization specification's flow (an
ordinary request without a token, answered with 401 and `WWW-Authenticate`).

- **Unit (`internal/oauth`)**: with an httptest upstream answering 401 and a challenge, the probe sends exactly
  one POST; its body is `server/discover` with protocol version `2026-07-28` in `_meta`; it carries
  `MCP-Protocol-Version` and `Mcp-Method` and no `Authorization`; discovery reads the challenge as before. An
  upstream answering 200 with a `server/discover` result gets one POST too, and discovery goes on to the
  well-known locations. An upstream that stalls past a shortened timeout gets one POST, and the error is the
  timeout with `reached` false. A second `Send` on `probeConn` sends nothing; `Recv` returns `io.EOF` after
  `Close`, also when `Close` came first, and the probe leaves no goroutine of `Open` running. The existing
  discovery tests (redirects, resource mismatch, scopes) pass unchanged.
- **Real upstreams**: before release, the built binary's `login` against Slack's MCP server up to the
  authorization URL (the challenge is read and the authorization server found).

## References

- ADR-0006 (discovery with a pre-registered client), ADR-0008 §2, §9 and §10 (this record supersedes §10)
- Organization ADR-025, ADR-029 and ADR-030 §6
- nlink-jp/mcp v0.2.0: `client.Open`, `client.Conn`, `httpwire.RequestHeaders`
- MCP specification 2026-07-28: Authorization; Streamable HTTP (protocol headers)
- RFC 9728 (OAuth 2.0 Protected Resource Metadata)
