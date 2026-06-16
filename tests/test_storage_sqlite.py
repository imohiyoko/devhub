#!/usr/bin/env python3
"""SQLite ストレージ層 (backend/storage.py) のユニットテスト。

実 settings/devhub.db を汚さないよう DB_PATH と各種ソースパスを一時ディレクトリへ
差し替える。round-trip / example seed / 最小 JSON→SQLite 移行 を検証する。
実行: python3 -m unittest tests.test_storage_sqlite
"""
import json
import os
import sys
import tempfile
import shutil
import unittest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)

import backend.storage as storage  # noqa: E402


class _IsolatedStorage(unittest.TestCase):
    """storage の DB とソース JSON パスを一時ディレクトリへ隔離する基底クラス。"""

    def setUp(self):
        self._tmp = tempfile.mkdtemp()
        self._orig = {k: getattr(storage, k) for k in (
            'DB_PATH', 'SETTINGS_DIR', 'CONFIG_PATH', 'CONFIG_EXAMPLE_PATH',
            'ENVS_PATH', 'ENVS_EXAMPLE_PATH', 'LAUNCHES_PATH',
            'SERVER_PATH', 'SERVER_EXAMPLE_PATH', 'TOOLS_SETTINGS_DIR',
        )}
        self._orig_init = set(storage._initialized)
        storage.DB_PATH = os.path.join(self._tmp, 'devhub.db')
        storage.SETTINGS_DIR = self._tmp
        storage.CONFIG_PATH = os.path.join(self._tmp, 'config.json')
        storage.CONFIG_EXAMPLE_PATH = os.path.join(self._tmp, 'config.example.json')
        storage.ENVS_PATH = os.path.join(self._tmp, 'envs.json')
        storage.ENVS_EXAMPLE_PATH = os.path.join(self._tmp, 'envs.example.json')
        storage.LAUNCHES_PATH = os.path.join(self._tmp, 'launches.json')
        storage.SERVER_PATH = os.path.join(self._tmp, 'server.json')
        storage.SERVER_EXAMPLE_PATH = os.path.join(self._tmp, 'server.example.json')
        storage.TOOLS_SETTINGS_DIR = os.path.join(self._tmp, 'tools')
        storage._initialized.discard(storage.DB_PATH)

    def tearDown(self):
        for k, v in self._orig.items():
            setattr(storage, k, v)
        storage._initialized.clear()
        storage._initialized.update(self._orig_init)
        shutil.rmtree(self._tmp, ignore_errors=True)

    def _write(self, path, obj):
        with open(path, 'w', encoding='utf-8') as f:
            json.dump(obj, f)


class RoundTripTest(_IsolatedStorage):
    def setUp(self):
        super().setUp()
        storage.init_db()
        storage._initialized.add(storage.DB_PATH)  # 移行を抑止して空 DB から検証

    def test_config_round_trip(self):
        storage.save_config({'scan_roots': ['~/a'], 'hidden_repos': ['/h']})
        self.assertEqual(storage.load_config()['scan_roots'], ['~/a'])
        self.assertEqual(storage.load_config()['hidden_repos'], ['/h'])

    def test_settings_merge_and_persist(self):
        storage.save_settings({'editor': 'vim'})
        storage.save_settings({'protected_ports': [80]})
        s = storage.load_settings()
        self.assertEqual(s['editor'], 'vim')          # 個別 patch がマージされる
        self.assertEqual(s['protected_ports'], [80])
        self.assertEqual(s['port'], 8765)             # デフォルトは残る

    def test_tool_settings_round_trip(self):
        self.assertEqual(storage.load_tool_settings('git'), {})
        storage.save_tool_settings('git', {'log_limit': 50})
        self.assertEqual(storage.load_tool_settings('git'), {'log_limit': 50})

    def test_envs_round_trip(self):
        storage.save_envs({'environments': [{'id': 'x'}]})
        self.assertEqual(storage.load_envs()['environments'][0]['id'], 'x')

    def test_launches_round_trip_and_replace(self):
        storage.save_launches({'launches': [
            {'launch_id': 'b', 'launched_at': '2026-01-02', 'env_id': 'e2'},
            {'launch_id': 'a', 'launched_at': '2026-01-01', 'env_id': 'e1'},
        ]})
        got = storage.load_launches()['launches']
        # launched_at 昇順で返る
        self.assertEqual([l['launch_id'] for l in got], ['a', 'b'])
        # save は全置換
        storage.save_launches({'launches': [{'launch_id': 'c', 'launched_at': '2026-01-03'}]})
        self.assertEqual([l['launch_id'] for l in storage.load_launches()['launches']], ['c'])

    def test_config_falls_back_to_default_shape(self):
        cfg = storage.load_config()
        self.assertEqual(cfg['hidden_repos'], [])
        self.assertIn('scan_roots', cfg)


class SeedTest(_IsolatedStorage):
    def setUp(self):
        super().setUp()
        storage.init_db()
        storage._initialized.add(storage.DB_PATH)

    def test_config_seeds_from_example(self):
        self._write(storage.CONFIG_EXAMPLE_PATH, {'scan_roots': ['/seed'], 'hidden_repos': []})
        cfg = storage.load_config()
        self.assertEqual(cfg['scan_roots'], ['/seed'])
        # seed 後は保存され、example 不在でも残る
        os.remove(storage.CONFIG_EXAMPLE_PATH)
        self.assertEqual(storage.load_config()['scan_roots'], ['/seed'])


class MigrationTest(_IsolatedStorage):
    def test_imports_existing_json_once(self):
        os.makedirs(storage.TOOLS_SETTINGS_DIR, exist_ok=True)
        self._write(storage.CONFIG_PATH, {'scan_roots': ['/old'], 'hidden_repos': ['/x']})
        self._write(storage.SERVER_PATH, {'editor': 'emacs'})
        self._write(storage.ENVS_PATH, {'environments': [{'id': 'migrated'}]})
        self._write(os.path.join(storage.TOOLS_SETTINGS_DIR, 'git.json'), {'log_limit': 7})
        self._write(storage.LAUNCHES_PATH, {'launches': [{'launch_id': 'L1', 'launched_at': '2026-01-01'}]})

        # _ensure_db 経由で1度だけ移行が走る
        self.assertEqual(storage.load_config()['scan_roots'], ['/old'])
        self.assertEqual(storage.load_settings()['editor'], 'emacs')
        self.assertEqual(storage.load_envs()['environments'][0]['id'], 'migrated')
        self.assertEqual(storage.load_tool_settings('git'), {'log_limit': 7})
        self.assertEqual(storage.load_launches()['launches'][0]['launch_id'], 'L1')

        # 元 JSON は消さない
        self.assertTrue(os.path.exists(storage.CONFIG_PATH))

        # 移行は冪等: ソースを書き換えても再取り込みされない
        self._write(storage.CONFIG_PATH, {'scan_roots': ['/changed']})
        storage.migrate_json_to_sqlite()
        self.assertEqual(storage.load_config()['scan_roots'], ['/old'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
