# ADR-0009: OAuth の探索の打診は、自前の `initialize` ではなく nlink-jp/mcp が組む開始の要求を送る

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-27 |
| Binds | mcp-bridge |
| Supersedes | ADR-0008 §10 |
| Decision makers | nlink-jp maintainers |
| Triggered by | 組織 ADR-030 §6: nlink-jp/mcp の外のリポジトリは `initialize` も `server/discover` も自分で扱わない。2026-11-27 の調査で、組織のテストコード以外に残る自前の `initialize` は探索の打診の本文だけだと分かった |

## Context

探索（ADR-0006）は、設定されたサーバへ認証なしで POST し、401 の `WWW-Authenticate` から
`resource_metadata` と `scope` を読むところから始まる（`internal/oauth/discovery.go` の `probeChallenge`）。
その本文は手書きである。

```json
{"jsonrpc":"2.0","id":"discovery","method":"initialize","params":{}}
```

MCP のプロトコルヘッダを持たず、`initialize` としても正しくない（`protocolVersion`・`capabilities`・
`clientInfo` がない）。ADR-0008 §10 は、MCP の処理に届かないことを理由にこれを残し、ADR-030 §6 の検査を
書くときに `.github` に除外してもらうことにした。これでは、握手を自分で書かないことを規則にしている
（CLAUDE.md、ADR-0008 §2）このリポジトリに自前の握手が残り、組織の所有者には、下の実測のとおり要らない
除外を頼むことになる。

### 実測（2026-11-27）

登録済みの各上流と Slack の MCP サーバに、同じ 5 通りの要求を認証なしで送った。ほかには何も送っていない
（`Authorization` も、設定の固定ヘッダもない）。

| 上流 | 今の `initialize` | MCP ヘッダなしの `ping` | 本文なしの POST | GET `text/event-stream` | `client.Open` が送る開始の要求 |
|---|---|---|---|---|---|
| GitHub のホスト型 MCP | 401、`resource_metadata` あり | 同じ | 同じ | 同じ | 同じ |
| Slack の MCP | 401、`resource_metadata` あり | 同じ | 同じ | 同じ | 同じ |
| Obsidian（ローカル） | 401、チャレンジなし | 401 | 401 | **405** | 401 |
| NanoKVM（LAN） | 401、チャレンジなし | 401 | 401 | 401 | 401 |

- GitHub と Slack では、5 通りすべてで `WWW-Authenticate` の値がバイト単位で同じだった。どちらも今は
  `scope` を入れていない。
- 4 つとも本文を見る前に認証する。Obsidian は認証の前に HTTP のメソッドで振り分ける。
- Obsidian と NanoKVM は OAuth ではなく固定の `Authorization` ヘッダで設定しており、探索は走らない。
  登録済みの上流なので表に入れた。
- `client.Open`（nlink-jp/mcp v0.2.0）が送った開始の要求は `server/discover` 1 通で、
  `_meta.io.modelcontextprotocol/protocolVersion` が `2026-07-28`、`clientInfo` と `clientCapabilities` を持ち、
  `httpwire.RequestHeaders` から `MCP-Protocol-Version: 2026-07-28` と `Mcp-Method: server/discover` の
  ヘッダが付いた。`Send` が失敗すると `Open` はそれ以上送らずに終わる（実測。期限切れ以外の失敗に対する
  `client.open` の動き）。
- 測定のプログラムは `discovery-probe` と名乗り（`mcp-bridge`/`discovery` ではない）、モジュール既定の 5 秒の
  `ProbeTimeout` を使った。本文を読む前に 401 で答えるサーバには、どちらも関係しない。

## Decision

### 1. 打診は、ふつうのクライアントの開始の要求とし、nlink-jp/mcp が組む

`probeChallenge` は本文を書かない。1 回きりの `client.Conn`（`internal/oauth` の `probeConn`）の上で
`client.Open` を走らせ、モジュールが送る最初の要求への答えを読む。これは MCP の認可仕様が述べる流れ
（クライアントがトークンなしでふつうの要求を送り、サーバが 401 で答える）であり、mcp-bridge のもう一つの
クライアント `inspect` がすでにしていることでもある（ADR-0008 §9）。

`probeConn`:

- **`Send` はその要求を 1 回だけ POST し、結果を控える。** 要求は `bytes.Reader` を本文にして
  `http.NewRequestWithContext(ctx, …)` で組む（https への 307/308 を今と同じく追って送り直せるように）。
  `Content-Type: application/json`、`Accept: application/json, text/event-stream` と、
  `httpwire.RequestHeaders(msg, "", nil)` が導くヘッダを付け、探索の `http.Client` を通す（格下げの拒否、
  リダイレクトの上限、30 秒のタイムアウトはそのまま。探索に証明書の固定はない — 設定は `oauth` と
  `tlsCertSha256` の併用を拒む、ADR-0005）。資格情報も固定ヘッダも、今と同じく送らない。答えがあれば本文を
  `maxMetadataBytes` まで読んで捨て、状態コードと `WWW-Authenticate` の値を控える。通信が失敗すればその
  エラーを控える。どちらの場合も `errProbeAnswered` を返し、`Open` をそこで終わらせる。
- **`probeChallenge` は `Open` のエラーではなく控えを読む。** `reached` とエラーは、最初の `Send` が控えた
  ものから決める。答えがあれば `reached`、通信の失敗はそうでない（拒んだリダイレクトは今と同じく例外）。
  要求を出す前の失敗（`RequestHeaders` のエラー、組めない要求）は mcp-bridge 側のエラーとして報告し、
  「サーバに届かなかった」とは言わない。
- **要求を出すのは 1 回目の `Send` だけ。** 2 回目以降は要求を出さずにエラーを返す。これは将来のモジュール
  への備えだけではない。`client.Open` は、期限切れにあたる `Send` のエラー
  （`errors.Is(err, context.DeadlineExceeded)`。`http.Client` のタイムアウトはこれに当たる）を「答えなし」と
  みなして `initialize` へ戻る。どの結果にも `errProbeAnswered` を返すことでその道を避け、万一その道に
  入っても、この拒否が POST 1 回の規則を守る。
- **`Recv` は `Close` まで待ち、その後 `io.EOF` を返す**（`Close` が先なら即座に）。`Open` は読み取りと応答の
  ループを起こし、失敗時には閉じるための `Client` を返さないので、**`probeChallenge` が `probeConn` を自分で
  閉じる**（`defer`）。これでループは終わる。
- `Open` には `Client: {Name: "mcp-bridge", Version: "discovery"}`（`inspect` と同じく役割を版の欄に置く）と、
  探索の `http.Client` と同じ 30 秒の `ProbeTimeout` を渡す。待ち時間の上限は今と変わらない。
  `Open` 全体も同じ上限のコンテキストで走らせ、万一後続の要求が出ても限りなく待つことはない。

答えの読み方は変えない。`resource_metadata` 付きの 401 なら保護リソースのメタデータへ進む。それ以外の
状態コードや、それのない 401 は報告する試行の一つとなり、探索は既知の場所へ進む（ADR-0006）。

### 2. ADR-0008 §10 を取り下げる

本番のコードについて、ADR-030 §6 に除外を求めない。mcp-bridge のどのパッケージも `initialize` も
`server/discover` も書かない。mcp-bridge のテストには、相手の代わりに `initialize` に答える上流（`inspect`、
証明書の固定、時代の組み合わせのテスト）や、`server/discover` を確かめる上流（この記録のテスト）がある。
テストコードについて検査が何を挙げるかは、検査を書くときに `.github` が決めることで（ADR-025）、この記録の
決めることではない。

### 3. 変えないこと

探索の順序、スコープ、URL の規則、リダイレクトの規則、設定、CLI、`go.mod`（`client` と `httpwire` を含む
モジュールは v0.2.0 ですでに require している）。

## Alternatives considered

- **`ping`。** 握手ではないので ADR-030 §6 には掛からないが、手書きの JSON-RPC の要求であることは変わらない。
  新方式ではこれにもヘッダが要り、旧方式では `initialize` の前に許される唯一の要求というだけで、サーバが
  認証の前に何を検査するかについては何も言わない。手書きを無くすのではなく移すだけなので採らない。
- **本文なしの POST。** MCP の文字列が一切なく、4 つとも 401 だった。MCP の要求ではなく、認証の前に要求を
  検査するサーバは、ふつうのクライアントの要求なら認証まで届くところで 400 を返すので採らない。
- **GET。** Obsidian は認証の前に 405 を返し、新方式のサーバは GET のストリームを持たない。実測により採らない。
- **本文を残して除外を頼む**（ADR-0008 §10）。モジュールですでに済むことの除外を組織の所有者に頼むことに
  なるので採らない。

## Consequences

- mcp-bridge に自前の握手は残らない。
- 認証の前にプロトコルヘッダを検査する新方式専用のサーバにも、正しい要求が届くようになる。今の本文は
  ヘッダを持たない。そうしたサーバがあるかは**未測定**である（4 つとも違う）。
- メソッドごとに認可するサーバ — `initialize` と `tools/list` にトークンなしで答えるものがある（Google
  Workspace のサーバ、`internal/cli/auth.go`）— は、今の `initialize` には 401 で答えたのに `server/discover`
  には 401 で答えない、あるいはその逆のことがありうる。記録にあるもう一つの OAuth の上流 Microsoft Agent 365
  （`initialize` に `resource_metadata` 付きの 401 で答えた。`docs/ja/history/v0.3.0-design.ja.md` §6）に
  `server/discover` を送ったことは**なく**、測るためのテナントも手元にない。401 が来なければ、チャレンジの
  ない答えと同じく既知の場所へ進む。記録にある 3 つの OAuth のサーバでは、そこでの最初の候補（RFC 9728 の
  パスを挟んだ場所）が、チャレンジの指す URL そのものなので、なお見つかる。失われるのはチャレンジの `scope`
  だが、3 つとも送っていない。そのための扱いは足さない。
- 認証の前にメソッドを読む旧方式専用のサーバは、`server/discover` に 401 ではなくエラーで答えるかもしれず、
  結果は上と同じである。今の空の `params` の `initialize` も、そうしたサーバにとって正しい要求ではない。
  **未測定**である。
- 認証のないサーバに、クライアントの名前（`mcp-bridge`）と役割（`discovery`）が伝わる。今は何も伝わらない。
  どちらも利用者についての情報は持たない。

## Verification

期待する結果を決めるもの: モジュールの開始の契約（`client.Open` はまず `server/discover` を送り、期限切れ
でない `Send` のエラーで終わる）と、MCP の認可仕様の流れ（トークンなしのふつうの要求に、401 と
`WWW-Authenticate` で答える）。

- **単体（`internal/oauth`）**: 401 とチャレンジで答える httptest の上流に対し、打診は POST をちょうど 1 回送る。
  本文は `_meta` にプロトコル版 `2026-07-28` を持つ `server/discover` で、`MCP-Protocol-Version` と
  `Mcp-Method` を持ち、`Authorization` を持たない。探索はチャレンジを今と同じく読む。`server/discover` の
  結果を 200 で返す上流にも POST は 1 回で、探索は既知の場所へ進む。短くしたタイムアウトを越えて止まる上流にも
  POST は 1 回で、エラーはタイムアウト、`reached` は偽。`probeConn` の 2 回目の `Send` は何も送らない。
  `Recv` は `Close` の後に `io.EOF` を返し（`Close` が先でも同じ）、打診の後に `Open` のゴルーチンは残らない。
  既存の探索のテスト（リダイレクト、リソースの不一致、スコープ）は変えずに通る。
- **実際の上流**: リリース前に、ビルドしたバイナリの `login` を Slack の MCP サーバに対して認可 URL まで
  走らせる（チャレンジを読み、認可サーバが見つかるところまで）。

## References

- ADR-0006（事前登録クライアントでの探索）、ADR-0008 §2・§9・§10（この記録は §10 を置き換える）
- 組織 ADR-025、ADR-029、ADR-030 §6
- nlink-jp/mcp v0.2.0: `client.Open`、`client.Conn`、`httpwire.RequestHeaders`
- MCP 仕様 2026-07-28: Authorization、Streamable HTTP（プロトコルヘッダ）
- RFC 9728（OAuth 2.0 Protected Resource Metadata）
