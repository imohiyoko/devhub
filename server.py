#!/usr/bin/env python3
import json, os, platform, shlex, subprocess, sys, threading, time, webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

BASE         = os.path.dirname(os.path.abspath(__file__))
SETTINGS_DIR = os.path.join(BASE, 'settings')
CONFIG_PATH  = os.path.join(SETTINGS_DIR, 'config.json')
CONFIG_EXAMPLE_PATH = os.path.join(SETTINGS_DIR, 'config.example.json')


# ── Settings (settings.json + settings.local.json) ──────────────────────────

def load_settings():
    defaults = {'port': 8765, 'editor': 'code', 'open_browser_on_start': True}
    for name in ('server.example.json', 'server.json'):
        try:
            with open(os.path.join(SETTINGS_DIR, name)) as f:
                defaults.update(json.load(f))
        except FileNotFoundError:
            pass
    return defaults

def save_settings(patch):
    path = os.path.join(SETTINGS_DIR, 'server.json')
    current = {}
    try:
        with open(path) as f:
            current = json.load(f)
    except FileNotFoundError:
        pass
    current.update(patch)
    with open(path, 'w') as f:
        json.dump(current, f, indent=2, ensure_ascii=False)
        f.write('\n')

SETTINGS = load_settings()
PORT     = SETTINGS['port']
EDITOR   = SETTINGS['editor']


# ── Routes ───────────────────────────────────────────────────────────────────

ROUTES = {
    '/':            os.path.join(BASE, 'dashboard', 'index.html'),
    '/diff-kun':    os.path.join(BASE, 'tools', 'diff-kun', 'index.html'),
    '/diff-kun/':   os.path.join(BASE, 'tools', 'diff-kun', 'index.html'),
    '/workspace':   os.path.join(BASE, 'tools', 'workspace', 'index.html'),
    '/workspace/':  os.path.join(BASE, 'tools', 'workspace', 'index.html'),
    '/diagram':     os.path.join(BASE, 'tools', 'diagram', 'index.html'),
    '/diagram/':    os.path.join(BASE, 'tools', 'diagram', 'index.html'),
    '/csv-tsv':     os.path.join(BASE, 'tools', 'csv-tsv', 'index.html'),
    '/csv-tsv/':    os.path.join(BASE, 'tools', 'csv-tsv', 'index.html'),
}


# ── Config (config.json) ─────────────────────────────────────────────────────

def load_config():
    try:
        with open(CONFIG_PATH) as f:
            return json.load(f)
    except FileNotFoundError:
        # first run: copy from example if available
        example = CONFIG_EXAMPLE_PATH
        try:
            with open(example) as f:
                cfg = json.load(f)
            save_config(cfg)
            return cfg
        except FileNotFoundError:
            pass
    except Exception:
        pass
    return {'scan_roots': ['~/developer'], 'excludes': [], 'pinned_repos': [], 'repo_order': []}


def save_config(cfg):
    with open(CONFIG_PATH, 'w') as f:
        json.dump(cfg, f, indent=2, ensure_ascii=False)
        f.write('\n')


# ── Repo discovery ────────────────────────────────────────────────────────────

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
    excludes = set(cfg.get('excludes', []))
    seen = set()
    repos = []
    for root in cfg.get('scan_roots', ['~/developer']):
        for r in find_repos(root):
            if r['path'] not in seen and r['path'] not in excludes:
                seen.add(r['path'])
                repos.append(r)
    for path in cfg.get('pinned_repos', []):
        expanded = os.path.expanduser(path)
        if expanded not in seen and expanded not in excludes and os.path.isdir(expanded):
            seen.add(expanded)
            repos.append({'name': os.path.basename(expanded), 'path': expanded})
    return repos


# ── Editor ────────────────────────────────────────────────────────────────────

def open_in_editor(path):
    if platform.system() == 'Windows':
        subprocess.Popen(f'"{EDITOR}" "{path}"', shell=True)
    elif platform.system() == 'Darwin' and EDITOR in ('code', 'cursor', 'windsurf'):
        _DARWIN_APP = {'code': 'Visual Studio Code', 'cursor': 'Cursor', 'windsurf': 'Windsurf'}
        subprocess.Popen(['open', '-a', _DARWIN_APP[EDITOR], path])
    else:
        subprocess.Popen([EDITOR, path])


# ── HTTP Handler ──────────────────────────────────────────────────────────────

class Handler(BaseHTTPRequestHandler):
    def send_json(self, data, status=200):
        body = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def read_body(self):
        length = int(self.headers.get('Content-Length', 0))
        return self.rfile.read(length)

    def do_GET(self):
        parsed = urlparse(self.path)
        path   = parsed.path
        params = parse_qs(parsed.query)

        if path == '/api/repos':
            self.send_json(all_repos())
            return

        if path == '/api/config':
            self.send_json(load_config())
            return

        if path == '/api/settings':
            self.send_json(load_settings())
            return

        if path == '/api/info':
            self.send_json({'base': BASE, 'port': PORT})
            return

        if path == '/api/open':
            target = params.get('path', [None])[0]
            if not target or not os.path.isdir(target):
                self.send_json({'error': 'invalid path'}, 400)
                return
            open_in_editor(target)
            self.send_json({'ok': True})
            return

        if path == '/api/ls':
            target = os.path.expanduser(params.get('path', ['~'])[0])
            if not os.path.isdir(target):
                self.send_json({'error': 'not a directory'}, 400)
                return
            try:
                workspace_paths = {r['path'] for r in all_repos()}
                entries = []
                for e in sorted(os.scandir(target), key=lambda x: x.name):
                    if not e.is_dir() or e.name.startswith('.'):
                        continue
                    is_git = os.path.exists(os.path.join(e.path, '.git'))
                    entries.append({
                        'name': e.name,
                        'path': e.path,
                        'is_git': is_git,
                        'in_workspace': e.path in workspace_paths,
                    })
                parent = str(os.path.dirname(target)) if target != os.path.sep else None
                self.send_json({'path': target, 'parent': parent, 'entries': entries})
            except PermissionError:
                self.send_json({'error': 'permission denied'}, 403)
            return

        file_path = ROUTES.get(path)
        if file_path is None:
            self.send_response(302)
            self.send_header('Location', '/')
            self.end_headers()
            return
        with open(file_path, 'rb') as f:
            body = f.read()
        self.send_response(200)
        self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        parsed = urlparse(self.path)
        path   = parsed.path

        if path == '/api/config':
            try:
                data = json.loads(self.read_body())
                cfg  = load_config()
                for key in ('scan_roots', 'excludes', 'pinned_repos', 'repo_order'):
                    if key in data:
                        cfg[key] = data[key]
                save_config(cfg)
                self.send_json({'ok': True})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/settings':
            try:
                data = json.loads(self.read_body())
                allowed = {'disabled_tools', 'tool_order', 'editor', 'open_browser_on_start'}
                save_settings({k: v for k, v in data.items() if k in allowed})
                self.send_json({'ok': True})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/restart':
            self.send_json({'ok': True})
            def do_restart():
                time.sleep(0.3)
                args = ' '.join(shlex.quote(a) for a in sys.argv)
                cmd = (
                    f'lsof -ti :{PORT} | xargs kill 2>/dev/null; '
                    f'sleep 0.3; '
                    f'exec {shlex.quote(sys.executable)} {args}'
                )
                subprocess.Popen(['sh', '-c', cmd], close_fds=True)
            threading.Thread(target=do_restart, daemon=True).start()
            return

        self.send_json({'error': 'not found'}, 404)

    def log_message(self, *_):
        pass


# ── Entry point ───────────────────────────────────────────────────────────────

open_browser = SETTINGS.get('open_browser_on_start', True) and '--no-browser' not in sys.argv
if open_browser:
    threading.Thread(
        target=lambda: (time.sleep(0.5), webbrowser.open(f'http://localhost:{PORT}')),
        daemon=True,
    ).start()

print(f'devhub → http://localhost:{PORT}  (Ctrl+C to quit)')
print(f'  platform : {platform.system()}')
print(f'  editor   : {EDITOR}')
HTTPServer(('127.0.0.1', PORT), Handler).serve_forever()
