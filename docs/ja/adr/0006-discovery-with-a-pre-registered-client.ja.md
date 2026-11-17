# ADR-0006: 事前登録クライアントのエンドポイントとスコープを、サーバが公開する情報から決める

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-17 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | Microsoft Entra ID で保護された MCP サーバ（Microsoft Agent 365）へ接続できないという利用者からの報告 |
| Extends | [ADR-0001](0001-pre-registered-oauth-client.ja.md)。[ADR-0004](0004-single-file-strict-json-config.ja.md) の「`oauth` の中身が埋まっていれば事前登録クライアント」は、「`clientId` があれば事前登録クライアント」に改まる |

## Context

ADR-0001 は対象プロバイダとして Microsoft Entra ID を挙げたが、検証したのは Slack だけで、
Entra ID は「同じ形のはず」という推論にとどまっていた。利用者からの報告と 2026-11-17 の確認で、
同じ形ではない点が 4 つ見つかった。

**1. リダイレクト先のホスト名。** Entra ID は `http` のリダイレクト先を localhost 系にだけ許し、
管理画面の入力欄は `http://127.0.0.1` を受け付けない（マニフェストの直接編集が要る）。
そのため登録は `http://localhost:<port>/callback` になっていることが多い。mcp-bridge は `http` の
とき `127.0.0.1` を固定で使うので、照合に失敗する（`AADSTS50011`）。`localhost` ではポートは
照合されず、パスは照合される。

**2. メタデータの置き場所。** Entra ID の認可サーバのメタデータは
`<issuer>/.well-known/openid-configuration` にだけある。mcp-bridge が試す 2 か所（well-known を
パスの前に挿入する形）は、実測でどちらも 404 を返した。MCP 仕様は 3 か所を順に試すことを求めている。

**3. クライアントの登録。** Entra ID には動的登録（RFC 7591）の口が無い。mcp-bridge の設定は
「`"oauth": {}` で全部探して登録する」か「`authorizeUrl`・`tokenUrl`・`clientId` を全部書く」の
どちらかで、「クライアントは登録済み、他はサーバの公開情報から決める」という書き方ができない。
MCP 仕様は、事前登録のクライアント情報を登録方式の第 1 候補にしている。

**4. スコープ。** mcp-bridge は探索のとき認可サーバのスコープ一覧を使う。Entra ID の一覧は
`openid profile email offline_access` だけで、MCP サーバが要求する API のスコープを含まない。
MCP 仕様は、401 応答の `scope`、次に MCP サーバ自身のメタデータ（RFC 9728）の
`scopes_supported` の順で決めるとしている。Entra ID は `offline_access` を要求しないと
更新トークンを出さない。

確かめられていないことがある。Agent 365 のサーバが未認証の要求に返す応答そのものは、
テナントが無く観測できていない（ダミーのテナント ID には `400 TenantIdInvalid` が返る）。
以下は Microsoft の公開文書、Entra ID の公開メタデータ、MCP 仕様から決めたものである。

## Decision

**リダイレクト先のホスト名を設定で書けるようにする。** `callbackHost` は `"127.0.0.1"` か
`"localhost"` を取る。ADR-0001 が設定に持たせた 3 項目に続く 4 つ目である。書かないときは
これまでどおり、`http` なら `127.0.0.1`、`https` なら `localhost` になる。`localhost` のとき、
リスナーは `127.0.0.1` と `::1` の両方で待つ。ブラウザが `localhost` をどちらに解決しても届く。

**`clientId` だけを書いた `oauth` ブロックは、エンドポイントを探し、クライアントは登録しない。**
`authorizeUrl` と `tokenUrl` は、両方書くか、両方省くかのどちらかである。片方だけは設定エラーの
ままとする。ブロックに何か書いたら `clientId` が要る点は変わらない。

**エンドポイントを探す構成では `clientSecret` を書けない。** 探した先のトークンエンドポイントは
MCP サーバの応答が指す先であり、設定ファイルに書かれた先ではない。設定に書いたシークレットは、
設定に書いたトークンエンドポイントにだけ送る。シークレットを持つクライアントは URL を書く。

**探す順序は MCP 仕様（2025-11-25）に合わせる。** MCP サーバのメタデータは、401 応答が指す URL、
無ければ well-known の 2 か所（サーバのパスつき、ルート）の順に探す。認可サーバのメタデータは
仕様の 3 か所を順に試す。どれも得られないときは、これまでどおりサーバ自身のホストの認可サーバ
メタデータを試す。

**MCP サーバのメタデータは、そのサーバ自身のものでなければ使わない。** メタデータの `resource` が
設定の `url` と同じスキーム・ホスト・ポートで、パスが `url` のパスの先頭に一致することを確かめる。
一致しなければログインを止め、両方の値を示す。

**探して得た URL は `https` でなければ使わない。** 設定の `url` 自体が `http` のときに限り、
`http` の URL も使う。暗号化しない接続先を選んだのは設定を書いた人だからである。`https` の
サーバが、認可やトークンのやり取りを `http` へ落とすことはできない。ログインと更新の通信は、
`https` から `https` 以外へのリダイレクトにも従わない（ADR-0005 と同じ規則）。

**スコープは次の順で決める。** 設定の `scopes`、401 応答の `scope`、MCP サーバのメタデータの
`scopes_supported`、認可サーバのメタデータの `scopes_supported`。最後の段は MCP 仕様には無いが、
`"oauth": {}` の利用者はスコープを設定で書けないので、これまでの動きを残す。

**サーバが示したスコープには `offline_access` を足す。** 条件は、スコープが 401 応答か MCP サーバの
メタデータから決まり、認可サーバの一覧に `offline_access` があること。MCP 仕様の次期版の記述に
合わせたもので、Entra ID で更新トークンを得るために要る。設定に `scopes` を書いたときは足さない。
書かれたものをそのまま使う。

**登録済みクライアントの探索結果は、ログインのたびに取り直す。** 取り直しても相手側には何も
残らないので、古い結果を使い回す理由が無い。結果はこれまでどおり保存し、`run` がトークンの更新に
使う。動的登録の使い回しは、リダイレクト先に加えて `url` も一致するときに限る。

**ログインは、探して決めたことを出所とともに表示する。** 認可サーバ、スコープとその出所である。
実機で確かめられない相手に対して、利用者が貼り付けた出力だけで原因を切り分けるための材料になる。
表示とエラーにはサーバが返した文字列が入るので、端末に出す直前に制御文字を取り除く。

**これまでどおり行わないこと。** いずれも MCP 仕様や関連仕様が求めているが、行うと Entra ID に
繋がらなくなるか、確かめる手段が無い。

- `resource` パラメータ（RFC 8707）を付けない。Entra ID の v2 エンドポイントが `scope` との併用を
  拒否するという報告がある。必要な相手には `extraParams` で書ける。
- 認可サーバが PKCE 対応を公開しているか（`code_challenge_methods_supported`）を確かめない。
  Entra ID のメタデータにはこの項目が無い。PKCE そのものは常に使う（ADR-0001）。
- 認可サーバのメタデータの `issuer` を確かめない。Entra ID の共通エンドポイントは、置き換え前の
  文字列 `{tenantid}` を含む `issuer` を返す。
- `prompt=consent` を付けない。

## Consequences

- **要求するスコープを決めるのは、設定した MCP サーバである。** 登録済みクライアントが複数の API に
  同意済みの場合、サーバは自分のものでない API のスコープを名指しできる。Entra ID は `resource`
  パラメータでトークンの宛先を縛れないので、この構成では防げない。同意画面にスコープが表示される
  こと、`url` は利用者が自分で書いた接続先であることから、許容する。縛りたいときは `scopes` を書く。
- **Entra ID に対しては実機未検証のままリリースする。** 両 README にその区別を残す。確認は、
  報告した利用者の環境での試用による。
- この版より古い mcp-bridge は `callbackHost` を未知のキーとして拒否する（ADR-0004）。
  更新が先で、設定の変更が後である。
- `"oauth": {}` の利用者には 4 つの変化がある。この構成には設定で調整する手段が無いので、
  当てはまる場合はログインが止まる。
  - サーバがスコープを示す場合、要求するスコープが認可サーバの一覧からサーバの示したものに変わる。
  - サーバのメタデータの `resource` が `url` と一致しない、または無い場合、そのメタデータを
    使わない。MCP 公式 SDK を使うクライアントも同じ相手には繋がらない。
  - `url` が `https` なのに、探した先が `http` である場合、その先を使わない。
  - `--callback-port` で動的登録を使い回していた場合、更新後の最初のログインで一度だけ登録し直す。
- `::1` の同じポートを他のプロセスが使っていると、`::1` では待てない。そのときは `127.0.0.1`
  だけで続け、対処を 1 行表示する。その機械に IPv6 のループバックが無いだけなら何も表示しない。
  認可コードが他のプロセスに届いても、PKCE の検証値が無ければトークンに交換できない。
- 設定に `scopes` を書いた利用者は、更新トークンが要るなら `offline_access` を自分で書く。

## Alternatives considered

**`callbackHost` だけを足し、URL とスコープは手で書かせる。** 却下。サーバごとに違うスコープを
利用者が `curl` で調べて写すことになる。その値はサーバが公開しており、MCP 仕様が決め方を定めている。
メタデータの置き場所の問題（上記 2）は `"oauth": {}` にも残る。

**登録済みのリダイレクト先を 1 つのキー（`redirectUri`）でそのまま書かせる。** 却下。
`callbackPort`・`callbackScheme`・`--callback-port` と同じことを書く方法が 2 つになる。
Entra ID はポートを照合しないので、「登録した文字列」と「送る文字列」も一致しない。

**リダイレクト先のパスを設定できるようにする（`callbackPath`）。** 見送り。`/callback` 以外で
登録された事例が手元に無い。具体的な登録が示されたときに決める。

**エンドポイントを探す構成でも `clientSecret` を許す。** 却下。MCP サーバの応答ひとつで、
プロバイダとの間のシークレットが別の宛先へ送られる。

**MCP サーバのメタデータの `resource` を、`url` との完全一致で確かめる（RFC 9728 §3.3）。**
採らず、パスの先頭一致にした。MCP 公式 SDK と同じ規則である。対象のサーバを観測できないので、
SDK を使うクライアントが接続できる相手を mcp-bridge だけが拒否することを避ける。

**MCP 仕様どおり、認可サーバのスコープ一覧を使う段を無くす。** 却下。`"oauth": {}` には
スコープを書く手段が無く、動かなくなったときの逃げ道が無い。

**設定に `scopes` があっても `offline_access` を足す。** 却下。設定に書かれたものが、
送られるものと一致しなくなる。

## References

- [Model Context Protocol — Authorization (2025-11-25)](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization) — 探索の順序、登録方式の優先順位、スコープの決め方
- [Model Context Protocol — Authorization (draft)](https://modelcontextprotocol.io/specification/draft/basic/authorization) — `offline_access` の扱い
- RFC 9728 — Protected Resource Metadata（§3.3 が `resource` の検証）
- RFC 8414 — Authorization Server Metadata
- [Redirect URI (reply URL) outline and restrictions](https://learn.microsoft.com/en-us/entra/identity-platform/reply-url) — localhost の扱い
- [Microsoft identity platform and OAuth 2.0 authorization code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow) — `offline_access` と更新要求
- [`docs/ja/history/v0.3.0-design.ja.md`](../history/v0.3.0-design.ja.md) — この判断の詳細設計と確認の記録
