# devhub

ローカル開発を補助するツール群を `localhost:8765` に集約するダッシュボード。  
外部依存なし（Python 標準ライブラリのみ）。

## ツール

| ツール | パス | 概要 |
|---|---|---|
| **workspace** | `/workspace` | `~/developer` 配下のリポジトリ一覧から VSCode で開く |
| **diff-kun** | `/diff-kun` | テキスト差分をリアルタイム確認（unified / context / side-by-side） |
| **diagram** | `/diagram` | Mermaid 記法のリアルタイムプレビュー・Draw.io XML 変換 |

## セットアップ

### 必要環境

- Python 3.8+
- VSCode（`code` コマンドが PATH に通っていること）

### インストール

```bash
git clone https://github.com/imohiyoko/devhub.git
cd devhub

# グローバルコマンドとして登録（任意）
chmod +x install.sh && ./install.sh
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

### ワークスペース設定 (`settings/config.json`)

初回起動時に `settings/config.example.json` から自動生成されます。  
workspace ツールの UI からも編集可能です。

| キー | 説明 |
|---|---|
| `scan_roots` | リポジトリをスキャンするディレクトリ一覧 |
| `excludes` | 非表示にするリポジトリのパス一覧 |
| `pinned_repos` | スキャン外から個別に追加したリポジトリ |
| `repo_order` | workspace での表示順（ドラッグ&ドロップで自動更新） |

## ライセンス

[MIT](LICENSE)
