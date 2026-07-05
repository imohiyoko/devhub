---
name: dep-audit
description: 手動ベンダリングしたサードパーティ依存（shared/vendor/ の JS/CSS blob 等）の棚卸しを行う。dep-audit.json の固定版・最終確認日と最新安定版を突き合わせ、新版が出ている or 最終確認から一定日数経過のどちらかを満たしたらレポートを要求する。依存を追加/更新するとき、またはこのリポジトリで依存の供給網チェックが必要なときに使う。Dependabot が追跡しない frontend 同梱物が対象。
---

# dep-audit — ベンダリング依存の棚卸し

Dependabot は `go.mod` と GitHub Actions を追跡するが、**フロントに手動同梱した blob（`shared/vendor/` の JS/CSS 等）は追跡しない**。この穴を、コミット済みの台帳 `dep-audit.json`（リポジトリルート）と、このスキルで埋める。

## dep-audit.json の形

```jsonc
{
  "staleness_days": 90,           // 最終確認からこの日数を超えたら「古い」と判定
  "vendored": [
    {
      "name": "mermaid",                          // 表示名
      "npm": "mermaid",                           // npm パッケージ名（最新版取得に使う）
      "pinned": "11.4.1",                         // 現在リポジトリに入れている固定版
      "file": "shared/vendor/mermaid-11.4.1.min.js",
      "sha256": "…",                              // 同梱 blob のハッシュ（改ざん検知用）
      "source": "https://registry.npmjs.org/mermaid/-/mermaid-11.4.1.tgz",
      "last_checked": "2026-07-06"                // このスキルで最後に突き合わせた日（YYYY-MM-DD）
    }
  ]
}
```

`vendored` が空なら同梱物はまだ無い。追加時に 1 エントリ足す。

## 棚卸しの手順（`/dep-audit` で走らせる）

1. リポジトリルートの `dep-audit.json` を読む。`vendored` が空なら「同梱依存なし」で終了。
2. 各エントリについて **最新安定版を取得**する:
   - npm パッケージ: `https://registry.npmjs.org/<npm>/latest` を WebFetch し、`version` を読む。
   - （将来 npm 以外の手動同梱を足す場合はその公式レジストリを使う。Go モジュールは `go.sum` で整合性検証され Dependabot が版・脆弱性を追跡するため、このスキルの棚卸し対象外。）
3. **発火判定**（**どちらか**を満たしたらそのエントリを「要対応」とする）:
   - **新版あり**: 取得した最新安定版 ≠ `pinned`。
   - **古い**: 今日 − `last_checked` ≥ `staleness_days`（下記のとおり厳密な暦日数で判定）。
4. **要対応が 1 件でもあれば、レポートを要求する**（下記フォーマット）。ユーザーの承認なしに勝手にバージョンを上げたり blob を差し替えたりしない。
5. 要対応が 0 件（全て最新かつ新しい）なら、各エントリの `last_checked` を今日の日付に更新して `dep-audit.json` を保存し、「全依存が最新・確認済み」と報告して終了。

> 今日の日付は環境コンテキスト（currentDate）から取る。スタレス判定は **厳密な暦日数**で行う: `currentDate` と `last_checked` をそれぞれ `YYYY-MM-DD` の暦日として解釈し、その差の日数が `staleness_days` 以上かで判定する。概算・月境界での丸め・「だいたい N ヶ月」といった近似はしない（閾値付近のエントリを取りこぼすため）。

## レポートのフォーマット（要対応があるとき）

対応が要るエントリごとに:

- **依存名 / 固定版 → 最新版**（例: `mermaid 11.4.1 → 11.6.0`）
- **発火理由**: 新版あり / 最終確認から N 日経過 / 両方
- **確認すべき脆弱性**: そのバージョン範囲に未修正の CVE・GHSA が無いか（`https://github.com/advisories?query=<name>` や npm audit 情報を確認）
- **推奨アクション**: 更新するか据え置くか。更新する場合は下の「供給網チェック」を全部通す。

レポートを出したら、更新するかどうかをユーザーに確認する。更新を実施したエントリだけ `pinned` / `file` / `sha256` / `source` / `last_checked` を書き換える。

## 供給網チェック（同梱 blob を追加・更新するとき必ず通す）

対象はこのスキルが管理する **`shared/vendor/` の手動同梱 blob**。追加・更新のたびに:

1. **出所は公式か** — canonical な発行元（npm 公式パッケージ / GitHub リリース）から取る。CDN 直リンクはしない（devhub は実行時に外部通信を発生させない方針）。
2. **バージョンを固定** — レンジや `latest` ではなく特定バージョン。ファイル名にバージョンを含める（例 `mermaid-11.4.1.min.js`）。
3. **改ざん検知** — SHA-256 を `dep-audit.json` に記録し、CI で照合できるようにする。
4. **既知脆弱性を確認** — 追加・更新前に、そのバージョンに未修正の CVE / GHSA が無いか確認。
5. **由来を記録** — `dep-audit.json` のエントリに取得元 URL・バージョンを残す。

新規に blob を足したら、`dep-audit.json` の `vendored` に対応エントリを追加すること。

> Go モジュールは対象外（`go.sum` が整合性を、Dependabot が版・脆弱性を担保する）。ただし「公式出所・版固定・脆弱性確認」という原則は Go でも同じく守る。
