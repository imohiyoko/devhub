# devhub

ローカル開発を補助するツール群を `localhost:8765` に集約するダッシュボード。  
単一の実行ファイルで動作（ランタイム不要・OS / CPU アーキ別バイナリを配布）。

## ツール

| ツール | パス | 概要 |
|---|---|---|
| **workspace** | `/workspace` | スキャン対象ディレクトリ配下のリポジトリ一覧からエディタで開く |
| **git** | `/git` | status / log / diff / stash / branch / worktree などを GUI から操作（PR から worktree 作成も対応） |
| **env-launcher** | `/env-launcher` | 検証環境（複数プロセス）を OS 別ターミナルで依存順に起動・管理 |
| **diff-kun** | `/diff-kun` | テキスト差分をリアルタイム確認（unified / context / side-by-side） |
| **diagram** | `/diagram` | Mermaid 記法と Draw.io XML の相互変換（外部CDNは読み込まない） |
| **db-table** | `/db-table` | SQLite / MySQL / MariaDB の接続管理、表表示、テーブル/横断カラム/横断要素検索、TSV/CSVコピー、列コピー、セル編集 |
| **ports** | `/ports` | 開いている TCP ポートの確認、ラベル付け、保護対象設定、LISTEN プロセスの kill |

`db-table` の MySQL / MariaDB パスワードは保存されません。接続時に必要に応じて入力してください。  
デフォルトでは外部DBホストへの接続は禁止され、`localhost` / `127.0.0.1` / `::1` のみ接続できます。

## セットアップ

devhub は単一バイナリで配布されます。インストーラは GitHub Releases から**バージョン固定**の成果物を取得し、**SHA256 を検証**してから配置します（ランタイム不要）。

> 🔽 **ダウンロードページ**: <https://imohiyoko.github.io/devhub/> — OS を自動判定して、あなたの環境向けのコマンドとバイナリを表示します。

### 必要環境

- インストール時: `curl`（macOS / Linux）/ PowerShell（Windows）のみ
- 実行時（各ツールを使うときだけ必要なもの）:
  - **git** … git / env-launcher ツール
  - **mysql** コマンド … db-table で MySQL / MariaDB を編集する場合
  - VSCode / Cursor / Windsurf など … workspace からエディタで開く場合

### クイックセットアップ

macOS / Linux:

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/imohiyoko/devhub/main/install.sh)"
```

Windows PowerShell:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "iwr -useb https://raw.githubusercontent.com/imohiyoko/devhub/main/install.ps1 | iex"
```

最新リリースのバイナリをダウンロードして SHA256 を検証し、`devhub` コマンドを登録します。

| OS | インストール先 | コマンド配置先 |
|---|---|---|
| macOS / Linux | `~/.devhub` | `~/.local/bin/devhub` |
| Windows | `%LOCALAPPDATA%\devhub` | `%USERPROFILE%\bin\devhub.cmd` |

### バージョンを固定してインストール

`DEVHUB_VERSION` を指定すると特定リリースに固定できます（汚染対策・再現性のため推奨）。

```bash
DEVHUB_VERSION=v1.0.0 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/imohiyoko/devhub/main/install.sh)"
```

```powershell
$env:DEVHUB_VERSION="v1.0.0"; powershell -NoProfile -ExecutionPolicy Bypass -Command "iwr -useb https://raw.githubusercontent.com/imohiyoko/devhub/main/install.ps1 | iex"
```

### 手動インストール

[Releases](https://github.com/imohiyoko/devhub/releases) から自分の OS / アーキの資産（`devhub_<version>_<os>_<arch>.tar.gz`、Windows は `.zip`）と `checksums.txt` をダウンロードし、SHA256 を検証して展開、PATH の通った場所に `devhub` を置きます。

```bash
# 例: macOS arm64
shasum -a 256 -c <(grep devhub_1.0.0_darwin_arm64.tar.gz checksums.txt)
tar -xzf devhub_1.0.0_darwin_arm64.tar.gz
install -m 0755 devhub ~/.local/bin/devhub
```

### 起動

```bash
devhub               # ダッシュボードを起動してブラウザを開く
devhub --no-browser  # ブラウザを開かない
devhub --version     # バージョンを表示
```

## 設定

設定は `$DEVHUB_HOME/settings/devhub.db`（既定 `~/.devhub/settings/devhub.db`、SQLite）に保存されます。  
初回起動時にバイナリ同梱の既定値から自動生成され、各ツールの UI から編集できます（編集用の JSON ファイルを置く必要はありません）。

### サーバー設定

ダッシュボード右上の設定、および各ツールの UI から変更できます。

| キー | デフォルト | 説明 |
|---|---|---|
| `port` | `8765` | サーバーのポート番号 |
| `editor` | `"code"` | エディタコマンド（`cursor`、`zed` 等も可） |
| `open_browser_on_start` | `true` | 起動時にブラウザを自動で開くか |
| `db_local_only` | `true` | db-table の MySQL / MariaDB 接続をローカルホストのみに制限 |
| `protected_ports` | `[]` | ports ツールで kill できないよう保護するポート番号の配列 |

環境変数で上書きできる項目:

| 環境変数 | 説明 |
|---|---|
| `DEVHUB_PORT` | ポート設定より優先してバインドするポート |
| `DEVHUB_HOME` | データ保存先（既定 `~/.devhub`。旧 Python 版の `devhub.db` をそのまま引き継げます） |

### ワークスペース設定

workspace / git ツールの UI から編集できます。

| キー | 説明 |
|---|---|
| `scan_roots` | リポジトリをスキャンするディレクトリ一覧 |
| `excludes` | 非表示にするリポジトリのパス一覧 |
| `pinned_repos` | スキャン外から個別に追加したリポジトリ |
| `repo_order` | workspace での表示順（ドラッグ&ドロップで自動更新） |

## ライセンス

[MIT](LICENSE)
