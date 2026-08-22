# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The Core phase of the RFP is complete: a server needing no authentication or a
static header can be bridged end to end. OAuth login is not implemented, so the
providers this tool exists for are not usable yet.

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
- Configured-but-unimplemented authentication (`oauth`, `tokenCommand`) is
  rejected by name at startup rather than failing later as an unexplained 401.

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

First release. Planned scope: config loader, stdio to Streamable HTTP relay,
no-auth and static-header authentication, `run` / `list` / `version`.
