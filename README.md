# mcp-bridge

Connect stdio-only MCP clients to Streamable HTTP MCP servers that require a
**pre-registered OAuth client**.

> **Status: not released.** The scaffold is in place; the bridge is not
> implemented yet. See [Development status](#development-status).

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

The project is scaffolded; the commands are wired but return
`not implemented yet`. Implementation follows the RFP
([English](docs/en/mcp-bridge-rfp.md) / [日本語](docs/ja/mcp-bridge-rfp.ja.md)):

| Phase | Scope | State |
|-------|-------|-------|
| Core | Config loader, stdio ⇄ Streamable HTTP relay, no-auth and static headers, `run` / `list` / `version` | not started |
| Features | OAuth authorization_code, https loopback callback, RFC 8414 + RFC 7591 discovery, `login` / `logout` / `inspect`, `tokenCommand` | not started |
| Release | Docs, ADRs, signing, Homebrew tap, umbrella integration | not started |

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
