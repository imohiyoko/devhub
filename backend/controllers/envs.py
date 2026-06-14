import os
import platform
import sys
import subprocess
import shlex
import tempfile
import shutil
import time
import threading
import json
import re
from collections import deque
from backend.storage import load_envs, save_envs, load_settings

def open_in_terminal(cwd, command, env=None):
    if not cwd:
        cwd = os.getcwd()
    sys_name = platform.system()
    settings = load_settings()
    terminal = settings.get('terminal', {})
    term_cfg = terminal.get(sys_name, {})
    emulator = term_cfg.get('emulator')
    shell = term_cfg.get('shell') or ('powershell' if sys_name == 'Windows' else 'bash')
    shell_args = term_cfg.get('shell_args', [])

    is_powershell = shell and ('powershell' in shell.lower() or 'pwsh' in shell.lower())

    cmd_with_env = command
    if env:
        env_exports = []
        for k, v in env.items():
            if sys_name == 'Windows':
                if is_powershell:
                    v_escaped = str(v).replace("'", "''")
                    env_exports.append(f"$env:{k}='{v_escaped}'")
                else:
                    v_escaped = str(v).replace('"', '\\"')
                    env_exports.append(f'set "{k}={v_escaped}"')
            else:
                env_exports.append(f'export {k}={shlex.quote(str(v))}')

        if env_exports:
            if sys_name == 'Windows' and is_powershell:
                separator = ' ; '
            elif sys_name == 'Windows' and not is_powershell:
                separator = ' & '
            else:
                separator = ' && '
            cmd_with_env = separator.join(env_exports) + separator + command

    merged_env = os.environ.copy() | (env or {})

    if not emulator or (emulator not in ('Terminal.app', 'iTerm', 'wt') and not shutil.which(emulator)):
        subprocess.Popen(command, cwd=cwd, shell=True, env=merged_env)
        return

    if sys_name == 'Darwin':
        if emulator == 'ghostty':
            cmd = ['ghostty', f'--working-directory={cwd}', '-e', shell] + shell_args + ['-c', command]
            subprocess.Popen(cmd, env=merged_env)
        elif emulator == 'Terminal.app':
            sh_cmd = f"cd {shlex.quote(cwd)} && {cmd_with_env}"
            safe_sh_cmd = sh_cmd.replace('\\', '\\\\').replace('"', '\\"')
            script = f'tell application "Terminal" to do script "{safe_sh_cmd}"'
            subprocess.Popen(['osascript', '-e', script])
        elif emulator == 'iTerm':
            sh_cmd = f"cd {shlex.quote(cwd)} && {cmd_with_env}"
            safe_sh_cmd = sh_cmd.replace('\\', '\\\\').replace('"', '\\"')
            script = f'''
            tell application "iTerm"
                create window with default profile
                tell current session of current window
                    write text "{safe_sh_cmd}"
                end tell
            end tell
            '''
            subprocess.Popen(['osascript', '-e', script])
        else:
            subprocess.Popen(command, cwd=cwd, shell=True, env=merged_env)

    elif sys_name == 'Windows':
        if emulator == 'wt':
            flag = '-Command' if is_powershell else '/c'
            cmd = ['wt', 'new-tab', '--startingDirectory', cwd, shell] + shell_args + [flag, cmd_with_env]
            subprocess.Popen(cmd, env=merged_env)
        else:
            subprocess.Popen(command, cwd=cwd, shell=True, env=merged_env)

    elif sys_name == 'Linux':
        if emulator == 'gnome-terminal':
            cmd = ['gnome-terminal', f'--working-directory={cwd}', '--', shell] + shell_args + ['-c', cmd_with_env]
            subprocess.Popen(cmd, env=merged_env)
        elif emulator == 'xterm':
            cmd = ['xterm', '-e', shell] + shell_args + ['-c', cmd_with_env]
            subprocess.Popen(cmd, cwd=cwd, env=merged_env)
        else:
            subprocess.Popen(command, cwd=cwd, shell=True, env=merged_env)
    else:
        subprocess.Popen(command, cwd=cwd, shell=True, env=merged_env)

def launch_process(process_def, cwd_override=None):
    raw_cwd = process_def.get('cwd')
    cwd = cwd_override if cwd_override else (os.path.expanduser(raw_cwd) if raw_cwd else None)
    if cwd == '':
        cwd = None
    env = process_def.get('env', {})
    open_in_terminal(cwd, process_def.get('command', ''), env)

def setup_worktree(env_id, worktree_def):
    # Note: Because open_in_terminal executes via system terminal emulators,
    # devhub cannot track when the user actually closes the terminal process.
    # Therefore, the worktree temporary directories cannot be automatically
    # cleaned up on process exit and must be cleaned up manually.
    if not worktree_def or not worktree_def.get('enabled'):
        return None
    repo_path = os.path.expanduser(worktree_def.get('repo_path', ''))
    branch = worktree_def.get('branch', '')
    if not repo_path or not branch:
        return None

    tmp_path = tempfile.mkdtemp(prefix=f"devhub-env-{env_id}-")
    try:
        subprocess.run(['git', 'worktree', 'add', tmp_path, branch], cwd=repo_path, check=True, capture_output=True, text=True)
        return tmp_path
    except subprocess.CalledProcessError as e:
        shutil.rmtree(tmp_path, ignore_errors=True)
        err_msg = e.stderr.strip() if e.stderr else str(e)
        if 'already checked out' in err_msg:
            raise ValueError(f"Git worktree creation failed: branch '{branch}' is already checked out at another location. ({err_msg})")
        raise ValueError(f"Git worktree creation failed: {err_msg}")
    except Exception:
        shutil.rmtree(tmp_path, ignore_errors=True)
        raise

def launch_environment(env_id):
    envs_data = load_envs()
    env_def = next((e for e in envs_data.get('environments', []) if e.get('id') == env_id), None)
    if not env_def:
        raise ValueError(f"Environment '{env_id}' not found")

    processes = env_def.get('processes', [])

    # Topological sort
    in_degree = {p['id']: 0 for p in processes}
    adj = {p['id']: [] for p in processes}
    for p in processes:
        for dep in p.get('depends_on', []):
            if dep not in adj:
                raise ValueError(f"Dependency '{dep}' for process '{p['id']}' not found in environment")
            adj[dep].append(p['id'])
            in_degree[p['id']] += 1

    queue = deque([pid for pid, deg in in_degree.items() if deg == 0])
    sorted_pids = []
    while queue:
        pid = queue.popleft()
        sorted_pids.append(pid)
        for nxt in adj[pid]:
            in_degree[nxt] -= 1
            if in_degree[nxt] == 0:
                queue.append(nxt)

    if len(sorted_pids) != len(processes):
        raise ValueError("Circular dependency detected in depends_on")

    cwd_override = setup_worktree(env_id, env_def.get('worktree', {}))

    pid_to_def = {p['id']: p for p in processes}

    def run_all():
        try:
            for i, pid in enumerate(sorted_pids):
                p_def = pid_to_def[pid]
                launch_process(p_def, cwd_override=cwd_override)
                if i < len(sorted_pids) - 1:
                    try:
                        raw = p_def.get('delay_seconds')
                        delay = max(0.0, float(raw)) if raw is not None else 1.0
                    except (ValueError, TypeError):
                        delay = 1.0
                    time.sleep(delay)
        except Exception as e:
            print(f"Error in run_all for env '{env_id}': {e}", file=sys.stderr)

    threading.Thread(target=run_all, daemon=True).start()

def handle_get(handler, path, params):
    if path == '/api/envs':
        handler.send_json(load_envs())
        return
    handler.send_json({'error': 'not found'}, 404)

def handle_post(handler, path, data):
    if path == '/api/envs':
        env_ids = set()
        for env in data.get('environments', []):
            eid = env.get('id')
            if not eid or not re.fullmatch(r'[a-zA-Z0-9_-]+', eid):
                raise ValueError("invalid environment id")
            if eid in env_ids:
                raise ValueError(f"Duplicate environment ID '{eid}'")
            env_ids.add(eid)
            proc_ids = set()
            for proc in env.get('processes', []):
                pid = proc.get('id')
                if not pid or not isinstance(pid, str):
                    raise ValueError(f"Process ID is required and must be a string in environment '{eid}'")
                if pid in proc_ids:
                    raise ValueError(f"Duplicate process ID '{pid}' in environment '{eid}'")
                proc_ids.add(pid)

            # Verify dependencies and circular references
            processes = env.get('processes', [])
            in_degree = {p['id']: 0 for p in processes}
            adj = {p['id']: [] for p in processes}
            for p in processes:
                pid = p.get('id')
                for dep in p.get('depends_on', []):
                    if dep not in adj:
                        raise ValueError(f"Dependency '{dep}' for process '{pid}' not found in environment '{eid}'")
                    adj[dep].append(pid)
                    in_degree[pid] += 1

            queue = deque([pid for pid, deg in in_degree.items() if deg == 0])
            sorted_pids = []
            while queue:
                pid = queue.popleft()
                sorted_pids.append(pid)
                for nxt in adj[pid]:
                    in_degree[nxt] -= 1
                    if in_degree[nxt] == 0:
                        queue.append(nxt)

            if len(sorted_pids) != len(processes):
                raise ValueError(f"Circular dependency detected in environment '{eid}'")

        save_envs(data)
        handler.send_json({'ok': True})
        return

    if path == '/api/envs/launch':
        env_id = data.get('env_id')
        if not env_id:
            raise ValueError("env_id is required")
        launch_environment(env_id)
        handler.send_json({'ok': True})
        return

    if path == '/api/envs/launch/process':
        env_id = data.get('env_id')
        process_id = data.get('process_id')
        if not env_id or not process_id:
            raise ValueError("env_id and process_id are required")
        envs_data = load_envs()
        env_def = next((e for e in envs_data.get('environments', []) if e.get('id') == env_id), None)
        if not env_def:
            raise ValueError(f"Environment '{env_id}' not found")
        process_def = next((p for p in env_def.get('processes', []) if p.get('id') == process_id), None)
        if not process_def:
            raise ValueError(f"Process '{process_id}' not found")

        launch_process(process_def)
        handler.send_json({'ok': True})
        return

    handler.send_json({'error': 'not found'}, 404)
