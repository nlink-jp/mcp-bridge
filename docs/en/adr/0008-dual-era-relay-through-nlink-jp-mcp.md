# ADR-0008: Pass both eras through unchanged — add, through nlink-jp/mcp, only what the specification requires of an HTTP client

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-27 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | Organization ADR-030 (dual-era support for MCP 2026-07-28, relays first; §1 and §2 amended on 2026-11-27 to match this design) and the release of the shared module nlink-jp/mcp v0.1.0. A measurement on 2026-11-27 showed that the connection to GitHub through mcp-bridge works only because of the client's leniency |

## Context

### mcp-bridge today

mcp-bridge connects a stdio client to a Streamable HTTP upstream. It does not read message bodies, rewrite
them, or answer anything locally (the contract of `internal/bridge`; CLAUDE.md "JSON-RPC passes through
unmodified"). A POST to the upstream carries only `Content-Type`, `Accept`, `Mcp-Session-Id` (when the
upstream issued one), `Authorization` and the configured static headers (`internal/transport/streamhttp.go`).
`go.mod` has zero require lines (CLAUDE.md).

### What is missing (specification, measurement, code)

1. **The required header is not sent even in the legacy era.** Since 2025-06-18 an HTTP client must carry
   the negotiated version in `MCP-Protocol-Version`. Requests are served today only because a server
   **MAY** treat a request without the header as 2025-03-26 (2026-07-28 `streamable-http` §Protocol Version
   Header; confirmed in the code).
2. **Modern requests cannot carry the required headers.** A 2026-07-28 HTTP client **MUST** derive
   `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` from the body, and mirror every argument a tool's schema
   marks with `x-mcp-header` into `Mcp-Param-{Name}`. It **MUST** exclude tools whose marks are invalid.
   GitHub's tools already mark `owner` and `repo` (measured).
3. **It rests on the client's leniency.** On 2026-11-27 Claude Code 2.1.311 was connected to GitHub through
   mcp-bridge with every line recorded. The client's `server/discover` reached the upstream without the headers
   and GitHub answered with the modern error `-32020`. By the specification, a dual-era client that receives a
   modern error does **not** fall back to the legacy era. Claude Code fell back and worked; a client that follows
   the specification gets no GitHub tools.
4. **One over-long line ends the relay.** Client lines are read with `bufio.Scanner` (limit 8 MiB). A line
   over the limit leaves the read loop, closes the upstream and exits (confirmed in the code).
5. **A large upstream answer leaves a request unanswered.** A 2xx JSON body is silently cut at 8 MiB and then
   dropped as malformed JSON. An SSE line over 8 MiB silently ends the stream, and the size of an event's joined
   data lines has no bound. Either way the request's answer never arrives, and mcp-bridge has no per-request
   timeout (confirmed in the code).

The peers (measured again on 2026-11-27, versions included, recorded): GitHub (`github-mcp-server`, the remote
build) and Obsidian (generic 2.4.0) are dual-era; NanoKVM (nanokvm-cube-remote-control v1.0.0) is legacy only.
Claude Code 2.1.311 is dual-era.

## Decision

### 1. nlink-jp/mcp is an allowed dependency

The rule becomes "the standard library and nlink-jp modules that hold the same rule (standard library only)"
— the wording slack-mcp-extender uses. The only require line is `github.com/nlink-jp/mcp`. The rule's purpose,
keeping third-party modules out, is unchanged.

### 2. The era passes through; it is not converted

The upstream sees the era the client opened. mcp-bridge answers neither `initialize` nor `server/discover`
itself (ADR-030 §1 as amended). The relay is dual-era whenever its client is. A client that speaks only the
legacy era reaches a modern-only upstream after its own migration.

### 3. Bodies are changed in exactly three ways

- **Tools whose `x-mcp-header` marks are invalid are removed from modern `tools/list` results** (a MUST of
  the specification; `httpwire.Tracker.Incoming`; tools beyond 5000 are removed too). The tool names and
  reasons go to stderr. This is not the "tool masking" CLAUDE.md puts out of scope: no tool is chosen by
  policy; only definitions the specification tells an HTTP client to exclude are removed.
- **A failure the upstream did not answer as JSON-RPC is answered with a synthesized `-32603`** (as today;
  ADR-0002). When a non-2xx JSON-RPC error body has no `id`, a `null` one, or a different one, the request's
  `id` is put in its place (`httpwire.Classify`). A 2xx `application/json` body that is not valid JSON also gets
  a `-32603` under the request's `id` (`wire.Writer` refuses malformed JSON, so without it the request would go
  unanswered).
- **An answer that is too large is answered with a synthesized `-32603`** (§5; fixes Context 5).

Every other body passes through with its content unchanged (compacted onto one line, as today).

### 4. Headers are derived from the body

Every POST to the upstream carries the headers `httpwire.Tracker.Outgoing` returns.

- Legacy messages: `MCP-Protocol-Version` set to the version `initialize` negotiated (fixes Context 1). Not on
  `initialize` itself.
- Modern messages: `MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name` and `Mcp-Param-*` (omitted when the value
  is absent or null; the Base64 sentinel when the value cannot be sent plainly).

When `Outgoing` returns an error:

- **A batch (an array body)**: sent as today, without protocol headers (batches belong to 2025-03-26 only),
  and recorded on stderr.
- **A marked argument that cannot be mirrored into a header** (an object, an array, a fractional number, …)
  or **a `tools/call` without `params.name`**: not sent upstream; the request is answered with `-32602`
  ("argument \"<name>\" cannot be sent as a header", and so on; the value is never put in the message).

The protocol headers are applied after the configured static headers. A configured `headers` entry named
`MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name`, or starting with `Mcp-Param-` is refused when the config is
read (case-insensitively; ADR-0004's strict config).

### 5. Every response passes through the Tracker before delivery

- A 2xx JSON body, and the body of each SSE `message` event (each member after a batch is opened), goes
  through `Tracker.Incoming` before delivery. The version is learned from the `initialize` result; marks from
  modern `tools/list` results.
- A non-2xx response is judged by `httpwire.Classify` (Deliver, Synthesized, Drop). The 401 retry stays as it is
  and happens first.
- **Bounds**: a 2xx JSON body up to 8 MiB; beyond that the request's `id` gets `-32603` "upstream response
  larger than 8388608 bytes" (today it is silently cut). SSE: 8 MiB per line (as today) and also 8 MiB for one
  event's joined data lines. When either is exceeded, reading that stream stops, and if the request that opened
  it has not been answered yet, its `id` gets the same `-32603`.

### 6. A HeaderMismatch is retried once, inside the transport

**When**: the request mcp-bridge sent is a `tools/call` to which `Outgoing` added `Mcp-Method` (a modern one),
and its answer is HTTP 400 with a JSON-RPC `error.code` of `-32020`. mcp-bridge decides this by reading its own
request and the answer's body (it does not use `Classify`'s `Modern`, which covers several codes at once).

1. mcp-bridge sends a `tools/list` of its own to the upstream. Its `id` is `"mcp-bridge-refresh-<n>"` (a
   string). Its `_meta` copies `io.modelcontextprotocol/protocolVersion`, `…/clientCapabilities` and
   `…/clientInfo` from the original request. It follows `nextCursor` for up to 10 pages. The answers only feed
   `Tracker.Incoming`; they are not delivered to the client.
2. The original request is sent once more, with headers derived again. Its answer is delivered as it is.
3. If the refresh fails (an error, the page limit, no answer), the original request is not resent and the first
   `-32020` is delivered. If it fails after the first page, the `Tracker` holds the marks of the pages read; a
   call to a tool on a later page gets `-32020` again, and that triggers a refresh.

One retry per request; refreshes are serialized. This is the only request mcp-bridge originates.

### 7. Client lines are read and written with nlink-jp/mcp/wire

The limit stays at 8 MiB (`wire.NewReader(in, 8<<20)`). An over-long line is skipped and reading continues;
its `id` cannot be read, so mcp-bridge answers `-32700` (`id: null`, "message longer than 8388608 bytes") and
records it on stderr (fixes Context 4). Writes go through `wire.Writer` (one write, serialized). The answer to
malformed JSON is as today (`-32700`, `id: null`).

### 8. What does not change

- Sessions: `Mcp-Session-Id` is sent only when the upstream issued one, and DELETE is sent at shutdown (legacy
  upstreams). Modern upstreams issue none.
- No GET stream is opened (none is today).
- The CLI other than `inspect`, OAuth, and the shape of the config (apart from §4's refusal).

### 9. `inspect` opens through nlink-jp/mcp/client

`inspect` stops sending its own `initialize`, in both places.

- **The main path**: `client.Open` over a `client.Conn` on HTTP. mcp-bridge writes the `Conn` (nlink-jp/mcp
  detailed design §5.5): `Send` POSTs with `Tracker.Outgoing`'s headers; a response goes through `Classify` →
  `Tracker.Incoming` to `Recv` (each SSE event; nothing for Accepted and Drop). `inspect` opens in the modern or
  legacy era, as the upstream allows (reading the `400` body to decide is `client`'s job), and its output gains
  the era and the version.
- **Whether the server answers without a credential** (`serverAnswersUnauthenticated`): the same `Conn`, built
  without a credential, and judged by whether `client.Open` succeeds. Against a modern-only upstream it no
  longer reports "credential accepted" without having tested the credential.

### 10. The OAuth discovery POST stays

The `initialize` POST in `internal/oauth/discovery.go` is an authentication probe for a 401 and its
`WWW-Authenticate`; it never reaches MCP processing. Its body stays. So that ADR-030 §6's check does not read it
as a hand-written handshake, `.github` is told through an issue when that check is written (ADR-025; not a step
of this record). Whether a modern-only upstream checks the body before authentication and answers 400 is
**not measured**.

## Detailed design

| Component | Change | Oracle (where expected results come from) |
|---|---|---|
| `internal/bridge` | `bufio.Scanner` → `wire.Reader` (8 MiB); writes → `wire.Writer`. `-32700` for an over-long line | wire's contract (module detailed design §3.1) and §7 |
| `internal/transport` (`streamhttp.go`) | Holds a `Tracker`. `post` adds `Outgoing`'s headers (after the static ones) and handles §4's errors. `route` sends non-2xx to `Classify`, and 2xx bodies and each SSE message to `Incoming`. The synthesized `-32603` and bounds of §3 and §5. The §6 retry | Specification streamable-http §Protocol Version Header, §Standard Request Headers, §Custom Headers, §Server Validation, §Backward Compatibility |
| `internal/transport` (new `conn.go`) | A `client.Conn` for `inspect` | Module detailed design §5.5 |
| `internal/cli/inspect.go` | The main path and the no-credential check replaced by `client.Open`; shows era and version | §9 |
| `internal/config` | Refuses protocol header names in `headers` | §4 |

Bounds: client lines 8 MiB (as today). Upstream JSON body 8 MiB. SSE line and event 8 MiB each. Refresh up to 10
pages. The `Tracker` bounds (1024 remembered requests, 5000 tools) are the module's.

Errors: a failed refresh delivers the first `-32020`. A removed tool is logged on stderr by name and reason only
(argument and result content is never logged). When a synthesized error quotes the upstream body, it follows
`Classify`'s rule (the first 200 bytes, control characters removed).

## Test design

- **Unit (transport)**: with an httptest upstream, requests after a legacy `initialize` carry
  `MCP-Protocol-Version`. A modern `tools/call` carries `Mcp-Method`, `Mcp-Name` and the marks' `Mcp-Param-*`,
  and none for an argument with no value. Static headers do not override protocol headers. Marks are learned
  on both the SSE and the JSON path. A tool with an invalid mark is removed and the other keys remain. An HTML
  502, an empty 400, a 400 with `id: null`, and malformed JSON in a 2xx. A JSON body, an SSE line and an SSE event
  over 8 MiB each answer `-32603` under the request's `id`. A batch is sent without headers. An unmirrorable
  argument and a `tools/call` without a name are not sent and get `-32602`.
- **Unit (retry)**: `-32020` → refresh → resend succeeds. A second `-32020` is delivered. A failed refresh
  delivers the first `-32020` (including a failure after the first page). Refresh answers never reach the
  client. Only one retry. A non-modern request, a request other than `tools/call`, and a 400 other than
  `-32020` (such as `-32022`) are not retried. `-race`.
- **Unit (bridge)**: a line at the limit and one over it, each followed by a valid line — the relay keeps
  running and answers `-32700`.
- **Unit (config)**: protocol header names (in any letter case) are refused.
- **Unit (inspect)**: the no-credential check opens in the modern era against a modern-only upstream and judges
  there.
- **Probe matrix**: three httptest upstreams written from the specification text (legacy only, dual-era,
  modern only with header validation and `x-mcp-header`) against nlink-jp/mcp's three probe clients (legacy
  only, dual-era, modern only), through mcp-bridge's stdio. Expected results: the specification's
  compatibility matrix (client and upstream as they are). The HTTP upstream probes live in mcp-bridge's tests,
  because the module ships stdio probes only (ADR-030 §7's "the module ships the probes" means the stdio ones).
- **Real peers (E2E)**: Claude Code 2.1.311 through mcp-bridge, every line recorded. With GitHub and Obsidian,
  `server/discover` returns a result, the client stays modern, and no `initialize` appears. With NanoKVM the
  client falls back to the legacy era. Each returns `tools/list`, and one read-only tool can be called (GitHub
  `get_me`, Obsidian `get_server_info`; no tool is called on NanoKVM). A legacy client (gem-agent) reaches
  GitHub in the legacy era. `inspect` shows modern for GitHub and legacy for NanoKVM.

## Development plan

1. Approval of this ADR (en and ja).
2. Dependency and line framing (§1, §7). Done when: bridge unit tests pass.
3. Headers and responses (§3–§5) and the config refusal (§4). Done when: transport and config unit tests and the
   probe matrix pass.
4. Retry (§6). Done when: retry unit tests pass with `-race`.
5. `inspect` (§9). Done when: unit tests pass and the real output shows the era.
6. Documents (README en/ja, CLAUDE.md's dependency, transparency and out-of-scope items, AGENTS.md, the RFP's
   transparency item, CHANGELOG).
7. Real-peer E2E on the final code, and one independent pre-release pass (against this ADR).
8. Release (minor: 0.4.0 — a new dependency and changed behaviour).

## Consequences

- mcp-bridge reaches a modern-only upstream whenever its client is dual-era, and speaks to legacy upstreams
  with the headers the specification requires. A large answer no longer leaves a request unanswered.
- "Never read the body" becomes "read the body, derive headers, and remove only what the specification says
  must be removed". CLAUDE.md's dependency, transparency and out-of-scope items (tool masking does not cover the
  specification's exclusion) and the RFP are amended to match.
- One require line. Updates of nlink-jp/mcp reach users through mcp-bridge releases.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Convert the era (terminate MCP on both sides) | Legacy clients would reach modern-only upstreams, but the relay becomes a protocol endpoint and the transparency rule changes deeply. Some things, such as `input_required`, cannot be written in the legacy era. The clients' migration gives the same result |
| Return a HeaderMismatch as it is | A stdio client cannot fix headers. The tool keeps failing until the client next calls `tools/list` |
| Keep zero require lines and write the protocol handling here | One more of the copies ADR-030 sets out to end. The defect found this time (the missing header) lived in that very copy |

## References

- Organization ADR-030 (§1 and §2 amended on 2026-11-27), ADR-025
- nlink-jp/mcp v0.1.0: reference manual (`httpwire`, `wire`, `client`), detailed design §3.1, §5.4, §5.5
- MCP 2026-07-28 `basic/transports/streamable-http` (§Protocol Version Header, §Standard Request Headers,
  §Custom Headers from Tool Parameters, §Server Validation, §Backward Compatibility)
- ADR-0002 (answer every request), ADR-0004 (single-file strict JSON config)
