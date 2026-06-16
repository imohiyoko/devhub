import os
import platform
import sys
import subprocess
import shlex
import shutil
import time
import threading
import json
import re
import secrets
from datetime import datetime
from collections import deque
from backend.storage import load_envs, save_envs, load_settings, load_launches, save_launches
import backend.controllers.ports as ports_controller
import backend.controllers.workspace as workspace_controller
import backend.controllers.git as git_controller

def _applescript_escape(s):
    """AppleScript の二重引用符文字列リテラル用に文字列をエスケープする。

    バックスラッシュとダブルクォートを退避し、改行は AppleScript が解釈できる
    ``\\n`` (バックスラッシュ + n) に変換する。生の改行を文字列リテラル内に
    置くと osascript の構文エラーになるため、複数行コマンドを Terminal.app /
    iTerm に渡すにはこの変換が必須。CR は除去して CRLF の二重改行を防ぐ。
    """
    return (
        s.replace('\\', '\\\\')
         .replace('"', '\\"')
         .replace('\r', '')
         .replace('\n', '\\n')
    )

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
            # --wait-after-command を付けないと、起動コマンドが終了/クラッシュした瞬間に
            # ghostty が窓を閉じてしまい（既定 wait-after-command=false）、起動失敗の
            # エラーが読めないまま消える。-e より前に置く必要がある（-e 以降はすべて
            # 実行コマンド扱いになるため）。
            cmd = ['ghostty', f'--working-directory={cwd}', '--wait-after-command=true',
                   '-e', shell] + shell_args + ['-c', command]
            subprocess.Popen(cmd, env=merged_env)
        elif emulator == 'Terminal.app':
            sh_cmd = f"cd {shlex.quote(cwd)} && {cmd_with_env}"
            safe_sh_cmd = _applescript_escape(sh_cmd)
            script = f'tell application "Terminal" to do script "{safe_sh_cmd}"'
            subprocess.Popen(['osascript', '-e', script])
        elif emulator == 'iTerm':
            sh_cmd = f"cd {shlex.quote(cwd)} && {cmd_with_env}"
            safe_sh_cmd = _applescript_escape(sh_cmd)
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

def open_terminal_in_dir(cwd):
    """Open an interactive terminal *at* `cwd` without running a command.

    open_in_terminal() always appends `&& <command>`, so it can't be reused for a
    plain "open a shell here" action. This mirrors its emulator branches but drops
    into an interactive shell in `cwd` instead, leaving the session open. Falls
    back to opening the directory in the configured editor when no usable terminal
    emulator is available.
    """
    if not cwd or not os.path.isdir(cwd):
        raise ValueError('worktree directory does not exist')

    sys_name = platform.system()
    settings = load_settings()
    term_cfg = settings.get('terminal', {}).get(sys_name, {})
    emulator = term_cfg.get('emulator')

    def _editor_fallback():
        workspace_controller.open_in_editor(cwd)

    if not emulator:
        _editor_fallback()
        return

    if sys_name == 'Darwin':
        if emulator == 'ghostty' and shutil.which('ghostty'):
            subprocess.Popen(['ghostty', f'--working-directory={cwd}'])
        elif emulator == 'Terminal.app':
            sh_cmd = f"cd {shlex.quote(cwd)}"
            safe_sh_cmd = sh_cmd.replace('\\', '\\\\').replace('"', '\\"')
            script = f'tell application "Terminal" to do script "{safe_sh_cmd}"'
            subprocess.Popen(['osascript', '-e', script])
        elif emulator == 'iTerm':
            sh_cmd = f"cd {shlex.quote(cwd)}"
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
            _editor_fallback()
    elif sys_name == 'Windows':
        if emulator == 'wt' and shutil.which('wt'):
            subprocess.Popen(['wt', 'new-tab', '--startingDirectory', cwd])
        else:
            _editor_fallback()
    elif sys_name == 'Linux':
        if emulator == 'gnome-terminal' and shutil.which('gnome-terminal'):
            subprocess.Popen(['gnome-terminal', f'--working-directory={cwd}'])
        elif emulator == 'xterm' and shutil.which('xterm'):
            subprocess.Popen(['xterm'], cwd=cwd)
        else:
            _editor_fallback()
    else:
        _editor_fallback()

def launch_process(process_def, cwd_override=None, extra_env=None):
    raw_cwd = process_def.get('cwd')
    cwd = cwd_override if cwd_override else (os.path.expanduser(raw_cwd) if raw_cwd else None)
    if cwd == '':
        cwd = None
    env = process_def.get('env', {})
    if extra_env:
        # extra_env (e.g. an offset-assigned port) overrides the declared env.
        env = {**env, **extra_env}
    open_in_terminal(cwd, process_def.get('command', ''), env)

def _resolve_worktree(repo_path, branch):
    """Resolve (repo, branch) to an EXISTING worktree path, or None.

    git is the source of truth: we look up the branch among the repo's
    registered worktrees and never create one. Worktrees are long-lived,
    user-owned parallel-dev checkouts (managed in the git tool), so env-launcher
    only references them.
    """
    repo = os.path.expanduser(repo_path or '')
    if not repo or not branch:
        return None
    try:
        worktrees = git_controller.list_worktrees(repo)
    except (subprocess.CalledProcessError, OSError):
        return None
    for wt in worktrees:
        if wt.get('branch') == branch and wt.get('exists'):
            return wt.get('path')
    return None

def setup_worktree(env_id, worktree_def):
    """Resolve the env-level worktree binding to an existing worktree path.

    Returns None when no env-level worktree is configured. Raises ValueError when
    one is configured but no matching worktree exists — we never auto-create
    (the user makes worktrees in the git tool).
    """
    if not worktree_def or not worktree_def.get('enabled'):
        return None
    repo_path = worktree_def.get('repo_path', '')
    branch = worktree_def.get('branch', '')
    if not repo_path or not branch:
        return None
    wt = _resolve_worktree(repo_path, branch)
    if not wt:
        raise ValueError(
            f"branch '{branch}' の worktree が見つかりません（{repo_path}）。"
            "git tool で作成してください。"
        )
    return wt

def _resolve_cwds(env_def, env_cwd_override=None):
    """Build a {process_id: cwd} map by resolving each process's binding.

    A process with a binding {repo_path, branch} runs in that branch's existing
    worktree (ValueError if it doesn't exist — fail fast, before any side
    effect). A process without a binding falls back to env_cwd_override (the
    env-level worktree) or None (its own declared cwd, applied in launch_process).
    """
    cwds = {}
    for p in env_def.get('processes', []):
        binding = p.get('binding') or {}
        repo, branch = binding.get('repo_path'), binding.get('branch')
        if repo and branch:
            wt = _resolve_worktree(repo, branch)
            if not wt:
                raise ValueError(
                    f"process '{p.get('id')}': branch '{branch}' の worktree が"
                    f"見つかりません（{repo}）。git tool で作成してください。"
                )
            cwds[p.get('id')] = wt
        else:
            cwds[p.get('id')] = env_cwd_override
    return cwds

def _run_processes(env_def, cwd_by_pid=None, cwd_override=None, env_by_pid=None):
    """Topologically sort an environment's processes and launch them on a thread.

    Each process runs in cwd_by_pid[pid] when present (its resolved worktree),
    otherwise cwd_override, otherwise its own declared cwd. env_by_pid[pid] holds
    extra env vars to inject (e.g. an offset-assigned port). Does NOT touch the
    worktree or the launch registry — the caller owns that.
    """
    cwd_by_pid = cwd_by_pid or {}
    env_by_pid = env_by_pid or {}
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

    pid_to_def = {p['id']: p for p in processes}
    env_id = env_def.get('id')

    def run_all():
        try:
            for i, pid in enumerate(sorted_pids):
                p_def = pid_to_def[pid]
                launch_process(p_def, cwd_override=cwd_by_pid.get(pid, cwd_override),
                               extra_env=env_by_pid.get(pid))
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

# Serializes read-modify-write of launches.json. save_launches() is atomic on
# its own (os.replace), but load -> mutate -> save is not; without this lock two
# concurrent launches/removals could clobber each other's record.
_REGISTRY_LOCK = threading.Lock()

def _record_launch(env_def, worktree_path, cwds=None, assigned=None):
    """Append a launch record to the registry. Returns the created record.

    cwds is the {process_id: resolved worktree path} map from _resolve_cwds, so
    each process records the worktree/branch it was actually launched in.
    assigned is the {process_id: offset-assigned port} map (offset processes
    only).
    """
    wt = env_def.get('worktree', {}) or {}
    cwds = cwds or {}
    assigned = assigned or {}
    record = {
        'launch_id': datetime.now().strftime('%Y%m%d-%H%M%S-') + secrets.token_hex(3),
        'env_id': env_def.get('id'),
        'env_name': env_def.get('name') or env_def.get('id'),
        'worktree_path': worktree_path,
        'repo_path': os.path.expanduser(wt.get('repo_path', '')) if wt.get('enabled') else '',
        'branch': wt.get('branch', '') if wt.get('enabled') else '',
        'launched_at': datetime.now().astimezone().isoformat(timespec='seconds'),
        'processes': [
            {
                'id': p.get('id'),
                'label': p.get('label') or p.get('id'),
                'command': p.get('command', ''),
                'port': p.get('port'),
                'worktree_path': cwds.get(p.get('id')),
                'repo_path': (p.get('binding') or {}).get('repo_path', ''),
                'branch': (p.get('binding') or {}).get('branch', ''),
                'assigned_port': assigned.get(p.get('id')),
            }
            for p in env_def.get('processes', [])
        ],
    }
    with _REGISTRY_LOCK:
        data = load_launches()
        data['launches'].append(record)
        save_launches(data)
    return record

def launch_environment(env_id):
    envs_data = load_envs()
    env_def = next((e for e in envs_data.get('environments', []) if e.get('id') == env_id), None)
    if not env_def:
        raise ValueError(f"Environment '{env_id}' not found")

    # Resolve every process's worktree binding up front so a missing worktree
    # aborts the launch BEFORE any side effect (port kills, record, processes).
    env_cwd = setup_worktree(env_id, env_def.get('worktree', {}))
    cwds = _resolve_cwds(env_def, env_cwd_override=env_cwd)

    processes = env_def.get('processes', [])
    # baton processes take their fixed port by force (kill the current holder);
    # offset processes are left alone and instead get a free port assigned.
    _kill_ports_for([p for p in processes if not _is_offset(p)])
    assigned = _assign_ports(env_def, _live_port_index())
    env_by_pid = {pid: {p['port_env_var']: str(assigned[pid])}
                  for p in processes if (pid := p.get('id')) in assigned}

    _record_launch(env_def, env_cwd, cwds=cwds, assigned=assigned)
    _run_processes(env_def, cwd_by_pid=cwds, cwd_override=env_cwd, env_by_pid=env_by_pid)

def _parse_port_spec(spec):
    """Expand a process 'port' field into a sorted list of concrete ports.

    Accepts a single port (int or numeric string) or a range string like
    "3333-3340". Returns [] for empty/None. Raises ValueError on anything
    malformed so it can double as the save-time validator.

    Ranges exist because many dev servers auto-pick the next free port when the
    preferred one is taken (e.g. 3333 -> 3334), so a single declared port can't
    reliably match the process that actually came up.
    """
    if spec is None or spec == '':
        return []
    if isinstance(spec, bool):
        raise ValueError('invalid port')
    if isinstance(spec, int):
        ports = [spec]
    elif isinstance(spec, str):
        s = spec.strip()
        if '-' in s:
            a_str, b_str = s.split('-', 1)
            a, b = int(a_str.strip()), int(b_str.strip())
            if a > b:
                a, b = b, a
            ports = list(range(a, b + 1))
        else:
            ports = [int(s)]
    else:
        raise ValueError('invalid port')
    for p in ports:
        if p < 1 or p > 65535:
            raise ValueError('port out of range')
    if len(ports) > 1000:
        raise ValueError('port range too large')
    return ports

def _is_offset(proc):
    """True when a process opts into parallel offset ports (needs an env var to
    carry the assigned port). Everything else is baton (mutual exclusion)."""
    return proc.get('port_strategy') == 'offset' and bool(proc.get('port_env_var'))

def _assign_port(base, port_index, limit=200):
    """First free port >= base that nothing is currently listening on.

    Used by the offset strategy so parallel worktrees each get their own port
    instead of fighting over a fixed one. Falls back to base if the window is
    exhausted (the launch still proceeds)."""
    port = base
    while port <= 65535 and (port - base) < limit:
        if port not in port_index:
            return port
        port += 1
    print(f"launch: no free port within {limit} of base {base}; "
          f"falling back to {base} (may collide)", file=sys.stderr)
    return base

def _assign_ports(env_def, port_index):
    """Map {process_id: assigned_port} for offset processes only.

    Reserves each assigned port within this batch so two offset processes that
    share the same base don't both receive the same number. Operates on a copy
    of port_index so the caller's live-port snapshot is left intact.

    Scope is a single launch: separate launch_environment() calls take their own
    _live_port_index() snapshots, so two concurrent launches whose processes have
    not bound yet could still pick the same port. Acceptable for a local
    single-user tool; not a global guarantee.
    """
    port_index = dict(port_index)
    assigned = {}
    for p in env_def.get('processes', []):
        if not _is_offset(p):
            continue
        try:
            ports = _parse_port_spec(p.get('port'))
        except ValueError:
            ports = []
        if not ports:
            # offset with no base port: nothing to assign, so the env var is not
            # injected. /api/envs validation blocks this on save; warn for legacy
            # or hand-edited records (mirrors _assign_port's exhaustion log).
            print(f"launch: offset process '{p.get('id')}' has no base port; "
                  f"skipping port assignment", file=sys.stderr)
            continue
        port = _assign_port(ports[0], port_index)
        assigned[p.get('id')] = port
        port_index[port] = {'pid': None}  # reserve within this batch
    return assigned

def _find_launch(launches, launch_id):
    return next((l for l in launches if l.get('launch_id') == launch_id), None)

def _live_port_index():
    """Map declared port -> the live listening process, for enrichment & kill.

    Best-effort: returns {} if ports can't be listed (e.g. lsof missing). When a
    port has several listeners (IPv4 + IPv6 of the same server) the first match
    wins, which is enough to label and to kill.
    """
    index = {}
    try:
        for p in ports_controller.list_open_ports():
            index.setdefault(p['port'], p)
    except Exception:
        pass
    return index

def enrich_launches():
    """Return launch records annotated with live worktree/port status."""
    data = load_launches()
    port_index = _live_port_index()
    enriched = []
    for rec in data.get('launches', []):
        rec = dict(rec)
        wt = rec.get('worktree_path')
        rec['worktree_exists'] = bool(wt) and os.path.isdir(wt)
        procs = []
        for proc in rec.get('processes', []):
            proc = dict(proc)
            # An offset launch actually bound its assigned_port, so prefer it
            # over the declared port/range when checking live status.
            if proc.get('assigned_port'):
                spec_ports = [proc['assigned_port']]
            else:
                try:
                    spec_ports = _parse_port_spec(proc.get('port'))
                except ValueError:
                    spec_ports = []
            live = [
                {'port': p, 'pid': port_index[p]['pid']}
                for p in spec_ports if p in port_index
            ]
            proc['live_ports'] = live
            proc['running'] = bool(live)
            # Per-process worktree status (binding-based launches run each
            # process in its own worktree).
            pwt = proc.get('worktree_path')
            proc['worktree_exists'] = bool(pwt) and os.path.isdir(pwt)
            procs.append(proc)
        rec['processes'] = procs
        enriched.append(rec)
    return {'launches': enriched}

def _kill_ports_for(procs):
    """Kill any live listeners on the given processes' declared ports.

    Run before (re)starting so each process can bind its preferred port instead
    of auto-incrementing to the next free one, and so stale duplicates from a
    previous launch are cleared. Protected ports and kill failures are skipped
    silently — freeing the port is best-effort, the launch proceeds regardless.
    """
    port_index = _live_port_index()
    killed = False
    for proc in procs:
        try:
            spec_ports = _parse_port_spec(proc.get('port'))
        except ValueError:
            spec_ports = []
        for p in spec_ports:
            live = port_index.get(p)
            if live:
                try:
                    ports_controller.kill_port_process(live['port'], live['pid'])
                    killed = True
                except Exception as e:
                    print(f"launch: skip killing port {p}: {e}", file=sys.stderr)
    # Give the OS a moment to release the ports before re-binding.
    if killed:
        time.sleep(0.5)

def remove_launch(launch_id, force=False):
    """Drop a launch record from the runtime registry.

    Worktrees are long-lived, user-owned parallel-dev checkouts, so removing a
    launch only clears the tracking record — it NEVER deletes the worktree.
    Worktree removal lives in the git tool. (force is accepted for API
    compatibility but unused: there is nothing destructive to force.)
    """
    rec = _find_launch(load_launches().get('launches', []), launch_id)
    if not rec:
        raise ValueError('launch record not found')

    # Re-read under the lock so we don't clobber a record appended concurrently.
    with _REGISTRY_LOCK:
        data = load_launches()
        data['launches'] = [l for l in data.get('launches', []) if l.get('launch_id') != launch_id]
        save_launches(data)

def open_launch(launch_id, target):
    data = load_launches()
    rec = _find_launch(data.get('launches', []), launch_id)
    if not rec:
        raise ValueError('launch record not found')
    wt = rec.get('worktree_path')
    if not wt or not os.path.isdir(wt):
        raise ValueError('worktree directory does not exist')
    if target == 'editor':
        workspace_controller.open_in_editor(wt)
    elif target == 'terminal':
        open_terminal_in_dir(wt)
    else:
        raise ValueError('invalid target')

def handle_get(handler, path, params):
    if path == '/api/envs':
        handler.send_json(load_envs())
        return
    if path == '/api/envs/launches':
        handler.send_json(enrich_launches())
        return
    if path == '/api/envs/worktrees':
        # Cross-repo worktree inventory sourced from git itself, so the
        # env-launcher UI can pick an existing (repo, branch) -> worktree
        # instead of having the user hand-type a path. git is the source of
        # truth; a repo whose `git worktree list` fails is skipped, not fatal.
        repos = []
        for repo in git_controller.all_repos():
            try:
                worktrees = git_controller.list_worktrees(repo['path'])
            except (subprocess.CalledProcessError, OSError):
                continue
            repos.append({'name': repo['name'], 'path': repo['path'], 'worktrees': worktrees})
        handler.send_json({'repos': repos})
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

                # Optional declared port(s): a single port (3000) or a range
                # ("3000-3010"). Used to track/kill the process via /ports.
                try:
                    _parse_port_spec(proc.get('port'))
                except ValueError:
                    raise ValueError(f"Process '{pid}' port must be a port (3000) or range (3000-3010) within 1-65535 in environment '{eid}'")

                # Optional per-process worktree binding {repo_path, branch}: the
                # process runs in that branch's existing worktree. Both fields go
                # together; partial bindings are rejected to fail loudly.
                binding = proc.get('binding')
                if binding is not None:
                    if not isinstance(binding, dict):
                        raise ValueError(f"Process '{pid}' binding must be an object in environment '{eid}'")
                    brepo = binding.get('repo_path', '')
                    bbranch = binding.get('branch', '')
                    if not isinstance(brepo, str) or not isinstance(bbranch, str):
                        raise ValueError(f"Process '{pid}' binding repo_path/branch must be strings in environment '{eid}'")
                    if bool(brepo) != bool(bbranch):
                        raise ValueError(f"Process '{pid}' binding needs both repo_path and branch in environment '{eid}'")

                # Optional port strategy: 'baton' (default, mutual exclusion) or
                # 'offset' (parallel — assign a free port, inject via env var).
                strategy = proc.get('port_strategy')
                if strategy is not None and strategy not in ('baton', 'offset'):
                    raise ValueError(f"Process '{pid}' port_strategy must be 'baton' or 'offset' in environment '{eid}'")
                if strategy == 'offset':
                    env_var = proc.get('port_env_var')
                    if not env_var or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', env_var):
                        raise ValueError(f"Process '{pid}' offset strategy needs a valid port_env_var (e.g. PORT) in environment '{eid}'")
                    if not _parse_port_spec(proc.get('port')):
                        raise ValueError(f"Process '{pid}' offset strategy needs a base port in environment '{eid}'")

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

        # Resolve this process's worktree (its binding, else the env-level
        # worktree) before any side effect — raises if it doesn't exist.
        env_cwd = setup_worktree(env_id, env_def.get('worktree', {}))
        cwds = _resolve_cwds({'processes': [process_def]}, env_cwd_override=env_cwd)
        extra_env = None
        if _is_offset(process_def):
            ap = _assign_ports({'processes': [process_def]}, _live_port_index()).get(process_id)
            if ap is not None:
                extra_env = {process_def['port_env_var']: str(ap)}
        else:
            _kill_ports_for([process_def])
        launch_process(process_def, cwd_override=cwds.get(process_id), extra_env=extra_env)
        handler.send_json({'ok': True})
        return

    if path == '/api/envs/launches/remove':
        launch_id = data.get('launch_id')
        if not launch_id:
            raise ValueError('launch_id is required')
        force = data.get('force', False)
        # Explicit bool check: bool("false") is True, so a stray string from an
        # external client must not silently become a force-remove.
        if not isinstance(force, bool):
            raise ValueError('force must be a boolean')
        remove_launch(launch_id, force=force)
        handler.send_json({'ok': True})
        return

    if path == '/api/envs/launches/open':
        launch_id = data.get('launch_id')
        target = data.get('target')
        if not launch_id:
            raise ValueError('launch_id is required')
        open_launch(launch_id, target)
        handler.send_json({'ok': True})
        return

    handler.send_json({'error': 'not found'}, 404)
