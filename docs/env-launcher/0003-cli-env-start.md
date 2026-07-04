# 0003. CLI からの環境起動を追加（レジストリを行単位書き込みにして安全化）

- **Status**: Accepted (2026-07-05)
- **対象ツール**: env-launcher（一部 storage / ports）
- **関連**: docs/env-launcher/0002（stop は DB 直読み + OS kill。start は保留していた）

## Context（背景）

0002 で `devhub env stop` を入れた際、`start` は見送った。起動は launch レコードの
追記を伴い、当時の `AppendLaunch` / `RemoveLaunch` が「全テーブル読み込み → 変更 →
全消去 + 再挿入（`SaveLaunches`）」をプロセス内 mutex（`RegistryMu`）で直列化する
実装だったため、別プロセス（CLI）からの書き込みはレコード喪失の競合を生み得た。
0002 は「行単位 INSERT 化 or サーバー経由化を別 ADR で決める」としていた。

## Decision（決定）

**行単位 INSERT / DELETE 化を採用し、`devhub env start <env-id>` を追加する。**

- `storage.AppendLaunch` は単一行 `INSERT OR REPLACE`、`RemoveLaunch` は単一行
  `DELETE` に変更。SQL レイヤでアトミックになり、WAL + busy_timeout の下で
  別プロセスからの書き込みが安全になる（`SaveLaunches` の全置換は移行用として
  残すが、サーバープロセス内に限る）。launch_id 無しレコードは黙って消える代わりに
  エラーで拒否する。
- envs コントローラに **同期起動** `StartEnvironment` を追加。HTTP 経路
  （`launchEnvironment`）は従来どおり goroutine で起動するが、CLI は短命プロセス
  なので goroutine に積んだまま exit すると起動が失われる — 依存順ループを
  インラインで実行する（`runProcesses` に async フラグ）。
- **baton の意味論は CLI でも殺さない**: 宣言ポートを他プロセスが握っていれば
  kill して奪取する（上書き起動）。それが baton の定義であり、env 定義側の明示的
  オプトインだから。ただし CLI は奪取内容（port / pid）を必ず表示する。
  `killPortsFor` が kill 結果（`BatonKill`）を返すようになったのはこのため。
  - 対比: `env stop` の「devhub 本体ポート除外」ガードは維持。stop は宣言 spec
    からの**推測**で kill 対象を選ぶ（devhub-verify の base 8765 宣言の LISTEN が
    本体、という collateral がある）。start の baton kill は**明示指定**で、
    devhub を奪取対象にした env は意図として尊重する。

## Consequences（結果）

### 正
- `devhub env start devhub-verify` のように、検証インスタンスの起動が
  ワンコマンドになる（worktree 解決・offset 採番・依存順・遅延も UI と同一経路）。
- レジストリ書き込みの競合クラスが構造的に消える（サーバー同時稼働中でも安全）。

### 負 / 留意（受け入れる）
- CLI 起動でもターミナル設定（settings.terminal）に従う。emulator が無い環境では
  バックグラウンドの `runShell` にフォールバックし、ウィンドウは開かない。
- baton が protected_ports に当たる場合は従来どおり kill されず、プロセスは
  bind に失敗し得る（UI と同じ挙動）。

## Alternatives considered（検討した代替案）

1. **サーバー経由（HTTP POST /api/envs/launch）** — トークンの永続化問題（0002 と
   同じ）に戻る上、サーバー停止中に起動できない。不採用。
2. **CLI の baton kill にも devhub 本体ガード** — 「port kill できないと上書き起動
   できない」— baton の存在意義を CLI で損なう。表示付きで実行する方針とした。
