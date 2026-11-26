# ADR-0007: Renew tokens one process at a time, reading the stored login again each time

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-26 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | A user's improvement proposal (2026-11-26). With Atlassian Remote MCP, the first renewal after a period of disuse repeatedly failed with `invalid_grant`, and the only way out was to run `mcp-bridge login` again |

## Context

An MCP client starts each server as a stdio child process, once per session or
per window. Several `mcp-bridge run` processes for the same server running at
once is therefore the normal state. The reporter counted six per server
(two from Claude Desktop, two from Claude Code, two from gem-agent); on the
maintainer's machine github, nanokvm and obsidian each had six as well
(2026-11-26, checked with `ps`). They all share one `state/<server>/tokens.json`.

In v0.3.0, `oauth.Provider` reads `tokens.json` once at start-up and keeps the
refresh token in memory from then on. It does not read the file again before a
renewal, and its only exclusion is a `sync.Mutex`, which works inside one
process.

With a server that replaces the refresh token on every use and retires the old
one — the behaviour OAuth 2.1 and RFC 9700 recommend for public clients — this
breaks as follows.

1. Processes A and B start and both read the same `RT0`.
2. A renews with `RT0`, receives `RT1` and stores it. `RT0` is now spent.
3. B renews with the `RT0` it holds in memory and fails with `invalid_grant`.
4. A server that revokes the whole grant when a spent refresh token is
   presented again (reuse detection) also invalidates `RT1`. The login left in
   the file is dead, and every process started afterwards fails too.

It shows up most after a period of disuse because every process reads the same
`expires_at` from the same file. All their access tokens expire at about the
same moment, and the next action sends each process to renew within a few
seconds of the others.

The proposal's reproduction test (two `Provider`s sharing one file, renewing
against a test server that issues single-use refresh tokens) fails on v0.3.0
with the same error text as the report. Entra ID (five m365 servers), with the
same six processes per server, has recorded no failure at all. Microsoft's
documentation says Entra ID does not invalidate a used refresh token, which
fits the observation. No public document was found saying whether Atlassian
performs reuse detection. A server that issues single-use refresh tokens makes
B fail at step 3 with or without it, though, so the defect exists whatever
Atlassian does.

Looking into it turned up two more defects with the same cause.

- **Logging in again does not heal a running bridge.** A process that failed
  with `invalid_grant` keeps the spent refresh token and sends it again on the
  next renewal. When the user runs `mcp-bridge login` again, that process does
  not read the file, so it keeps failing until the MCP client is restarted.
- **A running bridge writes back a login that was logged out.** `logout`
  deletes `tokens.json`, but a running process renews with the refresh token in
  its memory at the next expiry and saves the result. The deleted login comes
  back.

## Decision

**The stored file (`tokens.json`) is the one authoritative copy of a server's
login.** What a process holds in memory is a copy of it; before renewing, and
before giving up, the process reads the file again and decides from that.

### 1. The file is rewritten by one process at a time

Reading `tokens.json`, deciding, and rewriting it happen under an exclusive OS
lock on `tokens.json.lock` (mode 0600) in the same directory: `flock(LOCK_EX)` on
Unix, `LockFileEx` on Windows, both called through the standard library's
`syscall`. No dependency is added.

- The lock is taken on a separate file, not on `tokens.json`. `SaveTokens`
  replaces the file by renaming a temporary file over it, so a lock on
  `tokens.json` would be left behind on a different file at every replacement.
- When a process holding the lock dies, the kernel releases it. There is no
  stale lock to clean up.
- `tokens.json.lock` is never deleted. Deleting a lock file another process may
  hold lets two processes lock two different files, and the exclusion is gone.
- Only reading the file again, deciding, the renewal request and saving happen
  under the lock. The renewal request already has a 30-second limit.

### 2. When a renewal is needed, read the file again under the lock and decide

When `Token()` finds that the token in memory will not do (there is a refresh
token and the access token has expired, or the server rejected it), it takes
the lock, reads the file again and decides in this order.

| State of the file | What happens |
|-------------------|--------------|
| Missing | Treated as logged out. No renewal; the error names `mcp-bridge login` |
| Unreadable or corrupt | No renewal; the error says why and names `mcp-bridge login` |
| The access token is usable (not empty, not the one the server rejected for this process, and within its expiry — or there is no refresh token, so no expiry is acted on) | Adopted without renewing (another process's renewal, or a new login) |
| None of the above, and there is a refresh token | Renewed with the refresh token in the file, and the result is saved |
| None of the above, and no refresh token | Cannot renew. The error says the server rejected the login and names `mcp-bridge login` |

"The one the server rejected for this process" is decided by remembering the
access token at the moment of the 401. Even while the same token is still in
the file, it is not adopted.

There is one exception. After this process renewed but could not save the
result (a read-only directory, a full disk), and the file is still as it was,
the newer copy is the one in memory. The renewal then uses the refresh token in
memory, not the one in the file. Using the file's would send a spent token to a
server that issues single-use refresh tokens even with only one process running
(v0.3.0 used the in-memory value, so this did not happen there).

A token with no refresh token that has not been rejected is returned as before,
without the lock and without reading the file again (ADR-0003). The first read
at start-up also happens under the lock.

### 3. `login` and `logout` rewrite the file under the same lock

`login` does not hold the lock while waiting for the browser; only saving the
tokens it obtained happens under the lock. `logout` deletes the file under the
lock. A login or logout that lands in the middle of a renewal can then neither
be overwritten by the renewal of the old login nor be undone by writing the
deleted login back.

### 4. A lock that cannot be used and a lock that is not released are handled differently

**When the lock file cannot be opened** (a read-only state directory, an OS
with no file locking), one line says so on stderr and step 2 runs without the
lock. In such an environment no process can use the lock, so waiting would
coordinate nothing. The lock never makes a renewal that would have worked fail
(the property the existing `TestUnwritableTokenFileWarnsButSucceeds` asks for).

**When another process holds the lock and it is not obtained within a limit
(45 seconds)**, the request fails ("another mcp-bridge process is renewing the
token for this server; try again"). No refresh token is sent. Waits add up
behind each holder in turn (the first renewal fails at its 30-second limit, the
second starts with the same refresh token, and a third reaches the limit
meanwhile), so renewing without the lock after the limit would present the same
refresh token twice at once against a slow server — the very thing this ADR is
meant to prevent. One failed request costs less than a lost login. The MCP
client still gets an answer, as a JSON-RPC error (ADR-0002). `logout` does the
same: if the lock is not obtained in time, nothing is deleted and it fails.

`login` alone, when the lock is not obtained in time, warns and saves without
the lock, so that a user who has just finished in the browser does not lose the
login. A renewal that slips in may still write the result of the old login, but
the user is watching at that moment and can run `login` again.

### 5. Renewals are recorded

One line goes to stderr when a renewal succeeds, and one when the renewal of
another process is adopted. No token value is printed. A renewal also says
whether the server issued a new refresh token. A user can then see for
themselves whether a server's refresh tokens are single-use, and after a
failure can tell when the last renewal succeeded and how many processes went to
renew.

## Consequences

- However many bridges run for one server, only one of them renews after an
  expiry and the others adopt its result. The stored login survives a server
  that issues single-use refresh tokens.
- After logging in again, a running bridge picks up the new login at its next
  renewal. The MCP client does not need a restart.
- A login that was logged out is not written back by a running bridge. A
  running bridge keeps using the access token in its memory until it expires
  (for a token with no expiry, until a 401). There is still no way to revoke a
  token on the server.
- While a process holding the lock is stopped (paused in a debugger, say),
  requests to the same server fail after waiting 45 seconds from the moment a
  renewal is needed, until that process resumes or exits.
- The state directory gains `tokens.json.lock`. It is empty and never deleted.
- Bridges of v0.3.0 and earlier take no lock. After upgrading, the race remains
  as long as an old bridge is running, so every MCP client using the server
  must be restarted. The release notes say so.
- An advisory lock may not work on a state directory on a network file system.
  The README says so.
- `LockFileEx` on Windows cannot be checked on a real machine (the maintainers
  have no Windows). Cross-compilation and static checks are as far as it goes,
  and the release notes say so. On Windows, replacing `tokens.json` (the rename)
  can fail while another process has the file open. Every read by `run` and
  `inspect` happens under the lock, so mcp-bridge processes do not collide with
  each other. If another program (a backup, an antivirus scanner) has it open at
  that moment, the save fails and the exception in decision 2 applies
  (unverified).
- On an OS with no file locking (aix, solaris and the like, none of them
  distributed), the lock is treated as unusable.
- Each renewal adds a line to stderr: about once per access-token lifetime
  (often an hour) per process.

## Alternatives considered

**Check the file's mtime on every `Token()` and read it again (no lock).**
Rejected. In the simultaneous renewal after a period of disuse, every process
reads the file again and then renews at the same time, so the race is not
prevented. Reading the file again (decision 2) works only together with the
lock.

**Read the file again and retry once when `invalid_grant` comes back.**
Rejected. With reuse detection, the grant is already revoked the moment the
spent token is first sent; retrying is too late.

**Move token handling into one resident broker process.** Rejected. It goes
far beyond mcp-bridge's narrow scope of relaying between stdio and HTTP (the
RFP). A file lock achieves the same.

**Keep a separate token file per process.** Rejected. Every process would need
its own login.

**Run fewer processes at once.** Rejected. The MCP client decides how many,
and neither the user nor mcp-bridge controls it.

**Wait for the lock without a limit.** Rejected. While the holder is stopped
(paused in a debugger, say), every client's requests to the server would hang
without an answer.

**After waiting up to a limit, warn and renew without the lock.** Rejected
(found in the design's verification). Waits add up behind each holder, so
whatever the limit, a slow server still sees the same refresh token presented
twice at once.

**Renew with the refresh token in memory when the file cannot be read.**
Rejected. A missing file is the result of a logout, and renewing from memory
writes the login back. A corrupt file is never produced by `SaveTokens`'s
replacement — only by editing it by hand — so asking for a new login is the
clearer answer.

## Out of scope

- **Keeping the refresh token alive** (the proposal's P3: a
  `mcp-bridge refresh` subcommand, or periodic renewal inside `run`). Whether
  failures after a long idle period remain once this ADR removes the race is
  for the reporter to establish with the procedure in section 4.2 of the
  proposal (wait 25 hours with no process running, then `inspect`). If they do
  remain, a new ADR will take it up, including its relation to ADR-0003's
  rejection of probing the server periodically.
- Limits no client can extend (the 24 hours of an Entra ID redirect URI
  registered as SPA, reauthentication demanded by Conditional Access). These
  belong to the setup guides, not to this ADR.

## References

- A user's improvement proposal, "mcp-bridge: when several mcp-bridge run
  processes run at once, the refresh token is lost and a new login is needed"
  (2026-11-26), including the analysis of the cause, a reproduction test and a
  PoC. The PoC was not taken in as is; it was rewritten following this ADR and
  the design note (organization ADR-024)
- RFC 9700 §4.14.2 — refresh token rotation for public clients, and detecting
  a spent token presented again
- OAuth 2.1 (draft) §4.3 — renewing with a refresh token, and rotation for
  public clients
- Microsoft Learn, "Refresh tokens in the Microsoft identity platform" — a used
  refresh token is not invalidated
- [ADR-0002](0002-answer-every-request.md) — answer every request
- [ADR-0003](0003-refreshless-tokens-do-not-expire.md) — tokens with no refresh token
- Design details: [v0.3.1 design note](../history/v0.3.1-design.md)
