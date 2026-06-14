#!/usr/bin/env python3
import json
import os
import platform
import shlex
import subprocess
import sys
import threading
import time
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

import backend.controllers.git as git_controller
import backend.controllers.workspace as workspace_controller
import backend.controllers.envs as envs_controller
import backend.controllers.database as database_controller
import backend.controllers.ports as ports_controller
import backend.controllers.settings as settings_controller
from backend.storage import load_settings

BASE = os.path.dirname(os.path.abspath(__file__))
SETTINGS = load_settings()
PORT = SETTINGS.get('port', 8765)
EDITOR = SETTINGS.get('editor', 'code')
TERMINAL = SETTINGS.get('terminal', {})

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
    '/db-table':    os.path.join(BASE, 'tools', 'db-table', 'index.html'),
    '/db-table/':   os.path.join(BASE, 'tools', 'db-table', 'index.html'),
    '/ports':       os.path.join(BASE, 'tools', 'ports', 'index.html'),
    '/ports/':      os.path.join(BASE, 'tools', 'ports', 'index.html'),
    '/env-launcher':  os.path.join(BASE, 'tools', 'env-launcher', 'index.html'),
    '/env-launcher/': os.path.join(BASE, 'tools', 'env-launcher', 'index.html'),
    '/git':       os.path.join(BASE, 'tools', 'git', 'index.html'),
    '/git/':      os.path.join(BASE, 'tools', 'git', 'index.html'),
}

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

        try:
            if path == '/api/config' or path == '/api/settings' or path.startswith('/api/settings/tool/'):
                settings_controller.handle_get(self, path)
                return

            if path == '/api/repos':
                self.send_json(git_controller.all_repos())
                return

            if path == '/api/envs':
                envs_controller.handle_get(self, path, params)
                return

            if path.startswith('/api/git/'):
                git_controller.handle_get(self, path, params)
                return

            if path == '/api/ls':
                workspace_controller.handle_ls(self, params)
                return

            if path == '/api/open':
                workspace_controller.handle_open(self, params)
                return



            if path == '/api/info':
                current_settings = load_settings()
                self.send_json({
                    'base': BASE,
                    'port': current_settings.get('port', 8765),
                    'home': os.path.expanduser('~'),
                    'is_windows': platform.system() == 'Windows'
                })
                return

            if path == '/api/db/tables' or path == '/api/db/rows':
                database_controller.handle_get(self, path, params)
                return

            if path == '/api/ports':
                ports_controller.handle_get(self, path, params)
                return
        except Exception as e:
            self.send_json({'error': str(e)}, 400)
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

        try:
            body = self.read_body()
            data = json.loads(body) if body else {}
        except Exception:
            data = {}

        try:
            if path == '/api/config' or path == '/api/settings' or path.startswith('/api/settings/tool/'):
                settings_controller.handle_post(self, path, data)
                return

            if path.startswith('/api/git/'):
                git_controller.handle_post(self, path, data)
                return

            if path.startswith('/api/envs'):
                envs_controller.handle_post(self, path, data)
                return

            if path.startswith('/api/db/'):
                database_controller.handle_post(self, path, data)
                return

            if path.startswith('/api/ports/'):
                ports_controller.handle_post(self, path, data)
                return

            if path == '/api/restart':
                self.send_json({'ok': True})
                def do_restart():
                    time.sleep(0.3)
                    try:
                        self.server.server_close()
                    except Exception:
                        pass
                    if platform.system() == 'Windows':
                        subprocess.Popen([sys.executable] + sys.argv, close_fds=True)
                        os._exit(0)
                    else:
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
        except Exception as e:
            self.send_json({'error': str(e)}, 400)

    def log_message(self, *_):
        pass

open_browser = SETTINGS.get('open_browser_on_start', True) and '--no-browser' not in sys.argv
if open_browser:
    threading.Thread(
        target=lambda: (time.sleep(0.5), webbrowser.open(f'http://localhost:{PORT}')),
        daemon=True,
    ).start()

print(f'devhub → http://localhost:{PORT}  (Ctrl+C to quit)')
print(f'  platform : {platform.system()}')
print(f'  editor   : {EDITOR}')
sys_terminal = TERMINAL.get(platform.system(), {})
if sys_terminal:
    print(f'  terminal : {sys_terminal.get("emulator", "?")} / {sys_terminal.get("shell", "?")}')
HTTPServer(('127.0.0.1', PORT), Handler).serve_forever()
