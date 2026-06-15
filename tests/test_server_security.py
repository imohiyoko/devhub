#!/usr/bin/env python3
"""ローカル API のセキュリティ防御 (server.py) に対する回帰テスト。

実際に server.py をサブプロセスで起動し、(A) Host 許可リスト / (B) トークン認証 /
(C) Sec-Fetch-Site 検証 / トークン注入 / 再起動をまたいだトークン安定性を検証する。

標準ライブラリのみ。実行: python3 -m unittest tests.test_server_security
                       または python3 tests/test_server_security.py
"""
import json
import os
import socket
import subprocess
import sys
import time
import unittest
import urllib.error
import urllib.request

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# 既知の固定トークンを env で注入できる (server.py は DEVHUB_API_TOKEN を起点に読む)。
TEST_TOKEN = 'test-token-fixed-value-0123456789'


def _free_port():
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(('127.0.0.1', 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _request(method, url, headers=None, status_only=True):
    req = urllib.request.Request(url, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status if status_only else (resp.status, resp.read())
    except urllib.error.HTTPError as e:
        return e.code if status_only else (e.code, e.read())


class LocalApiSecurityTest(unittest.TestCase):
    proc = None
    port = None

    @classmethod
    def setUpClass(cls):
        cls.port = _free_port()
        # DEVHUB_PORT / DEVHUB_API_TOKEN を env で渡し、リポジトリの設定ファイルは一切書き換えない。
        env = {**os.environ,
               'DEVHUB_PORT': str(cls.port),
               'DEVHUB_API_TOKEN': TEST_TOKEN}
        cls.proc = subprocess.Popen(
            [sys.executable, 'server.py', '--no-browser'],
            cwd=REPO_ROOT, env=env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        # 起動待ち (トークンありで /api/info が 200 になるまで)。
        base = f'http://127.0.0.1:{cls.port}'
        deadline = time.time() + 10
        while time.time() < deadline:
            try:
                if _request('GET', base + '/api/info',
                            {'X-Devhub-Token': TEST_TOKEN}) == 200:
                    break
            except Exception:
                pass
            time.sleep(0.2)
        else:
            cls.tearDownClass()
            raise RuntimeError('test server did not start')

    @classmethod
    def tearDownClass(cls):
        if cls.proc:
            cls.proc.terminate()
            try:
                cls.proc.wait(timeout=5)
            except Exception:
                cls.proc.kill()

    @property
    def base(self):
        return f'http://127.0.0.1:{self.port}'

    # --- 配信 HTML / トークン注入 -----------------------------------------
    def test_html_served_with_token_shim(self):
        status, body = _request('GET', self.base + '/', status_only=False)
        self.assertEqual(status, 200)
        text = body.decode('utf-8', 'replace')
        self.assertIn(TEST_TOKEN, text)            # トークンが埋め込まれている
        self.assertIn('X-Devhub-Token', text)      # シムが注入されている
        self.assertIn('XMLHttpRequest', text)      # fetch だけでなく XHR もラップ

    def test_token_is_stable_via_env(self):
        # 再起動は env でトークンを引き継ぐ。env 注入トークンがそのまま配信されることで
        # 「再起動をまたいで既存タブが動作し続ける」性質を担保する (#1 回帰防止)。
        _, body = _request('GET', self.base + '/', status_only=False)
        self.assertIn(f'"{TEST_TOKEN}"', body.decode('utf-8', 'replace'))

    # --- (B) トークン認証 --------------------------------------------------
    def test_api_without_token_rejected(self):
        self.assertEqual(_request('GET', self.base + '/api/info'), 401)

    def test_api_with_token_ok(self):
        self.assertEqual(
            _request('GET', self.base + '/api/info', {'X-Devhub-Token': TEST_TOKEN}), 200)

    def test_api_info_reports_actual_port(self):
        # DEVHUB_PORT 上書き時、設定ファイル値ではなく実待受ポートを返すこと。
        status, body = _request('GET', self.base + '/api/info',
                                {'X-Devhub-Token': TEST_TOKEN}, status_only=False)
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body).get('port'), self.port)

    def test_api_with_wrong_token_rejected(self):
        self.assertEqual(
            _request('GET', self.base + '/api/info', {'X-Devhub-Token': 'nope'}), 401)

    def test_post_api_without_token_rejected(self):
        # simple request (text/plain) による CSRF 攻撃を模す。
        self.assertEqual(
            _request('POST', self.base + '/api/envs',
                     {'Content-Type': 'text/plain'}), 401)

    # --- (A) Host 許可リスト ----------------------------------------------
    def test_forbidden_host_rejected(self):
        # DNS リバインディングを模し、トークンが正しくても Host が不正なら 403。
        self.assertEqual(
            _request('GET', self.base + '/api/info',
                     {'X-Devhub-Token': TEST_TOKEN, 'Host': 'evil.attacker.com'}), 403)

    def test_forbidden_host_on_html_rejected(self):
        self.assertEqual(_request('GET', self.base + '/', {'Host': 'evil.com'}), 403)

    # --- (C) Sec-Fetch-Site ------------------------------------------------
    def test_cross_site_rejected_even_with_token(self):
        self.assertEqual(
            _request('GET', self.base + '/api/info',
                     {'X-Devhub-Token': TEST_TOKEN, 'Sec-Fetch-Site': 'cross-site'}), 401)

    def test_same_origin_with_token_ok(self):
        self.assertEqual(
            _request('GET', self.base + '/api/info',
                     {'X-Devhub-Token': TEST_TOKEN, 'Sec-Fetch-Site': 'same-origin'}), 200)


if __name__ == '__main__':
    unittest.main(verbosity=2)
