# devhub

ローカル開発を補助するツール群を `localhost:8765` に集約するダッシュボード。  
外部依存なし（Python 標準ライブラリのみ）。

## ツール

| ツール | パス | 概要 |
|---|---|---|
| **workspace** | `/workspace` | `~/developer` 配下のリポジトリ一覧から VSCode で開く |
| **diff-kun** | `/diff-kun` | テキスト差分をリアルタイム確認（unified / context / side-by-side） |
| **diagram** | `/diagram` | Mermaid 記法と Draw.io XML の相互変換（外部CDNは読み込まない） |
| **csv-tsv** | `/csv-tsv` | CSV / TSV の相互変換 |
| **db-table** | `/db-table` | SQLite / MySQL / MariaDB の接続管理、表表示、テーブル/横断カラム/横断要素検索、TSV/CSVコピー、列コピー、セル編集 |
| **ports** | `/ports` | 開いている TCP ポートの確認、ラベル付け、保護対象設定、LISTEN プロセスの kill |

`db-table` の MySQL / MariaDB パスワードは保存されません。接続時に必要に応じて入力してください。  
デフォルトでは外部DBホストへの接続は禁止され、`localhost` / `127.0.0.1` / `::1` のみ接続できます。

## セットアップ

### 必要環境

- Python 3.8+
- Git
- MySQL / MariaDB を編集する場合は `mysql` コマンド
- VSCode / Cursor / Windsurf など（workspace からエディタで開く場合）

### クイックセットアップ

macOS / Linux:

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/imohiyoko/devhub/main/install.sh)"
```

Windows PowerShell:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "iwr -useb https://raw.githubusercontent.com/imohiyoko/devhub/main/install.ps1 | iex"
```

上記は devhub を標準の場所へ clone / 更新し、`devhub` コマンドを登録します。

| OS | インストール先 | コマンド配置先 |
|---|---|---|
| macOS / Linux | `~/.devhub` | `~/.local/bin/devhub` |
| Windows | `%LOCALAPPDATA%\devhub` | `%USERPROFILE%\bin\devhub.cmd` |

### 手動インストール

```bash
git clone https://github.com/imohiyoko/devhub.git
cd devhub

# グローバルコマンドとして登録
chmod +x install.sh && ./install.sh
```

Windows:

```powershell
git clone https://github.com/imohiyoko/devhub.git
cd devhub
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

### 起動

```bash
# インストール済みの場合
devhub

# スクリプトから直接
./start.sh          # macOS / Linux
start.bat           # Windows
```

macOS では `devhub.app` をダブルクリックしても起動できます。

## 設定

`settings/` 配下のファイルで動作をカスタマイズできます。  
`.example.json` をコピーして編集してください（個人設定は gitignore 済み）。

### サーバー設定 (`settings/server.json`)

```bash
cp settings/server.example.json settings/server.json
```

| キー | デフォルト | 説明 |
|---|---|---|
| `port` | `8765` | サーバーのポート番号 |
| `editor` | `"code"` | エディタコマンド（`cursor`、`zed` 等も可） |
| `open_browser_on_start` | `true` | 起動時にブラウザを自動で開くか |
| `db_local_only` | `true` | db-table の MySQL / MariaDB 接続をローカルホストのみに制限 |
| `protected_ports` | `[]` | ports ツールで kill できないよう保護するポート番号の配列 |

### ワークスペース設定 (`settings/config.json`)

初回起動時に `settings/config.example.json` から自動生成されます。  
workspace ツールの UI からも編集可能です。

> **Note**: 古いバージョンからアップデートした場合、`settings/config.json` が Git の管理対象として残っている場合があります。その場合は `git rm --cached settings/config.json` を実行して管理から外してください。

| キー | 説明 |
|---|---|
| `scan_roots` | リポジトリをスキャンするディレクトリ一覧 |
| `excludes` | 非表示にするリポジトリのパス一覧 |
| `pinned_repos` | スキャン外から個別に追加したリポジトリ |
| `repo_order` | workspace での表示順（ドラッグ&ドロップで自動更新） |

## ライセンス

[MIT](LICENSE)
