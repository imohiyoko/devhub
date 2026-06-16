#!/usr/bin/env python3
"""env-launcher のランチレジストリ (backend/controllers/envs.py) のユニットテスト。

実サーバや実ファイルを触らず、load/save とポート列挙を差し替えて検証する。
実行: python3 -m unittest tests.test_launches
"""
import os
import sys
import unittest
from unittest import mock

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)

import backend.controllers.envs as envs  # noqa: E402


class FakeHandler:
    """handle_post 用の最小ハンドラ。send_json の引数を記録するだけ。"""
    def __init__(self):
        self.json = None
        self.status = None

    def send_json(self, data, status=200):
        self.json = data
        self.status = status


class PortValidationTest(unittest.TestCase):
    def _post_envs(self, env):
        handler = FakeHandler()
        with mock.patch.object(envs, 'save_envs') as saver:
            envs.handle_post(handler, '/api/envs', {'environments': [env]})
        return handler, saver

    def test_valid_port_accepted(self):
        env = {'id': 'e1', 'processes': [{'id': 'p', 'port': 3000}]}
        handler, saver = self._post_envs(env)
        self.assertEqual(handler.json, {'ok': True})
        saver.assert_called_once()

    def test_valid_range_accepted(self):
        env = {'id': 'e1', 'processes': [{'id': 'p', 'port': '3000-3010'}]}
        handler, _ = self._post_envs(env)
        self.assertEqual(handler.json, {'ok': True})

    def test_missing_port_accepted(self):
        env = {'id': 'e1', 'processes': [{'id': 'p'}]}
        handler, saver = self._post_envs(env)
        self.assertEqual(handler.json, {'ok': True})

    def test_out_of_range_port_rejected(self):
        env = {'id': 'e1', 'processes': [{'id': 'p', 'port': 70000}]}
        with self.assertRaises(ValueError):
            self._post_envs(env)

    def test_out_of_range_range_rejected(self):
        env = {'id': 'e1', 'processes': [{'id': 'p', 'port': '3000-70000'}]}
        with self.assertRaises(ValueError):
            self._post_envs(env)

    def test_non_integer_port_rejected(self):
        env = {'id': 'e1', 'processes': [{'id': 'p', 'port': 'abc'}]}
        with self.assertRaises(ValueError):
            self._post_envs(env)

    def test_bool_port_rejected(self):
        # bool は int のサブクラスなので明示的に弾けているか確認。
        env = {'id': 'e1', 'processes': [{'id': 'p', 'port': True}]}
        with self.assertRaises(ValueError):
            self._post_envs(env)


class ParsePortSpecTest(unittest.TestCase):
    def test_single(self):
        self.assertEqual(envs._parse_port_spec(3000), [3000])
        self.assertEqual(envs._parse_port_spec('3000'), [3000])

    def test_range(self):
        self.assertEqual(envs._parse_port_spec('3000-3002'), [3000, 3001, 3002])

    def test_reversed_range_normalized(self):
        self.assertEqual(envs._parse_port_spec('3002-3000'), [3000, 3001, 3002])

    def test_empty(self):
        self.assertEqual(envs._parse_port_spec(None), [])
        self.assertEqual(envs._parse_port_spec(''), [])

    def test_too_large_range_rejected(self):
        with self.assertRaises(ValueError):
            envs._parse_port_spec('1-5000')


class EnrichLaunchesTest(unittest.TestCase):
    def test_worktree_and_port_status(self):
        record = {
            'launch_id': 'L1',
            'env_id': 'e1',
            'env_name': 'Env 1',
            'worktree_path': REPO_ROOT,  # 実在するディレクトリ
            'processes': [
                {'id': 'up', 'label': 'Up', 'port': 3000},
                {'id': 'range', 'label': 'Range', 'port': '3330-3340'},
                {'id': 'down', 'label': 'Down', 'port': 4000},
                {'id': 'nop', 'label': 'NoPort', 'port': None},
            ],
        }
        # 'range' の宣言は 3330-3340 だが、実際に上がっているのは 3334（next-free）。
        live_ports = [
            {'port': 3000, 'pid': 111, 'host': '127.0.0.1', 'command': 'x'},
            {'port': 3334, 'pid': 222, 'host': '127.0.0.1', 'command': 'y'},
        ]
        with mock.patch.object(envs, 'load_launches', return_value={'launches': [record]}), \
             mock.patch.object(envs.ports_controller, 'list_open_ports', return_value=live_ports):
            out = envs.enrich_launches()

        rec = out['launches'][0]
        self.assertTrue(rec['worktree_exists'])
        by_id = {p['id']: p for p in rec['processes']}
        self.assertTrue(by_id['up']['running'])
        self.assertEqual(by_id['up']['live_ports'], [{'port': 3000, 'pid': 111}])
        # 範囲内の実バインドポート 3334 を検出できること。
        self.assertTrue(by_id['range']['running'])
        self.assertEqual(by_id['range']['live_ports'], [{'port': 3334, 'pid': 222}])
        self.assertFalse(by_id['down']['running'])
        self.assertEqual(by_id['down']['live_ports'], [])
        self.assertFalse(by_id['nop']['running'])

    def test_missing_worktree_marked_gone(self):
        record = {
            'launch_id': 'L2', 'env_id': 'e2', 'env_name': 'E2',
            'worktree_path': '/no/such/dir/devhub-env-x', 'processes': [],
        }
        with mock.patch.object(envs, 'load_launches', return_value={'launches': [record]}), \
             mock.patch.object(envs.ports_controller, 'list_open_ports', return_value=[]):
            out = envs.enrich_launches()
        self.assertFalse(out['launches'][0]['worktree_exists'])

    def test_port_listing_failure_is_nonfatal(self):
        record = {
            'launch_id': 'L3', 'env_id': 'e3', 'env_name': 'E3',
            'worktree_path': None, 'processes': [{'id': 'p', 'port': 3000}],
        }
        with mock.patch.object(envs, 'load_launches', return_value={'launches': [record]}), \
             mock.patch.object(envs.ports_controller, 'list_open_ports', side_effect=OSError('lsof missing')):
            out = envs.enrich_launches()
        proc = out['launches'][0]['processes'][0]
        self.assertFalse(proc['running'])
        self.assertEqual(proc['live_ports'], [])


class KillPortsForTest(unittest.TestCase):
    """起動前の pre-kill: 宣言ポート（単一/範囲）の稼働リスナーを kill する。"""

    def test_kills_occupied_single_and_range(self):
        procs = [
            {'id': 'a', 'port': 5070},
            {'id': 'b', 'port': '3330-3340'},  # 実際は 3334 が稼働
            {'id': 'c', 'port': None},
        ]
        live = [
            {'port': 5070, 'pid': 11, 'host': '127.0.0.1', 'command': 'x'},
            {'port': 3334, 'pid': 22, 'host': '127.0.0.1', 'command': 'y'},
        ]
        with mock.patch.object(envs.ports_controller, 'list_open_ports', return_value=live), \
             mock.patch.object(envs.ports_controller, 'kill_port_process') as killer, \
             mock.patch.object(envs.time, 'sleep'):
            envs._kill_ports_for(procs)
        killed = sorted(c.args for c in killer.call_args_list)
        self.assertEqual(killed, [(3334, 22), (5070, 11)])

    def test_no_kill_when_ports_free(self):
        procs = [{'id': 'a', 'port': 5070}]
        with mock.patch.object(envs.ports_controller, 'list_open_ports', return_value=[]), \
             mock.patch.object(envs.ports_controller, 'kill_port_process') as killer, \
             mock.patch.object(envs.time, 'sleep'):
            envs._kill_ports_for(procs)
        killer.assert_not_called()

    def test_kill_failure_is_swallowed(self):
        # 保護ポート等で kill が失敗しても例外を投げず起動を続行できること。
        procs = [{'id': 'a', 'port': 8765}]
        live = [{'port': 8765, 'pid': 99, 'host': '127.0.0.1', 'command': 'devhub'}]
        with mock.patch.object(envs.ports_controller, 'list_open_ports', return_value=live), \
             mock.patch.object(envs.ports_controller, 'kill_port_process', side_effect=ValueError('protected')), \
             mock.patch.object(envs.time, 'sleep'):
            envs._kill_ports_for(procs)  # should not raise


class RecordLaunchTest(unittest.TestCase):
    def test_record_fields(self):
        env_def = {
            'id': 'e1', 'name': 'Env One',
            'worktree': {'enabled': True, 'repo_path': '~/repo', 'branch': 'feat/x'},
            'processes': [{'id': 'be', 'label': 'Backend', 'command': 'run', 'port': 3000}],
        }
        store = {'launches': []}
        with mock.patch.object(envs, 'load_launches', return_value=store), \
             mock.patch.object(envs, 'save_launches') as saver:
            rec = envs._record_launch(env_def, '/tmp/devhub-env-e1-abc')

        self.assertEqual(rec['env_id'], 'e1')
        self.assertEqual(rec['env_name'], 'Env One')
        self.assertEqual(rec['worktree_path'], '/tmp/devhub-env-e1-abc')
        self.assertEqual(rec['branch'], 'feat/x')
        self.assertEqual(rec['repo_path'], os.path.expanduser('~/repo'))
        self.assertEqual(rec['processes'][0]['port'], 3000)
        self.assertTrue(rec['launch_id'])
        self.assertTrue(rec['launched_at'])
        saver.assert_called_once()


class RemoveLaunchTest(unittest.TestCase):
    def _run_remove(self, force):
        rec = {'launch_id': 'L1', 'worktree_path': '/tmp/wt', 'repo_path': '/repo', 'processes': []}
        ok = mock.Mock(returncode=0, stderr='')
        with mock.patch.object(envs, 'load_launches', return_value={'launches': [dict(rec)]}), \
             mock.patch.object(envs, 'save_launches') as saver, \
             mock.patch.object(envs.os.path, 'isdir', return_value=True), \
             mock.patch.object(envs, '_validate_worktree_path', return_value=None), \
             mock.patch.object(envs.subprocess, 'run', return_value=ok) as run:
            envs.remove_launch('L1', force=force)
        return run, saver

    def test_force_appends_flag_and_drops_record(self):
        run, saver = self._run_remove(force=True)
        cmd = run.call_args_list[0].args[0]
        self.assertEqual(cmd[:4], ['git', 'worktree', 'remove', '--force'])
        self.assertIn('/tmp/wt', cmd)
        self.assertEqual(saver.call_args.args[0]['launches'], [])

    def test_no_force_omits_flag(self):
        run, _ = self._run_remove(force=False)
        cmd = run.call_args_list[0].args[0]
        self.assertEqual(cmd, ['git', 'worktree', 'remove', '/tmp/wt'])

    def test_git_failure_raises_and_keeps_record(self):
        rec = {'launch_id': 'L1', 'worktree_path': '/tmp/wt', 'repo_path': '/repo', 'processes': []}
        fail = mock.Mock(returncode=1, stderr='contains modified files')
        with mock.patch.object(envs, 'load_launches', return_value={'launches': [dict(rec)]}), \
             mock.patch.object(envs, 'save_launches') as saver, \
             mock.patch.object(envs.os.path, 'isdir', return_value=True), \
             mock.patch.object(envs, '_validate_worktree_path', return_value=None), \
             mock.patch.object(envs.subprocess, 'run', return_value=fail):
            with self.assertRaises(ValueError):
                envs.remove_launch('L1', force=False)
        saver.assert_not_called()

    def test_missing_worktree_just_drops_record(self):
        rec = {'launch_id': 'L1', 'worktree_path': None, 'repo_path': '', 'processes': []}
        with mock.patch.object(envs, 'load_launches', return_value={'launches': [dict(rec)]}), \
             mock.patch.object(envs, 'save_launches') as saver, \
             mock.patch.object(envs.subprocess, 'run') as run:
            envs.remove_launch('L1')
        run.assert_not_called()
        self.assertEqual(saver.call_args.args[0]['launches'], [])


if __name__ == '__main__':
    unittest.main(verbosity=2)
