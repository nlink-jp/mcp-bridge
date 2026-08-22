# ADR-0004: One configuration file, strictly decoded, in JSON

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-23 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | The predecessor's configuration became unreadable, and the measured cause was structure, not volume |

## Context

mcp-bridge is extracted from mcp-guardian because that tool's configuration had
become hard to follow. Before deciding a new shape, the old one was measured:

- 14 CLI flags, of which 3 were used routinely
- ~26 profile keys, of which 5 were used
- ~16 global-config keys, of which 0 were used — the file did not exist

Volume was part of it, but the structural faults did the real damage:

**Two tiers with different rules.** A system-global file held telemetry and
defaults; per-server profiles held everything else. The profiles were decoded
strictly and the global file was not, so the same typo was fatal in one file
and silently ignored in the other.

**Keys read at different times.** `authorizeUrl` and `extraParams` were
consumed only by the login command and never reached the runtime config
struct. Nothing in the file distinguished them from keys that applied to every
session.

**A name that did not match the protocol.** `transport: "sse"` selected the
Streamable HTTP transport, a name that had been wrong since the protocol was
revised.

**Coupled orthogonal settings.** `mask` took effect only when `enforcement`
was `strict`, so hiding a tool silently depended on an unrelated knob.

The organization's convention for new tools is sectioned TOML at
`~/.config/<tool>/config.toml`, with a documented env-override precedence.

## Decision

**One file, one shape, decoded strictly.** `~/.config/mcp-bridge/config.json`
holds every setting. There is no second tier and no per-server file, and every
key applies at the same time — when a session or a login starts, the whole file
is read and validated together. `XDG_CONFIG_HOME` relocates the base
directory, which is also how the test suite stays off a developer's real
configuration and tokens.

**JSON, not the organization's sectioned TOML.** This deviates from convention
deliberately. A TOML parser is an external dependency, and mcp-bridge handles
OAuth client secrets and access tokens; `go.mod` keeps zero require lines so
there is no third-party code in the path of a credential. JSON also matches
the formats the surrounding MCP ecosystem already uses (`.mcp.json`,
`claude.json`), and `encoding/json` supports strict decoding directly.

**Unknown keys are an error.** `DisallowUnknownFields` on the single file.
Hand-written configuration accumulates typos, and `callbackSchema` for
`callbackScheme` is a feature that silently never happens with nothing on
screen to explain it.

**Authentication is chosen by presence, not by a mode string.** No `flow` key,
no `transport` key. `oauth` present and populated means a pre-registered
client; `"oauth": {}` means discovery; `tokenCommand` means an external
command; `headers` alone means a static credential; none of them means no
authentication. A `mode` field would be a second source of truth that can
disagree with the block beside it.

**Every problem is reported at once.** Validation collects all failures rather
than stopping at the first, because fixing a file one error per run is
needless work when the file is open in front of you.

## Consequences

- The tool does not follow the organization's TOML convention. That is a real
  cost: a user who knows the other tools has to notice the difference. The
  READMEs state the reason where the format is introduced.
- Configuration cannot carry comments. The examples in both READMEs carry the
  explanation instead.
- Nothing can be configured per-invocation except the config path itself and
  the OAuth callback port. That is intentional: the predecessor's flag surface
  is what made its modes hard to see.
- Adding a genuinely global setting later would need a new decision. There is
  currently nothing to put in such a tier — telemetry, which filled it before,
  is out of scope.

## Alternatives considered

**Sectioned TOML with `BurntSushi/toml`**, following the organization
convention. Rejected for the dependency, on a tool whose whole job involves
credentials. Comments would have been a genuine gain.

**Hand-written TOML subset parser**, keeping both the convention and zero
dependencies. Rejected: roughly 150 lines of parser whose failure modes on
valid-but-unusual TOML would be ours to own, to gain comment support.

**Keep the two-tier layout with a global file.** Rejected: after dropping
telemetry there is nothing left for the global tier to hold, and the tier was
half of what made the predecessor confusing.

**Per-server files in a directory**, as the predecessor had. Rejected: with a
handful of servers it splits one readable file into several, and it was the
layout that let login-time and run-time keys blend together unnoticed.

## References

- [`docs/en/mcp-bridge-rfp.md`](../mcp-bridge-rfp.md) — the measurements above
  and the full scope decision
- nlink-jp CONVENTIONS.md — the sectioned-TOML convention this departs from
