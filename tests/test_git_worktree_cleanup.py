#!/usr/bin/env python3
"""git worktree クリーンアップ検出ロジックの回帰テスト。

git ツールは worktree タブで「マージ済みブランチの worktree」と「ディレクトリが
消えた worktree」を検出し、理由別に削除を提案する。その土台となる

  (A) マージ判定の基準ブランチ解決 (_base_merge_ref)
      - リモート無し → ローカル main / master フォールバック
      - origin/HEAD あり → origin/main を優先
  (B) マージ済みローカルブランチ集合 (_merged_branch_set)
      - マージ済みを含み、未マージ／基準ブランチ自身を含まない
  (C) ディレクトリ欠落判定 (os.path.isdir) が worktree 一覧で機能すること

を、実 git リポジトリを一時ディレクトリに作って検証する。サーバは起動しない。
標準ライブラリのみ。実行: python3 -m unittest tests.test_git_worktree_cleanup
                       または python3 tests/test_git_worktree_cleanup.py
"""
import os
import sys
import shutil
import tempfile
import subprocess
import unittest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)

import backend.controllers.git as gitc


def _git(cwd, *args):
    return subprocess.run(['git', *args], cwd=cwd, capture_output=True, text=True, check=True)


class WorktreeCleanupTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix='devhub-wt-')
        self.repo = os.path.join(self.tmp, 'repo')
        os.makedirs(self.repo)
        # 確定的にするため init.defaultBranch を main に固定して初期化。
        _git(self.repo, 'init', '-b', 'main')
        _git(self.repo, 'config', 'user.email', 'test@example.com')
        _git(self.repo, 'config', 'user.name', 'Test')
        self._commit('initial')

        # feature/x: コミットして main にマージ → マージ済み。
        _git(self.repo, 'checkout', '-b', 'feature/x')
        self._commit('x work')
        _git(self.repo, 'checkout', 'main')
        _git(self.repo, 'merge', '--no-ff', '-m', 'merge x', 'feature/x')

        # feature/y: main から分岐してコミット → 未マージ。
        _git(self.repo, 'checkout', '-b', 'feature/y')
        self._commit('y work')
        _git(self.repo, 'checkout', 'main')

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _commit(self, msg):
        # ファイルを変えてコミット (空コミットを避け、マージ判定を素直にする)。
        fn = os.path.join(self.repo, 'file.txt')
        with open(fn, 'a') as f:
            f.write(msg + '\n')
        _git(self.repo, 'add', 'file.txt')
        _git(self.repo, 'commit', '-m', msg)

    # --- (A) 基準ブランチ解決 -------------------------------------------------
    def test_base_merge_ref_local_fallback(self):
        # リモートが無いのでローカル main にフォールバックする。
        self.assertEqual(gitc._base_merge_ref(self.repo), 'main')

    def test_base_merge_ref_prefers_origin_head(self):
        # bare リモートを用意し push、origin/HEAD を設定すると origin/main を優先。
        remote = os.path.join(self.tmp, 'remote.git')
        _git(self.tmp, 'init', '--bare', '-b', 'main', remote)
        _git(self.repo, 'remote', 'add', 'origin', remote)
        _git(self.repo, 'push', '-u', 'origin', 'main')
        _git(self.repo, 'remote', 'set-head', 'origin', '-a')
        self.assertEqual(gitc._base_merge_ref(self.repo), 'origin/main')

    def test_base_merge_ref_master_fallback(self):
        # main も origin も無く master だけの場合は master を返す。
        repo2 = os.path.join(self.tmp, 'repo2')
        os.makedirs(repo2)
        _git(repo2, 'init', '-b', 'master')
        _git(repo2, 'config', 'user.email', 'test@example.com')
        _git(repo2, 'config', 'user.name', 'Test')
        with open(os.path.join(repo2, 'a.txt'), 'w') as f:
            f.write('a\n')
        _git(repo2, 'add', 'a.txt')
        _git(repo2, 'commit', '-m', 'init')
        self.assertEqual(gitc._base_merge_ref(repo2), 'master')

    # --- (B) マージ済み集合 ---------------------------------------------------
    def test_merged_branch_set(self):
        merged = gitc._merged_branch_set(self.repo, 'main')
        self.assertIn('feature/x', merged)      # マージ済み
        self.assertNotIn('feature/y', merged)   # 未マージ
        self.assertNotIn('main', merged)        # 基準ブランチ自身は除外

    def test_merged_branch_set_excludes_base_local_name(self):
        # base_ref が origin/main のとき、ローカル名 'main' も除外される。
        merged = gitc._merged_branch_set(self.repo, 'origin/main')
        # origin/main に対する --merged は (push 済みでなくても) ローカル到達性で判定。
        self.assertNotIn('main', merged)

    def test_merged_branch_set_none_base(self):
        self.assertEqual(gitc._merged_branch_set(self.repo, None), set())

    def test_force_delete_needed_for_remote_merged_branch(self):
        # PR マージ相当: origin/main にだけマージされ、ローカル HEAD(main) には未マージの
        # ブランチは `git branch -d` で「未マージ」として拒否されるが、`-D` なら削除できる。
        # _merged_branch_set は origin/main 基準で merged と判定するため、提案経由の削除に
        # -D を使うのが正しい（= フロントが force:true を使う根拠）。
        base = os.path.join(self.tmp, 'remote2.git')
        work = os.path.join(self.tmp, 'work')
        _git(self.tmp, 'init', '--bare', '-b', 'main', base)
        _git(self.tmp, 'clone', base, work)
        _git(work, 'config', 'user.email', 'test@example.com')
        _git(work, 'config', 'user.name', 'Test')
        with open(os.path.join(work, 'f.txt'), 'w') as f:
            f.write('1\n')
        _git(work, 'add', 'f.txt')
        _git(work, 'commit', '-m', 'init')
        _git(work, 'push', '-u', 'origin', 'main')
        _git(work, 'remote', 'set-head', 'origin', '-a')

        # feature/z をコミットし、origin/main 側にだけ反映（PR マージ相当）。
        _git(work, 'checkout', '-b', 'feature/z')
        with open(os.path.join(work, 'f.txt'), 'a') as f:
            f.write('z\n')
        _git(work, 'add', 'f.txt')
        _git(work, 'commit', '-m', 'z work')
        _git(work, 'push', 'origin', 'feature/z:main')  # origin/main を z 先端へ
        _git(work, 'checkout', 'main')                   # ローカル main は古いまま
        _git(work, 'fetch', 'origin')

        # origin/main 基準では feature/z はマージ済み。
        self.assertEqual(gitc._base_merge_ref(work), 'origin/main')
        self.assertIn('feature/z', gitc._merged_branch_set(work, 'origin/main'))

        # ローカル HEAD(main) には未マージなので -d は拒否、-D は成功。
        soft = subprocess.run(['git', 'branch', '-d', 'feature/z'],
                              cwd=work, capture_output=True, text=True)
        self.assertNotEqual(soft.returncode, 0)
        force = subprocess.run(['git', 'branch', '-D', 'feature/z'],
                               cwd=work, capture_output=True, text=True)
        self.assertEqual(force.returncode, 0, force.stderr)

    # --- (C) ディレクトリ欠落判定 -------------------------------------------
    def test_missing_worktree_dir_detection(self):
        wt_x = os.path.join(self.tmp, 'wt-x')
        wt_y = os.path.join(self.tmp, 'wt-y')
        _git(self.repo, 'worktree', 'add', wt_x, 'feature/x')
        _git(self.repo, 'worktree', 'add', wt_y, 'feature/y')

        # wt-x のディレクトリを手で削除 → git の管理情報だけ残る (幽霊)。
        shutil.rmtree(wt_x)

        # worktree はまだ list に出る (prune 前)。
        listed = _git(self.repo, 'worktree', 'list', '--porcelain').stdout
        self.assertIn(wt_x, listed)

        # enrichment と同じ判定: 欠落側は False、存在側は True。
        self.assertFalse(os.path.isdir(wt_x))
        self.assertTrue(os.path.isdir(wt_y))

        # feature/x は (マージ済みだが) ディレクトリ欠落側に分類されるべき。
        merged = gitc._merged_branch_set(self.repo, 'main')
        self.assertIn('feature/x', merged)


if __name__ == '__main__':
    unittest.main()
