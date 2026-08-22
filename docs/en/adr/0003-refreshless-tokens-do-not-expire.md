# ADR-0003: A token with no refresh has no expiry we can act on

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-23 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | Slack, with token rotation disabled, returns neither `expires_in` nor a refresh token; treating that as an expiry forced a re-login every hour |

## Context

An OAuth token response may omit `expires_in`, omit `refresh_token`, or both.
The combination matters, because it decides what a client can usefully do:

| `expires_in` | `refresh_token` | What the client can do |
|--------------|-----------------|------------------------|
| present | present | Renew before it lapses |
| absent | present | Probe periodically; renewal is available |
| present | absent | Nothing — when it lapses, only a new login helps |
| absent | absent | Nothing — there is no expiry to act on |

The last row is the Slack case with token rotation disabled, and it is the
default for a Slack app that has not opted into rotation. Verified against the
live Slack MCP server on 2026-08-23: a fresh login returned neither field, and
a token stored three months earlier with a recorded expiry long past was still
accepted by Slack.

The predecessor stored a synthetic one-hour expiry for such tokens and refused
to use them afterwards. The result was a demand to log in again every hour, for
a credential the server was perfectly happy with.

## Decision

`expires_at == 0` means **"no known expiry"**, not "expired at the epoch".

A token with no refresh token is returned as stored, whatever its recorded
expiry says. Nothing in mcp-bridge could act on that expiry: there is no
renewal path, so refusing the token locally only replaces a working session
with a failed one. The server is the judge, and its verdict arrives as a 401.

When a refresh token exists, the expiry is honoured and renewal happens
automatically, with a small margin so a token cannot lapse between the check
and the request that uses it.

At login, the stored expiry is decided as:

- `expires_in` given → honour it.
- No `expires_in` but a refresh token → check back in an hour. Renewal is
  available, so the guess costs nothing.
- Neither → store zero.

A 401 that leaves no usable credential is reported with both the rejection and
the reason no replacement exists, and names the login command (ADR-0002).

## Consequences

- A non-expiring Slack token works for as long as Slack honours it, with no
  periodic re-login.
- A token that really has expired is used once more before the 401 arrives.
  That is one wasted request in exchange for never refusing a live credential.
- The stored `expires_at` for such tokens carries no information. It is written
  as zero rather than omitted so the distinction from "expired long ago" is
  explicit in the file.

## Alternatives considered

**Default to a one-hour expiry when the provider gives none.** Rejected: this
is what the predecessor did, and it is what this ADR exists to undo. It
converts a non-expiring credential into an hourly interruption.

**Refuse to store a token with neither field and require rotation.** Rejected:
rotation is a provider-side setting the user may not control, and the token
works.

**Probe the server periodically to detect revocation early.** Rejected: it
adds traffic to discover something the next real request discovers anyway, and
the response to a revoked token is the same either way — log in again.

## References

- RFC 6749 §4.2.2, §5.1 — `expires_in` is optional
- Inherited from mcp-guardian ADR-0003, re-issued here because an ADR belongs
  in the log whose scope it binds
- Verified against the live Slack MCP server on 2026-08-23, in both directions:
  a fresh login returned no expiry, and a three-month-old token whose recorded
  expiry had passed was still accepted
