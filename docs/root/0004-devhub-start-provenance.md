---
description: devhub start binary / homebrew / code で起動元をその場限りで選ぶ仕組み。リリース版と手元ソースを使い分けたいときに読む。
---

# 0004. `devhub start <provenance>` — 起動元を都度選ぶランチャ

- **Status**: Accepted (2026-07-05)
- **対象**: cmd/devhub / internal/execaudit / internal/platform / docs
- **関連**: [root/0002](0002-single-command-slot-and-cli.md)（決定1・Alt#2）、[root/0003](0003-devhub-start-explicit.md)。本 ADR は両者を**覆さない**（`start` は依然明示、コマンドスロットは 1 つのまま）。0002 Alt#2 で退けた「永続スイッチャ」との差分を明確にするために起こす。

## Context（背景）

`devhub` の実体は環境により **リリースバイナリ / ソースの `go run` / Homebrew** と複数あり得る（`devhub doctor` が provenance を分類する）。だが「どれで起動するか」を選ぶ手段は現状、**インストーラ / `scripts/dev install` を再実行してコマンドスロットを上書きする**＝**永続的にどれが勝つかを切り替える**方法しかない。

開発中には「reinstall せず、**その一回だけ別の実体でサーバを立てたい**」需要がある（例: 手元ソースの挙動を見て、直後にリリース版と比較）。

0002 Alt#2 は `devhub slot use release|source` を YAGNI として退けた。理由は「インストーラ再実行がスイッチとして機能する」「専用機構＝隠れた永続状態を増やす」。しかしそれが退けたのは **永続・暗黙のスロット切替**であって、**一回限り・明示・状態非変更**の起動選択は別物である。

## Decision（決定）

1. **`devhub start` に任意の位置引数 provenance を受ける**: `devhub start [<provenance>] [flags]`。無指定は従来どおり現在の devhub で in-process 起動。
2. provenance は**先頭トークンのみ**（Go の `flag` は先頭非フラグで停止するため、位置引数を Parse 前に切り出す）。以降の引数は対象の `start` へ透過。正規名 `binary` / `homebrew` / `code`、別名 `release` / `brew` / `source`。
3. **ハンドオフ型ランチャ**: 対象の起動 argv を解決し、その実体へ制御を渡して自身は終わる。unix は `syscall.Exec` でイメージ置換、Windows は `exec.Command` + `Run` で子の exit code を伝播（Windows に exec(2) は無いため薄い親が 1 つ残る）。ハンドオフ後は各 provenance の通常の `start`（`-no-browser`・自己再起動・reclaim は対象側の既存挙動）。**provenance トークンは剥がして**渡すので対象の `os.Args` は `start <flags>` となり、以後の自己再起動も同じ provenance に留まる（無限ループしない）。
4. **状態を変えない**: スロットも PATH も一切書き換えない。その `start` の間だけの選択。→ 0002 の反対理由（隠れた永続状態）に抵触しない。新コマンド名も足さない（0002 決定1「別名を増やさない」と整合）。
5. **解決方法**: `binary` = `<DevhubHome>/bin/devhub`（doctor の release bin と同一）、`homebrew` = PATH 上で解決パスが Homebrew prefix 配下の devhub（`platform.IsHomebrewPath`、**Windows 非対応**）、`code` = checkout で `go run ./cmd/devhub start`（checkout は **cwd 上昇 → dev shim の参照先 → `$DEVHUB_SRC`** の順に探索、`go.mod` + `cmd/devhub/main.go` の両方で devhub の checkout だと確認）。見つからなければ理由付きエラー + exit 1、未知 provenance は usage + exit 2。
6. **self short-circuit**: 解決先が実行中バイナリ自身なら（例: リリースバイナリで `devhub start binary`）exec せず in-process 起動にフォールバックし、無駄なプロセスを増やさない。
7. **exec 監査**: Windows の `exec.Command` 1 箇所に `//execaudit:start-launch` を付け registry に Surface を追加。unix の `syscall.Exec` は監査スキャン対象外（`restart` と同じ）だが Surface に文書化する。

## Consequences（結果）

### 正
- reinstall なしで起動元を都度選べる（開発時の A/B が軽い）。
- 状態を変えないので「今どれが勝つか」の混乱を増やさない（0002/0003 の "観測可能にする" 思想を保つ）。
- `devhub start -h` が起動モデル（前景・`Ctrl+C`・provenance）を説明するようになり、0003 の「起動を名前付きにする」を補強。

### 負 / 留意（受け入れる）
- `code` は go ツールチェインと checkout を、`homebrew` は brew 導入済み devhub を要し **Windows 非対応**。満たさなければエラー（暗黙フォールバックしない）。
- provenance は先頭トークン限定（`devhub start code -no-browser` は可。`devhub start -no-browser code` の `code` はフラグ扱いで無視）。usage に明記。
- Windows はハンドオフで薄い親プロセスが 1 つ残る（unix は `syscall.Exec` で置換）。

## Alternatives considered（検討した代替案）

1. **フラグ形式 `devhub start -from=code`** — flag の位置引数問題を回避できるが、ユーザの求めた `devhub start code`（サブコマンド風）から外れる。先頭トークンの切り出しで足りるため不採用。
2. **永続スイッチャ `devhub slot use`** — 0002 Alt#2 で退けた線。隠れた永続状態を増やす。本 ADR の per-invocation ランチャはこれとは別物であり、引き続き不採用。
3. **設定で拡張できる launch profiles**（`settings` に名前→コマンド、`devhub start staging` 等）— 一般性は高いが設定スキーマと信頼境界（誰が書けるか）を新設する必要があり、既知 3 provenance の需要に対して過剰。将来必要になれば別途。今回は組み込み 3 種に限定。
4. **Homebrew formula を新設** — `homebrew` を「必ず入っている」前提にできるが配布運用の別タスク。本 ADR は「既に brew で入っている devhub を解決するだけ（無ければエラー）」に留める。
