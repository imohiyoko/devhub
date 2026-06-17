# 0001. 検証環境は専用機能を作らず、既存 env-launcher で立てる

- **Status**: Accepted (2026-06-17)
- **対象ツール**: env-launcher（一部 git）
- **関連**: docs/git/0001（worktree クリーンアップ提案）

## Context（背景）

「ブランチ / PR を渡したら、その検証環境を立ち上げたい」という要件が増えている。
専用の「検証起動」機能（起動時にブランチ/PRを渡して worktree を自動作成し起動する経路 +
専用モーダル）を作るかどうかを検討した。

ただし env-launcher にはすでに必要な部品が揃っている。

- **worktree バインド**: env / プロセス単位で、git ツールが作成済みの**既存 worktree** を CWD に。
- **offset ポート**: 空きポートを割り当て、`port_env_var` でアプリに渡す（並列インスタンス向け）。

worktree の作成は git ツールの責務（PR URL からの作成も既存機能）であり、env-launcher は
「既存 worktree を参照するだけ／勝手に作らない」という設計方針を持つ。

## Decision（決定）

**検証環境は専用機能を作らず、既存 env-launcher の通常フローで立てる。**

運用フローは 2 ツールにまたがるが、いずれも既存機能で完結する。

1. **git ツール** で検証したいブランチ / PR の worktree を作成（PR URL からの作成も可）。
2. **env-launcher** で、その worktree にバインドし offset ポートを持つ env を `▶ 全て起動`。
   別の PR を検証するときは env の binding ブランチを編集モーダルのドロップダウン
   （既存 worktree から自動列挙）で選び直す。

env-launcher 側に「起動時 worktree オーバーライド」や専用の「検証起動」UI は**追加しない**。
「env-launcher は worktree を作らない」不変条件をそのまま保つ。

### 今回の最小限の追加（検証専用ではない汎用改善）

- **`{{port}}` プレースホルダ置換**（`backend/controllers/envs.py`）:
  offset 起動時、コマンド内の `{{port}}` を割り当てポートに置換する。これにより、
  - 環境変数を読むアプリ（例: devhub の `DEVHUB_PORT`）はそのまま、
  - ポートを CLI 引数 / make 変数で受けるアプリは `myapp --port {{port}}` /
    `PORT={{port}} make run`

  のいずれでも offset に乗れる。割り当てポートがある時のみ作用し、それ以外は素通し。

  **注意**: offset の判定（`_is_offset`）と保存時バリデーションは、現状 `port_env_var`
  の宣言を必須とする。そのため `{{port}}` を CLI 引数で受けるアプリでも、offset に乗せるには
  `port_env_var` を宣言する必要がある（その env 変数も export されるが、アプリが読まなければ
  無視されるだけで害はない。`settings/envs.example.json` の `cli-port-verify` 参照）。
  この「ダミー `port_env_var` が必要」な制約を外すかは、必要になった時に別途検討する（YAGNI）。
- **git.py の関数抽出**（`add_worktree` / `ensure_worktree_from_pr`）:
  HTTP ハンドラ内インラインだった worktree 確保処理を純関数化。既存エンドポイントが
  これを呼ぶよう置換しただけで**挙動は不変**（既存テストで担保）。失敗は
  `WorktreeError(message, status)` で送出し HTTP 層がマップする。
- **見本 env**（`settings/envs.example.json`）:
  `devhub-verify`（devhub 自身を offset + `DEVHUB_PORT` で検証起動）と、
  `cli-port-verify`（ポートを CLI 引数 `--port {{port}}` で受けるアプリの offset 例）。

## Consequences（結果）

### 正
- 新しい概念・専用UIを増やさず、既存の env-launcher / git ツールだけで検証環境が立つ。
  設計方針（作成は git、参照は env-launcher）に一貫。
- `{{port}}` により offset が「環境変数を読まないアプリ」にも広く使えるようになった
  （検証用途に限らない汎用改善）。

### 負 / 留意（受け入れる）
- 別の PR を検証するたびに env の binding ブランチを選び直す一手間がある（worktree さえ
  作ってあればドロップダウン選択）。「PR URL 一発で起動」までは自動化しない。
- ポートを**外から一切指定できない**（env も CLI も設定もない、ハードコード）アプリは
  offset 不可。その場合は `baton`（固定ポートを kill して奪取）にフォールバックし、
  **並列 worktree は不可**。

## Alternatives considered（検討した代替案）

1. **専用「検証起動」機能（起動時 worktree オーバーライド + 専用モーダル）** — PR URL 一発で
   worktree 作成〜起動まで完結でき便利だが、env-launcher に新概念と専用UIを増やし、
   「worktree を作らない」不変条件を破る。最小主義に反するため不採用。既存の git ツール
   ＋ env-launcher の組み合わせで要件は満たせる。
2. **`{{port}}` を入れず `$PORT` 参照のみ** — offset は元々 env を export するので `$PORT`
   でも渡せるが、CLI 引数アプリには明示的なプレースホルダの方が分かりやすいので併設した。
