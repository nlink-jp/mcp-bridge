# Connecting to an MCP server protected by Microsoft Entra ID

Microsoft Entra ID is the third kind of provider after [Slack](slack-setup.md)
and [GitHub](github-setup.md). It has no dynamic client registration, so the
client is one you or your administrator registered — but unlike Slack, the
server publishes everything else, so the client ID and the callback are all
there is to write.

**This route was run against a tenant by a user, not by the maintainers.** On
2026-11-17 a user connected v0.3.0 to a Microsoft Agent 365 server
(`mcp_MailTools`) in their tenant with the configuration below, as it is
written: the login, `inspect` listing the server's 22 tools, and a renewal of
the token with the refresh token all worked. The examples on this page are what
that server answered. The maintainers have no Entra ID tenant; a server other
than Agent 365 may publish something else, and if your login fails,
[what to send](#if-it-does-not-work) is at the end.

## What Entra ID does differently

- **`http` redirect URIs are for `localhost` only.** The admin console does not
  accept `http://127.0.0.1` (that takes an edit of the app manifest), so apps
  are registered with `http://localhost:<port>/callback`. mcp-bridge's default
  for `http` is `127.0.0.1`; `"callbackHost": "localhost"` changes it. For
  `localhost`, Entra ID does not compare the port. It does compare the path,
  and mcp-bridge always uses `/callback`.
- **There is no registration endpoint**, so `"oauth": {}` cannot work.
- **A refresh token is issued only when `offline_access` is requested.** An
  Agent 365 server lists it among its own scopes. Where a server does not,
  mcp-bridge adds it to the scopes the server names.
- **A token is for one API**, chosen by the scopes requested. Each MCP server
  names its own, which is why two servers need two logins even with one client.

## 1. Have a client

You need the **Application (client) ID** of an app registration that:

- has `http://localhost:<port>/callback` among its redirect URIs, on the
  **Mobile and desktop applications** platform (a public client: no secret);
- has been granted the API permissions the MCP server requires.

If your organization already hands out a client ID for connecting MCP clients
to this server, use that one — and the port it was registered with, if you were
given one.

## 2. Configure

```json
{
  "servers": {
    "m365-mail": {
      "url": "https://agent365.svc.cloud.microsoft/agents/tenants/<tenant-id>/servers/mcp_MailTools",
      "oauth": {
        "clientId": "<application-client-id>",
        "clientAuthMethod": "none",
        "callbackHost": "localhost",
        "callbackPort": 8080
      }
    },
    "m365-calendar": {
      "url": "https://agent365.svc.cloud.microsoft/agents/tenants/<tenant-id>/servers/<name-of-the-calendar-server>",
      "oauth": {
        "clientId": "<application-client-id>",
        "clientAuthMethod": "none",
        "callbackHost": "localhost",
        "callbackPort": 8080
      }
    }
  }
}
```

There is no `authorizeUrl`, `tokenUrl` or `scopes`: with a `clientId` and no
URLs, mcp-bridge takes them from what the server publishes. There can be no
`clientSecret` in this form either — a secret is sent only to a token endpoint
written in the file.

The two servers share a client and a port. That works because only `login`
uses the port, and the logins are run one after the other.

## 3. Log in

```bash
mcp-bridge login m365-mail
```

Before the browser opens, the login prints what it found:

```text
Discovering the authorization server...
Authorization server: https://login.microsoftonline.com/organizations/v2.0 (named by https://agent365.svc.cloud.microsoft/.well-known/oauth-protected-resource/agents/tenants/<tenant-id>/servers/mcp_MailTools)
Client: <application-client-id> (the pre-registered one from the configuration)
Scopes: https://agent365.svc.cloud.microsoft/agents/tenants/<tenant-id>/servers/mcp_MailTools/.default openid profile offline_access (from the server's metadata)
Opening a browser to authorize m365-mail.
```

- **Authorization server** is the one the server named, and the document that
  said so. An Agent 365 server names Entra ID's `organizations` endpoint, which
  takes a work or school account of any tenant, and not your tenant's own.
- **Scopes** are what is about to be requested and who named them: the server's
  401 response, the server's metadata, or the authorization server's metadata.
  Where mcp-bridge added `offline_access`, the line says so.

Sign in and consent in the browser. The login ends with

```text
Logged in to m365-mail. expires <time>; renews automatically
```

`renews automatically` means a refresh token was issued: `run` and `inspect`
renew the access token by themselves when it expires, and keep the new refresh
token Entra ID returns with it. Repeat the login for each server.

## 4. Wire it into your MCP client

```json
{
  "mcpServers": {
    "m365-mail": {
      "type": "stdio",
      "command": "mcp-bridge",
      "args": ["run", "m365-mail"]
    }
  }
}
```

Sessions for several servers run at the same time; `run` does not use the
callback port.

## Choosing the scopes yourself

The server you configure names the scopes, and Entra ID issues a token for
whatever API they belong to. If the client has consent for more than this
server should be given, pin them:

```json
"scopes": ["<the-scope-this-server-needs>", "offline_access"]
```

Configured scopes are sent exactly as written. Nothing is added, so include
`offline_access` yourself or the login will not be able to renew.

## Troubleshooting

**`AADSTS50011` in the browser (the redirect URI does not match)** — the app is
registered with a different host or path. Check that it has
`http://localhost:<port>/callback`, and that the configuration has
`"callbackHost": "localhost"`. `http://127.0.0.1:...` and
`http://localhost:...` are different URIs to Entra ID.

**Any other `AADSTS` code** — the text beside it is Entra ID's own explanation,
shown in the browser or at the end of the login. Microsoft's
[error code reference](https://learn.microsoft.com/en-us/entra/identity-platform/reference-error-codes)
lists them.

**`could not reach <url>`** — the server did not answer at all. Check the `url`
and the network.

**`the metadata at ... describes ..., which is not this server`** — what the
server publishes names a different resource than the `url` in the
configuration. Check the `url` for a typo or a missing path segment first.

**`could not discover the authorization server`** — the list under `Tried:`
shows every place asked and what each returned. If the server publishes no
metadata, add `authorizeUrl` and `tokenUrl`
(`https://login.microsoftonline.com/<tenant-id>/oauth2/v2.0/authorize` and
`.../token`) and `scopes` to the block.

**The login must be repeated every hour** — no refresh token was issued. The
`Scopes:` line shows whether `offline_access` was requested; with configured
`scopes` it is yours to include.

**`Note: could not listen on [::1]:<port>`** — another program holds that port
on the IPv6 loopback address, and the browser may reach it instead of
mcp-bridge. If the page after signing in is not mcp-bridge's "Authorized",
replace `localhost` with `127.0.0.1` in the address bar and load it again.

## If it does not work

Send the complete output of `mcp-bridge login <name>`, from the command to the
last line. It holds the server's URL, your tenant ID and the client ID; it
holds no token, secret or authorization code. Together with the `AADSTS` code
from the browser, if there was one, it shows which step was reached and what
the server published.
