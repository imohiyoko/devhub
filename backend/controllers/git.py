import os
import subprocess
import re
import json
from backend.storage import load_config

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

def handle_get(handler, path, params):
    repo_path = _validated_repo_path(params)
    if not repo_path:
        handler.send_json({'error': 'invalid or missing repository path'}, 400)
        return

    if path == '/api/git/status':
        try:
            res = subprocess.run(['git', 'status', '--porcelain=v1', '-u'], cwd=repo_path, capture_output=True, text=True, check=True)
            
            # Dynamic poll interval calculation based on commits in the last hour
            dynamic_interval = 600
            try:
                log_res = subprocess.run(
                    ['git', 'log', '--since="1 hour ago"', '--format=%ct'],
                    cwd=repo_path, capture_output=True, text=True
                )
                timestamps = [int(t) for t in log_res.stdout.splitlines() if t.strip().isdigit()]
                if len(timestamps) >= 2:
                    intervals = [timestamps[i] - timestamps[i+1] for i in range(len(timestamps)-1)]
                    avg_interval = sum(intervals) / len(intervals)
                    dynamic_interval = max(10, min(600, int(avg_interval / 4)))
                else:
                    # 0 or 1 commits -> treat as 1 commit -> 1 hour interval -> 1/4 is 15 mins (900s) -> capped at 10 mins (600s)
                    dynamic_interval = 600
            except Exception:
                pass

            handler.send_json({
                'output': res.stdout,
                'suggested_local_interval': dynamic_interval,
                'suggested_remote_interval': dynamic_interval * 3
            })
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
            res = subprocess.run(['git', 'worktree', 'list', '--porcelain'], cwd=repo_path, capture_output=True, text=True, check=True)
            lines = res.stdout.splitlines()
            worktrees = []
            current = {}
            for line in lines:
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
            if current:
                worktrees.append(current)
            handler.send_json({'worktrees': worktrees})
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
        if not branch or branch.startswith('-') or not re.fullmatch(r'[a-zA-Z0-9_./-]+', branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return
        try:
            res = subprocess.run(['git', 'checkout', branch], cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/branch/create':
        branch = data.get('branch', '')
        if not branch or branch.startswith('-') or not re.fullmatch(r'[a-zA-Z0-9_./-]+', branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return
        try:
            res = subprocess.run(['git', 'checkout', '-b', branch], cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
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
        if branch.startswith('-') or not re.fullmatch(r'[a-zA-Z0-9_./-]+', branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return

        if worktree_path.startswith('-'):
            handler.send_json({'error': 'invalid worktree path'}, 400)
            return

        if base_commit:
            if base_commit.startswith('-') or not re.fullmatch(r'[a-zA-Z0-9_./~^@{}#-]+', base_commit):
                handler.send_json({'error': 'invalid base commit/branch'}, 400)
                return

        # Prune stale worktrees before adding to avoid conflict with manually deleted worktrees
        try:
            subprocess.run(['git', 'worktree', 'prune'], cwd=repo_path, capture_output=True, text=True)
        except Exception:
            pass
            
        cmd = ['git', 'worktree', 'add']
        if new_branch:
            cmd.extend(['-b', branch, worktree_path])
            if base_commit:
                cmd.append(base_commit)
        else:
            cmd.extend([worktree_path, branch])
            
        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/worktree/remove':
        worktree_path = data.get('worktree_path')
        force = data.get('force', False)
        if not worktree_path:
            handler.send_json({'error': 'missing worktree_path'}, 400)
            return

        if worktree_path.startswith('-'):
            handler.send_json({'error': 'invalid worktree path'}, 400)
            return
            
        cmd = ['git', 'worktree', 'remove']
        if force:
            cmd.append('--force')
        cmd.append(worktree_path)
        
        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True)
            # Prune after removal
            try:
                subprocess.run(['git', 'worktree', 'prune'], cwd=repo_path, capture_output=True, text=True)
            except Exception:
                pass
            handler.send_json({'ok': True, 'output': res.stdout})
        except subprocess.CalledProcessError as e:
            handler.send_json({'error': e.stderr}, 400)
        return

    if path == '/api/git/branch/delete':
        branch = data.get('branch')
        force = data.get('force', False)
        if not branch:
            handler.send_json({'error': 'missing branch name'}, 400)
            return

        if branch.startswith('-') or not re.fullmatch(r'[a-zA-Z0-9_./-]+', branch):
            handler.send_json({'error': 'invalid branch name'}, 400)
            return
            
        cmd = ['git', 'branch', '-D' if force else '-d', branch]
        try:
            res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True)
            handler.send_json({'ok': True, 'output': res.stdout})
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
