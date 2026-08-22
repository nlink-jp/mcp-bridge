# ADR-0002: Always answer a request that could not be forwarded

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-23 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | A bridge that stays silent on failure makes the client block until its own timeout, hiding a cause that usually has a one-command fix |

## Context

A relay sits between a client that expects an answer and a server that may be
unreachable, unauthenticated, or broken. When forwarding fails there are two
options: say nothing, or synthesise a JSON-RPC error response addressed to the
request that failed.

Saying nothing is the tempting default — the bridge did not produce the
failure, and inventing a response feels like overstepping. It is the wrong
choice. A JSON-RPC client correlates responses by id and has no other signal:
a request with no answer blocks until the client's own timeout, which is
typically tens of seconds and reports only that the server did not respond.

The real causes are specific and actionable. An expired OAuth token, a
revoked one, a network failure, a misconfigured URL — each has a message worth
seeing, and the first two are fixed by a single `mcp-bridge login` command. All
of it is lost if the bridge stays quiet.

## Decision

Every client **request** that cannot be forwarded receives a JSON-RPC error
response carrying the id it was sent with and the underlying cause as its
message. This is the one class of message mcp-bridge originates; everything
else is relayed unmodified.

Three rules keep it honest:

**Notifications are never answered.** They carry no id, and a response to a
notification is a protocol violation. The failure is logged to stderr instead.

**The cause survives the whole path.** A 401 that leads to an unusable
credential is reported as both facts — the rejection and the reason no
replacement exists — not as the last error in the chain. This was found by
running against the live Slack MCP server with a revoked token: the message
had degraded to "no access token", which is the after-effect of invalidating
the rejected credential and reads as an empty token file.

**No double responses.** A handler either writes a response or returns an
error to be turned into one, never both.

Unparseable input is answered with a parse error carrying a null id, because
the id is inside the message that would not parse.

## Consequences

- The client sees an error it can display, at the moment the request fails,
  instead of a timeout tens of seconds later.
- mcp-bridge emits messages the server never sent. They are always errors,
  always addressed to a specific request, and never results — so a client
  cannot mistake one for data.
- Error text reaches the client, so it must not carry secrets. The messages
  name endpoints, status codes and commands, never tokens.

## Alternatives considered

**Stay silent and let the client time out.** Rejected: the diagnosis is lost
and the wait is long. The failure modes this hides are the common ones.

**Close the connection on any forward failure.** Rejected: it turns a
recoverable per-request error into a dead session, and clients report it as a
crash rather than as an authentication problem.

**Answer with a generic "upstream unavailable".** Rejected: it discards the
distinction between a revoked token, a network failure, and a bad URL — which
is the only part of the message worth having.

## References

- JSON-RPC 2.0 §5.1 — error object
- Inherited from mcp-guardian ADR-0002, re-issued here because an ADR belongs
  in the log whose scope it binds
- The cause-preservation rule was added on 2026-08-23 after live testing
