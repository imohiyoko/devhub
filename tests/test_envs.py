#!/usr/bin/env python3
"""env-launcher backend (backend/controllers/envs.py) の単体テスト。

複数行コマンドを Terminal.app / iTerm に渡す際の AppleScript エスケープ
(_applescript_escape) を検証する。生の改行が文字列リテラルに残らないこと、
バックスラッシュ・ダブルクォートが退避されることを確認する。

標準ライブラリのみ。実行: python3 -m unittest tests.test_envs
                       または python3 tests/test_envs.py
"""
import os
import sys
import unittest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO_ROOT)

from backend.controllers.envs import _applescript_escape


class ApplescriptEscapeTest(unittest.TestCase):
    def test_plain_string_unchanged(self):
        self.assertEqual(_applescript_escape('make server.run'), 'make server.run')

    def test_newline_becomes_backslash_n(self):
        # 生の改行 (LF) は AppleScript が解釈できる 2 文字 `\n` に変換される。
        self.assertEqual(_applescript_escape('echo a\necho b'), 'echo a\\necho b')
        self.assertNotIn('\n', _applescript_escape('echo a\necho b'))

    def test_crlf_normalized(self):
        # CR は除去され、改行は単一の `\n` になる (CRLF の二重改行を防ぐ)。
        self.assertEqual(_applescript_escape('a\r\nb'), 'a\\nb')

    def test_double_quote_escaped(self):
        self.assertEqual(_applescript_escape('echo "hi"'), 'echo \\"hi\\"')

    def test_backslash_escaped_before_quote(self):
        # バックスラッシュが先に倍化され、その後クォートが退避される。
        self.assertEqual(_applescript_escape('a\\b"c'), 'a\\\\b\\"c')

    def test_multiline_with_quotes(self):
        out = _applescript_escape('cd "/x"\nmake run')
        self.assertEqual(out, 'cd \\"/x\\"\\nmake run')
        self.assertNotIn('\n', out)


if __name__ == '__main__':
    unittest.main()
