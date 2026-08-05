---
description: docs/ の構成と ADR の書き方。devhub の設計判断がどこに記録されているかを探すときの入口。
---

# docs

devhub のドキュメント置き場。**ADR**（Architecture Decision Record）を **ツール単位** で、
**agent 向け how-to** を `agent/` で管理する。

## レイアウト

```
docs/
  agent/         agent 向け how-to（ADR ではない。下記参照）
  root/          リポジトリ全体・複数ツールに跨る決定、ハブ（/ ダッシュボード）
  git/           git ツール
  workspace/     workspace ツール
  diagram/       diagram ツール
  db-table/      db-table ツール
  ports/         ports ツール
  env-launcher/  env-launcher ツール
  diff-kun/      diff-kun ツール
  containers/    containers ツール
```

## 2 種類の文書

`agent/` 以外はすべて **ADR** —— 「なぜそう決めたか」の記録で、読者は devhub を
**変更する人**。

`agent/` は **how-to** —— 「どう使うか」「なぜ失敗したか」で、読者は devhub を
**操作するエージェント**。エラーの `hint` がここを名指しするので、
書き換えるときはリンク元（`internal/server/router.go` の hint 文字列）も確認する。
詳細は [root/0005](root/0005-agent-operable-devhub.md)。

## 埋め込みとコマンド

`docs/` はバイナリに埋め込まれ、次の経路で読める。

```bash
devhub docs list           # name と description の一覧（JSON）
devhub docs show <name>    # 本文
```

HTTP からは `GET /api/docs` と `GET /api/docs/<name>`（`/ai-api` でも可・承認不要）。

そのため **全ての `.md` に YAML frontmatter の `description` が必須**。
`docs list` は name と description しか出さないので、description が無い文書は
一覧の中で選びようがない。テスト（`internal/docs`）で強制している。

## ADR 規約

- 各ディレクトリ内で `NNNN-kebab-title.md`（4桁連番、**ディレクトリごとに**採番）。
- 先頭に `Status`（`Proposed` / `Accepted` / `Superseded by NNNN` / `Deprecated`）と日付を書く。
- 1つのツールに閉じる決定はそのツールのディレクトリへ。**複数ツールに跨る**決定は `root/` に置く。
- 既存 ADR を覆す場合は新規 ADR を起こし、旧 ADR の Status を `Superseded by ...` に更新する（履歴を消さない）。

## agent 向け how-to

| 文書 | 概要 |
|-----|------|
| [agent/ai-api](agent/ai-api.md) | `/ai-api` の使い方。トークンが要らない理由、書き込みがユーザーの承認を待つ仕組み、主なエンドポイント |
| [agent/troubleshooting](agent/troubleshooting.md) | エラー `code` 一覧と対処。再試行すべきか諦めるべきかの判断 |
| [agent/cli](agent/cli.md) | サーバー停止中でも使える CLI 経路と、`devhub start` を自分で実行してはいけない理由 |

## ADR 一覧

| ADR | 概要 |
|-----|------|
| [root/0001](root/0001-reversible-hide-hidden-repos-vs-excludes.md) | git の「非表示」は `excludes` ではなく `hidden_repos`（取り消し可能・フロントフィルタ）を用いる |
| [root/0002](root/0002-single-command-slot-and-cli.md) | コマンドスロットは 1 つのまま（最後の install が勝つ）、置き換えは告知し `devhub doctor`/`status`/`stop` で見える化する |
| [root/0003](root/0003-devhub-start-explicit.md) | サーバー起動は明示的な `devhub start` に一本化し、bare `devhub` はヘルプ表示にする（反射的な起動事故を防ぐ） |
| [root/0004](root/0004-devhub-start-provenance.md) | `devhub start <provenance>`（binary/homebrew/code）で起動元を都度選ぶ。スロットや PATH は変えない per-invocation ランチャ（0002 Alt#2 の永続スイッチャとは別物） |
| [root/0005](root/0005-agent-operable-devhub.md) | エラーに `code`/`hint` を付け、承認の拒否（403）と時間切れ（408）を分け、`devhub docs` と揮発リクエストログを追加する（エージェントが失敗から立ち直れるようにする） |
| [git/0001](git/0001-worktree-cleanup-suggestions.md) | worktree／マージ済みローカルブランチのクリーンアップを理由別トーストで提案する（マージ済み worktree・ディレクトリ欠落・マージ済みブランチ） |
| [env-launcher/0001](env-launcher/0001-worktree-verification-launch.md) | 検証環境は専用機能を作らず、既存 env-launcher（worktree バインド + offset ポート + `{{port}}`）で立てる |
| [env-launcher/0002](env-launcher/0002-cli-env-stop.md) | `devhub env list` / `devhub env stop` は HTTP を経由せず、共有 SQLite の直読み + OS ポート kill で実装する |
| [env-launcher/0003](env-launcher/0003-cli-env-start.md) | `devhub env start` を追加。launch レジストリを行単位 INSERT/DELETE 化して別プロセス書き込みを安全にし、baton の奪取は CLI でも表示付きで実行する |
| [env-launcher/0004](env-launcher/0004-component-state-model.md) | コンポーネントの稼働状態はポートで近似し、判定できないものは `unknown` として扱う |
| [env-launcher/0005](env-launcher/0005-runtime-selection.md) | `docker context use` を実行せず、コンテナ実行基盤（Docker context / Colima profile）を暗黙に切り替えない |
