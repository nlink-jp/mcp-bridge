# ADR-0008: 新旧両方の時代をそのまま通す — 仕様が HTTP のクライアントに求める分だけを nlink-jp/mcp で足す

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-27 |
| Binds | mcp-bridge |
| Decision makers | nlink-jp maintainers |
| Triggered by | 組織 ADR-030（MCP 2026-07-28 への両時代対応。移行の順は中継が最初。§1・§2 は 2026-11-27 にこの設計に合わせて改められた）と、共通モジュール nlink-jp/mcp v0.1.0 の公開。2026-11-27 の実測で、mcp-bridge 越しの GitHub への接続がクライアントの寛容さだけで成り立っていると分かった |

## Context

### いまの mcp-bridge

mcp-bridge は stdio のクライアントと Streamable HTTP の上流をつなぐ。本文は読まず、書き換えず、手元で
答えない（`internal/bridge` の約束、CLAUDE.md「JSON-RPC passes through unmodified」）。上流への POST に
付けるのは `Content-Type`・`Accept`・`Mcp-Session-Id`（上流が発行したとき）・`Authorization`・設定の静的
ヘッダだけである（`internal/transport/streamhttp.go`）。go.mod の require は 0 行（CLAUDE.md）。

### 何が足りないか（仕様と実測・コード）

1. **旧方式でも、必須のヘッダを送っていない。** 2025-06-18 から、HTTP のクライアントは交渉した版を
   `MCP-Protocol-Version` に載せなければならない。いま通っているのは、サーバーが「ヘッダの無い要求を
   2025-03-26 として扱ってよい（MAY）」からである（2026-07-28 `streamable-http` §Protocol Version Header、
   コードで確認）。
2. **新方式の要求に、必須のヘッダを付けられない。** 2026-07-28 の HTTP のクライアントは、本文から
   `MCP-Protocol-Version`・`Mcp-Method`・`Mcp-Name` を作り、ツールのスキーマで `x-mcp-header` の印が
   付いた引数を `Mcp-Param-{Name}` に写さなければならない（MUST）。印の不正なツールは除かなければ
   ならない（MUST）。GitHub のツールはすでに `owner`・`repo` に印を付けている（実測）。
3. **クライアントの寛容さに乗っている。** 2026-11-27、Claude Code 2.1.311 を mcp-bridge 越しに GitHub へ
   つないで行を記録した。クライアントの `server/discover` はヘッダなしで上流へ届き、GitHub は新方式の
   エラー `-32020` を返した。仕様では、新方式のエラーを受けた両対応のクライアントは旧方式へ**戻らない**。
   Claude Code は戻って動いたが、仕様どおりのクライアントは GitHub のツールを得られない。
4. **長すぎる行 1 つで中継が終わる。** クライアント側の行を `bufio.Scanner`（上限 8 MiB）で読んでおり、
   上限を超えると読み取りの繰り返しを抜け、上流を閉じて終了する（コードで確認）。
5. **上流の大きな応答で、要求が答えられないまま残る。** 2xx の JSON の本文は 8 MiB で黙って切られ、壊れた
   JSON として捨てられる。SSE は 1 行 8 MiB の上限を超えると読み取りが黙って終わり、イベントのデータ行を
   つないだ大きさには上限が無い。どちらの場合も、要求への答えは届かず、mcp-bridge には要求ごとの
   タイムアウトが無い（コードで確認）。

相手（2026-11-27 に版も含めて測り直した。記録あり）: GitHub（`github-mcp-server`、remote 版）・Obsidian
（generic 2.4.0）は両対応、NanoKVM（nanokvm-cube-remote-control v1.0.0）は旧方式のみ。Claude Code 2.1.311 は
両対応。

## Decision

### 1. 依存は nlink-jp/mcp を許す

規則を「標準ライブラリと、同じ規則（標準ライブラリだけ）を守る nlink-jp のモジュールだけ」に改める
（slack-mcp-extender と同じ書き方）。require に入るのは `github.com/nlink-jp/mcp` だけである。第三者の
モジュールを入れないという規則の目的は変わらない。

### 2. 時代は変換せず、そのまま通す

クライアントが開いた時代のまま上流へ通す。mcp-bridge は `initialize` にも `server/discover` にも自分では
答えない（ADR-030 §1 の改めた文面）。中継はクライアントが両対応なら両対応である。旧方式しか話さない
クライアントが新方式専用の上流につながるのは、そのクライアントの移行の後である。

### 3. 本文を変えるのは、次の 3 つだけ

- **新方式の `tools/list` の結果から、`x-mcp-header` の印が不正なツールを除く**（仕様の MUST。
  `httpwire.Tracker.Incoming`。5000 を超えた分も除く）。除いたツールの名前と理由を stderr に出す。これは
  CLAUDE.md が範囲外とする「ツールの隠し（tool masking）」ではない。方針でツールを選ぶのではなく、
  仕様が HTTP のクライアントに除けと求める不正な定義だけを除く。
- **上流が JSON-RPC として答えなかった失敗には、`-32603` を作って返す**（いまと同じ。ADR-0002）。2xx でない
  JSON-RPC のエラー本文の `id` が欠けている・`null`・違うときは、要求の `id` に直す（`httpwire.Classify`）。
  2xx の `application/json` の本文が JSON として読めないときも、要求の `id` で `-32603` を作る（`wire.Writer` は
  壊れた JSON を書かないので、作らなければ要求は答えられないまま残る）。
- **大きすぎる応答には `-32603` を作って返す**（§5。Context 5 を直す）。

それ以外の本文は、中身を変えずに通す（1 行に詰める圧縮は、いまと同じく行う）。

### 4. ヘッダは本文から作る

上流への POST ごとに `httpwire.Tracker.Outgoing` のヘッダを付ける。

- 旧方式のメッセージ: `MCP-Protocol-Version` に `initialize` で決まった版（Context 1 を直す）。`initialize`
  そのものには付けない。
- 新方式のメッセージ: `MCP-Protocol-Version`・`Mcp-Method`・`Mcp-Name`・`Mcp-Param-*`（値が無い・null なら
  付けない。素の値で表せなければ Base64 の哨兵）。

`Outgoing` がエラーを返したとき:

- **バッチ（配列の本文）**: プロトコルのヘッダを付けずに、いまと同じく送る（バッチは 2025-03-26 だけの
  ものである）。stderr に記録する。
- **印の付いた引数をヘッダに写せない**（オブジェクト・配列・端数のある数など）、または **`params.name` の
  無い `tools/call`**: 上流へ送らず、その要求に `-32602` で答える（「argument "<名前>" cannot be sent as a
  header」など。値はメッセージに載せない）。

プロトコルのヘッダは設定の静的ヘッダより後に付ける。設定の `headers` に `MCP-Protocol-Version`・
`Mcp-Method`・`Mcp-Name`・`Mcp-Param-` で始まる名前があれば、設定の読み込みで拒む（大文字小文字を
問わない。ADR-0004 の厳格な設定）。

### 5. 応答は、届ける前にすべて Tracker を通す

- 2xx の JSON の本文、SSE の各 `message` イベントの本文（バッチを開いた後の各メッセージ）を
  `Tracker.Incoming` に通してから届ける。`initialize` の結果から版を、新方式の `tools/list` から印を覚える。
- 2xx でない応答は `httpwire.Classify` で判定する（Deliver・Synthesized・Drop）。401 のやり直しはいまの
  まま、その前に行う。
- **上限**: 2xx の JSON の本文は 8 MiB まで。超えたら、その要求の `id` で `-32603`「upstream response larger
  than 8388608 bytes」を返す（いまは黙って切れる）。SSE は、1 行 8 MiB（いまと同じ）に加えて、1 イベントの
  データ行をつないだ大きさも 8 MiB までとする。どちらかを超えたらそのストリームの読み取りをやめ、
  そのストリームを開いた要求にまだ答えが届いていなければ、その `id` で同じ `-32603` を返す。

### 6. HeaderMismatch は、転送層の中で 1 回だけやり直す

**対象**: mcp-bridge が送った要求が `tools/call` で、`Outgoing` が `Mcp-Method` を付けた（新方式の）もので
あり、その答えが HTTP 400 で、本文の JSON-RPC の `error.code` が `-32020` のとき。判定は mcp-bridge が自分の
要求と答えの本文を読んで行う（`Classify` の `Modern` は複数のコードをまとめた印なので使わない）。

1. mcp-bridge が自分で `tools/list` を上流へ送る。`id` は `"mcp-bridge-refresh-<連番>"`（文字列）。
   `_meta` は元の要求の `io.modelcontextprotocol/protocolVersion`・`…/clientCapabilities`・`…/clientInfo` を
   写す。`nextCursor` を最大 10 ページまでたどる。応答は `Tracker.Incoming` に覚えさせるだけで、
   クライアントには届けない。
2. 元の要求を、作り直したヘッダで 1 回だけ送り直す。その答えを、そのまま届ける。
3. 取り直しが失敗したら（エラー・ページの上限・応答なし）、送り直さずに最初の `-32020` を届ける。最初の
   ページを読んだ後に失敗した場合、`Tracker` が覚える印は読めたページの分になる。後のページのツールを呼ぶと
   再び `-32020` になり、そこで取り直しが起きる。

やり直しは要求ごとに 1 回、取り直しは直列にする。mcp-bridge が自分で要求を作るのは、これだけである。

### 7. クライアント側の行は nlink-jp/mcp/wire で読み書きする

上限はいまの 8 MiB を保つ（`wire.NewReader(in, 8<<20)`）。長すぎる行は読み捨てて続け、`id` が読めないので
`-32700`（`id: null`、「message longer than 8388608 bytes」）を返して stderr に記録する（Context 4 を直す）。
書き込みは `wire.Writer`（1 回の書き込み、直列化）。壊れた JSON への応答はいまと同じ（`-32700`、`id: null`）。

### 8. 変えないもの

- セッション: 上流が `Mcp-Session-Id` を発行したときだけ付け、終了時に DELETE する（旧方式の上流）。新方式の
  上流は発行しない。
- GET のストリームは開かない（いまも開いていない）。
- `inspect` 以外の CLI・OAuth・設定の形（§4 の拒否を除く）。

### 9. `inspect` は nlink-jp/mcp/client で開く

`inspect` は自前の `initialize` を 2 か所ともやめる。

- **本体**: `client.Open` を HTTP の上の `client.Conn` で使う。`Conn` は mcp-bridge が作る（nlink-jp/mcp
  詳細設計 §5.5）: `Send` は `Tracker.Outgoing` のヘッダで POST し、応答は `Classify` → `Tracker.Incoming` を
  通して `Recv` に渡す（SSE は各イベント、Accepted・Drop は何も渡さない）。`inspect` は上流の時代に合わせて
  新方式か旧方式で開き（このとき `400` の本文を見て判断するのは `client` である）、表示に時代と版を足す。
- **資格情報なしで答えるかの確かめ**（`serverAnswersUnauthenticated`）: 同じ `Conn` を資格情報なしで作り、
  `client.Open` が成功するかで判定する。新方式専用の上流でも、資格情報を確かめずに「受け入れられた」と
  表示することは無くなる。

### 10. OAuth の探索の POST は残す

*（2026-11-27 に ADR-0009 が置き換えた。打診は nlink-jp/mcp が組む開始の要求を送り、ADR-030 §6 に除外を求めない。）*

`internal/oauth/discovery.go` の `initialize` の POST は、401 と `WWW-Authenticate` を得るための認証の探りで、
MCP の処理には届かない。本文はそのままにする。ADR-030 §6 の検査がこれを握手の自前実装と読まないよう、
検査を書くときに `.github` へ issue で伝える（ADR-025。この記録の段ではない）。新方式専用の上流が、認証の
前に本文を検査して 400 を返すかどうかは**未測定**である。

### 11. リリース前の検証で改めたこと（2026-11-27）

リリース前の独立検証の指摘のうち、設計が定めていなかった次の点を、運用者の判断で決めた（5〜7 は修正の検証の後に足した）。§3〜§8 のこれらの
範囲は、この節が優先する。

1. **答えられないまま残る要求にも `-32603` で答える**（§3・§5 に足す）。要求が開いた SSE が、その要求の答えを
   送らずに終わったとき（JSON でないデータのイベントは捨てて記録する）と、要求への 2xx が空のとき（202 を
   含む）、その要求の `id` で `-32603` を返す。通知への 202・空の 2xx は、いまと同じく何も返さない。
2. **ヘッダを作れない要求の、残りの場合**（§4 に足す）。`params.name` の無い `prompts/get` と `params.uri` の
   無い `resources/read` は、`tools/call` と同じく送らずに `-32602` で答える。旧方式のメッセージで、
   `initialize` で決まった版がヘッダに載せられない文字を含むときは、プロトコルのヘッダを付けずに送って記録する
   （移行前と同じ動き。その版の誤りでセッション全体を使えなくしない）。新方式の要求で、版か method がヘッダに
   載せられないときは `-32602`。
3. **セッションを終える DELETE**（§8 に足す）。上流が発行したセッションを終える DELETE にも、`initialize` で
   決まった版の `MCP-Protocol-Version` を付ける（2025-06-18 から、HTTP のクライアントは後に続くすべての要求に
   このヘッダを付ける）。
4. **取り直しの期限**（§6 に足す）。1 回の取り直し（最大 10 ページ）の全体を 60 秒で打ち切り、過ぎたら取り直しの
   失敗として扱う（1 ページずつの期限では、最悪 10 分ほかの要求が待つ）。
5. **2xx の答えの `id` も要求の `id` に直す**（§3・§5 に足す）。要求への 2xx の `application/json` の本文が 1 件の
   応答（`result` か `error` を持ち `method` を持たない）で、その `id` が無い・`null`・違うときは、要求の `id` に
   直して届ける（2xx でない応答と同じ規則。直さなければ要求は答えられないまま残る）。
6. **JSON としては読めるが JSON-RPC のメッセージでない行には `-32600`**（§7 に足す）。壊れた JSON は `-32700`
   のまま、JSON の値として読めるがメッセージとして不正なもの（オブジェクトでない値、文字列でない `method` など）
   は `-32600`（`id: null`）で答える。
7. **DELETE の版のヘッダは、ヘッダに載せられるときだけ付ける**（3 を補う）。交渉の版がヘッダに載せられない
   文字を含むときは、ヘッダを付けずに DELETE を送る（付けると HTTP のクライアントが送信を拒み、セッションが
   終わらない）。

## 詳細設計

| 部品 | 変えること | 判定役（テストの期待値の出どころ） |
|---|---|---|
| `internal/bridge` | `bufio.Scanner` → `wire.Reader`（8 MiB）、書き込み → `wire.Writer`。長すぎる行に `-32700` | wire の約束（モジュール詳細設計 §3.1）と §7 |
| `internal/transport`（`streamhttp.go`） | `Tracker` を持つ。`post` で `Outgoing` のヘッダ（静的ヘッダの後）と §4 のエラーの扱い。`route` の 2xx 以外を `Classify` に、2xx の本文と SSE の各メッセージを `Incoming` に。§3・§5 の作る `-32603` と上限。§6 のやり直し | 仕様 streamable-http §Protocol Version Header・§Standard Request Headers・§Custom Headers・§Server Validation・§Backward Compatibility |
| `internal/transport`（新規 `conn.go`） | `inspect` のための `client.Conn` | モジュール詳細設計 §5.5 |
| `internal/cli/inspect.go` | 本体と資格情報なしの確かめを `client.Open` に置き換え、時代と版を表示 | §9 |
| `internal/config` | `headers` のプロトコルのヘッダ名を拒む | §4 |

上限: クライアントの行 8 MiB（いまと同じ）。上流の JSON の本文 8 MiB。SSE の 1 行と 1 イベント 8 MiB。取り直しは
10 ページまで。`Tracker` の上限（覚える要求 1024、ツール 5000）はモジュールのもの。

エラー: 取り直しの失敗は最初の `-32020` を届ける。除いたツールは stderr に名前と理由だけを出す（引数や結果の
中身はログに出さない）。作るエラーのメッセージに上流の本文を載せるときは、`Classify` の規則（先頭 200 バイト、
制御文字を除く）に従う。

## テスト設計

- **単体（transport）**: httptest の上流で、旧方式の `initialize` の後の要求に `MCP-Protocol-Version` が付く
  こと。新方式の `tools/call` に `Mcp-Method`・`Mcp-Name`・印の `Mcp-Param-*` が付き、値が無い引数には
  付かないこと。設定の静的ヘッダがプロトコルのヘッダを上書きしないこと。SSE と JSON の両方の経路で印を
  覚えること。印の不正なツールが除かれ、他のキーが残ること。HTML の 502・空の 400・`id: null` の 400・2xx の
  壊れた JSON の扱い。8 MiB を超える JSON の本文・SSE の行・SSE のイベントに、要求の `id` で `-32603` が返る
  こと。バッチがヘッダなしで送られること。写せない引数と名前の無い `tools/call` が送られずに `-32602` に
  なること。
- **単体（やり直し）**: `-32020` → 取り直し → 送り直しで成功する。2 回目も `-32020` ならそれを届ける。取り直しが
  失敗したら最初の `-32020` を届ける（最初のページの後の失敗を含む）。取り直しの応答はクライアントに届かない。
  やり直しは 1 回だけ。新方式でない要求・`tools/call` でない要求・`-32020` 以外の 400（`-32022` など）は
  やり直さない。`-race`。
- **単体（bridge）**: 上限ちょうど・上限+1 の行の後に正しい行が続き、中継が止まらず `-32700` を返す。
- **単体（config）**: プロトコルのヘッダ名（大文字小文字を変えたものも）を拒む。
- **単体（inspect）**: 資格情報なしの確かめが、新方式専用の上流でも新方式で開いて判定すること。
- **探りの組み合わせ**: 仕様の文から書いた httptest の上流 3 種（旧方式のみ・両対応・新方式のみで、ヘッダの
  検査と `x-mcp-header` を持つ）と、nlink-jp/mcp の探りのクライアント 3 種（旧方式のみ・両対応・新方式のみ）を、
  mcp-bridge の stdio を通してつなぐ。期待値は仕様の互換性表（クライアントと上流の組み合わせのまま）。
  HTTP の上流の探りは、モジュールが stdio の探りしか持たないので、mcp-bridge のテストに置く（ADR-030 §7 の
  「探りはモジュールが持つ」は stdio の探りを指す）。
- **実機（E2E）**: Claude Code 2.1.311 を mcp-bridge 越しに、行を記録しながらつなぐ。GitHub・Obsidian では
  `server/discover` が結果を返して新方式のまま進み、`initialize` が無いこと。NanoKVM では旧方式に戻ること。
  それぞれ `tools/list` が返り、読むだけのツールを 1 回呼べること（GitHub `get_me`、Obsidian
  `get_server_info`。NanoKVM はツールを呼ばない）。旧方式のクライアント（gem-agent）が GitHub へ旧方式でつながる
  こと。`inspect` が GitHub で新方式、NanoKVM で旧方式と表示すること。

## 開発計画

1. この ADR の承認（日英）。
2. 依存と行の読み書き（§1・§7）。完了: bridge の単体テストが通る。
3. ヘッダと応答（§3〜§5）と設定の拒否（§4）。完了: transport と config の単体テスト、探りの組み合わせが通る。
4. やり直し（§6）。完了: やり直しの単体テストが `-race` で通る。
5. `inspect`（§9）。完了: 単体テストと実機の表示。
6. 文書（README 日英・CLAUDE.md の依存・透明性・範囲外の項・AGENTS.md・RFP の透明性の項・CHANGELOG）。
7. 実機の E2E を最終のコードで行い、リリース前の独立検証を 1 回（この ADR に照らして）。
8. リリース（minor: 0.4.0。依存の追加と振る舞いの変更のため）。

## Consequences

- mcp-bridge は、上流が新方式専用になっても、クライアントが両対応であればつながる。旧方式の上流にも、
  仕様どおりのヘッダで話す。大きな応答で要求が答えられないまま残ることが無くなる。
- 「本文を読まない」は「本文を読んで、ヘッダを作り、仕様の MUST の分だけ除く」に変わる。CLAUDE.md の依存・
  透明性・範囲外（tool masking が仕様の除外を含まないこと）の項と RFP を、この ADR に合わせて改める。
- require が 1 行になる。nlink-jp/mcp の更新は、mcp-bridge のリリースで届く。

## Alternatives considered

| 案 | 採らない理由 |
|---|---|
| 中継が時代を変換する（両側で MCP を終端する） | 旧方式のクライアントも新方式専用の上流につながるが、中継がプロトコルの端点になり、透明性の規則を大きく変える。`input_required` のように旧方式に写せないものが出る。クライアントの移行で同じ結果になる |
| HeaderMismatch をそのまま返す | stdio のクライアントはヘッダを直せない。クライアントが次に `tools/list` を呼ぶまで、同じツールは失敗し続ける |
| 依存 0 行を守り、プロトコルの処理を自前で書く | ADR-030 が終わらせようとしている写しが 1 つ残る。今回見つかった不具合（ヘッダの欠け）は、その写しの中で起きていた |

## References

- 組織 ADR-030（§1・§2 は 2026-11-27 に改められた）、ADR-025
- nlink-jp/mcp v0.1.0: 参照マニュアル（`httpwire`・`wire`・`client`）、詳細設計 §3.1・§5.4・§5.5
- MCP 2026-07-28 `basic/transports/streamable-http`（§Protocol Version Header・§Standard Request Headers・
  §Custom Headers from Tool Parameters・§Server Validation・§Backward Compatibility）
- ADR-0002（すべての要求に答える）、ADR-0004（単一ファイルの厳格な JSON 設定）
