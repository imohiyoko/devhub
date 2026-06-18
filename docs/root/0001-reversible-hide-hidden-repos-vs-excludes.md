# 0001. git の「非表示」は excludes ではなく hidden_repos（取り消し可能・フロントフィルタ）を用いる

- **Status**: Accepted (2026-06-16)
- **対象ツール**: git / workspace（横断）
- **関連**: PR #20

## Context（背景）

リポジトリ一覧を扱うツールが 2 つあり、同じ `settings/config.json` を共有している。

- **workspace** は repo を `excludes` で隠す。`excludes` はバックエンドの `AllRepos()`
  （`internal/controllers/git/`）でフィルタされ、`/api/repos` の結果から**完全に除外**される。
  スキャン検出（`scan_roots`）も抑止する、破壊的・グローバルな除外。
- **git** ツールに「非表示」を追加するにあたり、ユーザーが誤って隠しても**簡単に元へ戻せる**
  （reversible / re-pop）ことを重視した。

`excludes` を流用すると、バックエンドの時点で repo が消えるため、git のドロップダウンに
『非表示(N)』一覧として表示できず、「その場で戻す」導線が作れない。

## Decision（決定）

git ツールの「非表示」は、新しい config キー **`hidden_repos`** で管理する。

- バックエンドは `hidden_repos` を **config スキーマ（`internal/storage/`）・`/api/config` POST
  の許可キー（`internal/controllers/settings/`）・`settings/config.example.json`** に追加するのみ。
- **`AllRepos()` は変更しない**。`hidden_repos` のフィルタは **フロントエンドでのみ** 行う。
  → 隠した repo も `/api/repos` では返り続けるため、『非表示(N)』一覧での表示と、
  「戻す」ボタン / 非表示直後の「元に戻す」トーストによる**即時復帰**が可能。
- git ヘッダーの ✕ ボタンも、従来の `excludes` 追加（破壊的）から `hidden_repos` による
  reversible hide に**統一**した。

## Consequences（結果）

### 正
- 非表示が取り消し可能で、git ローカルに閉じる。workspace やスキャン検出に影響しない。
- バックエンドの挙動（`AllRepos()`）を変えないため影響範囲が小さい。

### 負（既知の非対称 — 受け入れる）
- git で非表示にしても **workspace には出続ける**。
- workspace で `excludes` した repo は **git でも消える**（`AllRepos()` が依然フィルタするため）。
- 旧 git ヘッダー ✕ で過去に `excludes` 済みの repo は git の『非表示(N)』には出ない。
  ただし ヘッダーの ＋「リポジトリを追加」から再追加すると `excludes` が解除され復活する
  （`tools/git/index.html` の `addRepoFromBrowser` / `handlePathAdd` が再追加時に excludes をフィルタ）。

### 運用
- `hidden_repos` の永続化には devhub サーバの**再起動**が必要。許可キーの追加は
  稼働中プロセスに反映されないため（フロント側の挙動・「元に戻す」トーストは再起動なしで動作）。

## Alternatives considered（検討した代替案）

1. **`excludes` を流用** — workspace と完全に一致するが、バックエンドで消えるため
   「その場で戻す」UX が作れず、reversible 要件を満たせない。却下。
2. **`AllRepos()` が excluded/hidden を flag 付きで返すよう変更** — 1 つのキーに統一できるが、
   共有バックエンドの挙動を変えるため workspace やヘッダー ✕ の既存挙動への波及リスクが大きい。
   今回はスコープ過大と判断し見送り。

## Open question / Future direction（今後の論点）

「非表示」を repo 全体で統一するなら、workspace 側も
**`hidden_repos`（表示フィルタ・可逆）と `excludes`（スキャン除外・恒久）を分離**する方向が候補。
本 ADR ではそこまで踏み込まず、git の reversible hide を優先した。収束方針は別途合意する。
