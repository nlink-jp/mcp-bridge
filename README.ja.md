# mcp-bridge

stdio しか話せない MCP クライアントから、**事前登録済み OAuth クライアント**を要求する
Streamable HTTP MCP サーバへ接続するためのブリッジ。

> 実 Slack MCP サーバ（事前登録 OAuth）と実 GitHub MCP サーバ（token command）で
> 検証済み。Microsoft Entra ID で保護されたサーバへの経路は、利用者が自分のテナントの
> Microsoft Agent 365 に対して実行し、ログイン、ツールの取得、トークンの更新が動いています
> （[Entra ID の手順](docs/ja/reference/entra-setup.ja.md)）。GitHub Apps に対する
> 事前登録 OAuth 経路は、通しでは検証していません。

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

**初回ログインの前でも動きます。** 多くの MCP サーバはセッションの開始と `tools/list` に
資格情報なしで答え、`tools/call` でのみ認証を要求します。したがって OAuth アプリを
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

認証方式は、どのキーが存在するかで決まります:

| キー | 挙動 |
|-----|------|
| 以下のいずれも無し | 認証なし |
| `headers` | 全リクエストに静的ヘッダを付与（mcp-bridge が付ける MCP のプロトコルのヘッダは指定できない） |
| `tokenCommand` | 外部コマンドを実行して Bearer トークンを取得 |
| `oauth`（`clientId` と URL 2つ） | 事前登録クライアントに対する OAuth2 authorization_code |
| `oauth`（`clientId` があり URL 無し） | 同じクライアントで、エンドポイントとスコープをサーバの公開情報から決める |
| `oauth: {}` | エンドポイントを discovery し、クライアントを自動登録（RFC 7591） |

中身のある `oauth` ブロックには `clientId` が必要です。`authorizeUrl` と `tokenUrl` は、
両方書くか、両方省きます。省いたときは `clientSecret` を書けません。シークレットを送るのは
設定ファイルに書いたトークンエンドポイントだけで、discovery で得た先には送りません。

排他なのは `oauth` と `tokenCommand` の間だけです。`headers` は排他ではなく、
どちらかを置き換えるのではなく併存します — オプションをリクエストヘッダで受け取る
サーバ（GitHub MCP サーバの tool 露出制御など）に、認証済みの接続で到達するための
経路です。`Authorization` という名前のヘッダはトークンを上書きします。

トークンはサーバごとに `~/.config/mcp-bridge/state/<name>/tokens.json`（mode 0600）へ保存されます。

### 自己署名証明書の機器

ローカルネットワーク上の機器は、自己署名の証明書で HTTPS を提供していることが多く、
発行先も実際に到達する名前ではなく `localhost` になっていることがよくあります。通常の検証は
これを拒否し、CA を追加で信頼しても名前の不一致は直りません。代わりに `tlsCertSha256` で
証明書を固定します:

```json
{
  "servers": {
    "device": {
      "url": "https://kvm.example.internal/api/mcp",
      "headers": { "Authorization": "Bearer <your-api-key>" },
      "tlsCertSha256": "<機器の証明書の指紋>"
    }
  }
}
```

指紋は信頼できるネットワークで読み取り、`=` より後ろを貼り付けます:

```bash
openssl s_client -connect kvm.example.internal:443 </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256
```

形式は 16 進で、コロンの有無と大文字小文字は問いません。ブリッジはその証明書だけを受け入れ、
発行者・ホスト名・有効期限は確認しません。別の証明書が提示されると、ヘッダやトークンを
送る前に接続を止めます。`https` 以外へのリダイレクトも拒否します。

機器が証明書を作り直すと、エラーに新しい指紋が表示されます。自分で行った変更であることを
確かめてから、値を差し替えてください。

`tlsCertSha256` には `https` の URL が必要で、`oauth` とは併用できません。このキーを追加する
前に mcp-bridge を更新してください。古い版は未知のキーとして拒否し、ファイル全体を読み込めなく
なります。

## MCP のプロトコルの版

MCP 2026-07-28 は、`initialize` の握手をやめ、要求ごとに版を載せる形に変わりました。
mcp-bridge は二つの形を変換しません。上流には、クライアントが開いた形のまま届くので、
両方を話すクライアントなら、どちらのサーバにもつながります。仕様が HTTP のクライアントに
求めることは、途中で足します:

- 新しい形の要求には、その要求の `MCP-Protocol-Version` と、`Mcp-Method`・`Mcp-Name`、
  ツールのスキーマが求める `Mcp-Param-*` ヘッダ（`x-mcp-header`）を付けます。握手のセッションでは、
  `initialize` の後のすべてのメッセージに交渉した `MCP-Protocol-Version` を付けます（バッチには
  付けません）。ヘッダを作れない要求（ヘッダに載せられない引数、ツール名の無いツールの呼び出し）は、
  送らずにエラーで答えます。
- `x-mcp-header` の宣言が不正なツールは一覧から除き、どのツールをなぜ除いたかをログに出します。
- それらのヘッダがサーバのツールと合わなくなってツールの呼び出しを断られたら、ツールの一覧を
  1 回読み直して、呼び出しを送り直します。
- サーバが答えないままにした要求（空の応答、答えを送らずに終わったストリーム、8 MiB を超える応答）
  には、いつまでも待たせずにエラーで答えます。

これらのヘッダは mcp-bridge が付けるもので、設定の `headers` では指定できません。`inspect` は、
サーバが新しい形に対応していればその形で開き、`protocol:` の行にどちらかを示します
（`2026-07-28 (modern)` または `2025-06-18 (legacy)`）。

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

**Discovery。** `"oauth": {}` の場合と、`clientId` があって URL が無い場合は、MCP 仕様が定める順で
エンドポイントを探します。サーバの
401 チャレンジが protected-resource メタデータ (RFC 9728) の場所を示すか、サーバの well-known の
場所にそれがあり、その文書が認可サーバを示し、認可サーバ自身のメタデータがエンドポイントを
示します。この方式より古いサーバは、サーバ自身のホスト上の well-known パスから探します。

`"oauth": {}` の場合は、続けてクライアントを動的登録 (RFC 7591) します。登録はサーバごとに
保存されるため、コールバックポートを固定していれば毎回新しいクライアントレコードをプロバイダ側に
作らず、1つの登録を再利用します。`clientId` がある場合は何も登録せず、ログインのたびに探し直します。
この構成が合うのは、登録の口を持たず、他はすべて公開しているプロバイダです。Microsoft Entra ID が
その一つです。

`login` は、discovery で決まったことを表示します。認可サーバとそれを示した文書、クライアント、
スコープとその出所です。

メタデータは、そのサーバ自身のものでなければ使いません。`resource` が設定の `url` と
一致しないときは、ログインを止めます。

discovery は、サーバが求めるスコープを要求します。401 チャレンジの `scope`、無ければ
メタデータの `scopes_supported`、それも無ければ認可サーバの一覧です。前の 2 つには、
認可サーバが `offline_access` を提供していれば足します。refresh token を得るためです。
設定に `scopes` を書いた場合は、これらの代わりに、書いたとおりに送ります。refresh token に
`offline_access` が要るプロバイダでは、自分で書いてください。スコープを名指しするのは設定した
サーバなので、クライアントがそのサーバに渡したい範囲より多くの API に同意済みのときは、
`scopes` を書いて縛ります。

discovery で得た URL は `https` の場合だけ使います。例外は、設定の `url` 自体が `http` の
ときです。ログインとトークンの更新は、`https` から `https` 以外へのリダイレクトに従いません。

**https コールバック。** 一部のプロバイダ——特に Slack——は OAuth アプリ登録時に `http://` の
ループバック redirect URI を拒否します。`"callbackScheme": "https"` を指定するとリスナーは
メモリ外に出ないエフェメラル自己署名証明書を提示し、redirect URI には（IP リテラルを拒否する
プロバイダでも受け付けられる）`localhost` を使います。ブラウザは1回だけ「安全でない」警告を
出しますが、そのまま進むのが正常な経路です。

**コールバックのホスト名。** redirect URI のホスト名は、`http` なら `127.0.0.1`、`https` なら
`localhost` です。プロバイダに登録されているのがもう一方のときは、`"callbackHost"` に
`"localhost"` か `"127.0.0.1"` を書きます。Microsoft Entra ID は `http` の redirect URI を
`localhost` にしか認めません。`localhost` のとき、リスナーは `127.0.0.1` と `::1` の両方で待つので、
ブラウザがどちらに解決しても届きます。このキーを追加する前に mcp-bridge を更新してください。
古い版は未知のキーとして拒否します。

**ポート固定。** 事前登録した OAuth アプリは redirect URI をちょうど1つ宣言するため、
コールバックポートはログインごとに変えられません。`"callbackPort"` を設定するか、
一時的な指定として `--callback-port` を渡してください。そのポートが使用中の場合、
プロバイダが拒否する別ポートを黙って選ぶのではなく、その旨を報告します。

**トークンの寿命。** `expires_in` も refresh token も返さないプロバイダは「期限の分からない
トークン」を発行しています——Slack はトークンローテーション無効時にこれをします——ので、
mcp-bridge はサーバに拒否されるまでそのまま使います。ここで期限を捏造すると、理由もなく
毎時間の再ログインを強いることになります。refresh token がある場合の更新は自動です。

**同じサーバに複数の bridge が動く場合。** MCP クライアントはセッションや画面ごとに bridge を
起動するので、同じサーバの bridge が何本も動くのが普通です。これらは保存されたログインを共有します。
アクセストークンの期限が切れると、1 本がロック（トークンファイルの隣の `tokens.json.lock`）を取って
更新し、残りはその結果を使います。このため、使い捨ての refresh token を発行するサーバでもログインは
失われません。更新のたびに stderr（MCP クライアントのログ）に 1 行出ます。新しいトークンの期限と、
サーバが新しい refresh token を発行したかどうかが分かります。トークンの値は出ません。
`mcp-bridge login` をやり直すと、動いている bridge は次の更新で新しいログインを使います。再起動は
要りません。ほかの bridge が 45 秒を超えてロックを持ち続けた場合、その要求はその旨のエラーになり、
やり直せます。`logout` も同じで、bridge が更新中のログインは消さずにエラーになります。`login` は
その旨を表示したうえで新しいログインを保存します。`~/.config/mcp-bridge` がネットワークファイルシステム上にあると、ロックが効かない
ことがあります。0.3.0 以前の bridge はロックを取らないので、更新したら、そのサーバを使う MCP
クライアントをすべて再起動してください。

## ドキュメント

- [Slack セットアップ](docs/ja/reference/slack-setup.ja.md) — 全手順の実例。実サーバで検証済み
- [GitHub セットアップ](docs/ja/reference/github-setup.ja.md) — GitHub に対して discovery が
  成立しない理由、成立する2経路、tool 露出の絞り方
- [Entra ID セットアップ](docs/ja/reference/entra-setup.ja.md) — 事前登録クライアントで、
  他はすべて discovery する構成。利用者のテナントで Microsoft Agent 365 のサーバが返した内容つき
- [設計判断](docs/ja/adr/) — OAuth 設定を明示的にした理由 (0001)、失敗したリクエストに
  必ず応答する理由 (0002)、refresh を持たないトークンに対処可能な期限が存在しない理由 (0003)、
  設定ファイルを strict decode の JSON 1本にした理由 (0004)、機器の証明書を検証なしにせず
  固定する理由 (0005)、事前登録クライアントのエンドポイントとスコープをサーバの公開情報から
  決められるようにした理由 (0006)、同じサーバの bridge がトークンを 1 本ずつ更新する理由 (0007)、
  MCP のプロトコルの二つの時代をそのまま通す理由 (0008)
- [RFP](docs/ja/mcp-bridge-rfp.ja.md) — スコープ判断と、意図的に外したもの

## ビルド

```bash
make build        # dist/mcp-bridge
make build-all    # クロスコンパイル済みバイナリを dist/ へ
make test         # go test ./...
make check        # lint + test + docs mirror check
```

Go 標準ライブラリと、標準ライブラリだけで書かれた同じ組織のモジュール 1 つだけを使います:
nlink-jp のサーバと中継が共有する MCP のプロトコル層、[nlink-jp/mcp](https://github.com/nlink-jp/mcp) です。
これは意図的な選択で、mcp-bridge は OAuth の client_secret とアクセストークンを扱うため、
サードパーティのサプライチェーン面を持ち込みません。設定形式に TOML ではなく JSON を選んだ理由も同じです。

## ライセンス

MIT
