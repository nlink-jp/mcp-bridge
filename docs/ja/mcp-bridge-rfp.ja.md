# RFP: mcp-bridge

> Generated: 2026-08-23
> Status: Draft

## 1. Problem Statement

stdio しか話せない MCP クライアント（Claude Code / Claude Desktop 等）から、**RFC 7591 Dynamic Client Registration に非対応で、事前登録済み OAuth クライアント（client_id + client_secret + 固定 redirect URI）を要求する** Streamable HTTP MCP サーバへ接続するためのブリッジ。

Claude Code は `type: "http"` の MCP サーバを DCR ベースの OAuth で直接扱えるため、本ツールの守備範囲は「クライアント標準の OAuth では届かないプロバイダ」— Slack 公式 MCP、GitHub Apps、Microsoft Entra ID など — に意図的に限定する。

想定利用者は、対象プロバイダの管理コンソールで OAuth アプリを自分で登録できる運用者。管理者権限を持たない利用者への配布は想定しない。

本プロジェクトは mcp-guardian からブリッジ部分のみを抽出した新規リポジトリである。ガバナンスゲート・レシート監査・テレメトリは持ち込まない。

## 2. Functional Specification

### Commands / API Surface

サブコマンド構成とする。mcp-guardian は 14 個のフラグの組み合わせで 7 つのモードを切り替えており（`--tool` / `--outcome` / `--limit` は `--view` 専用、`--callback-port` は `--login` 専用という暗黙の依存があった）、これが設定の分かりづらさの主因だったため、モードはサブコマンドで明示する。

| コマンド | 説明 |
|---|---|
| `mcp-bridge run <name>` | ブリッジ起動。MCP クライアントが子プロセスとして起動する |
| `mcp-bridge login <name>` | OAuth ブラウザログイン |
| `mcp-bridge logout <name>` | 保存済みトークン削除 |
| `mcp-bridge list` | 定義済みサーバ一覧＋ログイン状態 |
| `mcp-bridge inspect <name>` | 接続して serverInfo と tools 一覧を表示 |
| `mcp-bridge version` | バージョン表示 |

フラグ:

- `--config <path>` （グローバル。設定ファイルパスの上書き）
- `--callback-port <n>` （`login` のみ。設定の `callbackPort` を上書き）

計 2 フラグ。`--version` も別名として受け付ける（組織規約: CLI は必ず `--version` に応答する）。

### Input / Output

- **下流（AI エージェント側）**: stdio 固定。stdin から JSON-RPC 2.0 を 1 行 1 メッセージで受け、stdout へ返す。
- **上流（MCP サーバ側）**: Streamable HTTP のみ。JSON POST + SSE ストリーム。
- **ログ**: 全て stderr。stdout に JSON-RPC 以外を出すと MCP 接続が壊れるため、この分離は厳守する。
- **中継方針**: JSON-RPC メッセージは原則として無改変で中継する。`initialize` / `tools/list` / `tools/call` の特別扱いは行わない（mcp-guardian はここでスキーマキャッシュ・メタツール注入・マスキングを行っていたが、いずれも本ツールでは廃止）。
- **上流への転送に失敗した場合、クライアントのリクエストには必ず JSON-RPC エラー応答を返す**（mcp-guardian ADR-0002 を継承）。無応答にするとクライアントが自身のタイムアウトまでブロックし、「トークン失効。再ログインが必要」といった実際の理由が利用者に見えない。

### Configuration

単一ファイル `~/.config/mcp-bridge/config.json`。mcp-guardian の「システムグローバル設定 + サーバ profile」の 2 階層はやめる。テレメトリを落とした結果、グローバル側に置くものが無くなるため。

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
    "atlassian": {
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
    "gcp": {
      "url": "https://mcp.example.com/mcp",
      "tokenCommand": { "command": "gcloud", "args": ["auth", "print-access-token"] }
    }
  }
}
```

設定キーは約 12 個（mcp-guardian は profile 26 + global 16）。

決定事項:

- **JSON を採用し、外部依存ゼロを維持する。** 組織には sectioned TOML の規約があるが、TOML パーサは外部依存を 1 件持ち込む。OAuth の client_secret とアクセストークンを扱うツールであることを優先し、`encoding/json` + `DisallowUnknownFields` とする。MCP エコシステム（`.mcp.json` / `claude.json`）と同じ形式である点も利点。
- **strict decode を全面適用する。** mcp-guardian は profile のみ strict で、global config は素の `json.Unmarshal` だった。同種のファイルで挙動が非対称なのは typo を握り潰す原因になるため、単一ファイル化と合わせて解消する。
- **`transport` キーは廃止。** mcp-guardian の `"sse"` は実態が Streamable HTTP であり、名前と中身が乖離していた。上流は HTTP 固定なのでキー自体を持たない。
- **`flow` キーは廃止。** client_credentials を対象外としたため、選択肢が authorization_code の 1 つしかない。
- **`"oauth": {}`（空オブジェクト）が DCR 自動 discovery を使う意思表示になる。** キーを省略した場合は認証なし。

状態ファイルは `~/.config/mcp-bridge/state/<name>/` に配置し、`tokens.json`（mode 0600）と `discovery.json` のみを持つ。mcp-guardian が持っていた `controller.json` / `authority.json` / `receipts-*.jsonl` は存在しない。カレントディレクトリの `.governance` へのフォールバックも作らない。

### External Dependencies

- Go 標準ライブラリのみ。`go.mod` の require 行はゼロを維持する。
- 実行時の外部依存は接続先の MCP サーバと、その OAuth 認可サーバのみ。
- `tokenCommand` を使う場合のみ、利用者が指定した外部コマンド（`gcloud` 等）が PATH 上に必要。

対応する認証方式:

| 方式 | 内容 |
|---|---|
| 認証なし | `oauth` / `headers` / `tokenCommand` をいずれも書かない |
| 静的ヘッダ | `headers` に `Authorization` 等を直書き |
| `tokenCommand` | 外部コマンドを実行して Bearer トークンを取得（短命トークン向け） |
| OAuth2 authorization_code | PKCE 常時。事前登録 confidential client と DCR 自動登録の両対応 |

**client_credentials（M2M）は対象外**とする。手元のクライアントから起動される stdio ブリッジという用途で出番がないため。

## 3. Design Decisions

**言語・依存**

Go、外部依存ゼロ。OAuth の client_secret とアクセストークンを扱うため、サプライチェーン面のリスクを持ち込まない。設定形式に JSON を選んだ理由も同じ。

**方向性を固定する**

下流は stdio 固定、上流は Streamable HTTP のみ。双方向ブリッジにはしない。「stdio MCP サーバを HTTP で公開する」という逆方向には使えないことを README に明記する（mcp-guardian では繰り返し誤解された点）。

**mcp-guardian から持ち込むもの**

- Streamable HTTP クライアントと OAuth 実装（transport 層）
- JSON-RPC メッセージ処理
- `login` / `discover` / `inspect` / 自己署名証明書生成
- 設計判断 3 件（ADR-0001 事前登録 OAuth クライアント、ADR-0002 上流転送失敗時の応答、ADR-0003 refresh-less トークンの無期限扱い）。参照ではなく mcp-bridge のリポジトリで再発行する。ADR は拘束範囲のあるリポジトリに置く。

**明示的に out of scope とするもの**

- 5 段階ガバナンスゲート（budget / schema / constraint / authority / convergence）
- レシート監査、SHA-256 ハッシュチェーン、`--view` / `--verify` / `--explain` / `--receipts`
- メタツール注入（`governance_*` 5 種）
- tool masking
- OTLP / Splunk HEC / webhook のテレメトリエクスポート
- `enforcement`（strict/advisory）と `schema`（off/warn/strict）の 2 ノブ
- client_credentials フロー
- 監査ログ全般（軽量 JSONL も持たない）

**既存ツールとの関係**

- **mcp-guardian**: 本プロジェクトの抽出元。mcp-bridge は依存せず、コードは複製して単純化する。ライブラリ化して依存させる案は、外部依存ゼロ方針の下では 2 リポジトリ間の同期コストが常時発生し、使っていない機能のために使っている機能のリリースが縛られるため採らない。mcp-guardian 自体の処遇（凍結 / アーカイブ / 存続）は本 RFP のスコープ外とし、別途決定する。
- **slack-mcp-extender**: レイヤーが異なる。mcp-bridge は接続の確立、extender はツールの拡張。原理的には両方を直列に通すことも可能。

**mcp-guardian で観測された分かりづらさと、その解消方法**

| mcp-guardian の問題 | mcp-bridge での解消 |
|---|---|
| フラグ 14 個で 7 モードを切り替え | サブコマンド 6 個 + フラグ 2 個 |
| profile JSON が「login 専用キー」と「実行時キー」の混合物（`authorizeUrl` / `extraParams` は Config に渡らない） | 設定は 1 種類の構造体に一本化し、読まれるタイミングを分けない |
| `enforcement: "advisory"` でも budget / schema ゲートはブロックする（README の記述と実装が不一致） | ガバナンスごと廃止 |
| `transport: "sse"` の実態が Streamable HTTP | キー廃止 |
| `.governance` レガシーパスがフォールバックとサンプルに残存 | フォールバックを作らない |
| `mask` が `enforcement: strict` でないと効かない（直交すべき概念の結合） | 機能ごと廃止 |
| global config だけ strict decode されない | 単一ファイル + 全面 strict |
| 存在しないフラグを案内するエラーメッセージ | scaffold 時点でエラーメッセージとフラグ定義の対応をテストで固定する |
| `--inspect` / `--callback-port` が README に未記載 | Phase 3 のドキュメントチェックに全サブコマンド・全フラグの記載確認を含める |

## 4. Development Plan

### Phase 1: Core

- JSON 設定ローダ（strict decode、単一ファイル、`--config` 上書き）
- stdio ⇄ Streamable HTTP の中継（JSON-RPC の無改変パススルー、上流転送失敗時のエラー応答）
- 認証なし / 静的ヘッダ
- サブコマンド `run` / `list` / `version`
- ユニットテスト全パッケージ + モック MCP サーバによる E2E

完了時点で認証不要の HTTP MCP サーバに対して実用になる。独立レビュー可。

### Phase 2: Features

- OAuth2 authorization_code（PKCE、事前登録 confidential client、`clientAuthMethod` post/basic/none）
- https loopback コールバック（エフェメラル自己署名 ECDSA P-256 証明書、メモリ内のみ）
- RFC 8414 メタデータ discovery
- RFC 7591 Dynamic Client Registration
- refresh-less トークンの無期限扱い（ADR-0003）
- サブコマンド `login` / `logout` / `inspect`
- `tokenCommand`

完了時点で Slack 公式 MCP に対して実データ E2E が通る。独立レビュー可。

### Phase 3: Release

- README.md / README.ja.md（全サブコマンド・全フラグの記載を検証）
- AGENTS.md（他プロジェクトからのコピー禁止、新規作成）
- CHANGELOG.md
- docs 3 層構造（`docs/{en,ja}/{adr,reference,history}/`）+ ADR 3 件の再発行 + docs-mirror-check
- Makefile（`make build` → `dist/`、バージョンは `git describe`）
- 署名・notarize、Homebrew tap
- umbrella リポジトリ（util-series）への submodule 統合
- 組織 profile README の更新
- `check-org.sh` 通過

独立レビュー可。

### 実データ E2E の到達点

Phase 1 完了で認証なしサーバ、Phase 2 完了で Slack が動作する。フェーズ境界と検証可能性の境界が一致するよう構成している。

## 5. Required API Scopes / Permissions

ツール自体が固有のスコープを要求することはない。接続先ごとに利用者が定義する。

ただし **利用者は対象プロバイダの管理コンソールで OAuth アプリを事前登録する必要がある**:

- **Slack**: user token scopes（例: `chat:write`, `channels:history`, `channels:read`, `groups:history`, `groups:read`, `im:history`, `im:read`, `mpim:history`, `mpim:read`, `search:read`, `users:read`）を宣言し、Redirect URL に `https://localhost:<port>/callback` を登録する
- **GitHub Apps / Microsoft Entra ID**: 同様に client_id / client_secret / redirect URI の事前登録が必要

**準備負荷の検証**（組織の知見: ツールの採否は「想定利用者が事前準備を自力で完了でき、詰まったときに自力で切り分けられるか」で決まる）:

本ツールは per-user の OAuth アプリ登録を必須とする。これは gdrive-collector を Phase 1 完了後に中止させたのと同じ構造である。ただし本件では想定利用者＝運用者本人であり、Slack アプリ登録は既に実績がある。したがって脱落点にはならないと判断する。

一方、**管理者権限を持たない利用者にはそのまま配布できない**。この制約を README の冒頭に明記し、想定利用者を偽らない。

## 6. Series Placement

Series: **util-series**

Reason: 単機能でパイプ的に他ツールへ繋がる CLI であり、抽出元の mcp-guardian、および data-toolbox-mcp / splunk-mcp / chrome-pilot-mcp 等の MCP 関連ツールと同じ系列。特定の外部サービス向け対話クライアント（cli-series）でも、Slack ChatOps 自動化（chatops-series）でも、セキュリティ用途（cybersecurity-series）でもない。実験段階ではなく実用を前提とするため lab-series でもない。

## 7. External Platform Constraints

**Slack 公式 MCP**

1. アプリ登録時に `http://` の loopback redirect URI を拒否する。`callbackScheme: "https"` とエフェメラル自己署名証明書が必須で、ブラウザは 1 回だけ「安全でない接続」の警告を出す（クリックスルーが正常動作）。証明書は SAN に 127.0.0.1 / ::1 / localhost を含み、メモリ内のみに存在する。
2. token endpoint が `oauth.v2.user.access` という非標準パスであり、RFC 8414 の discovery では得られない。手書き設定が必要。
3. トークンローテーションが無効の場合、`refresh_token` が空で `expires_in` も返らない。これを**無期限として扱う**（ADR-0003）。人為的に 1 時間の期限を捏造すると毎時再ログインを強いることになる。実際の失効は上流の 401 で検知する。
4. redirect URI はポート固定が必要なため `callbackPort` の明示が要る。

**MCP プロトコル**

- キャンセル通知が存在しない。中断手段は子プロセスの kill のみ。
- stdio MCP サーバは stdout に JSON-RPC 以外を出力すると接続が壊れる。ログは全て stderr に出す。

**Streamable HTTP**

- JSON POST + SSE ストリームの組み合わせ。セッション識別は `Mcp-Session-Id` ヘッダ。

**Claude Code / MCP クライアント側**

- Claude Code は `type: "http"` の MCP サーバを DCR ベース OAuth で直接扱える。したがって DCR 対応プロバイダは本ツールの主対象外（discovery を残すので使えはするが、推奨経路ではない）。この線引きを README に書き、守備範囲を誇張しない。

---

## Discussion Log

**発端** — mcp-guardian のパラメータと設定が分かりづらくなっているという問題提起、および「ガバナンスゲートウェイ機能が有効活用できていないため、MCP proxy 的な部分を抽出したものを作るのがよいのではないか」という提案。

**現状の実測** — 実装と実際の利用状況を調査した結果:

- CLI フラグ 14 個に対し常用は 3 個、profile JSON キー約 26 個に対し使用は 5 個、global config キー約 16 個に対し使用は 0 個（ファイル自体が存在しない）
- 定義済み profile は `aws` と `slack` の 2 件のみ
- state ディレクトリに receipts が 1 本もない = 監査証跡は実質的に生成されていない
- MCP クライアント側の配線は使い捨てのテストプロジェクト 1 件のみ。Slack は slack-mcp-extender に置き換え済み

実運用で効いているのは「stdio クライアントから、事前登録 OAuth クライアントが必要な HTTP MCP サーバへ繋ぐ」一点であることを確認した。

**分かりづらさの原因分析** — 9 件の具体的欠陥を特定（本文 §3 の表を参照）。うち 4 件（advisory の誤動作、strict decode の非対称、存在しないフラグを案内するエラー、README 未記載のフラグ）は方針に関係なく欠陥である。

**抽出対象の再定義** — 抽出すべきは *proxy*（MITM）ではなく **transport bridge**（stdio ⇄ Streamable HTTP + OAuth）であると整理した。名前で実態を正しく言い切らないと同じ肥大化を繰り返すため。コード量の実測では、ブリッジとして必要な部分が約 2,600 行、捨てられる部分が約 2,550 行でほぼ半分ずつ。残す側の中心である OAuth 実装（約 1,500 行）が最も価値の高い資産。

**守備範囲の限定** — Claude Code が `type: "http"` を DCR ベース OAuth で直接扱えるため、「HTTP MCP 全般の橋渡し」と定義すると存在価値が薄い。DCR 非対応で事前登録 confidential client を要求するプロバイダに絞ることで、狭いが確実な価値が残ると判断した。

**方針の選択** — 以下の 4 案を検討:

- A: mcp-guardian を 1 本のまま整理（サブコマンド化・設定 1 階層化・用語修正）
- B: 新規 bridge + mcp-guardian 凍結
- C: 新規 bridge + mcp-guardian を bridge の上に載せ替え
- D: 欠陥修正のみ先行

**新規にブリッジを製造する**方針を採用。A は 9 件の欠陥を直しても「使わないガバナンス設定 26 キー」が残り、分かりづらさの根本（未使用機能の量）が解決しないため却下。C は外部依存ゼロ方針の下で 2 リポジトリ間の同期コストが常時発生するため却下。

**過去の反省の適用** — mcp-guardian では「wrap/unwrap 統合の直後に wrap 自体を廃止」「`--server-config` の非推奨コードを書いた直後に削除」「インラインフラグを維持した後に profile 専用化」という手戻りが発生していた。いずれも最終形を先に決めていれば中間実装は不要だったもの。今回は実装着手前に本 RFP で最終形を固定する。

**個別の決定事項**:

| 論点 | 決定 | 理由 |
|---|---|---|
| ツール名 | `mcp-bridge` | 実態そのまま。util-series の `<domain>-<role>` 命名規則に合う。同名 OSS は存在するが自社 tap 配布なので実害なし |
| 設定形式 | JSON（外部依存ゼロ維持） | OAuth シークレットを扱うためサプライチェーンを増やさない。組織の TOML 規約とは衝突するが、依存ゼロを優先 |
| 監査ログ | なし | 純粋なブリッジに徹する。軽量 JSONL も持たない |
| 認証方式 | authorization_code / 認証なし・静的ヘッダ / tokenCommand | client_credentials は stdio ブリッジという用途で出番がないため除外 |
| discovery | RFC 8414 metadata + RFC 7591 DCR の両方を残す | DCR 対応サーバも mcp-bridge で一元管理できるようにする |
| CLI 構成 | サブコマンド 6 個 + フラグ 2 個 | フラグの組み合わせでモードを切り替える設計が分かりづらさの主因だったため |
| 設定ファイル構成 | 単一ファイル | テレメトリを落とした結果、グローバル層に置くものが無くなった |
| mcp-guardian の処遇 | 本 RFP のスコープ外 | 別途決定する。少なくとも本プロジェクトは mcp-guardian に依存しない |
