---
description: 引数なしの devhub がサーバーを起動せずヘルプを表示する理由。devhub を実行したのに起動しない、と感じたときに読む。
---

# 0003. サーバー起動は明示的な `devhub start` にし、bare `devhub` はヘルプにする

- **Status**: Accepted (2026-07-05)
- **対象**: cmd/devhub / scripts / installers / docs
- **関連**: [root/0002](0002-single-command-slot-and-cli.md)（決定5「未知サブコマンドはサーバー起動にフォールバックさせない」の延長）

## Context（背景）

0002 の決定5で「`devhub <未知語>` はサーバー起動にフォールバックせずエラー」とした
（typo で意図せずサーバーが立つ事故を防ぐため）。だが **引数なしの `devhub`**
だけは依然「サーバー起動（default action）」のままで、同じ事故の余地が残っていた:

- コマンドスロットの実体（dev shim / リリース版 / repo 直下の exe）を確かめようと
  反射的に `devhub` と打つと、**その場でサーバーが立ち上がる**。ポートを掴み、
  ブラウザまで開く。「何が起動するか見たいだけ」の操作が状態を変えてしまう。
- 「起動する」という副作用の大きい動作が、他の読み取り系サブコマンド
  （status/doctor/env list）と違って**無名のデフォルト**に隠れていた。

## Decision（決定）

1. **サーバー起動を `devhub start` に一本化する**。`-no-browser` は `start` の
   フラグ（`devhub start -no-browser`）。
2. **bare `devhub`（引数なし）はヘルプを表示**して終了する（exit 0）。サーバーは
   起動しない。純情報系の `-version` / `-h` はトップレベルでも従来どおり効く。
3. 自己再起動（restart / update / rebuild）は `os.Args` をそのまま引き継ぐため、
   `devhub start` で起動したインスタンスは再起動後も `start` を保持する。起動経路を
   `start` に一本化したことで、「再起動後に引数なしになってヘルプが出てサーバーが
   立たない」事故は起きない。
4. 「起動」を前提にしていた各所を `start` に更新する: `scripts/dev.{ps1,sh}` の
   `run`/`restart`、`.mise.toml` の `dev` タスク、install スクリプトの起動案内、
   README/CONTRIBUTING。dev shim 本体（`go run ./cmd/devhub %*`）は素通しなので
   変更不要（`devhub start` はそのまま `go run ./cmd/devhub start` になる）。

## Consequences（結果）

### 正
- 「起動」という副作用が名前を持ち、反射的な `devhub` がポートを掴む事故が消える。
- 読み取り系（status/doctor/env list）と副作用系（start/stop）が対等に並ぶ。
- 0002 決定5と同じ「意図しないサーバー起動を防ぐ」思想を bare にも一貫させた。

### 負 / 留意（受け入れる）
- **破壊的変更**: 従来 `devhub` = 起動、が `devhub` = ヘルプに変わる。手癖・ドキュメント・
  スクリプトの追従が要る（本 PR で同梱の追従を実施）。リリース版利用者は
  `devhub start` を打つことになる（install の起動案内も更新）。
- 稼働中の旧インスタンスは無関係（この変更はコマンド入口の挙動のみ）。

## Alternatives considered（検討した代替案）

1. **bare `devhub` は起動のまま、切替スイッチャ `devhub slot use …` を足す** —
   0002 Alt#2 で YAGNI として不採用にした線を蒸し返す上、「暗黙の起動方式を管理する
   機構」を増やす。副作用側に名前を与える本案の方が複雑さを増やさず筋が良い。不採用。
2. **bare `devhub` を起動のまま、初回だけ確認プロンプト** — 非対話環境（CI・
   スクリプト・GUI 起動）で破綻する。不採用。
