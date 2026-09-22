# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **`make verify-release` now fails closed.** Its last block chained unzip, the
  packaged binary's `--version` and `spctl` with `&&` and ended the whole chain
  in `|| true`, so a zip that did not unpack or a binary that did not run exited
  0 and the upload proceeded. Each step is now judged on its own, the packaged
  binary's `--version` must contain the tag being released, and only the
  informational `spctl` line may be ignored. Matches the org template
  (CONVENTIONS.md §Code Signing → Verifying a release).
- **The Linux archives no longer carry macOS file metadata.** macOS `tar` wrote
  each bundled file's extended attributes (`com.apple.provenance`, and a Dropbox
  attribute where the tree is synced) into the `.tar.gz` twice: as AppleDouble
  `._` members, which GNU tar extracts as stray `._<name>` files beside the real
  ones, and as `LIBARCHIVE.xattr.*` / `SCHILY.xattr.*` pax headers, which it
  reports as unknown keywords. `make package` now archives with
  `COPYFILE_DISABLE=1 tar --no-xattrs`; each setting stops one of the two.
  Archives already published still carry them; the files themselves are
  unaffected.

### Added

- **GitHub setup guide** (`docs/{en,ja}/reference/github-setup.md`). GitHub
  publishes RFC 9728 protected-resource metadata but its authorization server
  has no `registration_endpoint`, so `"oauth": {}` can never complete against
  it — the guide shows that evidence and the two routes that do work: a
  `tokenCommand` borrowing an existing GitHub CLI login (verified against the
  live server on 2026-08-30) and a pre-registered OAuth App (documented from
  published metadata, not exercised end to end). It also covers limiting the
  tool surface through the server's own request headers, with measured
  `tools/list` sizes, and records that no tool in the server deletes a
  repository.

### Changed

- The README now states that `headers` is **additive**: it accompanies `oauth`
  or `tokenCommand` rather than replacing them, and only those two are mutually
  exclusive. The behaviour is unchanged and was always the case, but the
  authentication table read as a list of alternatives, which made the GitHub
  tool-surface headers look impossible to combine with a credential.
  `TestHeadersAccompanyATokenCommand` now pins it.

### Internal

- `make verify-release` also judges each Linux archive: no AppleDouble or other
  macOS metadata members — listed with `--options 'tar:!mac-ext'`, because a
  plain macOS listing folds `._` members away — no extended attributes as pax
  headers, and exactly the canonical binary, `README.md` and `LICENSE`, compared
  in the C locale.
- The Linux-archive check in `make verify-release` reads each archive's pax
  headers with Python's `tarfile` instead of grepping the decompressed stream,
  which also matched file text that names the keywords (a bundled CHANGELOG,
  for one).

## [0.1.1] - 2026-08-23

### Changed

- `inspect` no longer requires a login. It is the command that answers "is my
  configuration right?", and before the first login is exactly when that gets
  asked; many MCP servers answer `initialize` and `tools/list` without a
  credential and only require one for `tools/call`. It now connects anyway and
  labels which view it is showing. When the server does demand a credential and
  none exists, the error still names the login command. `run` is unchanged and
  still refuses to start without one: an MCP client that launches the bridge
  cannot surface a per-request failure as legibly as a terminal can.
  ([#1](https://github.com/nlink-jp/mcp-bridge/issues/1))
- `inspect` now reports whether a presented credential was actually tested. A
  server that would have answered anyone proves nothing about a token, so the
  output says so instead of implying the login works.

## [0.1.0] - 2026-08-23

First release.

mcp-bridge connects stdio-only MCP clients to Streamable HTTP MCP servers that
require a **pre-registered OAuth client** — Slack, GitHub Apps, Microsoft Entra
ID and other providers without RFC 7591 dynamic client registration, which an
MCP client's own OAuth cannot reach. It is extracted from
[mcp-guardian](https://github.com/nlink-jp/mcp-guardian) with the governance,
audit and telemetry halves deliberately left behind.

### Added

- **Transparent stdio relay.** JSON-RPC messages are forwarded unmodified — no
  schema caching, no tool-list rewriting, no locally answered methods. Outgoing
  messages are compacted, because stdio framing is newline-delimited and a
  server that pretty-prints its JSON would otherwise split one message across
  several lines and desynchronise the client for the rest of the session.
- **Streamable HTTP transport.** All three shapes a server answers in —
  `application/json`, an SSE stream, and 202 Accepted with no body for
  notifications — plus `Mcp-Session-Id` affinity and a single
  invalidate-and-retry on 401. An HTTP error carrying a real JSON-RPC error
  object is forwarded to the client; anything else becomes a transport error
  rather than reaching the client as a phantom message.
- **OAuth 2.0 authorization_code login** with PKCE on every login, a loopback
  callback listener, and automatic refresh where a refresh token exists. Tokens
  are stored per server in `tokens.json`, mode 0600, written atomically.
- **An https callback** presenting an ephemeral self-signed certificate that
  never leaves memory, redirecting to `localhost`. This is what makes providers
  reachable that reject `http://` loopback redirect URIs at app-registration
  time — Slack most notably.
- **Endpoint discovery** through the 401 challenge's protected-resource
  metadata (RFC 9728), falling back to both well-known metadata paths (RFC 8414
  and OpenID provider metadata), plus dynamic client registration (RFC 7591).
  The result is cached together with the redirect URI it was registered for, so
  a fixed callback port reuses one registration instead of creating a client
  record on the provider at every login.
- **`tokenCommand`** — a Bearer token from an external command such as
  `gcloud auth print-access-token`. The command runs directly rather than
  through a shell, and multi-line output is refused rather than truncated to
  its first line.
- **Six subcommands and two flags**: `run`, `login`, `logout`, `list`,
  `inspect`, `version`; `--config` everywhere and `--callback-port` on `login`.
  `inspect` connects and authenticates for real, so a configuration can be
  checked before it is wired into an MCP client.
- **A single JSON configuration file**, decoded strictly so a typo fails loudly
  instead of disabling a setting in silence. Authentication is selected by
  which key is present rather than by a mode string: `"oauth": {}` asks for
  discovery, an absent `oauth` key means no authentication.
- **Errors that name their fix.** A missing or rejected login is reported at
  startup as the `mcp-bridge login` command that resolves it, not as an
  unexplained 401 later or a missing file the user never created by hand. A 401
  that leaves no usable credential carries both the rejection and the reason no
  replacement exists.
- **Documentation**: a four-record ADR log and a Slack setup reference, both
  mirrored in English and Japanese, plus the RFP recording what is deliberately
  out of scope.

### Verified

Against the live Slack MCP server on 2026-08-23: a fresh OAuth login through
the https callback, a full handshake and a real `tools/call` through the
bridge, and a 54 KB `tools/list` response relayed intact on one line.

Both directions of [ADR-0003](docs/en/adr/0003-refreshless-tokens-do-not-expire.md)
were confirmed with real data: a fresh login returned neither `expires_in` nor
a refresh token, and a three-month-old token whose recorded expiry had long
passed was still accepted by Slack. Treating that recorded expiry as
authoritative would have thrown away a working credential.

GitHub Apps and Microsoft Entra ID are expected to work — they present the same
pre-registered-client shape — but have not been exercised against a live
endpoint.

### Known limitations

- The reverse direction is not supported: mcp-bridge cannot expose a stdio MCP
  server over HTTP.
- Registering an OAuth app requires administrative access to the provider.
  There is no way around that step, and without it this tool cannot help.
- Out of scope by decision, not omission: governance gates, audit receipts,
  telemetry export, tool masking, and the client_credentials flow.

[0.1.1]: https://github.com/nlink-jp/mcp-bridge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nlink-jp/mcp-bridge/releases/tag/v0.1.0
