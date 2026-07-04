# 0002. コマンドスロットは 1 つのまま、CLI（status/stop/doctor）で見える化する

- **Status**: Accepted (2026-07-05)
- **対象**: リポジトリ全体（installers / cmd/devhub / scripts）
- **関連**: docs/env-launcher/0002（`devhub env` は DB 直読み + OS kill）

## Context（背景）

`devhub` コマンドの実体は環境によって「リリース版バイナリ」「ソース実行の dev shim」
「repo 直下のビルド済み exe」などがあり、何が起動するのか分かりにくかった。

- リリースインストーラ（install.ps1 / install.sh）と dev shim
  （scripts/dev.{ps1,sh} install）は**同じスロット**
  （`%USERPROFILE%\bin\devhub.cmd` / `~/.local/bin/devhub`）へ**黙って**上書きし合う。
- cmd.exe はカレントディレクトリを PATH より先に探すため、repo 直下の
  `devhub.exe` がスロットを隠すことがある（PowerShell では起きない）。
- Windows ではポート再取得（reclaim）が無効（lsof 前提）なので、新しく起動した
  devhub が bind エラーで死に、**古いインスタンスが黙って生き残る**。
- サーバー自身の停止手段が `scripts/dev.*`（要チェックアウト）にしかなく、
  配布バイナリ利用者には無かった。

## Decision（決定）

1. **スロットは 1 つのまま**とする（最後に install した方が勝つ）。`devhub` という
   1 コマンドに収れんさせるのが目的であり、`devhub-dev` のような別名は増やさない。
2. その代わり **置き換えを必ず告知する**: 4 つのインストーラは既存スロットの種類
   （dev shim / リリース版 / 参照先チェックアウト）を判定し、種類が変わる場合・
   dev shim の参照先が変わる場合に Notice を出す。Windows のリリース版 shim には
   判定用マーカー行（`rem devhub release shim …`）を追加する（dev shim には既存）。
3. **状態を観測可能にする**: `devhub doctor` が「この実行ファイル自身 / スロットの
   種類と PATH 解決順（PATHEXT 含む）/ リリース版バイナリの有無 / 設定ポートの
   稼働インスタンス」を診断し、警告があれば exit 1。
4. **サーバー自身の CLI 制御を追加する**: `devhub status`（稼働確認、非稼働で
   exit 1）と `devhub stop`。stop は kill 前に必ずトークン不要の読み取り面
   `GET /ai-api/info` で相手が devhub であることを確認し、確認できなければ拒否する
   （汎用ポートキラーにしない。protected_ports / self-PID チェックは「他アプリと
   稼働中サーバーを守る」ためのものなので、検証済みの自分自身を止めるこの経路では
   通さない — `ports.KillPID` を CLI 専用に公開）。
5. 入口も整備する: `devhub help` / `devhub version`、未知サブコマンドはサーバー起動に
   フォールバックせずエラー + usage（typo で意図せずサーバーが立つ事故を防ぐ）。
6. **Windows でもポート reclaim を有効化**（上書き起動の成立）: 従来 reclaim は
   lsof/ps 前提で Windows では no-op、新しい `devhub` は bind エラーで死に古い
   インスタンスが黙って生き残っていた。listener 探索を ports ツールの一覧
   （netstat/lsof）に寄せ、プロセス名ガードを per-OS（ps / tasklist）にして、
   unix と同じ「newest launch wins」を Windows でも成立させる。ガードは従来どおり
   実行ファイル名がちょうど devhub（.exe）のものだけ。

## Consequences（結果）

### 正
- 「何が起動するか」「何が動いているか」が `devhub doctor` 一発で分かる。
- スロットの取り合いは仕様のままだが、silent flip が無くなる。
- 配布バイナリ利用者も `devhub stop` でインスタンスを止められる
  （`scripts/dev.*` はチェックアウト向けヘルパとして残る）。

### 負 / 留意（受け入れる）
- `devhub <未知語>` の挙動が「サーバー起動」から「エラー」に変わる（意図的な破壊的変更。
  flag 引数 `-no-browser` 等は従来どおり）。
- doctor の PATHEXT 判定は既定順（.com/.exe/.bat/.cmd）を仮定する。カスタム PATHEXT
  には追従しない（稀すぎるため）。
- ~~Windows の「古いインスタンスが生き残る」根本（reclaim の Windows 実装）は今回
  スコープ外。~~ → 決定 6 で同 PR 内に実装した。doctor / status のバージョン不一致
  警告は「稼働中のものが古い」ことの可視化として引き続き有効。

## Alternatives considered（検討した代替案）

1. **スロットを分ける（`devhub-dev` 等の別コマンド名）** — どちらを打つべきか
   という新しい混乱を生む。単一コマンド + 告知 + doctor の方が筋が良い。不採用。
2. **`devhub slot use release|source` のようなスイッチャ** — インストーラ再実行が
   そのままスイッチとして機能する（告知付き）。専用機構は YAGNI。不採用。
3. **stop に `--force`（未確認 kill）を付ける** — 汎用ポートキラー化する。ports
   ツール（保護ポート・自 PID チェック付き）が既にその役割。不採用。
