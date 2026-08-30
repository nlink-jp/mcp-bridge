# GitHub MCP サーバへの接続

`https://api.githubcopilot.com/mcp/` にある GitHub のリモート MCP サーバは、
2つめの実例です。[Slack](slack-setup.ja.md) とは重要な点で異なります —
GitHub は dynamic client registration に対応していませんが、通常の GitHub
トークンを受け付けます。したがって経路が2つあり、安い方は OAuth アプリの
登録を一切必要としません。

2026-08-30 に実サーバに対して token-command 経路で検証済み。事前登録 OAuth App
の経路は GitHub の公開メタデータから起こしたもので、通しでの**検証はしていません**。

## discovery 経路はここでは成立しない

`"oauth": {}` は、エンドポイントの discovery とクライアントの登録を
mcp-bridge に依頼する指定です。前半は成功し、後半は成立しません:

```
$ curl -si -X POST https://api.githubcopilot.com/mcp/ ...
www-authenticate: Bearer error="invalid_request", ...
  resource_metadata="https://api.githubcopilot.com/.well-known/oauth-protected-resource/mcp/"
```

この RFC 9728 文書は認可サーバとして `https://github.com/login/oauth` を示し、
その RFC 8414 メタデータ
（`https://github.com/.well-known/oauth-authorization-server/login/oauth`）には
`registration_endpoint` がありません。GitHub は RFC 7591 を実装しておらず、
dynamic registration が呼ぶ先が存在しません。公開されているエンドポイントは:

| フィールド | 値 |
|---|---|
| `authorization_endpoint` | `https://github.com/login/oauth/authorize` |
| `token_endpoint` | `https://github.com/login/oauth/access_token` |
| `code_challenge_methods_supported` | `S256` |
| `registration_endpoint` | *無し* |

代わりに以下のいずれかの経路を使います。

## 経路A: 既存の GitHub CLI ログインを使う

`gh` が既に認証済みであれば、`tokenCommand` でそのトークンを借りられます。
OAuth アプリの登録は不要です:

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

mcp-bridge を起動する MCP クライアントが `gh` を含む shell の `PATH` を
継承しない場合は、コマンドを絶対パスで指定してください。

```bash
mcp-bridge inspect github
```

```
  server:     github-mcp-server github-mcp-server/remote-...
  protocol:   2025-06-18
  auth:       token-command (credential accepted)
```

重要なのは、単に *presented* ではなく `credential accepted` である点です。
このサーバは無認証の `initialize` を拒否するので、この表示はトークンが
実際に要求され受理されたことを意味します。

ログイン手順もトークンの保存もありません。`mcp-bridge list` は auth を
`token-command`、ログイン状態を無しとして表示し、サーバが 401 を返したときに
コマンドが再実行されます。

**代償は scope です。** トークンは `gh` が保持しているものそのままで、scope も
`gh auth status` が報告するとおり — このサーバのためではなく CLI のために
選ばれた集合です。[資格情報にできること](#資格情報にできること) を参照。

## 経路B: 事前登録した OAuth App

mcp-bridge が本来対象としている形です。MCP 用の資格情報を CLI のものと分けたい
場合、あるいは別の scope を持たせたい場合はこちらが正解です。

**1. アプリを登録する。** **Settings → Developer settings → OAuth Apps → New
OAuth App** で、authorization callback URL に次を正確に設定します:

```
http://127.0.0.1:7788/callback
```

Slack と違い GitHub は `http` のループバック callback を受け付けるので、
`callbackScheme` は既定のままで構いません。ポートは完全一致で照合され、
ログインごとに変えることはできないので、空いているポートを選んで両方に同じ
番号を書きます。7788 は例にすぎません。

**2. 設定を書く。**

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

サーバが protected-resource メタデータで公開している scope は `repo`、
`delete_repo`、`read:org`、`read:user`、`user:email`、`read:packages`、
`write:packages`、`read:project`、`project`、`gist`、`notifications`、
`workflow`、`codespace` です。使うつもりのあるものだけを要求してください。

**3. ログインする。**

```bash
mcp-bridge login github
mcp-bridge inspect github
```

OAuth App のトークンは、アプリ側でトークン失効を有効にしていない限り
`expires_in` も refresh token も伴いません。そのため mcp-bridge は
「期限不明」として記録し、GitHub が拒否するまで使い続けます — Slack と同じ
状況で、理由も同じです（[ADR-0003](../adr/0003-refreshless-tokens-do-not-expire.ja.md)）。

## tool の露出を絞る

GitHub MCP サーバは tool 露出の制御をリクエストヘッダで受け取ります。そして
`headers` は**加算的**です — `oauth` や `tokenCommand` を置き換えるのではなく
併存します（排他なのはその2つの間だけ）。したがって認証済みのサーバに対しても
制御を効かせられます:

| ヘッダ | 効果 |
|---|---|
| `X-MCP-Toolsets` | 有効にする toolset をカンマ区切りで指定し、既定集合を置き換える |
| `X-MCP-Tools` | 個別 tool の allowlist（カンマ区切り） |
| `X-MCP-Exclude-Tools` | 個別 tool の denylist（カンマ区切り） |
| `X-MCP-Readonly` | 読み取り系 tool のみ |

これをやる価値があるのは、tool 一覧が毎セッション、モデルが支払うコンテキスト
だからです。2026-08-30 の実測で、`tools/list` の応答は以下でした:

| 選択 | tools | 応答サイズ |
|---|---|---|
| `/x/all` | 89 | 242,040 B |
| 既定 toolset | 44 | 120,909 B |
| `X-MCP-Toolsets: repos,issues,pull_requests` | 38 | 105,735 B |
| `X-MCP-Tools` で12個指定 | 12 | 32,398 B |

toolset 単位の選択がほとんど効かない点に注意してください — 容量の大半は
`repos` / `issues` / `pull_requests` の tool 説明文にあり、それらは既定集合が
最初から含んでいます。数字を動かすのは個別指定です。

これを上流でやる方が下流でのフィルタより厳密に優れています — ペイロードが
そもそも線を通りません。mcp-bridge 自身が tool masking を持たない理由でも
あります（[RFP](../mcp-bridge-rfp.ja.md) 参照）。

破壊的な tool を1つだけ落とし、他はそのまま残す denylist の例:

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

`X-MCP-Toolsets` に未知の名前を書くと黙って無視されますが、`X-MCP-Tools` の
未知の名前はエラーになりサーバが起動しません。いずれにせよ上流で改名された
tool は照合されなくなるので、これらのリストは置きっぱなしにせず定期的に
見直す対象として扱ってください。

## 資格情報にできること

どこまで締めるかを決める前に知っておく価値があります。2026-08-30 の実測時点で、
**リポジトリを削除する tool はサーバに存在しません**。`/x/all` の全89 tools の
うち削除プリミティブは `delete_file` ただ1つで、ブランチ・タグ・リリース・
リポジトリの削除も force push もありません。

したがって GitHub が将来 tool を追加しても残る制限は、ヘッダではなく
トークンの scope です。リポジトリ削除が「未実装だから起きない」ではなく
「不可能」であるべきなら、`delete_repo` を持たない資格情報を使ってください —
それを持つ CLI ログインを借りるより、経路B か fine-grained personal access
token を選ぶ理由になります。

## MCP クライアントへの登録

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

## トラブルシューティング

**`no OAuth endpoints configured and nothing was discovered yet`** — 設定が
`"oauth": {}` になっています。GitHub に対して discovery は完了しません。
経路A か経路B を使ってください。

**`token command "gh auth token" failed`** — `gh` が認証されていないか、MCP
クライアントが `gh` を含まない `PATH` で mcp-bridge を起動しています。
`gh auth status` を確認し、PATH の問題であればコマンドを絶対パスで指定します。
macOS ではトークンが keychain にあるため、keychain に到達できないプロセスでも
ここで失敗します。

**`upstream returned HTTP 401`** — トークンが拒否されました。経路A なら
`gh auth status`、経路B なら `mcp-bridge login github` を実行します。

**ブラウザに "redirect_uri did not match" が出る**（経路B）— 登録した callback
URL と `callbackPort` が食い違っています。`127.0.0.1` のホスト名と `/callback`
のパスを含め、1文字単位で一致している必要があります。
