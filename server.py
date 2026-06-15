#!/usr/bin/env python3
import json
import os
import platform
import secrets
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

# --- Local API security ------------------------------------------------------
# devhub の API はユーザー権限で任意コマンド実行やローカルデータ読取を行うため、
# ブラウザ経由のクロスオリジン攻撃 (CSRF / DNS リバインディング) から守る必要がある。
# 127.0.0.1 バインドは LAN/外部からのアクセスを隠すだけで、閲覧サイトからの攻撃は防げない。
#
#  (A) Host ヘッダ許可リスト  -> DNS リバインディングを遮断
#  (B) 起動毎のランダムトークン -> /api/* に X-Devhub-Token 必須。配信 HTML にのみ埋め込む。
#      外部サイトは CORS で HTML を読めずトークンを取得できず、カスタムヘッダ必須化により
#      プリフライト不要の "simple request" にもできない (OPTIONS ハンドラを持たないため失敗する)。
#  (C) Sec-Fetch-Site -> ブラウザが付与し JS から偽装できない。cross-site/same-site を拒否。
#
# トークンは「devhub を実際に起動するたび」に新規生成するが、アプリ内の再起動
# (/api/restart) をまたいでは保持する。再起動は子プロセスを再 exec するため、環境変数
# DEVHUB_API_TOKEN で引き継ぐ。これにより既に開いているタブ (旧 HTML にトークンを保持)
# が「再起動のみ」後も 401 にならず継続動作できる。読み取り後は os.environ から除去し、
# devhub が起動する端末/エディタ等の無関係な子プロセスにトークンを漏らさない。
TOKEN = os.environ.pop('DEVHUB_API_TOKEN', None) or secrets.token_urlsafe(32)
ALLOWED_HOSTS = {
    f'localhost:{PORT}',
    f'127.0.0.1:{PORT}',
    f'[::1]:{PORT}',
}

# 配信 HTML に注入するブートストラップ。トークンを公開し、同一オリジンの /api/ 宛て
# fetch に自動で X-Devhub-Token を付与する。各ツールの fetch 呼び出しを個別に書き換え
# なくて済むよう window.fetch をラップする。<head> 直後に挿入され最初に実行される。
#
# 不変条件: すべての /api/ アクセスは window.fetch を経由すること。新しいツールが
# XMLHttpRequest / EventSource 等を使うと本シムを通らず 401 になる。その場合は
# window.__DEVHUB_TOKEN__ を読み、X-Devhub-Token ヘッダを手動付与すること。
_FETCH_SHIM_JS = '''(function(){
var T=%s;
window.__DEVHUB_TOKEN__=T;
var orig=window.fetch?window.fetch.bind(window):null;
if(!orig)return;
window.fetch=function(input,init){
init=init||{};
try{
var url=(typeof input==='string')?input:(input&&input.url)||'';
var u=new URL(url,window.location.href);
if(u.origin===window.location.origin&&u.pathname.indexOf('/api/')===0){
var h=new Headers((init&&init.headers)||(typeof input!=='string'&&input&&input.headers)||{});
h.set('X-Devhub-Token',T);
init.headers=h;
}
}catch(e){}
return orig(input,init);
};
})();'''
TOKEN_SCRIPT = ('<script>' + (_FETCH_SHIM_JS % json.dumps(TOKEN)) + '</script>').encode()

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

    def host_allowed(self):
        """(A) Host ヘッダ検証で DNS リバインディングを遮断する。"""
        return self.headers.get('Host', '') in ALLOWED_HOSTS

    def api_authorized(self):
        """(B) トークン + (C) Sec-Fetch-Site による /api/* の認可。"""
        # (C) ブラウザ付与の Sec-Fetch-Site。same-origin / none 以外 (cross-site, same-site) は拒否。
        #     古いブラウザ等で未付与の場合はトークン検証に委ねる。
        sfs = self.headers.get('Sec-Fetch-Site')
        if sfs is not None and sfs not in ('same-origin', 'none'):
            return False
        # (B) 起動毎ランダムトークン。定数時間比較でタイミング差を避ける。
        return secrets.compare_digest(self.headers.get('X-Devhub-Token', ''), TOKEN)

    def inject_token(self, body):
        """配信 HTML の <head ...> 開始タグ直後にトークン配布スクリプトを挿入する。

        大文字小文字・属性付き (<HEAD>, <head lang="ja"> 等) も許容する。
        <head> が無い場合は先頭に挿入する。
        """
        idx = body.lower().find(b'<head')
        if idx != -1:
            close = body.find(b'>', idx)
            if close != -1:
                pos = close + 1
                return body[:pos] + TOKEN_SCRIPT + body[pos:]
        return TOKEN_SCRIPT + body

    def do_GET(self):
        parsed = urlparse(self.path)
        path   = parsed.path
        params = parse_qs(parsed.query)

        if not self.host_allowed():
            self.send_json({'error': 'forbidden'}, 403)
            return
        if path.startswith('/api/') and not self.api_authorized():
            self.send_json({'error': 'unauthorized'}, 401)
            return

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
        body = self.inject_token(body)
        self.send_response(200)
        self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        parsed = urlparse(self.path)
        path   = parsed.path

        if not self.host_allowed():
            self.send_json({'error': 'forbidden'}, 403)
            return
        if path.startswith('/api/') and not self.api_authorized():
            self.send_json({'error': 'unauthorized'}, 401)
            return

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
                    # 再起動後の新プロセスへ現トークンを引き継ぎ、既存タブの 401 を防ぐ。
                    child_env = {**os.environ, 'DEVHUB_API_TOKEN': TOKEN}
                    if platform.system() == 'Windows':
                        subprocess.Popen([sys.executable] + sys.argv, close_fds=True, env=child_env)
                        os._exit(0)
                    else:
                        args = ' '.join(shlex.quote(a) for a in sys.argv)
                        cmd = (
                            f'lsof -ti :{PORT} | xargs kill 2>/dev/null; '
                            f'sleep 0.3; '
                            f'exec {shlex.quote(sys.executable)} {args}'
                        )
                        subprocess.Popen(['sh', '-c', cmd], close_fds=True, env=child_env)
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
