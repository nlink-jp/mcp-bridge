# mcp-bridge

stdio しか話せない MCP クライアントから、**事前登録済み OAuth クライアント**を要求する
Streamable HTTP MCP サーバへ接続するためのブリッジ。

> 実 Slack MCP サーバ（事前登録 OAuth）と実 GitHub MCP サーバ（token command）で
> 検証済み。GitHub Apps と Microsoft Entra ID に対する事前登録 OAuth 経路は同じ形を
> とるため動作すると考えられますが、実エンドポイントでは未検証です。

## 対象となる状況

このツールが必要なのは、**MCP クライアント自身では接続できない場合だけ**です。
Claude Code は `type: "http"` の MCP サーバを OAuth Dynamic Client Registration
(RFC 7591) で直接扱えます。DCR に対応したサーバであれば、クライアントから直接接続してください。

mcp-bridge が埋めるのはその隙間 — **DCR に非対応**で、自分で登録した OAuth アプリ
（固定の `client_id` / `client_secret` / redirect URI）を要求するプロバイダです。
具体的には Slack 公式 MCP サーバ、GitHub Apps、Microsoft Entra ID などが該当します。

**対象プロバイダの管理者権限が必要です。** 管理コンソールで OAuth アプリを作成し、
スコープを宣言し、redirect URI を登録できる必要があります。それができない場合、
このツールでは解決できません — 登録手順を迂回する方法はありません。

この登録手順は OAuth 経路では回避できません。ただしサーバによっては、既に手元に
あるトークンを受け付けます。その場合 `tokenCommand` で何も登録せずに供給できます —
[GitHub セットアップ](docs/ja/reference/github-setup.ja.md) が両方の経路を並べて
説明しています。

## 方向

```
MCP クライアント  ──stdio──▶  mcp-bridge  ──Streamable HTTP + OAuth──▶  MCP サーバ
```

下流は常に stdio、上流は常に Streamable HTTP です。**逆方向には対応しません** —
mcp-bridge で stdio MCP サーバを HTTP 公開することはできません。

## インストール

```bash
# Homebrew
brew install nlink-jp/tap/mcp-bridge

# ソースから
make build
sudo install -m 0755 dist/mcp-bridge /usr/local/bin/mcp-bridge
```

リリースバイナリは macOS (arm64)、Linux (amd64, arm64)、Windows (amd64) 向けに提供します。
macOS バイナリは署名・notarize 済みです。

## 使い方

```
mcp-bridge run <name>       ブリッジ起動（MCP クライアントが起動する）
mcp-bridge login <name>     OAuth ブラウザログイン
mcp-bridge logout <name>    保存済みトークン削除
mcp-bridge list             定義済みサーバ一覧とログイン状態
mcp-bridge inspect <name>   接続して serverInfo とツール一覧を表示
mcp-bridge version          バージョン表示
```

フラグ:

| フラグ | 対象 | 説明 |
|------|-----------|-------------|
| `--config <path>` | 全コマンド | 設定ファイルパス（既定 `~/.config/mcp-bridge/config.json`） |
| `--callback-port <n>` | `login` | OAuth コールバックの固定ループバックポート。設定値を上書きする |

`--version` は `version` サブコマンドの別名として受け付けます。

OAuth サーバの初回は次のようになります:

```bash
mcp-bridge login slack      # ブラウザが開き、トークンを保存する
mcp-bridge inspect slack    # 接続を確認しツール一覧を表示する
mcp-bridge list             # どのサーバがログイン済みかを表示する
```

設定が正しいかを確かめる最短経路は `inspect` です。MCP クライアントに組み込む前に、
接続してサーバの自己申告を表示します。

**初回ログインの前でも動きます。** 多くの MCP サーバは `initialize` と `tools/list` を
資格情報なしで返し、`tools/call` でのみ認証を要求します。したがって OAuth アプリを
設定している最中でも、URL・プロトコルバージョン・ツール一覧という実質的な情報が得られます。
出力はどちらの視点を表示しているかを明示します:

```
  auth:       oauth (not logged in)
              showing what the server returns without a credential
```

資格情報を送った場合は、**それで何かが証明されたのか**も報告します。誰にでも答えるサーバでは
トークンについて何も分からないので、ログインが機能しているかのように書かずにそう述べます:

```
  auth:       oauth (credential presented)
              the server answers these calls without a credential too,
              so this does not confirm the credential works
```

`auth: oauth (credential accepted)` とだけ出た場合は、サーバが実際に資格情報を要求し、
それが受理されたという意味です。

## 設定

単一ファイル `~/.config/mcp-bridge/config.json`。未知のキーは読み込み時に拒否されるため、
typo は黙って無視されずにエラーになります。

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

認証方式は、どのキーが存在するかで決まります:

| キー | 挙動 |
|-----|------|
| 以下のいずれも無し | 認証なし |
| `headers` | 全リクエストに静的ヘッダを付与 |
| `tokenCommand` | 外部コマンドを実行して Bearer トークンを取得 |
| `oauth`（フィールドあり） | 事前登録クライアントに対する OAuth2 authorization_code |
| `oauth: {}` | エンドポイントとクライアントを自動 discovery（RFC 8414 + RFC 7591） |

排他なのは `oauth` と `tokenCommand` の間だけです。`headers` は排他ではなく、
どちらかを置き換えるのではなく併存します — オプションをリクエストヘッダで受け取る
サーバ（GitHub MCP サーバの tool 露出制御など）に、認証済みの接続で到達するための
経路です。`Authorization` という名前のヘッダはトークンを上書きします。

トークンはサーバごとに `~/.config/mcp-bridge/state/<name>/tokens.json`（mode 0600）へ保存されます。

## MCP クライアントへの登録

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

クライアント起動前に `mcp-bridge login slack` を一度実行してください。

## 対応しないもの

- **逆方向。** mcp-bridge で stdio MCP サーバを HTTP 公開することはできません。
  下流は常に stdio、上流は常に HTTP です。
- **ガバナンス・監査・テレメトリ。** 漏れではなく判断としてスコープ外です——
  [RFP](docs/ja/mcp-bridge-rfp.ja.md) を参照。前身の
  [mcp-guardian](https://github.com/nlink-jp/mcp-guardian) はこれらを持っています。
- **client_credentials フロー。** 手元のクライアントから起動される stdio ブリッジに、
  サーバ間認証の出番はありません。

## OAuth ログインの仕組み

`login` はループバックリスナーを起動し、プロバイダの認可エンドポイントをブラウザで開き、
返ってきたコードをトークンと交換します。PKCE (RFC 7636) は confidential client の場合も含め
常に使用します。

**Discovery。** `"oauth": {}` の場合、サーバが 401 チャレンジで広告する protected-resource
メタデータ (RFC 9728) からエンドポイントを特定し、見つからなければサーバ自身のホスト上の
well-known パスにフォールバックします。クライアントは動的登録 (RFC 7591) されます。結果は
サーバごとにキャッシュされるため、コールバックポートを固定していれば毎回新しいクライアント
レコードをプロバイダ側に作らず、1つの登録を再利用します。

**https コールバック。** 一部のプロバイダ——特に Slack——は OAuth アプリ登録時に `http://` の
ループバック redirect URI を拒否します。`"callbackScheme": "https"` を指定するとリスナーは
メモリ外に出ないエフェメラル自己署名証明書を提示し、redirect URI には（IP リテラルを拒否する
プロバイダでも受け付けられる）`localhost` を使います。ブラウザは1回だけ「安全でない」警告を
出しますが、そのまま進むのが正常な経路です。

**ポート固定。** 事前登録した OAuth アプリは redirect URI をちょうど1つ宣言するため、
コールバックポートはログインごとに変えられません。`"callbackPort"` を設定するか、
一時的な指定として `--callback-port` を渡してください。そのポートが使用中の場合、
プロバイダが拒否する別ポートを黙って選ぶのではなく、その旨を報告します。

**トークンの寿命。** `expires_in` も refresh token も返さないプロバイダは「期限の分からない
トークン」を発行しています——Slack はトークンローテーション無効時にこれをします——ので、
mcp-bridge はサーバに拒否されるまでそのまま使います。ここで期限を捏造すると、理由もなく
毎時間の再ログインを強いることになります。refresh token がある場合の更新は自動です。

## ドキュメント

- [Slack セットアップ](docs/ja/reference/slack-setup.ja.md) — 全手順の実例。実サーバで検証済み
- [GitHub セットアップ](docs/ja/reference/github-setup.ja.md) — GitHub に対して discovery が
  成立しない理由、成立する2経路、tool 露出の絞り方
- [設計判断](docs/ja/adr/) — OAuth 設定を明示的にした理由 (0001)、失敗したリクエストに
  必ず応答する理由 (0002)、refresh を持たないトークンに対処可能な期限が存在しない理由 (0003)、
  設定ファイルを strict decode の JSON 1本にした理由 (0004)
- [RFP](docs/ja/mcp-bridge-rfp.ja.md) — スコープ判断と、意図的に外したもの

## ビルド

```bash
make build        # dist/mcp-bridge
make build-all    # クロスコンパイル済みバイナリを dist/ へ
make test         # go test ./...
make check        # lint + test + docs mirror check
```

Go 標準ライブラリのみを使用します — `go.mod` の require 行はゼロです。これは意図的な選択で、
mcp-bridge は OAuth の client_secret とアクセストークンを扱うため、サードパーティの
サプライチェーン面を持ち込みません。設定形式に TOML ではなく JSON を選んだ理由も同じです。

## ライセンス

MIT
