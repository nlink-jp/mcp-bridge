# ADR-0005: A device with an unverifiable certificate is pinned, not trusted blindly

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-09 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | MCP servers built into devices on a local network serve certificates that normal verification rejects |

## Context

mcp-bridge verifies the upstream TLS certificate with Go's defaults and offers
no way to change that. Appliances on a local network commonly fail that check.

The case that raised this is a NanoKVM. The certificate measured on the device
is self-signed, and is issued to `localhost`, `127.0.0.1` and `::1` — not to the
name the device is reached by on the network. Two independent checks fail:
nobody vouches for the issuer, and the name does not match.

The second failure rules out the usual answer. Trusting an extra CA file repairs
the issuer check only; the name still does not match, so the connection is still
refused.

What travels over this connection matters too. The credential for a KVM device
is control of the machine attached to it, sent in a header on every request.

## Decision

**A server may name the one certificate it is allowed to present.** The optional
per-server key `tlsCertSha256` holds the SHA-256 fingerprint of the server's
certificate, as hex with or without colons, in either case. That is the form
`openssl x509 -fingerprint -sha256` prints.

**A pin replaces verification; it does not add to it.** When the key is set, the
presented certificate must be byte-for-byte the pinned one, and that comparison
is the whole check. Issuer, host name and validity dates are not examined.
Examining them would reject the devices this exists for, and an exact match on
the certificate already answers the question they stand in for. A pinned
certificate past its expiry date therefore keeps working.

**A mismatch stops the connection during the TLS handshake**, before any request
is sent, so a header or token never reaches an impostor. The error shows the
fingerprint presented and the one configured, because replacing the value is the
fix when the certificate was regenerated on purpose. The check runs on every
connection, including a resumed session.

**The key applies to every connection a command makes to the MCP endpoint**:
`run`, `inspect`, and the unauthenticated probe inside `inspect`. Without the
last one, the probe would fail on the certificate and `inspect` would report a
credential as accepted without having tested it.

**Two combinations are configuration errors**, because in each the key would
silently do nothing somewhere (ADR-0004):

- With an `http` URL there is no certificate to compare.
- With `oauth`, the login flow talks to authorization and token endpoints that
  are often on another host, where the pin cannot apply. The devices this exists
  for use static headers or a token command.

## Consequences

- When a device regenerates its certificate, the bridge stops connecting until
  the configuration is updated. This is the intended trade: the same failure is
  what an impostor would cause, and telling the two apart is the operator's call.
- The first fingerprint has to come from somewhere. Reading it over a network
  the operator trusts is what makes the pin mean anything. The READMEs give the
  command. mcp-bridge does not offer to record it, since a tool that trusts
  whatever it sees first has moved the decision away from the operator.
- A version of mcp-bridge older than this one rejects the key as unknown and
  stops loading the whole file (ADR-0004), so the upgrade comes before the
  configuration change.
- The transport keeps using HTTP/1.1. It already did, because a custom dialer
  turns off automatic HTTP/2 in Go; setting a TLS configuration changes nothing
  here.

## Alternatives considered

**A switch that disables verification (`insecureSkipVerify`).** It needs no
fingerprint and survives a regenerated certificate. Rejected: anyone able to
answer for the device's name on the local network would receive the credential,
and for this class of device that credential is control of another machine. The
pin takes about as much code and one extra step at setup.

**A per-server CA file.** Rejected as the answer here, on the measurement above:
it repairs the issuer check and leaves the name mismatch. It would suit a private
CA that issues correctly named certificates, which is a different case and can
be decided if one appears.

**Offering both a switch and a pin.** Rejected: mcp-bridge exists because unused
options made its predecessor unreadable (ADR-0004), and one mechanism covers the
need.

**Applying the pin to the OAuth endpoints as well.** Rejected: an authorization
server on another host presents a different certificate, so one pin cannot cover
both, and no device in view combines the two.

## References

- [ADR-0002](0002-answer-every-request.md) — why a refused connection still
  reaches the MCP client as a JSON-RPC error
- [ADR-0004](0004-single-file-strict-json-config.md) — strict decoding, and no
  key that applies in one place and is ignored in another
