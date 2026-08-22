# mcp-bridge

Connect stdio-only MCP clients to Streamable HTTP MCP servers that require a
**pre-registered OAuth client**.

> **Status: not released.** Feature-complete and tested, but not yet packaged
> or verified against a live provider. See
> [Development status](#development-status).

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

## Direction

```
MCP client  ──stdio──▶  mcp-bridge  ──Streamable HTTP + OAuth──▶  MCP server
```

The downstream side is always stdio; the upstream side is always Streamable
HTTP. **The reverse direction is not supported** — mcp-bridge cannot expose a
stdio MCP server over HTTP.

## Install

```bash
# From source
make build
sudo install -m 0755 dist/mcp-bridge /usr/local/bin/mcp-bridge
```

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
connects, authenticates, and prints what the server says it is, without
wiring the bridge into an MCP client first.

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
| `headers` | Static headers sent with every request |
| `tokenCommand` | Run an external command to obtain a Bearer token |
| `oauth` with fields | OAuth2 authorization_code against a pre-registered client |
| `oauth: {}` | OAuth2 with endpoints and client discovered automatically (RFC 8414 + RFC 7591) |

Tokens are stored per server in `~/.config/mcp-bridge/state/<name>/tokens.json`
with mode 0600.

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

## Development status

Implementation follows the RFP
([English](docs/en/mcp-bridge-rfp.md) / [日本語](docs/ja/mcp-bridge-rfp.ja.md)):

| Phase | Scope | State |
|-------|-------|-------|
| Core | Config loader, stdio ⇄ Streamable HTTP relay, no-auth and static headers, `run` / `list` / `version` | done |
| Features | OAuth authorization_code, https loopback callback, RFC 8414 + RFC 7591 discovery, `login` / `logout` / `inspect`, `tokenCommand` | done |
| Release | Docs, ADRs, signing, Homebrew tap, umbrella integration | not started |

Every subcommand and every authentication mode in the configuration is
implemented. What remains before a release is packaging — signing,
notarization, the Homebrew tap — and an end-to-end run against a real provider,
which the tests approximate but do not replace.

## How the OAuth login works

`login` starts a loopback listener, opens a browser at the provider's
authorization endpoint, and exchanges the returned code for tokens. PKCE
(RFC 7636) is used on every login, including for confidential clients.

**Discovery.** With `"oauth": {}` the endpoints are found through the
protected-resource metadata the server advertises in its 401 challenge
(RFC 9728), falling back to the well-known metadata paths on the server's own
host, and a client is registered dynamically (RFC 7591). The result is cached
per server, so a fixed callback port reuses one registration instead of
creating a new client record on the provider at every login.

**The https callback.** Some providers — Slack in particular — reject an
`http://` loopback redirect URI when the OAuth app is registered. Setting
`"callbackScheme": "https"` makes the listener present an ephemeral
self-signed certificate that never leaves memory, and the redirect URI uses
`localhost`, which such providers accept where they reject the IP literal.
The browser shows a one-time "not secure" warning; continuing past it is the
expected path.

**Fixed ports.** A pre-registered OAuth app declares one exact redirect URI, so
its callback port cannot change between logins. Set `"callbackPort"`, or pass
`--callback-port` for a one-off. If that port is busy the login says so rather
than quietly picking another one the provider would refuse.

**Token lifetime.** A provider that returns neither `expires_in` nor a refresh
token has issued a token with no known expiry — Slack does this when token
rotation is disabled — and mcp-bridge uses it until the server rejects it.
Inventing an expiry for such a token would force a re-login every hour for no
reason. Where a refresh token exists, renewal is automatic.

## Documentation

- [Slack setup](docs/en/reference/slack-setup.md) — a worked example of the
  whole flow, verified against the live server
- [Design decisions](docs/en/adr/) — why the OAuth settings are explicit
  (0001), why a failed request is always answered (0002), why a token with no
  refresh has no expiry we can act on (0003), and why there is one strictly
  decoded JSON config file (0004)
- [RFP](docs/en/mcp-bridge-rfp.md) — the scope decision and what is
  deliberately out of it

## Build

```bash
make build        # dist/mcp-bridge
make build-all    # cross-compiled binaries in dist/
make test         # go test ./...
make check        # lint + test + docs mirror check
```

The project uses the Go standard library only — `go.mod` has zero require
lines. This is deliberate: mcp-bridge handles OAuth client secrets and access
tokens, so it does not take on third-party supply-chain surface. It is also why
the configuration format is JSON rather than TOML.

## License

MIT
