---
description: エージェントが使う devhub CLI。サーバーが動いていなくても環境の起動・停止ができる経路と、devhub start を自分で実行してはいけない理由。
---

# エージェント向け CLI

`/ai-api`（[`agent/ai-api`](ai-api.md)）と違い、CLI は **devhub サーバーが動いていなくても
使える**。ローカルの SQLite を直接読み、OS のプロセス／ポートを直接操作するため。

承認プロンプトも挟まらない。CLI を実行できる時点でそのマシンのシェルが取れているので、
`/ai-api` のような追加のゲートを置く意味がないという整理になっている。

## サーバーは自分で起動しない

**`devhub start` をエージェントが実行してはいけない。** フォアグラウンドで動き続けるので、
そのままセッションが張り付く。

サーバーが必要なら **ユーザーに依頼する**:

> devhub のサーバーが動いていないようです。ターミナルで `devhub start` を実行してもらえますか。

起動しているかの確認だけならエージェントでもできる（下記 `devhub status`）。

## 状態を調べる

```bash
devhub status    # 設定ポートで動いていれば情報を出す。動いていなければ exit 1
devhub doctor    # どの devhub が起動対象か / 実際に動いているのはどれか
devhub version
```

`doctor` は複数の devhub（リリース版・Homebrew・手元ソース）が混在する環境で、
「直したはずの挙動が反映されない」ときに効く。警告があれば exit 1。

`status` / `stop` は **devhub であることを確認してから**しか手を出さない。
無関係なプロセスが同じポートを掴んでいる場合は拒否して、その旨を表示する。

## 環境の起動・停止

```bash
devhub env list                       # 環境一覧と稼働中ポート
devhub env status <env-id>            # 各コンポーネントの状態とシナリオ
devhub env start <env-id>             # 依存順に起動（worktree 解決・offset 採番込み）
devhub env stop <env-id>              # その環境のポートで LISTEN 中のプロセスを kill
devhub env switch <env-id> <scenario> # シナリオ切替（計画を表示して確認を求める）
devhub env switch <env-id> --stop     # シナリオ範囲のコンポーネントを停止
```

`env-id` が分からなければ `devhub env list`。

注意点:

- `stop` は **保護ポート**（ports ツールで設定）と devhub 本体のポートには手を出さない
- `start` は baton セマンティクス —— 宣言ポートを他が握っていれば奪取する。
  何を kill したかは表示される
- `switch` は停止・維持・起動の計画を出して**確認を求める**。エージェントから
  非対話で流すなら `-y`（`--yes`）が要る。ただし何が止まるかを先にユーザーへ示すこと
- `env status` は compose_service のコンポーネントに対して `docker compose ps` を実行する

## ドキュメントを読む

```bash
devhub docs list          # name と description の一覧（JSON）
devhub docs show <name>   # 本文
```

ドキュメントはバイナリに埋め込まれているので、リポジトリのチェックアウトも
ネットワークも要らない。手元の devhub のバージョンに対応した内容が出る。

## 環境変数

| 変数 | 意味 |
|---|---|
| `DEVHUB_PORT` | 接続先／bind するポート（設定値より優先） |
| `DEVHUB_HOME` | データ保存先（既定 `~/.devhub`） |
