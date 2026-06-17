#!/usr/bin/env python3
"""PR から worktree を作る機能 (backend/controllers/git.py) のユニットテスト。

PR URL パース・gh ヘルパ・/api/git/worktree/from-pr 分岐を、実 git/gh を呼ばずに
検証する。実行: python3 -m unittest tests.test_git_worktree_from_pr
"""
import os
import sys
import unittest
from unittest import mock

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)

import backend.controllers.git as git  # noqa: E402


class ParseGithubPrUrlTest(unittest.TestCase):
    def test_standard_url(self):
        self.assertEqual(
            git._parse_github_pr_url('https://github.com/imohiyoko/devhub/pull/123'),
            ('imohiyoko', 'devhub', 123),
        )

    def test_trailing_path(self):
        self.assertEqual(
            git._parse_github_pr_url('https://github.com/owner/repo/pull/45/files'),
            ('owner', 'repo', 45),
        )

    def test_dot_git_and_scp_form(self):
        self.assertEqual(
            git._parse_github_pr_url('git@github.com:owner/repo.git/pull/7'),
            ('owner', 'repo', 7),
        )

    def test_number_is_int(self):
        owner, repo, number = git._parse_github_pr_url('https://github.com/o/r/pull/9')
        self.assertIsInstance(number, int)

    def test_non_github_host_rejected(self):
        self.assertIsNone(git._parse_github_pr_url('https://example.com/foo/bar/pull/9'))

    def test_garbage_rejected(self):
        self.assertIsNone(git._parse_github_pr_url('not a url'))
        self.assertIsNone(git._parse_github_pr_url(''))
        self.assertIsNone(git._parse_github_pr_url(None))


class GhPrHeadBranchTest(unittest.TestCase):
    def test_returns_branch_on_success(self):
        completed = mock.Mock(returncode=0, stdout='feature/cool\n', stderr='')
        with mock.patch.object(git.subprocess, 'run', return_value=completed) as run:
            self.assertEqual(git._gh_pr_head_branch('o', 'r', 12), 'feature/cool')
        # gh pr view を owner/repo 指定で呼んでいる
        args, _ = run.call_args
        self.assertEqual(args[0][:3], ['gh', 'pr', 'view'])
        self.assertIn('o/r', args[0])

    def test_nonzero_returns_none(self):
        completed = mock.Mock(returncode=1, stdout='', stderr='not found')
        with mock.patch.object(git.subprocess, 'run', return_value=completed):
            self.assertIsNone(git._gh_pr_head_branch('o', 'r', 12))

    def test_exception_returns_none(self):
        with mock.patch.object(git.subprocess, 'run', side_effect=FileNotFoundError):
            self.assertIsNone(git._gh_pr_head_branch('o', 'r', 12))

    def test_invalid_branch_name_rejected(self):
        completed = mock.Mock(returncode=0, stdout='bad branch name!\n', stderr='')
        with mock.patch.object(git.subprocess, 'run', return_value=completed):
            self.assertIsNone(git._gh_pr_head_branch('o', 'r', 12))


class FakeHandler:
    """handler.send_json(payload, status=200) を記録するだけのテストダブル。"""
    def __init__(self):
        self.payload = None
        self.status = None

    def send_json(self, payload, status=200):
        self.payload = payload
        self.status = status


class FromPrEndpointTest(unittest.TestCase):
    def _run(self, data, gh_branch='feature/x'):
        handler = FakeHandler()
        # repo path 検証と gh 解決を固定し、実際の git 呼び出しのみ mock する
        ok = mock.Mock(returncode=0, stdout='', stderr='')
        with mock.patch.object(git, '_validated_repo_path_from_body', return_value='/repos/app'), \
             mock.patch.object(git, '_gh_pr_head_branch', return_value=gh_branch), \
             mock.patch.object(git.subprocess, 'run', return_value=ok) as run:
            git.handle_post(handler, '/api/git/worktree/from-pr', data)
        return handler, run

    def test_invalid_url_returns_400(self):
        handler, _ = self._run({'path': '/repos/app', 'pr_url': 'nope'})
        self.assertEqual(handler.status, 400)
        self.assertIn('error', handler.payload)

    def test_success_uses_gh_branch_and_default_path(self):
        handler, run = self._run(
            {'path': '/repos/app', 'pr_url': 'https://github.com/o/r/pull/5'},
            gh_branch='feature/x',
        )
        self.assertTrue(handler.payload.get('ok'))
        self.assertEqual(handler.payload['branch'], 'feature/x')
        self.assertEqual(handler.payload['pr_number'], 5)
        self.assertTrue(handler.payload['used_gh'])
        # 既定パスは <repo>-wt-<sanitized branch>
        self.assertEqual(handler.payload['worktree_path'], '/repos/app-wt-feature-x')
        # fetch は pull/5/head、worktree add は -b feature/x ... FETCH_HEAD
        calls = [c.args[0] for c in run.call_args_list]
        self.assertIn(['git', 'fetch', 'origin', 'pull/5/head'], calls)
        add = next(c for c in calls if c[:3] == ['git', 'worktree', 'add'])
        self.assertIn('-b', add)
        self.assertIn('feature/x', add)
        self.assertEqual(add[-1], 'FETCH_HEAD')

    def test_falls_back_to_pr_number_when_gh_unavailable(self):
        handler, _ = self._run(
            {'path': '/repos/app', 'pr_url': 'https://github.com/o/r/pull/8'},
            gh_branch=None,
        )
        self.assertEqual(handler.payload['branch'], 'pr-8')
        self.assertFalse(handler.payload['used_gh'])


if __name__ == '__main__':
    unittest.main()
