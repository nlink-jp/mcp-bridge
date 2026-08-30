# Connecting to the GitHub MCP server

GitHub's remote MCP server at `https://api.githubcopilot.com/mcp/` is the
second worked example, and it differs from [Slack](slack-setup.md) in a way
worth knowing before you start: GitHub offers no dynamic client registration,
but it does accept an ordinary GitHub token. That gives two routes, and the
cheaper one needs no OAuth app at all.

Verified against the live server on 2026-08-30 via the token-command route.
The pre-registered OAuth App route is written from GitHub's published metadata
and has **not** been exercised end to end.

## The discovery route cannot work here

`"oauth": {}` asks mcp-bridge to discover the endpoints and register a client.
The first half succeeds and the second cannot:

```
$ curl -si -X POST https://api.githubcopilot.com/mcp/ ...
www-authenticate: Bearer error="invalid_request", ...
  resource_metadata="https://api.githubcopilot.com/.well-known/oauth-protected-resource/mcp/"
```

That RFC 9728 document names `https://github.com/login/oauth` as the
authorization server, and its RFC 8414 metadata — at
`https://github.com/.well-known/oauth-authorization-server/login/oauth` — has
no `registration_endpoint`. GitHub does not implement RFC 7591, so there is
nothing for dynamic registration to call. The endpoints it does publish:

| Field | Value |
|---|---|
| `authorization_endpoint` | `https://github.com/login/oauth/authorize` |
| `token_endpoint` | `https://github.com/login/oauth/access_token` |
| `code_challenge_methods_supported` | `S256` |
| `registration_endpoint` | *absent* |

Use one of the two routes below instead.

## Route A: an existing GitHub CLI login

If `gh` is already authenticated, `tokenCommand` borrows its token and no OAuth
app has to be registered:

```json
{
  "servers": {
    "github": {
      "url": "https://api.githubcopilot.com/mcp/",
      "tokenCommand": {
        "command": "gh",
        "args": ["auth", "token"]
      }
    }
  }
}
```

Name the command by an absolute path if the MCP client that launches
mcp-bridge does not inherit a shell `PATH` that contains it.

```bash
mcp-bridge inspect github
```

```
  server:     github-mcp-server github-mcp-server/remote-...
  protocol:   2025-06-18
  auth:       token-command (credential accepted)
```

`credential accepted` — not merely *presented* — is the confirmation that
matters: this server rejects an unauthenticated `initialize`, so the label
means the token was demanded and taken.

There is no login step and no stored token; `mcp-bridge list` shows the auth
mode as `token-command` with no login state, and the command is re-run when the
server answers 401.

**The trade-off is scope.** The token is whatever `gh` holds, with whatever
scopes `gh auth status` reports — a set chosen for the CLI, not for this server.
See [What the credential can do](#what-the-credential-can-do).

## Route B: a pre-registered OAuth App

This is the case mcp-bridge was built for, and the right one when the MCP
credential should be separate from the CLI's, or carry different scopes.

**1. Register the app.** In **Settings → Developer settings → OAuth Apps → New
OAuth App**, set the authorization callback URL to exactly:

```
http://127.0.0.1:7788/callback
```

Unlike Slack, GitHub accepts an `http` loopback callback, so
`callbackScheme` can be left at its default. The port is matched exactly and
cannot vary between logins, so pick a free one and use the same number in both
places. 7788 is only an example.

**2. Write the configuration.**

```json
{
  "servers": {
    "github": {
      "url": "https://api.githubcopilot.com/mcp/",
      "oauth": {
        "authorizeUrl": "https://github.com/login/oauth/authorize",
        "tokenUrl": "https://github.com/login/oauth/access_token",
        "clientId": "<your-client-id>",
        "clientSecret": "<your-client-secret>",
        "callbackPort": 7788,
        "clientAuthMethod": "post",
        "scopes": ["repo", "read:org", "read:user", "user:email", "gist", "workflow"]
      }
    }
  }
}
```

The scopes the server advertises in its protected-resource metadata are
`repo`, `delete_repo`, `read:org`, `read:user`, `user:email`, `read:packages`,
`write:packages`, `read:project`, `project`, `gist`, `notifications`,
`workflow` and `codespace`. Ask only for what you intend to use.

**3. Log in.**

```bash
mcp-bridge login github
mcp-bridge inspect github
```

An OAuth App token comes back with neither `expires_in` nor a refresh token
unless the app has token expiration enabled, so mcp-bridge records "no known
expiry" and uses it until GitHub rejects it — the same case as Slack, for the
same reason ([ADR-0003](../adr/0003-refreshless-tokens-do-not-expire.md)).

## Limiting the tool surface

The GitHub MCP server takes tool-surface controls as request headers, and
`headers` is **additive**: it travels alongside `oauth` or `tokenCommand`
rather than replacing them (only those two are mutually exclusive). So the
controls can be set on an authenticated server:

| Header | Effect |
|---|---|
| `X-MCP-Toolsets` | Comma-separated toolsets to enable, replacing the default set |
| `X-MCP-Tools` | Comma-separated allowlist of individual tools |
| `X-MCP-Exclude-Tools` | Comma-separated denylist of individual tools |
| `X-MCP-Readonly` | Read-only tools only |

This is worth doing because a tool list is context the model pays for on every
session. Measured on 2026-08-30, the `tools/list` response was:

| Selection | Tools | Response |
|---|---|---|
| `/x/all` | 89 | 242,040 B |
| default toolset | 44 | 120,909 B |
| `X-MCP-Toolsets: repos,issues,pull_requests` | 38 | 105,735 B |
| `X-MCP-Tools` naming 12 tools | 12 | 32,398 B |

Note that toolset-level selection barely helps — the bulk is in the tool
descriptions of `repos`, `issues` and `pull_requests`, which the default set
already includes. Individual selection is what moves the number.

Doing this upstream is strictly better than filtering downstream: the payload
never crosses the wire. It is also why mcp-bridge has no tool masking of its
own — see the [RFP](../mcp-bridge-rfp.md).

A denylist that removes the one destructive tool while leaving everything else
in place:

```json
{
  "servers": {
    "github": {
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": {
        "X-MCP-Exclude-Tools": "delete_file"
      },
      "tokenCommand": { "command": "gh", "args": ["auth", "token"] }
    }
  }
}
```

An unknown name in `X-MCP-Toolsets` is ignored silently, while an unknown name
in `X-MCP-Tools` is an error that stops the server from starting. Either way a
tool renamed upstream will stop being matched, so treat these lists as
something to re-check, not to set and forget.

## What the credential can do

Worth knowing before deciding how much to lock down: as measured on
2026-08-30, **no tool in the server deletes a repository**. Across all 89 tools
in `/x/all` the only deletion primitive is `delete_file`; there is no branch,
tag, release or repository deletion, and no force push.

The limit that survives GitHub adding tools later is therefore the token's
scopes, not a header. If repository deletion must be impossible rather than
merely unimplemented, use a credential without `delete_repo` — which is an
argument for Route B, or for a fine-grained personal access token, over
borrowing a CLI login that carries it.

## Wire it into your MCP client

```json
{
  "mcpServers": {
    "github": {
      "type": "stdio",
      "command": "mcp-bridge",
      "args": ["run", "github"]
    }
  }
}
```

## Troubleshooting

**`no OAuth endpoints configured and nothing was discovered yet`** — the config
has `"oauth": {}`. Discovery cannot complete against GitHub; use Route A or B.

**`token command "gh auth token" failed`** — `gh` is not authenticated, or the
MCP client launched mcp-bridge with a `PATH` that does not contain it. Run
`gh auth status`, and name the command by absolute path if it is a PATH problem.
On macOS the token lives in the keychain, so a process that cannot reach the
keychain will fail here too.

**`upstream returned HTTP 401`** — the token was rejected. For Route A, run
`gh auth status`; for Route B, `mcp-bridge login github`.

**The browser shows "redirect_uri did not match"** (Route B) — the registered
callback URL and `callbackPort` disagree. They must match character for
character, including the `127.0.0.1` host and the `/callback` path.
