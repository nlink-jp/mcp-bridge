# ADR-0001: 事前登録クライアントを要求する OAuth プロバイダへの対応

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-23 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | 橋渡しする価値のあるプロバイダ（Slack、GitHub Apps、Microsoft Entra ID）が RFC 7591 に非対応で、MCP クライアント内蔵の OAuth では到達できない |

## Context

OAuth を話す MCP クライアントは Dynamic Client Registration (RFC 7591) 経由で
それを行う。認可サーバを discovery し、自分自身を登録して進む。Claude Code は
`type: "http"` のサーバに対してこれを行うため、DCR 対応プロバイダに対して
ブリッジが付け加えるものは何もない。

実務上重要なプロバイダはこれに対応していない。Slack、GitHub Apps、
Microsoft Entra ID、および大半のエンタープライズ SaaS は管理コンソールで手動登録した
OAuth アプリを要求し、登録時に3つの事項が固定される。DCR ベースのクライアントは
そのいずれも自分が制御できる前提で動く。

1. **正確な redirect URI**（ポート番号を含む）。エフェメラルポートを bind する
   クライアントは実行のたびに異なる redirect URI を生成し、プロバイダはそのすべてを拒否する。
2. **URI スキーム**。Slack はアプリ登録時に `http://` のループバック redirect URI を拒否し、
   `https://` のみを受け付ける。
3. **token endpoint の認証方式**。事前登録アプリは confidential（シークレットを持つ）で
   あり得て、そのシークレットをフォームボディで送るか、`Authorization: Basic` ヘッダで送るか、
   まったく送らないかを要求し得る。

いずれも信頼できる形では discovery できない。DCR 非対応プロバイダは
`token_endpoint_auth_methods_supported` を誤って公開するか、そもそも公開しないことが多く、
それを読んで推測すると token endpoint で不透明な 401 が返り、ユーザーに示せる手がかりが残らない。

## Decision

3項目すべてを設定で明示的に持ち、mcp-bridge は一切推測しない。

**`callbackPort`** はループバックリスナーを1つのポートに固定する。設定済みでポートが
使用中の場合、プロバイダが拒否するエフェメラルポートに黙ってフォールバックせず、
変更方法を含めて失敗を報告する。未設定の場合は OS が選ぶ。これは DCR では正しい挙動で、
選ばれたポートでクライアントが新規登録されるためである。

**`callbackScheme: "https"`** はループバックリスナーを TLS でラップする。証明書は
ログインごとに生成されるエフェメラルな自己署名 ECDSA P-256 で、ディスクには一切書かれない。
SAN は `127.0.0.1` / `::1` / `localhost` をカバーし、redirect URI には `localhost` 名を使う。
redirect URI をホスト名に制限するプロバイダは、IP リテラルを拒否する一方でこれを受け付けるためである。
ブラウザは1回だけ「安全でない」警告を出す。ログインはブラウザを開く前に説明を出力し、
コールバックサーバ自身のエラーログは破棄する。警告を生む当のハンドシェイクが、
成功したログインで `tls: bad certificate` を併せて出力しないようにするためである。

**`clientAuthMethod`** は `post`（既定）/ `basic` / `none` を選択する。RFC 6749 §2.3.1 の方式である。
設定検証は「シークレットなしの `basic`」と「シークレットありの `none`」を拒否するため、
矛盾は token endpoint ではなく読み込み時に捕まる。

PKCE (RFC 7636) はクライアント種別によらず全ログインで適用する。コストはゼロであり、
confidential client に対しても認可コード横取りの窓を閉じる。

Discovery は対応プロバイダ向けに引き続きサポートする。空の `"oauth": {}` ブロックが
RFC 9728 protected-resource メタデータ、RFC 8414 認可サーバメタデータ、RFC 7591 登録を要求する。
discovery 結果は登録に使った redirect URI と併せてキャッシュされるため、ポート固定であれば
毎回プロバイダ側にクライアントレコードを作らず1つの登録を再利用する。

## Consequences

- OAuth アプリを登録するために、利用者はプロバイダの管理者権限を持っている必要がある。
  この点は両 README の冒頭に明記した。権限がなければ本ツールでは解決できず、
  登録手順を迂回する方法はない。
- ループバックリスナー上の自己署名証明書は、https ログインのたびにブラウザ警告を意味する。
  ループバックには現実的な傍受の脅威がないため、代替案は「それらのプロバイダにまったく到達しない」ことになる。
- `callbackPort` の固定は他プロセスと衝突し得る。失敗は明示的で、
  `oauth.callbackPort` と `--callback-port` の両方を案内する。
- `clientAuthMethod` は手で正しく設定すべき項目が1つ増える。上記の検証規則は
  起こり得る2つの矛盾を捕まえる。矛盾はしないが誤っている選択は、
  プロバイダ自身のメッセージを載せた token endpoint エラーとして表面化する。

## Alternatives considered

**認可サーバのメタデータから認証方式を読む。** 却下。本 ADR が対象とするプロバイダは、
まさにそのフィールドを信頼できない形で公開する集団である。推測が外れると token endpoint で
失敗するが、そのエラーは推測が原因であることを示さない。

**常にエフェメラルコールバックポートを使う。** 却下。事前登録アプリをすべて使用不能にする。
それが対象利用者の全体である。

**ローカルで信頼される証明書で TLS 終端する**（生成した CA をシステムの信頼ストアに導入）。
却下。利用者のマシンに CA を導入することはブラウザ警告よりはるかに大きなセキュリティ判断であり、
本ツールが要求すべきでない権限を必要とする。

**https コールバックを非対応とし、`http://` を登録するよう案内する。** 却下。
Slack がそれを受け付けず、Slack こそが動機となった事例である。

## References

- RFC 6749 §2.3.1 — client password authentication
- RFC 7636 — PKCE
- RFC 7591 — Dynamic Client Registration
- RFC 8414 — Authorization Server Metadata
- RFC 9728 — Protected Resource Metadata
- [`docs/ja/reference/slack-setup.ja.md`](../reference/slack-setup.ja.md) — 実例
- 2026-08-23 に実 Slack MCP サーバで検証済み
