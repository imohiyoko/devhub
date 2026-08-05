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
| **containers** | `/containers` | Docker context と Colima profile を横断したコンテナ一覧（宣言外・停止済みも表示）。logs / stop / restart と Colima profile の作成・サイズ変更 |
| **logs** | `/logs` | この起動中に処理した API リクエストの記録。承認結果・ステータス・パス・ボディで絞り込み、残したい分だけアーカイブ |

`db-table` の MySQL / MariaDB パスワードは保存されません。接続時に必要に応じて入力してください。  
デフォルトでは外部DBホストへの接続は禁止され、`localhost` / `127.0.0.1` / `::1` のみ接続できます。

## CLI

ブラウザを開かなくても、主要な操作はサブコマンドでできます。

```bash
devhub start                # サーバー起動（bare の devhub はヘルプ表示）
devhub status               # 設定ポートの稼働確認（非稼働なら exit 1）
devhub stop                 # そのインスタンスを停止（devhub であることを確認してから kill）
devhub doctor               # 「何が起動するか / 何が動いているか」を診断（スロット・PATH・稼働状況）
devhub env list             # env-launcher の環境一覧と稼働中ポート
devhub env start <env-id>   # 環境を起動（worktree 解決・依存順・offset 採番も UI と同一）
devhub env stop <env-id>    # その環境のポートで LISTEN 中のプロセスを kill
devhub docs list            # 同梱ドキュメントの一覧（JSON）
devhub docs show <name>     # ドキュメント本文を表示
devhub version / help
```

`env start` / `env stop` はサーバー停止中でも動作します。`env stop` では保護
ポート（ports ツールで設定）と devhub 本体のポートは kill されません。
`env start` の baton プロセスは宣言ポートを奪取します（kill 内容を表示）。
また、既に古い devhub が居座るポートへの `devhub start` は、Windows でも
「新しい起動が勝つ」（devhub という名前のプロセスに限り reclaim）ようになりました。
詳細は [docs/env-launcher/0002](docs/env-launcher/0002-cli-env-stop.md) と
[docs/root/0002](docs/root/0002-single-command-slot-and-cli.md)。

ドキュメントはバイナリに同梱されているため、チェックアウトもネットワークも不要で
`devhub docs` から読めます。コーディングエージェントに devhub を操作させる場合は
`devhub docs show agent/ai-api`（HTTP 面）と `agent/troubleshooting`（エラー対処）が
出発点です。

`devhub start` は**起動元を選べます**: `devhub start binary|homebrew|code`
（リリースバイナリ / Homebrew 版 / 手元ソースの `go run`）。コマンドスロットや
PATH は変えず、その一回だけ別の devhub に委譲します。詳細は
[docs/root/0004](docs/root/0004-devhub-start-provenance.md)。

## セットアップ

devhub は単一バイナリで配布されます。インストーラは GitHub Releases から**バージョン固定**の成果物を取得し、**SHA256 を検証**してから配置します（ランタイム不要）。加えて [cosign](https://github.com/sigstore/cosign) がインストールされていれば、`checksums.txt` の**署名（keyless / sigstore）を自動で検証**します（未インストールなら警告のうえ SHA256 のみで続行）。署名検証を必須にするには `DEVHUB_VERIFY_SIGNATURE=1`、無効化するには `=0` を指定します。

> 🔽 **ダウンロードページ**: <https://imohiyoko.github.io/devhub/> — OS を自動判定して、あなたの環境向けのコマンドとバイナリを表示します。

### 必要環境

- インストール時: `curl`（macOS / Linux）/ PowerShell（Windows）のみ
- 実行時（各ツールを使うときだけ必要なもの）:
  - **git** … git / env-launcher ツール
  - VSCode / Cursor / Windsurf など … workspace からエディタで開く場合

  （db-table の SQLite / MySQL / MariaDB は Go ドライバを内蔵しており、外部コマンドは不要です）

### Homebrew（macOS）

```bash
brew install --cask imohiyoko/devhub/devhub
```

初回のみ `brew tap imohiyoko/devhub` 相当が自動で行われます。更新は `brew upgrade --cask devhub`、削除は `brew uninstall --cask devhub`。

> ⚠️ devhub のバイナリは Apple の **コード署名・公証（notarization）を行っていません**。Cask の SHA256 検証は「リリース成果物が改ざんなくダウンロードできたか」の確認であり、`xattr` による Gatekeeper 隔離属性の除去も実行を通すための補助です。いずれも署名・公証の代替ではありません。導入は **配布元（GitHub Releases / この tap）を信頼する前提**で行ってください。

> Linux / Windows は下のクイックセットアップ（インストーラ）を使ってください（Homebrew Cask は macOS 専用です）。

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
devhub start               # ダッシュボードを起動してブラウザを開く
devhub start --no-browser  # ブラウザを開かない
devhub start code          # 手元のソースを go run で起動（起動元の選択。他に binary / homebrew）
devhub --version           # バージョンを表示
devhub                     # 引数なしはヘルプ（サーバーは起動しない）
```

> 配布バイナリを使えない環境などで**ソースから実行**したい開発者は [CONTRIBUTING.md](CONTRIBUTING.md) を参照してください（ポート分離・worktree ベースの起動方法を記載）。
>
> 手元のソースを `devhub` コマンドとして PATH に登録するには `make install`（Windows は `scripts\dev.ps1 install`）を実行します。固定バイナリではなく `go run` で**現在のチェックアウトをそのまま起動するシム**が置かれるため、編集が再ビルドなしで即反映されます（この場合 `devhub --version` は `dev` を表示）。

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
| `DEVHUB_HOME` | データ保存先（既定 `~/.devhub`。旧版の `devhub.db` をそのまま引き継げます） |

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
