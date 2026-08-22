# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The Core and Features phases of the RFP are complete. Every subcommand and
every authentication mode the configuration accepts is implemented. What
remains before a release is packaging and a run against a live provider.

### Added

- Single-file JSON configuration, decoded strictly. Authentication is selected
  by which key is present rather than by a mode string, and `"oauth": {}`
  (present but empty) asks for discovery while an absent key means no
  authentication.
- Streamable HTTP transport: the three response shapes (`application/json`, an
  SSE stream, and 202 Accepted with no body), `Mcp-Session-Id` affinity, and a
  single invalidate-and-retry on 401.
- Transparent stdio relay. Messages are forwarded unmodified; outgoing ones are
  compacted so a pretty-printing server cannot desynchronise the newline
  framing, and a request that cannot be forwarded still receives an error
  response carrying the cause.
- `run` for servers with no authentication or static headers, and `list`
  showing each server's authentication mode, login state, and URL.
- OAuth 2.0 authorization_code login with PKCE on every login, a loopback
  callback listener, and automatic refresh where a refresh token exists.
  Tokens live in `tokens.json` (mode 0600, written atomically).
- An https callback option that presents an ephemeral self-signed certificate
  and redirects to `localhost`. This is what makes providers reachable that
  reject `http://` loopback redirect URIs at app-registration time — Slack
  most notably.
- Endpoint discovery through the 401 challenge's protected-resource metadata
  (RFC 9728), falling back to both well-known metadata paths (RFC 8414 and
  OpenID provider metadata), plus dynamic client registration (RFC 7591). The
  result is cached with the redirect URI it was registered for, so a fixed
  callback port reuses one registration instead of creating a client record on
  the provider at every login.
- `tokenCommand`: a Bearer token from an external command such as
  `gcloud auth print-access-token`. The command runs directly rather than
  through a shell, and multi-line output is refused rather than truncated.
- `login`, `logout`, and `inspect`. `inspect` connects, authenticates, and
  prints the server's identity and tool list, so a configuration can be
  checked before it is wired into an MCP client.
- A missing or expired login is reported as the command that fixes it, at
  startup, rather than as an unexplained 401 later or a missing file the user
  never created by hand.

- Documentation: a four-record ADR log and a Slack setup reference, both
  mirrored in English and Japanese.

### Fixed

- A 401 from the server no longer loses its cause. Invalidating the rejected
  credential used to leave the next error saying "no access token", which
  describes the after-effect and sends the reader looking for an empty token
  file. Both the 401 and the login command now survive into the message.
  Found by running against the live Slack MCP server with a revoked token.
- The OAuth callback server no longer prints `http: TLS handshake error ...
  tls: bad certificate` on every successful https login. That handshake
  failure is the browser's first connection being refused — the very thing
  that produces the warning the user clicks through — so it was guaranteed
  noise that read as a failure. Found during a live Slack login.

- RFP (Phase 1 planning) in `docs/{en,ja}/`, fixing the final shape before
  implementation: scope limited to providers without RFC 7591 Dynamic Client
  Registration, JSON configuration in a single file, no audit logging, six
  subcommands and two flags.
- Project scaffold: `main.go` with subcommand dispatch, Makefile targeting
  `dist/`, `.gitignore`, MIT LICENSE, `docs/{en,ja}` mirror check.
- Tests pinning the CLI surface: `--version` and the `version` subcommand
  produce identical output; every documented command is dispatchable and every
  dispatched command is documented; every documented flag is registered. These
  exist because mcp-guardian shipped flags that appeared in no README and error
  messages naming flags that had been deleted — drift this repository fails on
  instead of accumulating.

## [0.1.0] - unreleased

First release. Planned scope: everything listed above, once it is packaged and
verified against a live provider.
