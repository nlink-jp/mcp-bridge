# Connecting to the Slack MCP server

A worked example of the case mcp-bridge exists for: a provider with no dynamic
client registration, an OAuth app registered by hand, an `https` loopback
callback, and a token that never expires.

The steps were verified against the live Slack MCP server on 2026-08-23.
GitHub Apps and Microsoft Entra ID follow the same shape; only the console and
the scope names differ.

## What you need

Permission to create an app in the Slack workspace you want to reach. Without
it there is no way to obtain a `client_id`, and no part of this can be worked
around.

## 1. Create the Slack app

In the Slack API console, create a new app in the target workspace.

## 2. Register the redirect URL

Under **OAuth & Permissions → Redirect URLs**, add exactly:

```
https://localhost:7777/callback
```

Two details matter here, and both are the reason this document exists.

**It must be `https`.** Slack refuses an `http://` loopback redirect URL at
registration time. mcp-bridge answers this with `"callbackScheme": "https"`,
which makes the local callback listener present a self-signed certificate held
only in memory.

**The port is fixed.** A registered redirect URL is matched exactly, so the
callback listener cannot take whatever port the operating system offers. Pick
any free port and use the same number here and in the configuration below.
7777 is only an example.

The host name must be `localhost`, not `127.0.0.1`: Slack accepts the name and
rejects the IP literal. mcp-bridge already uses the name, and its certificate
covers both.

## 3. Declare the user token scopes

Under **OAuth & Permissions → Scopes → User Token Scopes**, add the scopes the
Slack MCP server needs. A read-and-post working set:

```
chat:write        channels:history   channels:read
groups:history    groups:read        im:history
im:read           mpim:history       mpim:read
search:read       users:read
```

Add only what you intend to use — every scope here is a capability the bridge
will carry.

## 4. Copy the credentials

From **Basic Information → App Credentials**, take the **Client ID** and
**Client Secret**.

## 5. Write the configuration

In `~/.config/mcp-bridge/config.json`:

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
        "callbackPort": 7777,
        "callbackScheme": "https",
        "clientAuthMethod": "post",
        "scopes": [
          "chat:write", "channels:history", "channels:read",
          "groups:history", "groups:read", "im:history", "im:read",
          "mpim:history", "mpim:read", "search:read", "users:read"
        ]
      }
    }
  }
}
```

The token endpoint is `oauth.v2.user.access` — a Slack-specific path that no
metadata document advertises, which is why it is written out here rather than
discovered. `callbackPort` must match the port registered in step 2, and the
scopes should match step 3.

Unknown keys are rejected when the file is read, so a typo fails immediately
rather than disabling a setting silently.

## 6. Log in

```bash
mcp-bridge login slack
```

A browser opens. Expect this sequence:

1. **A certificate warning** on `https://localhost:7777`. This is the
   self-signed certificate for the local callback listener; continue past it.
   The warning is the expected path, not a failure — mcp-bridge prints a note
   saying so before opening the browser.
2. **The Slack authorization screen.** Confirm the workspace and approve.
3. **"Authorized"** — close the tab and return to the terminal.

On success mcp-bridge reports what kind of token it received:

```
Logged in to slack. no expiry reported and no refresh token: the token is used
until the server rejects it
```

That message is normal for Slack. See [Token lifetime](#token-lifetime).

## 7. Confirm it works

```bash
mcp-bridge inspect slack
```

```
https://mcp.slack.com/mcp
  server:     Slack MCP 1.0.0
  protocol:   2025-06-18
  auth:       oauth

19 tool(s):
  slack_send_message   Sends a message to a Slack channel or user...
  ...
```

`inspect` connects and authenticates for real, so it is the fastest way to
tell a configuration problem from a client problem.

## 8. Wire it into your MCP client

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

## Token lifetime

Unless token rotation is enabled on the Slack app, Slack issues a token with
neither an expiry nor a refresh token. mcp-bridge stores `expires_at: 0`,
meaning "no known expiry", and uses the token until Slack rejects it. This is
deliberate — see [ADR-0003](../adr/0003-refreshless-tokens-do-not-expire.md).
Inventing an expiry for such a token would demand a fresh login every hour for
a credential Slack is perfectly happy with.

When the token is genuinely revoked, the next request fails with the reason and
the fix:

```
upstream rejected the credentials (HTTP 401): the stored login for "slack" was
rejected by the server and there is no refresh token to renew it: run
"mcp-bridge login slack"
```

## Troubleshooting

**`cannot listen on the configured callback port 7777`** — another process
holds the port. Free it, or change both the Slack redirect URL and
`callbackPort`. For a one-off attempt on a different port, `--callback-port`
overrides the configuration, but the new port must also be registered with
Slack or the redirect will be refused.

**The browser shows "redirect_uri did not match"** — the registered URL and
`callbackPort` disagree, or the scheme is `http` on one side. They must match
character for character, including `https` and the `localhost` name.

**`token endpoint reported "invalid_code"`** — the authorization code was
already used or has expired. Run the login again; codes are single-use and
short-lived.

**The login waits and nothing happens** — the browser did not open. The
authorization URL is printed in the terminal; open it by hand.
