#!/usr/bin/env python3
"""`hidden_repos` config プラミングの回帰テスト。

git ツールの「非表示」機能は `hidden_repos` を config に保存して実現する。
この値が

  (A) `/api/config` POST の許可キーであり保存されること
  (B) 単一キーの POST が他キー (repo_order 等) を壊さないこと (per-key マージ)
  (C) `load_config()` のデフォルトに含まれること
  (D) 許可リスト外のキーは無視されること (ホワイトリストのロック)

を検証する。許可キーの追加 (settings.py) は稼働中プロセスに反映されないため、
退行すると「非表示が保存されない」形で静かに壊れる。そこを守るのが目的。

実 config を汚さないよう storage の CONFIG_PATH を一時ディレクトリへ差し替える
ユニットテスト (サーバは起動しない)。標準ライブラリのみ。
実行: python3 -m unittest tests.test_config_hidden_repos
      または python3 tests/test_config_hidden_repos.py
"""
import os
import sys
import tempfile
import shutil
import unittest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)

import backend.storage as storage
import backend.controllers.settings as settings


class _FakeHandler:
    """settings.handle_get/handle_post が呼ぶ send_json を捕捉する最小スタブ。"""
    def __init__(self):
        self.payload = None
        self.status = 200

    def send_json(self, data, status=200):
        self.payload = data
        self.status = status


class HiddenReposConfigTest(unittest.TestCase):
    def setUp(self):
        # storage を隔離した一時 DB へ向ける (実 settings/devhub.db は不変)。
        self._tmp = tempfile.mkdtemp()
        self._orig = (storage.DB_PATH, storage.CONFIG_EXAMPLE_PATH, set(storage._initialized))
        storage.DB_PATH = os.path.join(self._tmp, 'devhub.db')
        # 存在しない example を指してフォールバックのデフォルトを使わせる。
        storage.CONFIG_EXAMPLE_PATH = os.path.join(self._tmp, 'no-example.json')
        storage.init_db()
        # 隔離 DB は init 済み扱いにして、実 settings/*.json からの移行を走らせない。
        storage._initialized.add(storage.DB_PATH)

    def tearDown(self):
        storage.DB_PATH, storage.CONFIG_EXAMPLE_PATH, storage._initialized = self._orig
        shutil.rmtree(self._tmp, ignore_errors=True)

    # (C) デフォルトに hidden_repos が含まれる
    def test_default_config_includes_hidden_repos(self):
        cfg = storage.load_config()
        self.assertIn('hidden_repos', cfg)
        self.assertEqual(cfg['hidden_repos'], [])

    # (A) + (B) hidden_repos だけ POST しても保存され、他キーは保持される
    def test_post_hidden_repos_persists_and_preserves_other_keys(self):
        storage.save_config({
            'scan_roots': ['~/a'],
            'excludes': ['/x'],
            'pinned_repos': ['/p'],
            'repo_order': ['/p', '/q'],
        })
        h = _FakeHandler()
        settings.handle_post(h, '/api/config', {'hidden_repos': ['/Users/me/secret']})
        self.assertEqual(h.payload, {'ok': True})

        after = storage.load_config()
        self.assertEqual(after['hidden_repos'], ['/Users/me/secret'])
        self.assertEqual(after['repo_order'], ['/p', '/q'])
        self.assertEqual(after['excludes'], ['/x'])
        self.assertEqual(after['pinned_repos'], ['/p'])

    # GET が保存済み hidden_repos を返す
    def test_get_config_returns_hidden_repos(self):
        storage.save_config({'hidden_repos': ['/h']})
        g = _FakeHandler()
        settings.handle_get(g, '/api/config')
        self.assertEqual(g.payload.get('hidden_repos'), ['/h'])

    # (D) 許可リスト外のキーは保存されない (ホワイトリストのロック)
    def test_unknown_key_is_ignored(self):
        storage.save_config({'scan_roots': []})
        h = _FakeHandler()
        settings.handle_post(h, '/api/config',
                             {'hidden_repos': ['/h'], 'evil_key': 'nope'})
        self.assertEqual(h.payload, {'ok': True})
        after = storage.load_config()
        self.assertEqual(after['hidden_repos'], ['/h'])
        self.assertNotIn('evil_key', after)


if __name__ == '__main__':
    unittest.main(verbosity=2)
