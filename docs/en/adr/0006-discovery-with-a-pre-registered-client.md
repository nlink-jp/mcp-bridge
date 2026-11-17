# ADR-0006: A pre-registered client takes its endpoints and scopes from what the server publishes

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-17 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | A user's report that mcp-bridge cannot connect to an MCP server protected by Microsoft Entra ID (Microsoft Agent 365) |
| Extends | [ADR-0001](0001-pre-registered-oauth-client.md). [ADR-0004](0004-single-file-strict-json-config.md)'s "an `oauth` block with content is a pre-registered client" becomes "an `oauth` block with a `clientId` is a pre-registered client" |

## Context

ADR-0001 named Microsoft Entra ID as a target provider, but only Slack was
verified; Entra ID rested on the inference that it presents the same shape. A
user's report and the checks made on 2026-11-17 found four places where it does
not.

**1. The host name of the redirect URI.** Entra ID allows an `http` redirect
URI only for localhost, and the admin console's input box refuses
`http://127.0.0.1` (it takes a direct edit of the manifest). Apps are therefore
commonly registered with `http://localhost:<port>/callback`. mcp-bridge always
uses `127.0.0.1` for `http`, so the match fails (`AADSTS50011`). For
`localhost`, Entra ID does not compare the port and does compare the path.

**2. Where the metadata lives.** Entra ID publishes its authorization server
metadata only at `<issuer>/.well-known/openid-configuration`. The two locations
mcp-bridge tries — the forms that insert the well-known segment before the
path — both returned 404 when measured. The MCP specification requires three
locations to be tried in order.

**3. Client registration.** Entra ID has no dynamic registration endpoint
(RFC 7591). The configuration offers either "`"oauth": {}`: discover everything
and register" or "write `authorizeUrl`, `tokenUrl` and `clientId`", with no way
to say "the client is registered; take the rest from what the server
publishes". The MCP specification puts pre-registered client information first
among the registration approaches.

**4. Scopes.** During discovery mcp-bridge uses the authorization server's list
of scopes. Entra ID's list is `openid profile email offline_access` and does
not contain the API scopes an MCP server requires. The MCP specification takes
the scope from the 401 response's `scope`, then from `scopes_supported` in the
MCP server's own metadata (RFC 9728). Entra ID issues no refresh token unless
`offline_access` is requested.

Something is not verified. What an Agent 365 server actually answers to an
unauthenticated request has not been observed, for want of a tenant (a
placeholder tenant ID draws `400 TenantIdInvalid`). What follows was decided
from Microsoft's published documentation, Entra ID's public metadata and the
MCP specification.

## Decision

**The host name of the redirect URI can be written in the configuration.**
`callbackHost` takes `"127.0.0.1"` or `"localhost"`. It is the fourth item
after the three ADR-0001 put in the configuration. Left out, it is what it was:
`127.0.0.1` for `http`, `localhost` for `https`. With `localhost` the listener
waits on both `127.0.0.1` and `::1`, so the callback arrives whichever address
the browser resolves the name to.

**An `oauth` block with a `clientId` and no URLs discovers the endpoints and
registers nothing.** `authorizeUrl` and `tokenUrl` are written together or left
out together; one without the other stays a configuration error. A block with
anything in it still needs `clientId`.

**A configuration that discovers its endpoints cannot hold a `clientSecret`.**
A discovered token endpoint is wherever the MCP server's response points, not
somewhere the configuration file names. A secret written in the configuration
is sent only to a token endpoint written in the configuration. A client with a
secret writes its URLs.

**Discovery follows the order in the MCP specification (2025-11-25).** The MCP
server's metadata is taken from the URL the 401 response names, and otherwise
from the two well-known locations (with the server's path, then the root). The
authorization server's metadata is tried at the specification's three
locations in order. When none of that yields anything, authorization server
metadata on the server's own host is tried, as before.

**An MCP server's metadata is used only if it is that server's own.** The
metadata's `resource` must have the scheme, host and port of the configured
`url`, and its path must be a leading part of the `url`'s path. If it does not
match, the login stops and shows both values.

**A discovered URL is used only if it is `https`.** Only when the configured
`url` is itself `http` are `http` URLs used too: the person who wrote the
configuration is the one who chose an unencrypted server. An `https` server
cannot move the authorization or the token exchange down to `http`. Neither
do the login's and the refresh's requests follow a redirect from `https` to
anything else (the rule ADR-0005 set).

**Scopes are decided in this order.** The configured `scopes`, the 401
response's `scope`, `scopes_supported` in the MCP server's metadata,
`scopes_supported` in the authorization server's metadata. The last step is
not in the MCP specification; it stays because a user of `"oauth": {}` has no
way to write scopes, so what worked keeps working.

**`offline_access` is added to scopes the server named.** The conditions: the
scopes came from the 401 response or the MCP server's metadata, and the
authorization server lists `offline_access`. This follows the text of the next
revision of the MCP specification and is what Entra ID needs to issue a refresh
token. It is not added to configured `scopes`: what is written is what is sent.

**A pre-registered client's discovery is repeated at every login.** Repeating
it leaves nothing behind on the provider, so there is no reason to reuse an old
result. The result is stored as before, and `run` reads it to refresh tokens.
A dynamic registration is reused only when the `url` matches as well as the
redirect URI.

**The login prints what discovery decided, with where each came from:** the
authorization server, and the scopes and their source. Against a server that
cannot be exercised here, this is what lets a failure be diagnosed from the
output a user pastes. The output and the errors carry text the server
returned, so control characters and bidirectional controls are removed just
before anything reaches the terminal. Newlines are kept, since the program's
own messages have them: a server can break its own text across lines, and
cannot reach back over what was already printed.

**What is still not done.** The MCP specification or a related one asks for
each of these, but doing it would either stop Entra ID from connecting or
cannot be verified.

- No `resource` parameter (RFC 8707). Entra ID's v2 endpoints are reported to
  refuse it together with `scope`. A provider that needs it can be given it
  through `extraParams`.
- No check that the authorization server advertises PKCE support
  (`code_challenge_methods_supported`). Entra ID's metadata has no such field.
  PKCE itself is always used (ADR-0001).
- No check of the `issuer` in the authorization server's metadata. Entra ID's
  common endpoint returns an `issuer` holding the unsubstituted text
  `{tenantid}`.
- No `prompt=consent`.

## Consequences

- **The MCP server you configure decides which scopes are requested.** Where a
  pre-registered client has consent for several APIs, the server can name the
  scopes of an API that is not its own. Entra ID cannot bind a token's audience
  through the `resource` parameter, so this configuration cannot prevent it.
  It is accepted because the consent screen shows the scopes and the `url` is a
  destination the user wrote themselves. To pin them, write `scopes`.
- **This is released without having been run against Entra ID.** Both READMEs
  keep that distinction. Confirmation comes from the reporting user trying it
  in their environment.
- An mcp-bridge older than this version rejects `callbackHost` as an unknown
  key (ADR-0004). Update first, change the configuration second.
- Four things change for users of `"oauth": {}`. That configuration has nothing
  to adjust, so a login that falls under one of them stops.
  - Where the server names scopes, the scopes requested change from the
    authorization server's list to the ones the server named.
  - Where the `resource` in the server's metadata does not match the `url`, or
    is missing, that metadata is not used. A client built on the official MCP
    SDK does not connect to such a server either.
  - Where the `url` is `https` and a discovered URL is `http`, that URL is not
    used.
  - Where a dynamic registration was being reused through `--callback-port`,
    the first login after the update registers once more.
- When another process holds the same port on `::1`, the listener cannot wait
  there. It carries on with `127.0.0.1` alone and prints one line saying what
  to do. When the machine simply has no IPv6 loopback, nothing is printed. An
  authorization code that reaches another process cannot be exchanged for a
  token without the PKCE verifier.
- A user who writes `scopes` writes `offline_access` too if they want a refresh
  token.

## Alternatives considered

**Add only `callbackHost`, and have the URLs and scopes written by hand.**
Rejected. The user would look up each server's scopes with `curl` and copy
them. The server publishes those values and the MCP specification defines how
to choose among them. The metadata-location problem (2 above) would remain for
`"oauth": {}` as well.

**One key holding the registered redirect URI as is (`redirectUri`).**
Rejected. It would be a second way to write what `callbackPort`,
`callbackScheme` and `--callback-port` already say. Entra ID does not compare
the port, so "the string registered" and "the string sent" would not be the
same string either.

**Make the redirect URI's path configurable (`callbackPath`).** Not taken up.
No registration with a path other than `/callback` is at hand. It is decided
when a concrete registration is shown.

**Allow `clientSecret` in a configuration that discovers its endpoints.**
Rejected. One response from the MCP server would send a secret that belongs to
the relationship with the provider to another destination.

**Check the metadata's `resource` for exact equality with the `url`
(RFC 9728 §3.3).** Not taken; a leading-path match is used. It is the rule the
official MCP SDK applies. The target server cannot be observed, so mcp-bridge
should not be the one client that refuses a server SDK-based clients connect
to.

**Drop the authorization server's scope list, as the MCP specification
has it.** Rejected. `"oauth": {}` has no way to write scopes, and so no way out
if it stops working.

**Add `offline_access` even to configured `scopes`.** Rejected. What is written
in the configuration would no longer be what is sent.

## Follow-up

**2026-11-17 — run against a tenant.** After the release, the reporting user
connected v0.3.0 to a Microsoft Agent 365 server in their tenant with a
`clientId`-only block and `"callbackHost": "localhost"`. The login, the tool
listing and a renewal with the refresh token worked. What the Context called
not verified was observed:

- The server answers an unauthenticated request with 401 and a challenge that
  names its metadata and carries no `scope`. The metadata's `resource` is the
  configured `url` exactly, so the leading-path rule was not what let it
  through.
- The authorization server it names is Entra ID's `organizations` endpoint, not
  the tenant's own. Its document is at the appended location and its `issuer`
  holds `{tenantid}`, as measured on the common endpoint.
- The server's own scope list already holds `offline_access`. The scopes came
  from the server's metadata and nothing was added, so the rule that adds
  `offline_access` has still not acted against Entra ID.
- The login was made with no `resource` parameter, with PKCE not advertised
  and with the `issuer` unchecked: the three things listed as not done.
- A refresh request without `scope` returned a new access token and a new
  refresh token, and the server accepted the new token.

No decision changes. The consequence "released without having been run against
Entra ID" is closed for Agent 365; the maintainers still have no tenant, so a
change to this route cannot be run against one here. The observed responses
are recorded in section 6 of the design note and pinned by a test.

**2026-11-18 — more Agent 365 servers.** The same user reported that the
servers for SharePoint, Teams and OneDrive connected as well. Their responses
were not sent.

## References

- [Model Context Protocol — Authorization (2025-11-25)](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization) — discovery order, registration approaches, scope selection
- [Model Context Protocol — Authorization (draft)](https://modelcontextprotocol.io/specification/draft/basic/authorization) — `offline_access`
- RFC 9728 — Protected Resource Metadata (§3.3 is the `resource` validation)
- RFC 8414 — Authorization Server Metadata
- [Redirect URI (reply URL) outline and restrictions](https://learn.microsoft.com/en-us/entra/identity-platform/reply-url) — how localhost is treated
- [Microsoft identity platform and OAuth 2.0 authorization code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow) — `offline_access` and the refresh request
- [`docs/en/history/v0.3.0-design.md`](../history/v0.3.0-design.md) — the detailed design behind this decision and the record of what was checked
