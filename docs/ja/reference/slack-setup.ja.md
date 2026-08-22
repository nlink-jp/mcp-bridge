# Slack MCP サーバへの接続

mcp-bridge が存在する理由そのものの実例。動的クライアント登録に非対応のプロバイダ、
手動登録した OAuth アプリ、`https` ループバックコールバック、そして期限を持たないトークン。

手順は 2026-08-23 に実 Slack MCP サーバで検証済み。GitHub Apps と Microsoft Entra ID も
同じ形をとる。異なるのは管理コンソールとスコープ名だけである。

## 必要なもの

接続したい Slack ワークスペースでアプリを作成する権限。これがなければ `client_id` を
取得する手段がなく、この手順のどの部分も迂回できない。

## 1. Slack アプリを作成する

Slack API コンソールで、対象ワークスペースに新規アプリを作成する。

## 2. Redirect URL を登録する

**OAuth & Permissions → Redirect URLs** に、次を正確に追加する:

```
https://localhost:7777/callback
```

ここでは2点が重要で、そのどちらもが本ドキュメントの存在理由である。

**`https` でなければならない。** Slack は登録時に `http://` のループバック redirect URL を拒否する。
mcp-bridge は `"callbackScheme": "https"` でこれに応じる。ローカルのコールバックリスナーが、
メモリ内にのみ保持される自己署名証明書を提示するようになる。

**ポートは固定である。** 登録された redirect URL は完全一致で照合されるため、
コールバックリスナーは OS が提供する任意のポートを使えない。空いているポートを1つ選び、
ここと下記の設定で同じ番号を使うこと。7777 はあくまで例である。

ホスト名は `127.0.0.1` ではなく `localhost` でなければならない。Slack は名前を受け付け、
IP リテラルを拒否する。mcp-bridge は既に名前を使っており、証明書は両方をカバーしている。

## 3. User Token Scopes を宣言する

**OAuth & Permissions → Scopes → User Token Scopes** に、Slack MCP サーバが必要とする
スコープを追加する。読み取りと投稿の実用的な組み合わせ:

```
chat:write        channels:history   channels:read
groups:history    groups:read        im:history
im:read           mpim:history       mpim:read
search:read       users:read
```

使う予定のあるものだけを追加すること。ここに書くスコープはすべて、
ブリッジが運ぶことになる権限である。

## 4. 資格情報を控える

**Basic Information → App Credentials** から **Client ID** と **Client Secret** を取得する。

## 5. 設定を書く

`~/.config/mcp-bridge/config.json`:

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

token endpoint は `oauth.v2.user.access` — どのメタデータ文書も広告しない Slack 固有のパスであり、
discovery ではなくここに書き出している理由である。`callbackPort` は手順2で登録したポートと
一致させ、スコープは手順3と揃えること。

未知のキーはファイル読み込み時に拒否されるため、typo は設定を黙って無効化するのではなく
即座に失敗する。

## 6. ログインする

```bash
mcp-bridge login slack
```

ブラウザが開く。次の順序を想定しておくこと:

1. **証明書の警告**（`https://localhost:7777`）。ローカルのコールバックリスナーの
   自己署名証明書によるもの。そのまま進む。この警告は失敗ではなく想定された経路であり、
   mcp-bridge はブラウザを開く前にその旨を出力する。
2. **Slack の認可画面。** ワークスペースを確認して承認する。
3. **「Authorized」** — タブを閉じてターミナルに戻る。

成功すると、mcp-bridge は受け取ったトークンの種類を報告する:

```
Logged in to slack. no expiry reported and no refresh token: the token is used
until the server rejects it
```

このメッセージは Slack では正常である。[トークンの寿命](#トークンの寿命)を参照。

## 7. 動作を確認する

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

`inspect` は実際に接続して認証するため、設定の問題とクライアント側の問題を切り分ける
最短の手段である。

## 8. MCP クライアントに登録する

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

## トークンの寿命

Slack アプリでトークンローテーションを有効にしていない限り、Slack は期限も refresh token も
持たないトークンを発行する。mcp-bridge は「期限が分からない」を意味する `expires_at: 0` を保存し、
Slack が拒否するまでそのトークンを使う。これは意図的である——
[ADR-0003](../adr/0003-refreshless-tokens-do-not-expire.ja.md) を参照。
この種のトークンに期限を捏造すれば、Slack が何ら問題としていない資格情報に対して
毎時間の再ログインを要求することになる。

トークンが実際に失効した場合、次のリクエストは理由と対処を伴って失敗する:

```
upstream rejected the credentials (HTTP 401): the stored login for "slack" was
rejected by the server and there is no refresh token to renew it: run
"mcp-bridge login slack"
```

## トラブルシューティング

**`cannot listen on the configured callback port 7777`** — 他のプロセスがポートを保持している。
解放するか、Slack の redirect URL と `callbackPort` の両方を変更すること。
別ポートでの一時的な試行には `--callback-port` が設定を上書きするが、
その新しいポートも Slack に登録されていなければリダイレクトは拒否される。

**ブラウザに「redirect_uri did not match」が出る** — 登録した URL と `callbackPort` が
食い違っているか、片側のスキームが `http` になっている。`https` と `localhost` という名前を含め、
一字一句一致していなければならない。

**`token endpoint reported "invalid_code"`** — 認可コードが使用済みか期限切れである。
ログインをやり直すこと。コードは単回使用かつ短命である。

**ログインが待ち続けて何も起きない** — ブラウザが開かなかった。認可 URL は
ターミナルに出力されているので、手動で開くこと。
