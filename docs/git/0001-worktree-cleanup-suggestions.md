---
description: git ツールが worktree とマージ済みローカルブランチの削除を提案する条件と、理由別に分けている根拠。片付け提案が出る／出ない理由を調べるときに読む。
---

# 0001. worktree のクリーンアップを「マージ済み」「ディレクトリ欠落」の理由別に提案する

- **Status**: Accepted (2026-06-16)
- **対象ツール**: git
- **関連**: docs/root/0001（非表示の reversible 方針）

## Context（背景）

git ツールの worktree タブは worktree の追加・削除はできるが、**不要になった worktree を
見つけて消す導線がない**。実運用では 2 種類のゴミが溜まる。

1. **マージ済みブランチの worktree** — PR がマージされたのに worktree が残る。
2. **ディレクトリが消えた worktree** — ユーザーが手で worktree ディレクトリを削除したが、
   git の管理情報（`.git/worktrees/<name>`）が残り、`git worktree list` に幽霊として残る。

この 2 つは**原因も適切な後始末コマンドも異なる**ため、ひとまとめにせず別々に扱いたい。

## Decision（決定）

worktree 一覧取得時に各 worktree を検出・注釈し、**理由ごとに 2 つの提案トースト**を出す。

### 検出（バックエンド: `internal/controllers/git/`）
- `/api/git/worktrees` のレスポンスで各 worktree に以下を付与する。
  - `exists`: 対象パスのディレクトリ存在を直接チェックする。porcelain の `prunable` 行ではなく**ディレクトリ存在を直接**
    見る（git バージョン非依存・「ディレクトリがなくなった」要件に直接対応）。
  - `merged`: そのブランチが基準ブランチにマージ済みか。
- 基準ブランチは **origin/HEAD（例 `origin/main`）を優先**し、無ければ
  ローカル `main` → `master` にフォールバック。PR をリモートでマージするとローカル main が
  古いままになりがちなため、リモート基準を優先する。
- マージ済み集合（`_merged_branch_set`）は `git branch --merged <base>` のローカル短縮名集合。
  基準ブランチ自身（フル ref とローカル短縮名の両方）は除外し、誤って提案しない。

### 後始末コマンドの使い分け
- **マージ済み worktree** → `git worktree remove <path>`（既存エンドポイント）。トーストには
  2 ボタンを置き、**「worktree とブランチを削除」**（remove 後に `git branch -D`）と
  **「worktree のみ」**（ブランチを残す）を選べる。worktree を消すまではブランチが checkout 状態で
  削除できないため、必ず remove → branch delete の順に実行する。
- **マージ済みローカルブランチ（worktree なし）** → `git branch -D`（既存 `/api/git/branch/delete`）。
  worktree を持たないマージ済みブランチを一括削除。判定用に `/api/git/worktrees` が返す
  `merged_branches`（マージ済みローカル短縮名）から、worktree が付いているブランチを除いた集合を提案する。
- **ディレクトリ欠落** → 新規 `POST /api/git/worktree/prune`（`git worktree prune -v`）。
  ディレクトリが無い worktree は `git worktree remove` では消せない（"is not a working tree"）ため
  prune が正しい。prune は prunable な worktree をまとめて消すので一括 UX と一致する。

### ブランチ削除に `-d` ではなく `-D`（force）を使う理由
バックエンドが `git branch --merged <base>` で**基準ブランチへのマージ済みを確認した集合のみ**を
提案する。PR でマージしリモートブランチが削除されたケースでは、ローカルの追跡ブランチが消えるため
`git branch -d` は「現在の HEAD にマージされていない」と**誤って拒否**する（これが通常運用で頻発）。
基準ブランチへのマージは確定しているので、提案経由の削除は `-D` を用いる。**`-D` は提案集合
（merged ゲート）に対してのみ使い、UI からの通常のブランチ削除（既存）は従来どおり**。

### UX（フロントエンド: `tools/git/index.html`）
- **理由別にトースト**（マージ済み worktree／ディレクトリ欠落／マージ済みローカルブランチ）。
  各トーストに**対象一覧**を表示し一括処理ボタンを置く。`×` で却下。
- セッション内の**既読管理**（`dismissedCleanup`、キー `${repo}|${path}`）で、却下・実行済みの
  対象を同セッションで再表示しない（ポーリングごとのナグを防止）。同カテゴリのトーストが
  表示中は重複表示しない。
- 検出は worktree を再取得する**遅い cadence**（init / remote poll / 手動操作）でのみ走る。
  高頻度ローカルポーリングは worktree を取らないので走らない（既存の取得方針を踏襲）。
- マージ済みでディレクトリも欠落している場合は**欠落側に寄せ**、二重表示しない。

## Consequences（結果）

### 正
- 不要 worktree の検出と後始末がワンクリックで完結。理由ごとに後始末コマンドが正しく分かれる。
- ブランチを残すため「worktree だけ消したい」要求に合致し、可逆性が高い。

### 負 / 留意（受け入れる）
- `git worktree prune` は prunable な worktree を**まとめて**消すため、ネットワークマウントの
  一時切断などでも対象になり得る。提案＋対象一覧の確認があるため、localhost 単一ユーザー前提で許容。
- マージ済み remove は未コミット/未追跡ファイルがあると失敗し得る（force しない方針）。失敗は
  トーストで通知し、worktree タブからの手動 force remove に委ねる。

## Alternatives considered（検討した代替案）

1. **1 つのトーストにまとめる** — 後始末コマンド（remove / prune）が異なり、理由も別物のため
   ユーザーが判断しづらい。要件どおり理由別 2 トーストにした。
2. **`prunable`（porcelain）行で欠落判定** — git バージョン依存・「prune 猶予」ヒューリスティックが
   絡む。ディレクトリ存在を直接見る方が要件に素直なので**ディレクトリ存在チェック**を採用。
3. **欠落も per-item で `worktree remove --force`** — 欠落ディレクトリには remove が効かない。prune が正道。
