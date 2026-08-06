---
description: エージェントが devhub を HTTP から操作するための入口。/ai-api の使い方、トークンが要らない理由、書き込みがユーザーの承認を待つ仕組み。devhub の API を叩くならまずこれを読む。
---

# /ai-api — エージェント向け HTTP 面

devhub の HTTP API には入口が 2 つある。

| 入口 | 認証 | 想定する呼び出し元 |
|---|---|---|
| `/api/...` | セッショントークンが必須 | devhub 自身のページ（トークンは配信時に注入される） |
| `/ai-api/...` | **トークン不要** | ローカルの CLI / エージェント |

トークンは devhub が自分のページに埋め込むものなので、ブラウザ以外の呼び出し元が
入手する手段はない。**エージェントは `/ai-api` を使う。**

パスは `/api` と 1 対 1 で対応する。`/api/ports` を叩きたいなら `/ai-api/ports`。

```bash
curl -s http://localhost:8765/ai-api/ports
```

## 3 つの前提条件

`/ai-api` は次を満たさないと 403 を返す。

1. **同じマシンから接続する**。devhub は 127.0.0.1 にしか bind せず、リクエスト単位でも
   ループバックを確認する（`not_loopback`）
2. **`Sec-Fetch-Site` を送らない**。ループバックであることは「ブラウザではない」証明にならない
   —— ユーザーがたまたま開いている外部サイトも 127.0.0.1 を fetch できてしまう。
   ブラウザはクロスサイトのリクエストに `Sec-Fetch-Site` を付けるので、devhub はそれを弾く。
   curl や HTTP クライアントは付けないので影響を受けない（`cross_site`）
3. **Host は localhost か 127.0.0.1**（`host_not_allowed`）

## 書き込みはユーザーの承認を待つ

**これが `/ai-api` を使ううえで一番重要な性質。**

読み取り（GET）はそのまま通る。しかし**書き込み（GET 以外のすべて）は、devhub の
ダッシュボード上でユーザーがボタンを押すまでブロックする**。押されなければ通らない。

例外的に `GET /api/open` も承認を要する。読み取りに見えてエディタを起動するため。

逆に、承認を待っても通らないルートもある。`POST /ai-api/logs/clear` と
`/ai-api/logs/archive` は `no_ai_api_route`（403）で即座に拒否される ——
エージェントが自分の活動記録を消せてはならないため。ログを**読む**
`GET /ai-api/logs` は承認なしで通る。

そのログを引くときは、**`/ai-api` 接頭辞で絞っても何も返らない**。記録される
パスは `/api/...` に正規化されているので、`path=/ai-api/git` ではなく
`path=/api/git` で絞り、入口の区別は `surface` 列（`api` / `ai-api`）で行う。
1 つのパスフィルタが両方の入口を拾えるようにするための正規化で、これが無いと
「エージェントのリクエストだけが落ちる」という一番困る取りこぼし方になる。

なお `source=archive` を引くときは、**この正規化より前にアーカイブされた行は
`/ai-api/...` のまま残っている**。`path=/ai-api/git` で絞ると「何も返らない」ではなく
「古い行だけが返る」ので、新旧が混ざる期間は `surface=ai-api` で絞るほうが確実。

つまり **エージェントだけでは書き込みを完了できない**。書き込みを投げる前に、
ユーザーに「今から承認ダイアログが出るので押してほしい」と伝えること。誰も見ていない
状態で投げると 60 秒待って `approval_timeout` で失敗する。

承認プロンプトには、メソッド・パス・**クエリ文字列**・**リクエストボディの要約**が
表示される。`password` / `token` / `apikey` を含むキーは、ボディでもクエリでも
`***` に伏せられる。JSON でないボディは中身を出さず `(non-JSON body, N bytes)` になる。

クエリまで含めて表示するのは、それが承認の対象そのものだから。`GET /api/open` は
何を開くかがクエリにしかないので、パスだけで承認すると「どのディレクトリでも開いてよい」
を承認したことになってしまう。「常に許可」も同じ粒度で効く —— `?path=/a` を許可しても
`?path=/b` は改めて確認される。

### 承認の結果

| 結果 | HTTP | `code` | どうするか |
|---|---|---|---|
| 承認された | 実際の応答 | — | 続行 |
| 拒否された | 403 | `approval_rejected` | **再試行しない**。ユーザーに意図を確認する |
| 60 秒無反応 | 408 | `approval_timeout` | ユーザーに承認を依頼してから**投げ直す** |

`approval_timeout` のとき、そのリクエストは承認待ち一覧から既に消えている。
「ダッシュボードに出ているものを承認して」と頼んでも**何も出ていない**ので、
必ず送り直すこと。

### always-allow

ユーザーは承認時に「常に許可」を選べる。以後、同じ操作は待たずに通る。

照合の単位は**承認プロンプトに出たものと同じ 1 行**、つまり
`メソッド + パス + 秘匿化・正規化済みクエリ + ボディ要約` である。
上に書いたとおりクエリも含まれるので、`?path=/a` を許可しても `?path=/b` は
改めて確認される。ボディが違う場合も同じ。

**「見えたものだけが許可される」が原則**で、プロンプトに出ていない差分が
黙って通ることはない。逆に言えば、同じ操作を繰り返すつもりなら
クエリまで含めて同一にする必要がある。

## エラーの読み方

エラー応答は次の形をとる。

```json
{
  "error": "approval timed out",
  "code":  "approval_timeout",
  "hint":  "Nobody answered within 1m0s, ..."
}
```

- `error` は人間向けの文言。**将来変わりうるので条件分岐に使わない**
- `code` は安定した識別子。分岐にはこれを使う
- `hint` は次に取るべき行動

`code` の一覧と対処は [`agent/troubleshooting`](troubleshooting.md) にある。

## 主なエンドポイント

`GET /ai-api/tools` がツール一覧を返す。以下は代表的なもの。

| メソッド | パス | 内容 | 承認 |
|---|---|---|---|
| GET | `/ai-api/info` | ポート・バージョン・インスタンス ID | 不要 |
| GET | `/ai-api/tools` | ツール一覧 | 不要 |
| GET | `/ai-api/docs` | ドキュメント一覧 | 不要 |
| GET | `/ai-api/ports` | LISTEN 中の TCP ポート | 不要 |
| GET | `/ai-api/repos` | スキャン対象のリポジトリ | 不要 |
| GET | `/ai-api/envs` | env-launcher の環境定義 | 不要 |
| GET | `/ai-api/git/status?...` | git status ほか | 不要 |
| GET | `/ai-api/containers` | コンテナ一覧 | 不要 |
| GET | `/ai-api/open?path=...` | エディタで開く | **要** |
| POST | `/ai-api/envs/launch` | 環境を起動 | **要** |
| POST | `/ai-api/git/...` | commit / push / worktree など | **要** |
| POST | `/ai-api/ports/kill` | プロセスを kill | **要** |
| POST | `/ai-api/containers/start` | コンテナを起動 | **要** |
| POST | `/ai-api/containers/stop` | コンテナを停止 | **要** |
| POST | `/ai-api/containers/profiles/{name}/start` | Colima VM を起動 | **要** |
| POST | `/ai-api/containers/profiles/{name}/stop` | Colima VM を停止（中のコンテナは全部落ちる） | **要** |
| POST | `/ai-api/settings` | 設定を書き換え | **要** |

## サーバーが起動していないとき

`/ai-api` は当然ながら devhub が動いていないと使えない。環境の起動・停止だけなら
**サーバーなしで CLI から実行できる**（`devhub env start` / `env stop`）。
[`agent/cli`](cli.md) を参照。
