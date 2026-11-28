# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- The shared MCP protocol layer is updated to nlink-jp/mcp v0.3.0
  (organization ADR-030 §6: the module at its latest tag). v0.3.0 changes only
  the module's `ToolServer` — it answers the calls already received before it
  returns at end of input, and refuses nil streams — which this bridge does
  not use, so its behaviour does not change.


## [0.4.3] - 2026-11-27

### Changed

- **The OAuth discovery probe no longer writes its own `initialize`** (ADR-0009).
  The unauthenticated request that asks the server for its 401 challenge is now
  the opening nlink-jp/mcp builds — `server/discover` with
  `MCP-Protocol-Version` and `Mcp-Method` — sent once, without a credential, as
  before. Measured against GitHub's and Slack's MCP servers, the challenge is
  the same byte for byte. A probe that cannot be built is reported as such, not
  as an unreachable server.

## [0.4.2] - 2026-11-27

### Changed

- The shared MCP protocol layer is updated to nlink-jp/mcp v0.2.0
  (organization ADR-030 §6: the module at its latest tag). v0.2.0 changes only
  the client's line reading (`LineConn`), which this relay does not use, so
  its behaviour does not change.

## [0.4.1] - 2026-11-27

### Fixed

- **A stream that answered its request under a re-spelled id no longer gets a
  second answer.** Ids were compared by their bytes, so an upstream that spelled
  the id another way (an escaped `<` or `&`, say) was read as not answering, and
  the client got the real answer and a synthesized `-32603`. Ids are now
  compared by value.
- **Answers mcp-bridge writes keep the id's spelling.** A synthesized error and
  an id correction went through an HTML-escaping encoder; they no longer do.
- **A bare `null` or an empty object from the client is answered with
  `-32600`** instead of being forwarded as a message.

## [0.4.0] - 2026-11-27

mcp-bridge speaks both MCP eras (ADR-0008). MCP 2026-07-28 replaced the
`initialize` handshake with a version carried on every request; the bridge
passes the era your client opens through unchanged and adds what the
specification requires of an HTTP client. It now has one dependency,
[nlink-jp/mcp](https://github.com/nlink-jp/mcp), the MCP protocol layer shared
by nlink-jp's servers and relays, which itself uses only the standard library.

### Added

- **The MCP protocol headers.** A handshake session's messages after
  `initialize`, and the DELETE that ends the session, carry the negotiated
  `MCP-Protocol-Version` — required since 2025-06-18 and
  missing until now; servers accepted the requests only because they may treat
  a missing header as 2025-03-26. A 2026-07-28 request carries its version,
  `Mcp-Method`, `Mcp-Name`, and the `Mcp-Param-*` headers a tool's schema asks
  for with `x-mcp-header`.
- **Tools with an invalid `x-mcp-header` declaration are left out of the tool
  list**, as the specification requires of an HTTP client; the log names them.
- **A tool call refused because its headers no longer match the server's tools
  is sent once more** after mcp-bridge reads the tool list again.
- **`inspect` names the protocol era** (`2026-07-28 (modern)`, `2025-06-18
  (legacy)`) and opens a server in the newer protocol when it offers it.

### Changed

- **`headers` in the config cannot name the MCP protocol headers**
  (`MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name`, `Mcp-Param-*`); the config
  is refused at load. They are derived from each message.
- **An HTTP error without a JSON-RPC answer** (an HTML 502, an empty 400) is
  answered with `-32603` under the request's id, and a JSON-RPC error answered
  with `id: null` arrives under the request's id.

### Fixed

- **One over-long message from the client no longer ends the bridge.** It is
  skipped and answered with `-32700`; the session goes on.
- **A large or malformed answer no longer leaves a request waiting for ever.**
  A JSON body over 8 MiB used to be cut and dropped, an SSE line over 8 MiB
  ended the stream, and a 2xx body that was not JSON was dropped; each is now
  answered with `-32603`.
- **`inspect`'s credential check works against a server that speaks only the
  newer protocol.** It used to fail its handshake there and report "credential
  accepted" without having tested the credential.
- **`inspect`'s log is filtered for control characters like `run`'s.**
- **A request the server leaves unanswered gets an error.** An empty 2xx or a
  202 to a request, and a stream that ends without the request's answer (an
  event whose data is not JSON is dropped), are answered with `-32603`.
- **An SSE event whose data lines together exceed 8 MiB is answered with
  `-32603`**; the joined size used to be unbounded.
- **A 2xx answer that does not name its request is delivered under the
  request's id** (an `id: null` error, a response without an id), as non-2xx
  answers already were; such a request used to wait for ever.
- **A client line that is JSON but not a JSON-RPC message gets `-32600`**
  (Invalid Request); malformed JSON keeps `-32700`.
- **A request whose headers cannot be built is answered with `-32602` and not
  sent**: an argument a header cannot carry, a `tools/call` or `prompts/get`
  without a name, a `resources/read` without a URI. A batch is sent without
  protocol headers.

## [0.3.1] - 2026-11-26

Several bridges for one server renew its login one at a time (ADR-0007). An
MCP client starts a bridge per session or window, and against a server that
issues single-use refresh tokens they used to lose the stored login. Restart
every MCP client that uses an OAuth server after upgrading.

### Fixed

- **Several bridges for one server no longer lose its login** (ADR-0007).
  An MCP client starts a bridge per session or window, and each bridge kept
  the refresh token it read at start-up. Against a server that issues
  single-use refresh tokens — Atlassian Remote MCP was where it was reported —
  the first bridge to renew spent that token, the others presented it again
  and failed with `invalid_grant`, and with reuse detection the stored login
  died with them. Renewal now happens under a lock beside the token file
  (`tokens.json.lock`): the bridge that gets it reads the file again and
  either uses what another bridge stored or renews with the newest refresh
  token. `login` and `logout` write under the same lock. Reported, analysed
  and reproduced by a user, with a proof of concept.
- **Logging in again heals running bridges.** A bridge whose renewal failed
  kept the dead refresh token until its MCP client was restarted; it now reads
  the stored login again at its next attempt.
- **A logged-out login is no longer written back.** A running bridge renewed
  from memory after `mcp-bridge logout` and stored the result again; it now
  reports that there is no login.

### Added

- One line on stderr for each renewal, saying when the new token expires and
  whether the server issued a new refresh token, and one when a bridge uses
  the renewal of another. No token value is printed.

### Upgrading

- Bridges of 0.3.0 and earlier take no lock. Restart every MCP client that
  uses an OAuth server after upgrading.
- The lock on Windows (`LockFileEx`) is built and statically checked but has
  not been run on a Windows machine.

### Documentation

- **The Entra ID route has been run against a tenant.** A user connected
  v0.3.0 to a Microsoft Agent 365 server in their tenant: the login, the tool
  listing and the renewal with the refresh token worked with the
  configuration in the guide as written, and they reported that the servers
  for SharePoint, Teams and OneDrive connected as well. The
  [Entra ID guide](docs/en/reference/entra-setup.md) and both READMEs now say
  so, the guide's examples are what that server answered, and the record is
  in the v0.3.0 design note and under Follow-up in ADR-0006. No behaviour
  changes; a test pins the observed responses.

## [0.3.0] - 2026-11-17

Connecting to MCP servers protected by Microsoft Entra ID (ADR-0006). No login
against an Entra ID tenant has been run; the
[Entra ID guide](docs/en/reference/entra-setup.md) says what was checked.

Existing configurations are read as before. For `"oauth": {}` the login can
behave differently, in the ways listed under Changed: the scopes requested, the
check that a server's metadata is its own, and the `https` condition on
discovered URLs.

### Added

- **A pre-registered client can leave its endpoints to discovery.** An `oauth`
  block with a `clientId` and neither `authorizeUrl` nor `tokenUrl` takes the
  endpoints and the scopes from what the server publishes and registers
  nothing. The configuration was all or nothing — `"oauth": {}`, which needs
  dynamic registration, or every URL and scope by hand — and providers such as
  Microsoft Entra ID publish their metadata while offering no registration
  endpoint. Such a block cannot hold a `clientSecret`: a secret is sent only
  to a token endpoint written in the configuration. Discovery for it runs at
  every login. See ADR-0006.
- **`login` prints what discovery decided**: the authorization server and the
  document that named it, the client, and the scopes with their source.
- **`callbackHost` sets the host name of the redirect URI.** It was fixed by
  the scheme — `127.0.0.1` for `http`, `localhost` for `https` — and Microsoft
  Entra ID accepts an `http` redirect URI only for `localhost`, so an app
  registered with `http://localhost:<port>/callback` could not be logged in to
  (`AADSTS50011`). `"callbackHost": "localhost"` or `"127.0.0.1"` now says
  which; left out, the host follows the scheme as before. With `localhost` the
  listener waits on both `127.0.0.1` and `::1`, which the `https` callback now
  does too. See ADR-0006.

### Fixed

- **Discovery finds authorization server metadata published only at
  `<issuer>/.well-known/openid-configuration`.** For an issuer with a path,
  only the two locations that insert the well-known segment before the path
  were tried. Microsoft Entra ID answers 404 at both and serves its document at
  the appended location, so discovery could not find it. All three locations
  the MCP specification lists are now tried, in its order.

### Changed

- **A dynamic registration is reused only for the server it was made for.**
  The stored result now records the `url`, and one stored for another `url`,
  or by an earlier version, is not reused. A login that reused a registration
  through `--callback-port` registers once more after the update.
- **A server that does not answer is reported as unreachable.** Discovery went
  on to ask the well-known locations on the same host and ended with "could
  not discover the authorization server" over the same connection error
  several times. It now stops with `could not reach <url>`.
- **A session refuses a stored token endpoint that discovery would refuse.**
  The endpoint an earlier version stored was not checked; one that is `http`
  for a server configured with an `https` url now stops the session with the
  login command to run, instead of receiving the refresh token.
- **Text from a server reaches the terminal without its control characters.**
  The output of `login` and `inspect`, the log of `run`, and the error a
  command ends with, can quote what a server returned; control characters
  other than newline and tab, and bidirectional controls, are removed as they
  are written.
- **A session no longer pairs a stored client secret with a configured
  `clientId`.** The secret a dynamic registration left behind is sent only
  with the client ID stored beside it.
- **Discovery requests the scopes the server asks for.** It read only the
  authorization server's `scopes_supported`, which for an OpenID provider is
  `openid profile email offline_access` and none of the API scopes an MCP
  server needs. The order is now the MCP specification's — the `scope` in the
  server's 401 challenge, then `scopes_supported` in its protected-resource
  metadata — with the authorization server's list kept as the last step.
  `offline_access` is added to scopes the server named when the authorization
  server offers it, so that a refresh token is issued. For `"oauth": {}` this
  changes the scopes requested from a server that names its own.
- **Discovery looks for the server's metadata at the well-known locations**
  when the 401 challenge does not say where it is, or the server does not
  challenge at all: `/.well-known/oauth-protected-resource` with the server's
  path, then at the root.
- **Metadata that describes another resource stops the login.** A server's
  protected-resource metadata is used only when its `resource` has the scheme,
  host and port of the configured `url` and a path leading to it (RFC 9728
  §3.3, with the official MCP SDK's leading-path rule). A document with no
  `resource` is passed over.
- **Discovery uses only `https` URLs, unless the configured `url` is `http`.**
  An `https` server could name an `http` authorization or token endpoint and
  the login would use it, sending the authorization code and the tokens in the
  clear. Such a URL is now refused. The login's and the refresh's requests
  also stop following a redirect from `https` to anything else. A server
  configured with an `http` url behaves as before.
- `make package` runs `govulncheck ./...` first (`make vuln`) and stops on a
  finding, so a release cannot be built past one.
- `make vuln` — and so `make package` — scans every platform in `PLATFORMS`,
  with the environment `build-all` builds each with. It scanned the host's
  build alone, so an advisory in a Linux- or Windows-only package could not
  stop a release.

## [0.2.0] - 2026-11-09

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

- **`tlsCertSha256` pins a server's certificate.** Devices on a local network
  often serve a self-signed certificate issued to `localhost`, which usual
  verification refuses and an extra CA cannot repair. With the key set, that
  exact certificate is accepted in place of verification, and any other stops
  the connection before a header or token is sent. A pinned connection refuses
  a redirect to anything but `https`. It needs an `https` URL and cannot be
  combined with `oauth`. See ADR-0005.
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

[0.4.3]: https://github.com/nlink-jp/mcp-bridge/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/nlink-jp/mcp-bridge/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/nlink-jp/mcp-bridge/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/nlink-jp/mcp-bridge/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/nlink-jp/mcp-bridge/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nlink-jp/mcp-bridge/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/nlink-jp/mcp-bridge/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/nlink-jp/mcp-bridge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nlink-jp/mcp-bridge/releases/tag/v0.1.0
