# コントリビューションガイド

devhub は単一バイナリで配布されますが、**開発時はソースから実行**します。
配布バイナリを使えない環境（例: 会社規定でバイナリソフトウェアの実行が不可）でも、
このガイドの手順だけで開発・検証ができます。

## 前提

- **Go 1.26 以上**。`go.mod` / CI と同じバージョンを使うなら [mise](https://mise.jdx.dev/) が簡単です。

  ```bash
  mise install   # .mise.toml に固定された go 1.26 を用意
  ```

  mise を使わない場合は、各自で Go 1.26+ を入れてください（`go version` で確認）。
- 各ツールが内部で使うコマンド（必要なときだけ）: `git` / `lsof`（dev.sh の stop 用）など。

## ソースから実行する

なぜソース実行か: 配布物の `devhub` コマンドは**ビルドした時点の埋め込みアセットで固定**されるため、
いま編集中のコードを反映しません。開発では `go run` を使い、現在のソースをそのまま起動します。

```bash
# どちらでも可（既定ポート 8765 で起動）
mise run dev
# または
scripts/dev.sh run        # 実行ビットが無ければ: bash scripts/dev.sh run
```

起動すると `http://localhost:8765` が開きます。停止はフォアグラウンドなら `Ctrl+C`。

> アセットは module ルートからの `go:embed`（`assets.go`）です。`go run ./cmd/devhub` は
> 実行したディレクトリ（= その worktree）の `dashboard/` `tools/` `settings/` を焼き込みます。

## worktree ベースの開発

`scripts/dev.sh` は **自身が属する worktree の repo ルート**へ移動してから `go run` します。
そのため、worktree ごとに `scripts/dev.sh run` を実行すれば、**その worktree のコードがそのまま動きます**。
固定された `devhub` コマンドのように「repo の特定状態」に縛られません。

```bash
# 例: 機能ブランチの worktree を作ってそこで起動
git worktree add ../worktrees/feat-foo -b feat/foo
../worktrees/feat-foo/scripts/dev.sh run
```

## ポートを分けて同時に立てる

本番用の自分の devhub と、検証用インスタンスを**同時に**動かすときはポートを分けます。
ポートは環境変数 `DEVHUB_PORT` で指定できます（未指定なら 8765）。

| 用途 | 起動コマンド | URL |
|---|---|---|
| 本番（自分用） | `scripts/dev.sh run` | http://localhost:8765 |
| 検証 | `DEVHUB_PORT=9000 scripts/dev.sh run` | http://localhost:9000 |

mise でも同じです:

```bash
DEVHUB_PORT=9000 mise run dev
```

## 停止する

フォアグラウンド実行なら `Ctrl+C`。別ターミナル/バックグラウンドのインスタンスは
`stop` サブコマンドで止めます。

```bash
DEVHUB_PORT=9000 scripts/dev.sh stop     # port 9000 の devhub を停止
scripts/dev.sh status                    # LISTEN 状況を確認
```

> **注意**: `stop` は `DEVHUB_PORT`（未指定なら既定の **8765**）を LISTEN しているプロセスを
> 落とします。プロセス種別は問わないため、停止したいインスタンスのポートを `DEVHUB_PORT` で
> 正しく指定してください（例: 本番 8765 を残して検証 9000 だけ止めるなら `DEVHUB_PORT=9000`）。

> **なぜ専用の stop が必要か**: ダッシュボードの **ports ツールは安全のため
> devhub 自身（自分の PID）を kill できません**（`internal/controllers/ports/ports.go`）。
> 自分のプロセスを誤って落とさないための仕様です。dev インスタンスのポートを解放したいときは
> `scripts/dev.sh stop` を使ってください。

## ビルド

ローカルにバイナリが必要なときだけ:

```bash
mise run build          # ./devhub を生成（.gitignore 済み）
# または
scripts/dev.sh build    # ./devhub
```

Windows は `scripts\dev.ps1 build` で `devhub.exe` を生成します（`devhub` / `devhub.exe` とも `.gitignore` 済み）。

## データ/設定の分離（任意）

devhub は DB と設定を `DEVHUB_HOME`（既定: macOS/Linux は `~/.devhub`、Windows は
`%LOCALAPPDATA%\devhub`）に置きます。**既定ではポートだけ分けて HOME は共有**します
（ラベルや保護ポート、env-launcher の定義などを本番と共有できます）。

本番と検証で **DB/設定まで完全に分けたい**場合は `DEVHUB_HOME` を切り替えます:

```bash
DEVHUB_PORT=9000 DEVHUB_HOME="$HOME/.devhub-verify" scripts/dev.sh run
```

> 同一の `DEVHUB_HOME` を 2 プロセスで共有しても、SQLite（WAL）で動作はしますが
> 状態は共有されます。独立させたいときは上記のように HOME を分けてください。

## Windows

PowerShell では `scripts\dev.ps1` を使います（`dev.sh` と同じサブコマンド）。

```powershell
scripts\dev.ps1 run
$env:DEVHUB_PORT = 9000; scripts\dev.ps1 run     # 別ポート
$env:DEVHUB_PORT = 9000; scripts\dev.ps1 stop
```

## テスト

PR 前に CI（`.github/workflows/ci.yml`）と同じチェックを通してください:

```bash
go vet ./...
go test ./...
go build ./...
```
