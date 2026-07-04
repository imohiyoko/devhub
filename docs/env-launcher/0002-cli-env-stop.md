# 0002. CLI からの環境停止は DB 直読み + OS kill で行う（HTTP を経由しない）

- **Status**: Accepted (2026-07-05)
- **対象ツール**: env-launcher（一部 ports）
- **関連**: docs/env-launcher/0001（検証環境は既存 env-launcher で立てる）

## Context（背景）

env-launcher で起動した環境を止める手段が GUI（launch 一覧のポート別 kill ボタン）
しかなかった。「`devhub env stop <env-id>` のようにコマンドで止めたい」という要件に対し、
CLI がサーバーとどう通信するかが論点になった。

- devhub の API トークンは**サーバープロセスのメモリにのみ**存在する（起動毎に生成、
  環境変数からも意図的に unset）。CLI が `/api/` を叩くにはトークンをディスクへ
  永続化する必要があり、これはセキュリティ姿勢の後退になる。
- `/ai-api/` は書き込みに手動承認（ダッシュボード上のクリック）が要るため、
  CLI の用途（UI を開かずに操作したい）と矛盾する。
- 一方、状態は 1 つの SQLite（WAL + busy_timeout）にあり、**別プロセスからの
  読み取りは安全**。また「停止」の実体は元々「対象ポートの LISTEN プロセスを
  kill する」ことであり（UI の kill ボタンと同じ）、サーバーの協力を必要としない。

## Decision（決定）

**`devhub env list` / `devhub env stop <env-id>` サブコマンドを追加し、
HTTP を経由せず、共有 SQLite の読み取り + OS レベルのポート kill で実装する。**

- 対象ポートの算出は「env 定義の宣言ポート spec ∪ launch レコードのポート
  （offset 採番があれば `assigned_port`、なければ記録された spec）」。
  UI の稼働バッジ（`enrichLaunches`）と同じ優先順位。
- kill は `ports` コントローラの `KillPortProcess` を再利用する。したがって
  **保護ポート（protected_ports）は CLI からも殺せない**。
- **devhub 本体のポート（settings の `port`、`DEVHUB_PORT` で上書き）は kill
  対象から常に除外**し、skip として報告する。`devhub-verify` のような env は
  base port に 8765 を宣言する（実際の検証インスタンスは offset で 8766+ に載る）
  ため、除外しないと本体を巻き添えにする。
- CLI は **launch レジストリへ書き込まない**。`SaveLaunches` は全テーブル
  書き換えをプロセス内 mutex（`RegistryMu`）で直列化しており、別プロセスからの
  書き込みはレコード喪失の競合を生み得る。停止後もレコードは UI に「停止」として
  残る（UI の kill ボタンと同じ挙動。掃除は既存の「停止中をクリア」）。

### スコープ外（YAGNI）

- `devhub env start` は今回入れない。起動は launch レコードの追記（= レジストリ
  書き込み）を伴うため、上記の競合を先に解く必要がある。必要になった時に
  「レジストリ書き込みをサーバー経由にする」か「AppendLaunch を行単位 INSERT に
  変える」かを別 ADR で決める。**→ 0003 で行単位 INSERT 化を採用し、start を追加した。**
- サーバー側への `/api/envs/stop` エンドポイント追加（UI の「全て停止」ボタン）も
  見送り。追加する場合は `StopEnvironment` をそのまま配線すればよい。

## Consequences（結果）

### 正
- サーバーが落ちていても（むしろ落ちている時こそ）環境を止められる。
- トークンの永続化なし・新規 HTTP サーフェスなしで、攻撃面が増えない。
- 保護ポート・kill の安全チェックは ports コントローラの一枚岩を通るため、
  GUI と CLI で挙動が乖離しない。

### 負 / 留意（受け入れる）
- 「ポートを宣言していないプロセス」（例: `docker compose up db` だけの行）は
  CLI からは止められない。これは UI の kill ボタンも同じ制約。
- ポートで kill するため、ターミナルウィンドウ自体は残る（中のプロセスだけ死ぬ）。
  UI kill と同じ。
- `DEVHUB_PORT` を変えて CLI を呼ぶと除外ポートも変わる。「CLI が話しかける先の
  インスタンスを守る」という予測可能な規則として受け入れる。

## Alternatives considered（検討した代替案）

1. **トークンをディスクへ永続化して HTTP 経由**（Jupyter の runtime ファイル方式）—
   サーバー稼働が前提になる上、メモリのみというトークンの設計を崩す。不採用。
2. **`/ai-api/` を CLI から叩く** — 書き込みは手動承認が必要で「UI を開かず操作」
   という目的と矛盾。always-allow ルールで回避するのは承認機構の趣旨に反する。不採用。
3. **`scripts/dev.sh stop` の拡張** — あれは devhub サーバー自身を止める開発用
   ヘルパで、配布バイナリ利用者には届かない。ユーザー向け機能は `devhub` 本体の
   サブコマンドであるべき。不採用。
