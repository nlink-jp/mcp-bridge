# ADR-0001: Support OAuth providers that require a pre-registered client

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-23 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | The providers worth bridging — Slack, GitHub Apps, Microsoft Entra ID — do not implement RFC 7591, and an MCP client's built-in OAuth cannot reach them |

## Context

MCP clients that speak OAuth do so through Dynamic Client Registration
(RFC 7591): they discover the authorization server, register themselves, and
proceed. Claude Code does this for `type: "http"` servers, and for a provider
that supports DCR there is nothing for a bridge to add.

The providers that matter in practice do not support it. Slack, GitHub Apps,
Microsoft Entra ID and most enterprise SaaS require an OAuth app registered by
hand in an admin console, which fixes three things at registration time that
a DCR-based client assumes it controls:

1. **The exact redirect URI**, including its port. A client that binds an
   ephemeral port produces a different redirect URI on every run, and the
   provider rejects all of them.
2. **The URI scheme.** Slack refuses `http://` loopback redirect URIs when the
   app is registered, and accepts only `https://`.
3. **The token-endpoint authentication method.** A pre-registered app may be
   confidential (it has a secret) and may require that secret in the form body,
   in an `Authorization: Basic` header, or not at all.

None of these can be discovered reliably. Providers without DCR frequently
publish `token_endpoint_auth_methods_supported` incorrectly or not at all, so
reading it and guessing produces an opaque 401 at the token endpoint with
nothing to point the user at.

## Decision

The configuration carries all three explicitly, and mcp-bridge never infers
them.

**`callbackPort`** pins the loopback listener to one port. When it is set and
the port is busy, the login fails and says so — including how to change it —
rather than falling back to an ephemeral port the provider would refuse. When
it is unset the OS picks one, which is correct for DCR: the client is
registered fresh with whatever port was chosen.

**`callbackScheme: "https"`** wraps the loopback listener in TLS using an
ephemeral self-signed ECDSA P-256 certificate, generated per login and never
written to disk. Its SANs cover `127.0.0.1`, `::1` and `localhost`, and the
redirect URI uses the `localhost` name because providers that restrict
redirect URIs to host names accept it where they reject an IP literal. The
browser shows a one-time "not secure" warning; the login prints an explanation
before opening the browser, and the callback server's own error log is
discarded so the handshake that produces the warning does not also print
`tls: bad certificate` on a successful login.

**`clientAuthMethod`** selects `post` (default), `basic`, or `none` — the
RFC 6749 §2.3.1 methods. Configuration validation rejects `basic` without a
secret and `none` with one, so a contradiction is caught at load time rather
than at the token endpoint.

PKCE (RFC 7636) is applied on every login regardless of client type. It costs
nothing and closes the authorization-code interception window for confidential
clients too.

Discovery is still supported for providers that do offer it: an empty
`"oauth": {}` block asks for RFC 9728 protected-resource metadata, RFC 8414
authorization-server metadata, and RFC 7591 registration. The discovery result
is cached together with the redirect URI it was registered for, so a fixed port
reuses one registration instead of creating a client record on the provider at
every login.

## Consequences

- The user must have administrative access to the provider to register an
  OAuth app. This is stated at the top of both READMEs; without it the tool
  cannot help, and there is no way around the registration step.
- A self-signed certificate on a loopback listener means a browser warning on
  every https login. On loopback there is no realistic interception to defend
  against, so the alternative is not reaching those providers at all.
- A fixed `callbackPort` can collide with another process. The failure is
  explicit and names both `oauth.callbackPort` and `--callback-port`.
- `clientAuthMethod` is one more thing to get right by hand. The validation
  rules above catch the two contradictions that are possible; a wrong-but-
  consistent choice still surfaces as a token-endpoint error carrying the
  provider's own message.

## Alternatives considered

**Read the auth method from the authorization server's metadata.** Rejected:
the providers this ADR is about are precisely the ones that publish that field
unreliably. A wrong guess fails at the token endpoint with an error that does
not identify the guess as the cause.

**Always use an ephemeral callback port.** Rejected: it makes every
pre-registered app unusable, which is the entire audience.

**Terminate TLS with a locally trusted certificate** (a generated CA installed
in the system trust store). Rejected: installing a CA on the user's machine is
a far larger security decision than a browser warning, and it needs privileges
the tool should not ask for.

**Do not support https callbacks; tell users to register `http://`.**
Rejected: Slack does not accept it, and Slack is the motivating case.

## References

- RFC 6749 §2.3.1 — client password authentication
- RFC 7636 — PKCE
- RFC 7591 — Dynamic Client Registration
- RFC 8414 — Authorization Server Metadata
- RFC 9728 — Protected Resource Metadata
- [`docs/en/reference/slack-setup.md`](../reference/slack-setup.md) — a worked example
- Verified against the live Slack MCP server on 2026-08-23
