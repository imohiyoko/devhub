#!/usr/bin/env python3
"""git worktree 列挙 (backend/controllers/git.py) のユニットテスト。

porcelain パースと list_worktrees の注釈 (is_main/exists) を、実 git を呼ばずに検証する。
実行: python3 -m unittest tests.test_git_worktrees
"""
import os
import sys
import unittest
from unittest import mock

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)

import backend.controllers.git as git  # noqa: E402

PORCELAIN = """\
worktree /repos/app
HEAD abc123
branch refs/heads/main

worktree /repos/app-feature
HEAD def456
branch refs/heads/feature/x

worktree /repos/app-detached
HEAD 789abc
detached
"""


class ParseWorktreePorcelainTest(unittest.TestCase):
    def test_parses_branches_detached_and_paths(self):
        wts = git._parse_worktree_porcelain(PORCELAIN)
        self.assertEqual(len(wts), 3)
        self.assertEqual(wts[0]['path'], '/repos/app')
        self.assertEqual(wts[0]['branch'], 'main')
        self.assertEqual(wts[0]['head'], 'abc123')
        self.assertEqual(wts[1]['branch'], 'feature/x')
        self.assertTrue(wts[2].get('detached'))
        self.assertNotIn('branch', wts[2])

    def test_bare_main_worktree(self):
        wts = git._parse_worktree_porcelain("worktree /repos/bare\nbare\n")
        self.assertEqual(len(wts), 1)
        self.assertTrue(wts[0].get('bare'))

    def test_empty(self):
        self.assertEqual(git._parse_worktree_porcelain(""), [])


class ListWorktreesTest(unittest.TestCase):
    def test_annotates_is_main_and_exists(self):
        completed = mock.Mock(stdout=PORCELAIN)
        with mock.patch.object(git.subprocess, 'run', return_value=completed) as run, \
             mock.patch.object(git.os.path, 'isdir', side_effect=lambda p: p != '/repos/app-detached'):
            wts = git.list_worktrees('/repos/app')
        # git worktree list --porcelain を repo の cwd で実行している
        args, kwargs = run.call_args
        self.assertEqual(args[0], ['git', 'worktree', 'list', '--porcelain'])
        self.assertEqual(kwargs['cwd'], '/repos/app')
        # 先頭のみ is_main、exists は isdir 判定に従う
        self.assertTrue(wts[0]['is_main'])
        self.assertFalse(wts[1]['is_main'])
        self.assertTrue(wts[0]['exists'])
        self.assertFalse(wts[2]['exists'])


if __name__ == '__main__':
    unittest.main()
