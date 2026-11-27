# mcp-bridge

Connect stdio-only MCP clients to Streamable HTTP MCP servers that require a
**pre-registered OAuth client**.

> Verified against the live Slack MCP server (pre-registered OAuth) and the
> live GitHub MCP server (token command). The route for servers protected by
> Microsoft Entra ID was run by a user against Microsoft Agent 365 in their
> tenant — login, tool listing and token renewal; see the
> [Entra ID guide](docs/en/reference/entra-setup.md). The pre-registered OAuth
> route for GitHub Apps has not been exercised end to end.

## Who this is for

You need this only when your MCP client cannot reach the server on its own.
Claude Code already handles `type: "http"` MCP servers with OAuth Dynamic
Client Registration (RFC 7591) — for those servers, use the client directly.

mcp-bridge covers the gap: providers that **do not support DCR** and instead
require an OAuth app you register yourself, with a fixed `client_id`,
`client_secret`, and redirect URI. In practice that means the official Slack
MCP server, GitHub Apps, Microsoft Entra ID, and similar enterprise SaaS.

**This requires administrative access to the provider.** You must be able to
create an OAuth app in its admin console, declare its scopes, and register a
redirect URI. If you cannot do that, this tool will not help you — there is no
way around the registration step.

That registration step is unavoidable for the OAuth route. Some servers also
accept a token you already hold, which `tokenCommand` can supply without
registering anything — the [GitHub setup](docs/en/reference/github-setup.md)
covers both routes side by side.

## Direction

```
MCP client  ──stdio──▶  mcp-bridge  ──Streamable HTTP + OAuth──▶  MCP server
```

The downstream side is always stdio; the upstream side is always Streamable
HTTP. **The reverse direction is not supported** — mcp-bridge cannot expose a
stdio MCP server over HTTP.

## Install

```bash
# Homebrew
brew install nlink-jp/tap/mcp-bridge

# From source
make build
sudo install -m 0755 dist/mcp-bridge /usr/local/bin/mcp-bridge
```

Release binaries are built for macOS (arm64), Linux (amd64, arm64) and Windows
(amd64). The macOS binary is signed and notarized.

## Usage

```
mcp-bridge run <name>       Start the bridge (launched by the MCP client)
mcp-bridge login <name>     OAuth browser login
mcp-bridge logout <name>    Delete stored tokens
mcp-bridge list             List configured servers and their login state
mcp-bridge inspect <name>   Connect and print serverInfo and the tool list
mcp-bridge version          Print version
```

Flags:

| Flag | Applies to | Description |
|------|-----------|-------------|
| `--config <path>` | all commands | Config file path (default `~/.config/mcp-bridge/config.json`) |
| `--callback-port <n>` | `login` | Fixed loopback port for the OAuth callback, overriding the config |

`--version` is accepted as an alias for the `version` subcommand.

A first session with an OAuth server looks like this:

```bash
mcp-bridge login slack      # opens a browser, stores the tokens
mcp-bridge inspect slack    # confirms the connection and lists the tools
mcp-bridge list             # shows which servers are logged in
```

`inspect` is the quickest way to tell whether a configuration is right: it
connects and prints what the server says it is, without wiring the bridge into
an MCP client first.

It works **before** the first login. Many MCP servers open a session and answer
`tools/list` without a credential and only require one for `tools/call`, so
there is real information to be had — the URL, the protocol version, the tool
list — while the OAuth app is still being set up. The output says which view it
is showing:

```
  auth:       oauth (not logged in)
              showing what the server returns without a credential
```

And when a credential *is* sent, `inspect` reports whether that proved
anything. A server that would have answered anyone teaches you nothing about
your token, so it says so rather than implying the login works:

```
  auth:       oauth (credential presented)
              the server answers these calls without a credential too,
              so this does not confirm the credential works
```

A plain `auth: oauth (credential accepted)` means the server did demand the
credential and took it.

## Configuration

A single file, `~/.config/mcp-bridge/config.json`. Unknown keys are rejected at
load time, so a typo fails loudly instead of being silently ignored.

```json
{
  "servers": {
    "slack": {
      "url": "https://mcp.slack.com/mcp",
      "oauth": {
        "authorizeUrl": "https://slack.com/oauth/v2_user/authorize",
        "tokenUrl": "https://slack.com/api/oauth.v2.user.access",
        "clientId": "<your-client-id>",
        "clientSecret": "<your-client-secret>",
        "scopes": ["chat:write", "channels:history"],
        "callbackPort": 7777,
        "callbackScheme": "https",
        "clientAuthMethod": "post"
      }
    },
    "entra": {
      "url": "https://mcp.example.com/tenants/<tenant-id>/mcp",
      "oauth": {
        "clientId": "<your-client-id>",
        "clientAuthMethod": "none",
        "callbackHost": "localhost",
        "callbackPort": 8080
      }
    },
    "discovered": {
      "url": "https://mcp.example.com/v1/mcp",
      "oauth": {}
    },
    "plain": {
      "url": "https://mcp.example.com/mcp"
    },
    "static": {
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer <your-api-key>" }
    },
    "external-token": {
      "url": "https://mcp.example.com/mcp",
      "tokenCommand": { "command": "gcloud", "args": ["auth", "print-access-token"] }
    }
  }
}
```

Authentication is chosen by which key is present:

| Key | Behaviour |
|-----|-----------|
| none of the below | No authentication |
| `headers` | Static headers sent with every request (not the MCP protocol headers, which mcp-bridge sets) |
| `tokenCommand` | Run an external command to obtain a Bearer token |
| `oauth` with `clientId` and both URLs | OAuth2 authorization_code against a pre-registered client |
| `oauth` with `clientId` and no URLs | The same client, with the endpoints and scopes taken from what the server publishes |
| `oauth: {}` | OAuth2 with endpoints discovered and a client registered automatically (RFC 7591) |

An `oauth` block with anything in it needs `clientId`. `authorizeUrl` and
`tokenUrl` are written together or left out together. Without them there can
be no `clientSecret`: a secret is sent only to a token endpoint written in the
file, never to a discovered one.

`oauth` and `tokenCommand` are mutually exclusive. `headers` is not: it
accompanies either of them rather than replacing them, which is how a server
that takes options as request headers — the GitHub MCP server's tool-surface
controls, for one — reaches a connection that is also authenticated. A header
named `Authorization` overrides the token.

Tokens are stored per server in `~/.config/mcp-bridge/state/<name>/tokens.json`
with mode 0600.

### Devices with a self-signed certificate

Devices on a local network often serve HTTPS with a self-signed certificate,
frequently issued to `localhost` rather than the name you reach them by. Usual
verification refuses those, and trusting an extra CA would not fix the name.
Pin the certificate with `tlsCertSha256` instead:

```json
{
  "servers": {
    "device": {
      "url": "https://kvm.example.internal/api/mcp",
      "headers": { "Authorization": "Bearer <your-api-key>" },
      "tlsCertSha256": "<fingerprint of the device's certificate>"
    }
  }
}
```

Read the fingerprint over a network you trust, and paste the part after `=`:

```bash
openssl s_client -connect kvm.example.internal:443 </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256
```

It is hex, with or without colons, in either case. The bridge then accepts that
exact certificate and no other. Issuer, host name and expiry are not checked. A
different certificate stops the connection before any header or token is sent,
and a redirect to anything but `https` is refused.

When a device regenerates its certificate, the error shows the new fingerprint.
Make sure the change was yours, then replace the value.

`tlsCertSha256` needs an `https` URL and cannot be combined with `oauth`. Update
mcp-bridge before adding the key: older versions reject it as unknown and stop
reading the whole file.

## MCP protocol versions

MCP 2026-07-28 replaced the `initialize` handshake with a version carried on
every request. mcp-bridge does not convert between the two: the upstream sees
the era your client opens, so a client that speaks both reaches servers of
either kind. What the specification asks of an HTTP client is added on the way:

- a request in the newer protocol carries its `MCP-Protocol-Version` with
  `Mcp-Method`, `Mcp-Name` and the `Mcp-Param-*` headers a tool's schema asks
  for (`x-mcp-header`); in a handshake session, every message after
  `initialize` carries the negotiated `MCP-Protocol-Version` (a batch goes
  without it). A request whose headers cannot be built — an argument a header
  cannot carry, a tool call without a tool name — is answered with an error and
  not sent;
- a tool whose `x-mcp-header` declaration is invalid is left out of the tool
  list, and the bridge's log says which and why;
- if the server refuses a tool call because those headers no longer match its
  tools, mcp-bridge reads the tool list once and sends the call again;
- a request the server leaves unanswered — an empty answer, a stream that ends
  without the answer, an answer larger than 8 MiB — gets an error instead of
  waiting for ever.

These headers are mcp-bridge's to set: `headers` in the config cannot name
them. `inspect` opens the server in the newer protocol when it offers it, and
its `protocol:` line says which — `2026-07-28 (modern)` or `2025-06-18
(legacy)`.

## MCP client integration

```json
{
  "mcpServers": {
    "slack": {
      "type": "stdio",
      "command": "mcp-bridge",
      "args": ["run", "slack"]
    }
  }
}
```

Run `mcp-bridge login slack` once before the client starts.

## What is not supported

- **The reverse direction.** mcp-bridge cannot expose a stdio MCP server over
  HTTP. Downstream is always stdio and upstream is always HTTP.
- **Governance, auditing and telemetry.** Out of scope by decision rather than
  omission — see the [RFP](docs/en/mcp-bridge-rfp.md). The predecessor,
  [mcp-guardian](https://github.com/nlink-jp/mcp-guardian), has them.
- **The client_credentials flow.** A stdio bridge launched by a local client
  has no use for machine-to-machine authentication.

## How the OAuth login works

`login` starts a loopback listener, opens a browser at the provider's
authorization endpoint, and exchanges the returned code for tokens. PKCE
(RFC 7636) is used on every login, including for confidential clients.

**Discovery.** With `"oauth": {}`, or with a `clientId` and no URLs, the
endpoints are found the way the MCP specification lays out. The server's 401 challenge points at its
protected-resource metadata (RFC 9728), or that metadata is at a well-known
location on the server; the document names the authorization server, whose own
metadata gives the endpoints. A server that predates this is found through the
well-known metadata paths on its own host. The challenge is asked for with the
opening an ordinary MCP client sends, built by the shared module nlink-jp/mcp
(`server/discover` with its protocol headers), once and without a credential.

With `"oauth": {}` a client is then registered dynamically (RFC 7591). The
registration is stored per server, so a fixed callback port reuses it instead
of creating a new client record on the provider at every login. With a
`clientId` nothing is registered, and discovery runs again at each login. The
providers this suits have no registration endpoint at all — Microsoft Entra ID
is one — and publish everything else.

`login` prints what discovery decided: the authorization server and the
document that named it, the client, and the scopes with where they came from.

The metadata has to be the server's own: its `resource` must match the
configured `url`, and the login stops when it does not.

Discovery requests the scopes the server asks for: the `scope` in its 401
challenge, otherwise `scopes_supported` in its metadata, otherwise the
authorization server's list. `offline_access` is added to the first two when
the authorization server offers it, so that a refresh token is issued.
Configured `scopes` replace all of that and are sent exactly as written —
include `offline_access` yourself if the provider needs it for a refresh
token. The server you configure is what names the scopes, so write `scopes`
when the client has consent for more APIs than this server should be given.

A URL that discovery is handed is used only if it is `https`, unless the
configured `url` is itself `http`. The login and the token refresh do not
follow a redirect from `https` to anything else.

**The https callback.** Some providers — Slack in particular — reject an
`http://` loopback redirect URI when the OAuth app is registered. Setting
`"callbackScheme": "https"` makes the listener present an ephemeral
self-signed certificate that never leaves memory, and the redirect URI uses
`localhost`, which such providers accept where they reject the IP literal.
The browser shows a one-time "not secure" warning; continuing past it is the
expected path.

**The callback host.** The redirect URI names `127.0.0.1` for `http` and
`localhost` for `https`. Set `"callbackHost"` to `"localhost"` or `"127.0.0.1"`
when the provider registered the other one — Microsoft Entra ID accepts an
`http` redirect URI only for `localhost`. With `localhost` the listener waits
on both `127.0.0.1` and `::1`, whichever the browser resolves the name to.
Update mcp-bridge before adding the key: older versions reject it as unknown.

**Fixed ports.** A pre-registered OAuth app declares one exact redirect URI, so
its callback port cannot change between logins. Set `"callbackPort"`, or pass
`--callback-port` for a one-off. If that port is busy the login says so rather
than quietly picking another one the provider would refuse.

**Token lifetime.** A provider that returns neither `expires_in` nor a refresh
token has issued a token with no known expiry — Slack does this when token
rotation is disabled — and mcp-bridge uses it until the server rejects it.
Inventing an expiry for such a token would force a re-login every hour for no
reason. Where a refresh token exists, renewal is automatic.

**Several bridges for one server.** An MCP client starts a bridge per session
or window, so several often run for the same server. They share its stored
login: when the access token expires, one of them renews it under a lock
(`tokens.json.lock` beside the token file) and the others use the result, so a
server that issues single-use refresh tokens does not lose the login. Each
renewal is one line on stderr — in your MCP client's log — saying when the new
token expires and whether the server issued a new refresh token; no token is
printed. After `mcp-bridge login`, running bridges use the new login at their
next renewal, with no restart. If another bridge keeps the lock for more than
45 seconds, the request fails saying so, and can be tried again; `logout`
does the same rather than delete a login a bridge is renewing, while `login`
saves the new login anyway and says so. The lock may
not work when `~/.config/mcp-bridge` is on a network file system. Bridges of
version 0.3.0 and earlier take no lock: after upgrading, restart every MCP
client that uses such a server.

## Documentation

- [Slack setup](docs/en/reference/slack-setup.md) — a worked example of the
  whole flow, verified against the live server
- [GitHub setup](docs/en/reference/github-setup.md) — why discovery cannot work
  against GitHub, the two routes that do, and how to limit the tool surface
- [Entra ID setup](docs/en/reference/entra-setup.md) — a pre-registered client
  with everything else discovered, with what a Microsoft Agent 365 server
  answered in a user's tenant
- [Design decisions](docs/en/adr/) — why the OAuth settings are explicit
  (0001), why a failed request is always answered (0002), why a token with no
  refresh has no expiry we can act on (0003), why there is one strictly
  decoded JSON config file (0004), why a device's certificate is pinned
  rather than left unverified (0005), why a pre-registered client can take
  its endpoints and scopes from what the server publishes (0006), why
  bridges for one server renew its token one at a time (0007), why both
  MCP protocol eras pass through unchanged (0008), and why the discovery
  probe sends the shared module's opening rather than its own `initialize`
  (0009)
- [RFP](docs/en/mcp-bridge-rfp.md) — the scope decision and what is
  deliberately out of it

## Build

```bash
make build        # dist/mcp-bridge
make build-all    # cross-compiled binaries in dist/
make test         # go test ./...
make check        # lint + test + docs mirror check
```

The project uses the Go standard library and one module of the same
organization that itself uses only the standard library:
[nlink-jp/mcp](https://github.com/nlink-jp/mcp), the MCP protocol layer shared by
nlink-jp's servers and relays. This is deliberate: mcp-bridge handles OAuth
client secrets and access tokens, so it does not take on third-party
supply-chain surface. It is also why
the configuration format is JSON rather than TOML.

## License

MIT
