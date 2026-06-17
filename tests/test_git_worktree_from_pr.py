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

    def test_lookalike_host_rejected(self):
        # 左境界が無いと 'evilgithub.com' が github.com に化ける問題の回帰防止
        self.assertIsNone(git._parse_github_pr_url('https://evilgithub.com/o/r/pull/9'))

    def test_leading_dash_owner_rejected(self):
        self.assertIsNone(git._parse_github_pr_url('https://github.com/-flag/repo/pull/1'))

    def test_garbage_rejected(self):
        self.assertIsNone(git._parse_github_pr_url('not a url'))
        self.assertIsNone(git._parse_github_pr_url(''))
        self.assertIsNone(git._parse_github_pr_url(None))


class RemoteForGithubRepoTest(unittest.TestCase):
    def test_normalize_variants(self):
        self.assertEqual(git._normalize_github_remote('https://github.com/O/R.git'), 'o/r')
        self.assertEqual(git._normalize_github_remote('git@github.com:o/r.git'), 'o/r')
        self.assertEqual(git._normalize_github_remote('ssh://git@github.com/o/r'), 'o/r')
        self.assertIsNone(git._normalize_github_remote('https://gitlab.com/o/r.git'))
        self.assertIsNone(git._normalize_github_remote(None))

    def _remotes(self, text):
        completed = mock.Mock(returncode=0, stdout=text, stderr='')
        return mock.patch.object(git.subprocess, 'run', return_value=completed)

    def test_origin_match(self):
        with self._remotes('origin\thttps://github.com/o/r.git (fetch)\n'
                           'origin\thttps://github.com/o/r.git (push)\n'):
            self.assertEqual(git._remote_for_github_repo('/repo', 'o', 'r'), 'origin')

    def test_prefers_origin_when_multiple_match(self):
        with self._remotes('upstream\tgit@github.com:o/r.git (fetch)\n'
                           'origin\thttps://github.com/o/r (fetch)\n'):
            self.assertEqual(git._remote_for_github_repo('/repo', 'o', 'r'), 'origin')

    def test_matches_upstream_when_origin_is_a_fork(self):
        with self._remotes('origin\thttps://github.com/me/r.git (fetch)\n'
                           'upstream\thttps://github.com/o/r.git (fetch)\n'):
            self.assertEqual(git._remote_for_github_repo('/repo', 'o', 'r'), 'upstream')

    def test_no_match_returns_none(self):
        with self._remotes('origin\thttps://github.com/someone/else.git (fetch)\n'):
            self.assertIsNone(git._remote_for_github_repo('/repo', 'o', 'r'))


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
    def _run(self, data, gh_branch='feature/x', remote='origin', run_side_effect=None):
        handler = FakeHandler()
        # repo 検証・remote 照合・gh 解決を固定し、実 git 呼び出しのみ mock する。
        ok = mock.Mock(returncode=0, stdout='', stderr='')
        run_kwargs = {'side_effect': run_side_effect} if run_side_effect else {'return_value': ok}
        with mock.patch.object(git, '_validated_repo_path_from_body', return_value='/repos/app'), \
             mock.patch.object(git, '_remote_for_github_repo', return_value=remote), \
             mock.patch.object(git, '_gh_pr_head_branch', return_value=gh_branch), \
             mock.patch.object(git.subprocess, 'run', **run_kwargs) as run:
            git.handle_post(handler, '/api/git/worktree/from-pr', data)
        return handler, run

    def test_invalid_url_returns_400(self):
        handler, _ = self._run({'path': '/repos/app', 'pr_url': 'nope'})
        self.assertEqual(handler.status, 400)
        self.assertIn('error', handler.payload)

    def test_no_matching_remote_returns_400(self):
        handler, run = self._run(
            {'path': '/repos/app', 'pr_url': 'https://github.com/o/r/pull/5'},
            remote=None,
        )
        self.assertEqual(handler.status, 400)
        self.assertIn('o/r', handler.payload['error'])
        # remote が無いので fetch/add は一切呼ばれない
        self.assertEqual(run.call_count, 0)

    def test_success_uses_gh_branch_and_matched_remote(self):
        handler, run = self._run(
            {'path': '/repos/app', 'pr_url': 'https://github.com/o/r/pull/5'},
            gh_branch='feature/x', remote='upstream',
        )
        self.assertTrue(handler.payload.get('ok'))
        self.assertEqual(handler.payload['branch'], 'feature/x')
        self.assertEqual(handler.payload['pr_number'], 5)
        self.assertTrue(handler.payload['used_gh'])
        # 既定パスは <repo>-wt-<sanitized branch>
        self.assertEqual(handler.payload['worktree_path'], '/repos/app-wt-feature-x')
        # fetch は照合した remote (upstream) から pull/5/head、add は -b ... FETCH_HEAD
        calls = [c.args[0] for c in run.call_args_list]
        self.assertIn(['git', 'fetch', 'upstream', 'pull/5/head'], calls)
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

    def test_existing_branch_returns_409_with_hint(self):
        def side_effect(cmd, *a, **k):
            if cmd[:3] == ['git', 'worktree', 'add']:
                raise git.subprocess.CalledProcessError(
                    1, cmd, stderr="fatal: a branch named 'feature/x' already exists\n")
            return mock.Mock(returncode=0, stdout='', stderr='')
        handler, _ = self._run(
            {'path': '/repos/app', 'pr_url': 'https://github.com/o/r/pull/5'},
            run_side_effect=side_effect,
        )
        self.assertEqual(handler.status, 409)
        self.assertIn('既に存在', handler.payload['error'])


if __name__ == '__main__':
    unittest.main()
