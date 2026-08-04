# 0005. 実行基盤の選択（Docker context を変更しない・engine を暗黙に変えない）

- **Status**: Accepted (2026-08-04)
- **対象ツール**: env-launcher
- **関連**: 計画 `devhub-env-launcher-runtime-plan.md` §6.2 / §6.3 / §6.4 / §13、docs/env-launcher/0004（状態モデル）

## Context（背景）

複数の検証環境を Colima profile で分けたい、profile によっては containerd を
使いたい、という要求がある。一方で Colima は macOS 前提の任意ツールであり、
Linux / Windows の既存ユーザーの env-launcher を壊してはいけない。

ここで安易にやると壊れるものが 2 つある。

1. **`docker context use`** はグローバル設定を書き換える。devhub が実行すると、
   ユーザーが開いている**他のターミナルのすべての docker コマンド**が別の
   エンジンを向く。devhub の UI 操作が devhub の外に漏れる。
2. **profile の engine 変更**は、その profile の既存イメージとコンテナに影響する。
   「設定に containerd と書いたから切り替える」は、ユーザーのデータを壊しうる。

## Decision（決定）

### `docker context use` を実行しない。context はコマンド単位で渡す

環境が Colima provider を宣言していれば、その profile の Docker context を
**毎回の argv に** `--context colima-<profile>` として付ける。宣言が無ければ
`--context` を付けず、ユーザーのシェルが解決する ambient context をそのまま使う
（定義を書いた時点の context を固定しない）。

`--context` は `docker` 自身のフラグなので `compose` サブコマンドの**前**に置く
（`docker compose --context …` は unknown flag として拒否される）。

containerd では context ではなく `colima nerdctl --profile <p> -- compose …` で
profile を毎回明示する。どちらも「グローバルな選択状態を持たない」点で同じ。

### engine は profile の属性として扱い、暗黙に変更しない

- 設定の `engine` は「この profile はこの engine のはず」という**表明**であり、
  「この engine に切り替えろ」という指示ではない。
- 実体と食い違ったら**警告して、書かれたとおりに動かす**。別 profile を作るか
  profile を作り直すよう案内する。devhub が engine を変えることはない。
- 停止中の profile は engine を報告しない。**不明を不明のまま扱い**、推測しない
  （`colima.yaml` を読みに行けば分かるが、colima の内部レイアウトへの結合を作る）。
- devhub は profile を**起動も停止も再構成もしない**。停止中なら
  `colima start -p <profile>` を案内するに留める。VM の起動は数分かかり
  リソースを確保する、ユーザーの判断であるべき操作だから。

### Colima は optional capability として扱う

- macOS かつ `colima` が存在する場合だけ呼ぶ。**Linux / Windows ではコマンドを
  組み立てもしない**。
- capability API は provider ごとに `available`（今使えるか）と `supported`
  （このOSで使えうるか）を分けて返す。UI は前者を理由付きで見せ、後者が偽なら
  欄ごと隠す。「Colima を入れれば使える」と「あなたのOSでは無理」は別の話で、
  後者を灰色表示しても雑音にしかならない。
- provider が公開する engine 一覧は **devhub がアダプタを持つもの**に限る。
  Colima は incus も動かせるが、選べて動かせない選択肢を出すくらいなら
  理由付きで対象外と言う。

## Consequences（結果）

### 正
- devhub の操作が devhub の外に漏れない。ユーザーの他ターミナルは無傷。
- engine 起因のデータ破壊が構造的に起きない（devhub は engine を変えない）。
- Colima 非導入・非 macOS で既存機能が壊れない。
- capability を API から返すので、UI に provider 名や engine 名を焼き込まない。

### 負・制約
- profile が停止中だと engine が不明なままで、切替時に「起動しておいてください」
  という警告が出る。devhub が起動してあげるほうが親切に見えるが、採らない。
- containerd では起動完了（healthcheck）を待てない。`nerdctl compose up` に
  `--wait` が無いため、`up --detach` の成功は「コンテナが作られた」であって
  「ready」ではない。依存 component が先に起動しうることを警告で明示する。
- capability の取得に `docker compose version` と `colima list` の 2 プロセスを
  使うため、UI の初回ロードに 1 秒弱かかる。ポーリングはしない。

## 代替案

- **`docker context use` で切り替える**: 実装は最も簡単だが、devhub の外に
  副作用が出る。却下。
- **profile の engine を devhub が変更する**: 「設定どおりにする」という意味では
  一貫するが、既存イメージ／コンテナへの影響が大きく、UI のボタン一つで
  起こしてよい操作ではない。
- **engine を設定に持たず、常に実体から判定する**: 食い違いが検出できなくなり、
  「書いたものと動くものが違う」ことにユーザーが気付けない。
