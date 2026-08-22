# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Scaffolding only — no functionality yet. The commands are wired and documented
but return `not implemented yet`.

### Added

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
