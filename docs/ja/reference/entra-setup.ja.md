# Microsoft Entra ID で保護された MCP サーバへの接続

Microsoft Entra ID は、[Slack](slack-setup.ja.md)、[GitHub](github-setup.ja.md) に続く 3 つめの
種類のプロバイダです。dynamic client registration が無いので、クライアントは自分か管理者が
登録したものを使います。ただし Slack と違い、それ以外はサーバがすべて公開しているので、
書くのはクライアント ID とコールバックの設定だけです。

**この経路をテナントに対して実行したのは利用者で、メンテナではありません。** 2026-11-17 に、
ある利用者が自分のテナントの Microsoft Agent 365 のサーバ（`mcp_MailTools`）へ、下の設定を
そのまま使って v0.3.0 で接続しました。ログイン、`inspect` でのツール 22 個の取得、
refresh token によるトークンの更新が、すべて動いています。このページの例は、そのサーバが
返した内容です。同じ利用者から、続けて SharePoint、Teams、OneDrive の Agent 365 のサーバにも
接続できたと報告がありました。こちらは応答の内容を受け取っていないので、例には載せていません。
メンテナの手元には Entra ID のテナントがありません。Agent 365 以外のサーバは
別の内容を公開しているかもしれないので、ログインに失敗したときに
[送ってほしいもの](#うまくいかないとき)を末尾に書きました。

## Entra ID が他と違う点

- **`http` の redirect URI は `localhost` にだけ認められます。** 管理画面は `http://127.0.0.1` を
  受け付けない（アプリのマニフェストを直接編集する必要がある）ので、アプリは
  `http://localhost:<port>/callback` で登録されています。mcp-bridge の `http` の既定は
  `127.0.0.1` で、`"callbackHost": "localhost"` で切り替えます。`localhost` の場合、Entra ID は
  ポートを照合しません。パスは照合し、mcp-bridge は常に `/callback` を使います。
- **登録の口がありません。** そのため `"oauth": {}` は動きません。
- **refresh token は `offline_access` を要求したときにだけ発行されます。** Agent 365 の
  サーバは、自分のスコープの一覧にこれを含めています。含めていないサーバの場合は、
  mcp-bridge がサーバの示したスコープに足します。
- **トークンは 1 つの API のためのものです。** どの API かは要求したスコープで決まります。
  MCP サーバはそれぞれ自分のスコープを示すので、クライアントが 1 つでも、サーバが 2 つあれば
  ログインは 2 回になります。

## 1. クライアントを用意する

次の条件を満たすアプリ登録の **アプリケーション (クライアント) ID** が必要です。

- redirect URI に `http://localhost:<port>/callback` があること。プラットフォームは
  **モバイル アプリケーションとデスクトップ アプリケーション**（シークレットを持たない
  public client）
- MCP サーバが要求する API のアクセス許可が付与されていること

組織がこのサーバに MCP クライアントを繋ぐためのクライアント ID を既に配っているなら、
それを使います。登録されたポートを知らされているなら、そのポートも使います。

## 2. 設定する

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

`authorizeUrl`、`tokenUrl`、`scopes` はありません。`clientId` があって URL が無いとき、
mcp-bridge はそれらをサーバの公開情報から決めます。この形では `clientSecret` も書けません。
シークレットを送るのは、設定ファイルに書いたトークンエンドポイントだけです。

2 つのサーバは、クライアントとポートを共有しています。ポートを使うのは `login` だけで、
ログインは 1 つずつ順に実行するので、これで動きます。

## 3. ログインする

```bash
mcp-bridge login m365-mail
```

ブラウザが開く前に、見つけたものを表示します。

```text
Discovering the authorization server...
Authorization server: https://login.microsoftonline.com/organizations/v2.0 (named by https://agent365.svc.cloud.microsoft/.well-known/oauth-protected-resource/agents/tenants/<tenant-id>/servers/mcp_MailTools)
Client: <application-client-id> (the pre-registered one from the configuration)
Scopes: https://agent365.svc.cloud.microsoft/agents/tenants/<tenant-id>/servers/mcp_MailTools/.default openid profile offline_access (from the server's metadata)
Opening a browser to authorize m365-mail.
```

- **Authorization server** は、サーバが示した認可サーバと、それを示した文書です。
  Agent 365 のサーバが示すのは Entra ID の `organizations` で、自分のテナント専用のものでは
  ありません。どのテナントの職場・学校アカウントでも受け付ける入口です。
- **Scopes** は、これから要求するスコープと、それを示したものです。サーバの 401 応答、
  サーバのメタデータ、認可サーバのメタデータのいずれかになります。mcp-bridge が
  `offline_access` を足したときは、この行にそう表示されます。

ブラウザでサインインし、同意します。ログインは次の行で終わります。

```text
Logged in to m365-mail. expires <time>; renews automatically
```

`renews automatically` は、refresh token が発行されたという意味です。アクセストークンが
切れると `run` と `inspect` が自分で更新し、そのとき Entra ID が返す新しい refresh token に
置き換えます。ログインはサーバごとに繰り返します。

## 4. MCP クライアントに組み込む

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

複数のサーバのセッションは同時に動かせます。`run` はコールバックのポートを使いません。

## スコープを自分で決める

スコープを示すのは設定したサーバで、Entra ID は、そのスコープが属する API のトークンを
発行します。クライアントが、そのサーバに渡したい範囲より多くに同意済みのときは、
スコープを固定します。

```json
"scopes": ["<the-scope-this-server-needs>", "offline_access"]
```

設定したスコープは、書いたとおりに送られます。何も足されないので、`offline_access` は
自分で書いてください。書かないと、ログインは自動で更新できません。

## トラブルシューティング

**ブラウザに `AADSTS50011` が出る（redirect URI が一致しない）** — アプリが別のホスト名かパスで
登録されています。`http://localhost:<port>/callback` が登録されていること、設定に
`"callbackHost": "localhost"` があることを確かめます。Entra ID にとって
`http://127.0.0.1:...` と `http://localhost:...` は別の URI です。

**それ以外の `AADSTS` コード** — 横に出ている文が Entra ID 自身の説明で、ブラウザか、
ログインの最後に表示されます。Microsoft の
[エラーコード一覧](https://learn.microsoft.com/ja-jp/entra/identity-platform/reference-error-codes)に
載っています。

**`could not reach <url>`** — サーバから応答がありません。`url` とネットワークを確かめます。

**`the metadata at ... describes ..., which is not this server`** — サーバが公開している情報が、
設定の `url` とは別のリソースを指しています。まず `url` の打ち間違いやパスの欠けを確かめます。

**`could not discover the authorization server`** — `Tried:` の下に、問い合わせた場所と
それぞれの結果が並びます。サーバがメタデータを公開していないなら、ブロックに `authorizeUrl` と
`tokenUrl`（`https://login.microsoftonline.com/<tenant-id>/oauth2/v2.0/authorize` と
`.../token`）と `scopes` を書きます。

**1 時間ごとにログインし直しになる** — refresh token が発行されていません。`Scopes:` の行で、
`offline_access` を要求したかどうかが分かります。`scopes` を設定している場合は、自分で
書く必要があります。

**`Note: could not listen on [::1]:<port>`** — 別のプログラムが IPv6 のループバックアドレスで
そのポートを使っていて、ブラウザが mcp-bridge ではなくそちらに繋がることがあります。
サインインのあとに出た画面が mcp-bridge の「Authorized」でなければ、アドレス欄の
`localhost` を `127.0.0.1` に書き換えて開き直します。

## うまくいかないとき

`mcp-bridge login <name>` の出力を、コマンドから最後の行まで全部送ってください。出力には
サーバの URL、テナント ID、クライアント ID が含まれます。トークン、シークレット、認可コードは
含まれません。ブラウザに `AADSTS` コードが出ていればそれも添えると、どの手順まで進み、
サーバが何を公開していたかが分かります。
