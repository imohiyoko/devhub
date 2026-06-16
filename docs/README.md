# docs

devhub のドキュメント置き場。ADR（Architecture Decision Record）を **ツール単位** で管理する。

## レイアウト

```
docs/
  root/          リポジトリ全体・複数ツールに跨る決定、ハブ（/ ダッシュボード）
  git/           git ツール
  workspace/     workspace ツール
  diagram/       diagram ツール
  db-table/      db-table ツール
  ports/         ports ツール
  env-launcher/  env-launcher ツール
  diff-kun/      diff-kun ツール
```

## ADR 規約

- 各ディレクトリ内で `NNNN-kebab-title.md`（4桁連番、**ディレクトリごとに**採番）。
- 先頭に `Status`（`Proposed` / `Accepted` / `Superseded by NNNN` / `Deprecated`）と日付を書く。
- 1つのツールに閉じる決定はそのツールのディレクトリへ。**複数ツールに跨る**決定は `root/` に置く。
- 既存 ADR を覆す場合は新規 ADR を起こし、旧 ADR の Status を `Superseded by ...` に更新する（履歴を消さない）。

## 一覧

| ADR | 概要 |
|-----|------|
| [root/0001](root/0001-reversible-hide-hidden-repos-vs-excludes.md) | git の「非表示」は `excludes` ではなく `hidden_repos`（取り消し可能・フロントフィルタ）を用いる |
| [git/0001](git/0001-worktree-cleanup-suggestions.md) | worktree／マージ済みローカルブランチのクリーンアップを理由別トーストで提案する（マージ済み worktree・ディレクトリ欠落・マージ済みブランチ） |
