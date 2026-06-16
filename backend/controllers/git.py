import os
import subprocess
import re
import json
import logging
from backend.storage import load_config

logger = logging.getLogger(__name__)

def find_repos(root):
    repos = []
    root = os.path.expanduser(root)
    try:
        for entry in sorted(os.scandir(root), key=lambda e: e.name):
            if not entry.is_dir():
                continue
            if os.path.exists(os.path.join(entry.path, '.git')):
                repos.append({'name': entry.name, 'path': entry.path})
            else:
                try:
                    for sub in sorted(os.scandir(entry.path), key=lambda e: e.name):
                        if sub.is_dir() and os.path.exists(os.path.join(sub.path, '.git')):
                            repos.append({'name': f'{entry.name}/{sub.name}', 'path': sub.path})
                except PermissionError:
                    pass
    except (PermissionError, FileNotFoundError):
        pass
    return repos

def all_repos():
    cfg = load_config()
    excludes = {os.path.normcase(os.path.normpath(os.path.abspath(os.path.expanduser(p)))) for p in cfg.get('excludes', [])}
    seen = set()
    repos = []
    
    for root in cfg.get('scan_roots', []):
        for r in find_repos(root):
            norm_r = os.path.normpath(os.path.abspath(r['path']))
            case_r = os.path.normcase(norm_r)
            if case_r not in seen and case_r not in excludes:
                seen.add(case_r)
                repos.append({'name': r['name'], 'path': norm_r})
                
    for path in cfg.get('pinned_repos', []):
        expanded = os.path.normpath(os.path.abspath(os.path.expanduser(path)))
        case_expanded = os.path.normcase(expanded)
        if case_expanded in excludes or not os.path.isdir(expanded):
            continue
            
        if os.path.exists(os.path.join(expanded, '.git')):
            if case_expanded not in seen:
                seen.add(case_expanded)
                repos.append({'name': os.path.basename(expanded), 'path': expanded})
        else:
            sub_repos = find_repos(expanded)
            if sub_repos:
                for r in sub_repos:
                    norm_sub = os.path.normpath(os.path.abspath(r['path']))
                    case_sub = os.path.normcase(norm_sub)
                    if case_sub not in seen and case_sub not in excludes:
                        seen.add(case_sub)
                        repos.append({'name': r['name'], 'path': norm_sub})
            else:
                if case_expanded not in seen:
                    seen.add(case_expanded)
                    repos.append({'name': os.path.basename(expanded), 'path': expanded})
    return repos

def _get_validated_path(raw_path):
    if not raw_path:
        return None
    try:
        norm_raw = os.path.normcase(os.path.normpath(os.path.abspath(os.path.expanduser(raw_path))))
        for r in all_repos():
            if os.path.normcase(r['path']) == norm_raw:
                return r['path']
    except Exception:
        pass
    return None

def _validated_repo_path(params):
    return _get_validated_path(params.get('path', [None])[0])

def _validated_repo_path_from_body(data):
    raw = data.get('path') if isinstance(data, dict) else None
    return _get_validated_path(raw)

def _has_path_traversal(p):
    # Reject parent-directory references ('..') in user-supplied worktree paths.
    return any(part == '..' for part in re.split(r'[\\/]+', p) if part)

# Reject NUL and ASCII control characters (incl. newlines) in worktree paths.
# Note: argument injection is already prevented by the leading-dash check below
# plus list-form subprocess calls (no shell), so this is defense-in-depth and
# intentionally does NOT allowlist by codepoint — legitimate paths may contain
# non-ASCII characters (e.g. Japanese directory names).
_WORKTREE_PATH_BAD_CHARS = re.compile(r'[\x00-\x1f\x7f]')

def _validate_worktree_path(p):
    """Validate a user-supplied worktree path.

    Returns an error message string if invalid, or None if the path is OK.
    Requires an absolute path with no '..' segments, no leading dash (argument
    injection), and no NUL/control characters.

    NOTE: this closes the injection/traversal surface but deliberately does NOT
    constrain *where* on the filesystem the worktree may live (unlike repo paths,
    which are checked against configured roots). For a single-user localhost dev
    tool an absolute path anywhere is by design; tighten to an allowlisted parent
    directory here if this is ever exposed beyond localhost.
    """
    if not isinstance(p, str) or not p:
        return 'missing worktree path'
    if p.startswith('-') or _has_path_traversal(p):
        return 'invalid worktree path'
    if not os.path.isabs(p):
        return 'worktree path must be absolute'
    if _WORKTREE_PATH_BAD_CHARS.search(p):
        return 'invalid worktree path'
    return None

# Conservative allowlist for branch names passed to git as positional args.
# Intentionally stricter than git's real ref grammar: it rejects some otherwise
# valid branch names (e.g. ones containing '+' or unicode) in exchange for a
# small, easy-to-audit character set. The base_commit check in /worktree/add
# deliberately allows a broader set (~^@{}#) because it is a revspec, not a
# branch name — that asymmetry is by design, not an oversight.
_BRANCH_NAME_RE = re.compile(r'[a-zA-Z0-9_./-]+')

def _is_valid_branch_name(branch):
    """True if `branch` is safe to pass to git as a positional argument.

    Rejects empties, leading dashes (argument injection), names outside the
    allowlist, and any '..' segment — '..' is never valid in a git ref and is a
    path-traversal smell, mirroring _validate_worktree_path.
    """
    return (
        bool(branch)
        and not branch.startswith('-')
        and '..' not in branch
        and _BRANCH_NAME_RE.fullmatch(branch) is not None
    )

# Discrete poll-interval buckets (seconds) for the local status timer.
# Snapping the computed interval to a bucket gives two properties:
#  1. A sane floor (30s) — nothing meaningful changes in 10-30s, so polling
#     faster than this just wastes work.
#  2. Hysteresis — the suggested value only changes when commit cadence crosses
#     a bucket boundary, instead of jittering every poll (e.g. 45->50->42).
#     That keeps the frontend timers stable so the slower remote/fetch timer
#     actually reaches its deadline instead of being torn down each local poll.
_POLL_BUCKETS = [30, 60, 120, 300, 600]

def _bucketize_interval(seconds):
    for b in _POLL_BUCKETS:
        if seconds <= b:
            return b
    return _POLL_BUCKETS[-1]

def _base_merge_ref(repo_path):
    """Return the ref to use as the merge base for 'merged' worktree detection.

    Prefers origin/HEAD (e.g. 'origin/main') so branches merged via a PR on the
    remote are detected even when the local main is stale; falls back to a local
    'main' then 'master'. Returns None if none of these exist (merged detection
    is then skipped). Best-effort: never raises.
    """
    try:
        res = subprocess.run(
            ['git', 'symbolic-ref', '--short', 'refs/remotes/origin/HEAD'],
            cwd=repo_path, capture_output=True, text=True, timeout=10
        )
        ref = res.stdout.strip()
        if res.returncode == 0 and ref:
            return ref
    except Exception as e:
        logger.debug("origin/HEAD lookup failed: %s", e)

    for name in ('main', 'master'):
        try:
            res = subprocess.run(
                ['git', 'rev-parse', '--verify', '--quiet', name],
                cwd=repo_path, capture_output=True, text=True, timeout=10
            )
            if res.returncode == 0:
                return name
        except Exception as e:
            logger.debug("rev-parse %s failed: %s", name, e)
    return None

def _merged_branch_set(repo_path, base_ref):
    """Set of local branch short-names already merged into base_ref.

    The base branch itself is excluded — both the full base_ref ('origin/main')
    and its local short name ('main') — so the base is never proposed for removal.
    Returns an empty set if base_ref is None or the git call fails (best-effort).
    """
    if not base_ref:
        return set()
    try:
        res = subprocess.run(
            ['git', 'branch', '--merged', base_ref, '--format=%(refname:short)'],
            cwd=repo_path, capture_output=True, text=True, timeout=15
        )
        if res.returncode != 0:
            return set()
        names = {line.strip() for line in res.stdout.splitlines() if line.strip()}
    except Exception as e:
        logger.debug("git branch --merged failed: %s", e)
        return set()
    # Drop the base branch's own name(s) so it is never flagged as cleanable.
    names.discard(base_ref)
    names.discard(base_ref.split('/', 1)[1] if '/' in base_ref else base_ref)
    return names

def _parse_worktree_porcelain(text):
    """Parse `git worktree list --porcelain` output into records.

    Each record: {path, head?, branch?, detached?, bare?}. The first record is
    the main worktree. Pure string parsing — kept separate so it is unit-testable
    without spawning git.
    """
    worktrees = []
    current = {}
    for line in text.splitlines():
        if not line.strip():
            if current:
                worktrees.append(current)
                current = {}
            continue
        parts = line.split(' ', 1)
        if len(parts) == 2:
            key, val = parts
            if key == 'worktree':
                if current:
                    worktrees.append(current)
                current = {'path': val}
            elif key == 'HEAD':
                current['head'] = val
            elif key == 'branch':
                branch_ref = val
                if branch_ref.startswith('refs/heads/'):
                    current['branch'] = branch_ref[len('refs/heads/'):]
                else:
                    current['branch'] = branch_ref
        elif line.strip() == 'detached':
            current['detached'] = True
        elif line.strip() == 'bare':
            # bare main worktree: no branch/HEAD lines are emitted for it
            current['bare'] = True
    if current:
        worktrees.append(current)
    return worktrees

def list_worktrees(repo_path):
    """Return the worktrees registered for repo_path (source of truth = git).

    Runs `git worktree list --porcelain` and annotates each record with
    `is_main` (the first/primary worktree) and `exists` (the directory is still
    present on disk). Raises subprocess.CalledProcessError if git fails, or
    subprocess.TimeoutExpired if the call hangs past the timeout.

    The timeout bounds a single repo's git call: callers fan this out across
    many repos and block on all of them, so an unbounded hang on one repo would
    otherwise wedge the whole batch (e.g. the env-launcher worktree inventory).
    """
    res = subprocess.run(
        ['git', 'worktree', 'list', '--porcelain'],
        cwd=repo_path, capture_output=True, text=True, check=True, timeout=15,
    )
    worktrees = _parse_worktree_porcelain(res.stdout)
    for i, wt in enumerate(worktrees):
        wt['is_main'] = (i == 0)
        wt['exists'] = bool(wt.get('path')) and os.path.isdir(wt['path'])
    return worktrees

def handle_get(handler, path, params):
    repo_path = _validated_repo_path(params)
    if not repo_path:
        handler.send_json({'error': 'invalid or missing repository path'}, 400)
        return

    if path == '/api/git/status':
        try:
            res = subprocess.run(['git', 'status', '--porcelain=v1', '-u'], cwd=repo_path, capture_output=True, text=True, check=True)
            payload = {'output': res.stdout}

            # The dynamic poll interval spawns an extra `git log` subprocess, so it
            # is only computed when the client explicitly asks (?suggest=1). The
            # frontend requests it on the slower remote cadence, not on every
            # high-frequency local status poll.
            if params.get('suggest'):
                # Default to the slow ceiling. This is the value used both when
                # there are fewer than 2 commits in the last hour (not enough data
                # to estimate cadence) and when the `git log` below fails.
                dynamic_interval = 600
                try:
                    log_res = subprocess.run(
                        ['git', 'log', '--since=1 hour ago', '--format=%ct'],
                        cwd=repo_path, capture_output=True, text=True, timeout=10
                    )
                    timestamps = [int(t) for t in log_res.stdout.splitlines() if t.strip().isdigit()]
                    if len(timestamps) >= 2:
                        # abs(): git log is normally reverse-chronological, but a
                        # rebase/cherry-pick can skew committer dates and yield a
                        # negative delta, which would drag the average down.
                        intervals = [abs(timestamps[i] - timestamps[i+1]) for i in range(len(timestamps)-1)]
                        avg_interval = sum(intervals) / len(intervals)
                        dynamic_interval = _bucketize_interval(int(avg_interval / 4))
                except Exception as e:
                    # Best-effort: a failure here must not break the status response.
                    logger.debug("Failed to calculate dynamic poll interval: %s", e)

                payload['suggested_local_interval'] = dynamic_interval
                payload['suggested_remote_interval'] = dynamic_interval * 3

                # Whether any remote is configured. The frontend uses this to avoid
                # arming the origin-fetch timer (which would otherwise fail every
                # cycle) on a repo with no remote. Computed on the suggest cadence
                # only — it rarely changes, so we don't pay for it on each local poll.
                try:
                    remote_res = subprocess.run(
                        ['git', 'remote'], cwd=repo_path, capture_output=True, text=True, timeout=10
                    )
                    payload['has_remote'] = bool(remote_res.stdout.strip())
                except Exception as e:
                    logger.debug("Failed to check git remotes: %s", e)

            handler.send_json(payload)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/log':
        try:
            n = int(params.get('n', ['100'])[0])
        except ValueError:
            n = 100
        n = max(1, min(n, 1000))
        try:
            res = subprocess.run(['git', 'log', '--oneline', '--decorate', '--graph', f'-n{n}'], cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/branches':
        try:
            res = subprocess.run(['git', 'branch', '-a', '--format=%(refname)\t%(refname:short)\t%(HEAD)'], cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/diff':
        file_path = params.get('file', [''])[0]
        if not file_path:
            handler.send_json({'error': 'empty file path'}, 400)
            return
        staged = params.get('staged', ['0'])[0] == '1'
        cmd = ['git', 'diff']
        if staged:
            cmd.append('--cached')
        cmd.extend(['--', file_path])
        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/stash/list':
        try:
            res = subprocess.run(['git', 'stash', 'list'], cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/worktrees':
        try:
            # list_worktrees() already annotates `exists` and `is_main`.
            worktrees = list_worktrees(repo_path)
            # Additional cleanup-suggestion annotation: `merged` marks a worktree
            # whose branch is already merged into the base branch (origin/main,
            # falling back to local main/master), so it is safe to remove.
            base_ref = _base_merge_ref(repo_path)
            merged = _merged_branch_set(repo_path, base_ref)
            for wt in worktrees:
                wt['merged'] = bool(wt.get('branch') and wt['branch'] in merged)
            # merged_branches lets the frontend additionally propose deleting
            # merged LOCAL branches that have no worktree (a branch checked out
            # in a worktree must have its worktree removed first). The frontend
            # subtracts the worktree branches from this set.
            handler.send_json({
                'worktrees': worktrees,
                'base_branch': base_ref,
                'merged_branches': sorted(merged),
            })
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    handler.send_json({'error': 'not found'}, 404)

def handle_post(handler, path, data):
    repo_path = _validated_repo_path_from_body(data)
    if not repo_path:
        handler.send_json({'error': 'invalid or missing repository path'}, 400)
        return

    if path == '/api/git/stage':
        files = data.get('files', [])
        if not isinstance(files, list) or not all(isinstance(f, str) and f for f in files):
            handler.send_json({'error': 'invalid files list'}, 400)
            return
        try:
            res = subprocess.run(['git', 'add', '--'] + files, cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/unstage':
        files = data.get('files', [])
        if not isinstance(files, list) or not all(isinstance(f, str) and f for f in files):
            handler.send_json({'error': 'invalid files list'}, 400)
            return
        try:
            res = subprocess.run(['git', 'restore', '--staged', '--'] + files, cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/commit':
        message = data.get('message', '')
        if not message:
            handler.send_json({'error': 'no message specified'}, 400)
            return
        try:
            res = subprocess.run(['git', 'commit', '-m', message], cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/push':
        try:
            res = subprocess.run(['git', 'push'], cwd=repo_path, capture_output=True, text=True, check=True, timeout=60)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'push timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/pull':
        try:
            res = subprocess.run(['git', 'pull'], cwd=repo_path, capture_output=True, text=True, check=True, timeout=60)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'pull timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/checkout':
        branch = data.get('branch', '')
        if not _is_valid_branch_name(branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return
        try:
            res = subprocess.run(['git', 'checkout', branch], cwd=repo_path, capture_output=True, text=True, check=True, timeout=30)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'git checkout timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/branch/create':
        branch = data.get('branch', '')
        if not _is_valid_branch_name(branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return
        try:
            res = subprocess.run(['git', 'checkout', '-b', branch], cwd=repo_path, capture_output=True, text=True, check=True, timeout=30)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'git branch create timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/stash':
        action = data.get('action', '')
        cmd = ['git', 'stash']
        if action in ['push', 'pop', 'drop']:
            cmd.append(action)
            if action in ['pop', 'drop']:
                idx = data.get('index')
                if isinstance(idx, int):
                    cmd.append(f'stash@{{{idx}}}')
                else:
                    handler.send_json({'error': 'invalid index'}, 400)
                    return
        else:
            handler.send_json({'error': 'invalid action'}, 400)
            return

        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/worktree/add':
        worktree_path = data.get('worktree_path')
        branch = data.get('branch')
        new_branch = data.get('new_branch', False)
        base_commit = data.get('base_commit')
        
        if not worktree_path or not branch:
            handler.send_json({'error': 'missing worktree_path or branch'}, 400)
            return

        # Security validations to prevent argument injection
        if not _is_valid_branch_name(branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return

        path_err = _validate_worktree_path(worktree_path)
        if path_err:
            handler.send_json({'error': path_err}, 400)
            return

        if base_commit:
            # Revspec, not a branch name: a broader char set (~^@{}#) is allowed
            # on purpose (see _BRANCH_NAME_RE). The leading-dash / '..' guards still apply.
            if base_commit.startswith('-') or '..' in base_commit or not re.fullmatch(r'[a-zA-Z0-9_./~^@{}#-]+', base_commit):
                handler.send_json({'error': 'invalid base commit/branch'}, 400)
                return

        # Prune stale worktrees before adding to avoid conflict with manually deleted worktrees
        try:
            subprocess.run(['git', 'worktree', 'prune'], cwd=repo_path, capture_output=True, text=True, timeout=30)
        except Exception as e:
            logger.debug("worktree prune (pre-add) failed: %s", e)

        cmd = ['git', 'worktree', 'add']
        if new_branch:
            cmd.extend(['-b', branch, worktree_path])
            if base_commit:
                cmd.append(base_commit)
        else:
            cmd.extend([worktree_path, branch])

        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True, timeout=120)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'git worktree add timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/worktree/remove':
        worktree_path = data.get('worktree_path')
        force = data.get('force', False)
        if not worktree_path:
            handler.send_json({'error': 'missing worktree_path'}, 400)
            return

        path_err = _validate_worktree_path(worktree_path)
        if path_err:
            handler.send_json({'error': path_err}, 400)
            return

        cmd = ['git', 'worktree', 'remove']
        if force:
            cmd.append('--force')
        cmd.append(worktree_path)

        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True, timeout=60)
            # Prune after removal
            try:
                subprocess.run(['git', 'worktree', 'prune'], cwd=repo_path, capture_output=True, text=True, timeout=30)
            except Exception as e:
                logger.debug("worktree prune (post-remove) failed: %s", e)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'git worktree remove timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/worktree/pull':
        worktree_path = data.get('worktree_path')
        if not worktree_path:
            handler.send_json({'error': 'missing worktree_path'}, 400)
            return

        path_err = _validate_worktree_path(worktree_path)
        if path_err:
            handler.send_json({'error': path_err}, 400)
            return

        # Pull runs inside the worktree's own directory so it updates that
        # worktree's branch (not the main repo's). Harden against credential
        # prompts so a missing credential fails fast instead of hanging.
        env = os.environ.copy()
        env['GIT_TERMINAL_PROMPT'] = '0'
        env['GIT_SSH_COMMAND'] = 'ssh -o BatchMode=yes'
        try:
            res = subprocess.run(['git', 'pull'], cwd=worktree_path, env=env, capture_output=True, text=True, check=True, timeout=60)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'pull timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/worktree/prune':
        # Removes admin entries for worktrees whose directories no longer exist.
        # `git worktree remove` cannot do this (it fails with "is not a working
        # tree" once the dir is gone), so prune is the correct tool. It clears
        # all prunable entries at once, matching the batch cleanup UX.
        try:
            res = subprocess.run(['git', 'worktree', 'prune', '-v'], cwd=repo_path, capture_output=True, text=True, check=True, timeout=30)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'git worktree prune timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/branch/delete':
        branch = data.get('branch')
        force = data.get('force', False)
        if not branch:
            handler.send_json({'error': 'missing branch name'}, 400)
            return

        if not _is_valid_branch_name(branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return
            
        cmd = ['git', 'branch', '-D' if force else '-d', branch]
        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True, timeout=30)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'git branch delete timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/fetch':
        try:
            # Set environment variables to prevent git fetch from hanging on authentication prompt
            env = os.environ.copy()
            env['GIT_TERMINAL_PROMPT'] = '0'
            env['GIT_SSH_COMMAND'] = 'ssh -o BatchMode=yes'
            
            res = subprocess.run(['git', 'fetch', '--prune'], cwd=repo_path, env=env, capture_output=True, text=True, check=True, timeout=30)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.TimeoutExpired:
            handler.send_json({'error': 'git fetch timed out'}, 504)
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    handler.send_json({'error': 'not found'}, 404)
