#!/usr/bin/env python3
import ipaddress, json, os, platform, re, shlex, shutil, signal, sqlite3, subprocess, sys, threading, time, webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs
import xml.etree.ElementTree as ET

BASE         = os.path.dirname(os.path.abspath(__file__))
SETTINGS_DIR = os.path.join(BASE, 'settings')
CONFIG_PATH  = os.path.join(SETTINGS_DIR, 'config.json')
CONFIG_EXAMPLE_PATH = os.path.join(SETTINGS_DIR, 'config.example.json')


# ── Settings (settings.json + settings.local.json) ──────────────────────────

def load_settings():
    defaults = {'port': 8765, 'editor': 'code', 'open_browser_on_start': True, 'protected_ports': [], 'db_local_only': True}
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

SECRET_KEYS = {'password', 'secret', 'apiKey', 'api_key', 'token'}


def sanitize_db_connection(profile):
    if not isinstance(profile, dict):
        return profile
    return {k: v for k, v in profile.items() if k not in SECRET_KEYS}


def sanitize_settings(settings):
    sanitized = dict(settings)
    if isinstance(sanitized.get('db_connections'), list):
        sanitized['db_connections'] = [
            sanitize_db_connection(profile)
            for profile in sanitized['db_connections']
            if isinstance(profile, dict)
        ]
    return sanitized

SETTINGS = load_settings()
PORT     = SETTINGS['port']
EDITOR   = SETTINGS['editor']
DB_LOCAL_ONLY = SETTINGS.get('db_local_only', True)


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
    '/db-table':    os.path.join(BASE, 'tools', 'db-table', 'index.html'),
    '/db-table/':   os.path.join(BASE, 'tools', 'db-table', 'index.html'),
    '/ports':       os.path.join(BASE, 'tools', 'ports', 'index.html'),
    '/ports/':      os.path.join(BASE, 'tools', 'ports', 'index.html'),
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


# ── SQLite table editor ───────────────────────────────────────────────────────

def sqlite_db_path(raw_path):
    if not raw_path:
        raise ValueError('database path is required')
    path = os.path.abspath(os.path.expanduser(raw_path))
    if not os.path.isfile(path):
        raise ValueError('database file was not found')
    return path


def is_local_db_host(host):
    normalized = str(host or '').strip().lower()
    if normalized in ('localhost', 'localhost.localdomain'):
        return True
    try:
        return ipaddress.ip_address(normalized).is_loopback
    except ValueError:
        return False


def ensure_local_db_host(host):
    if DB_LOCAL_ONLY and not is_local_db_host(host):
        raise ValueError('external database hosts are disabled; use localhost, 127.0.0.1, or ::1')


def quote_identifier(name):
    if not isinstance(name, str) or not name or '\x00' in name:
        raise ValueError('invalid identifier')
    return '"' + name.replace('"', '""') + '"'


def normalize_sqlite_value(value):
    if isinstance(value, bytes):
        return '0x' + value.hex()
    return value


def sqlite_table_meta(conn, table_name):
    row = conn.execute(
        "SELECT name, type FROM sqlite_master WHERE name = ? AND type IN ('table', 'view')",
        (table_name,),
    ).fetchone()
    if row is None:
        raise ValueError('table was not found')
    return {'name': row['name'], 'type': row['type']}


def sqlite_columns(conn, table_name):
    rows = conn.execute(f'PRAGMA table_info({quote_identifier(table_name)})').fetchall()
    return [{
        'name': r['name'],
        'type': r['type'],
        'notnull': bool(r['notnull']),
        'default': r['dflt_value'],
        'pk': int(r['pk']),
    } for r in rows]


def sqlite_writable_columns(conn, table_name):
    return {c['name'] for c in sqlite_columns(conn, table_name)}


def connection_from_payload(data):
    profile = data.get('connection') if isinstance(data, dict) else None
    if not profile:
        profile = {}
    if data.get('path') and not profile.get('driver'):
        profile = {'driver': 'sqlite', 'path': data.get('path')}

    driver = (profile.get('driver') or 'sqlite').lower()
    if driver == 'mariadb':
        driver = 'mysql'
    if driver not in ('sqlite', 'mysql'):
        raise ValueError('unsupported database driver')

    normalized = dict(profile)
    normalized['driver'] = driver
    if driver == 'sqlite':
        normalized['path'] = sqlite_db_path(normalized.get('path'))
    else:
        normalized['host'] = normalized.get('host') or '127.0.0.1'
        ensure_local_db_host(normalized['host'])
        normalized['port'] = int(normalized.get('port') or 3306)
        normalized['user'] = normalized.get('user') or ''
        normalized['password'] = normalized.get('password') or ''
        normalized['database'] = normalized.get('database') or ''
        if not normalized['database']:
            raise ValueError('database name is required')
    return normalized


def public_connection(profile):
    return sanitize_db_connection(profile)


def sql_literal(value):
    if value is None:
        return 'NULL'
    return "'" + str(value).replace('\\', '\\\\').replace("'", "''") + "'"


def mysql_identifier(name):
    if not isinstance(name, str) or not name or '\x00' in name:
        raise ValueError('invalid identifier')
    return '`' + name.replace('`', '``') + '`'


def mysql_run(profile, sql):
    if shutil.which('mysql') is None:
        raise ValueError('mysql command was not found')

    args = [
        'mysql',
        '--xml',
        '--default-character-set=utf8mb4',
        '--protocol=TCP',
        '--connect-timeout=5',
        '-h', profile['host'],
        '-P', str(profile['port']),
        '-u', profile['user'],
    ]
    args.extend(['-e', sql])
    if profile.get('database'):
        args.append(profile['database'])

    env = os.environ.copy()
    if profile.get('password'):
        env['MYSQL_PWD'] = profile['password']

    proc = subprocess.run(
        args,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        timeout=30,
    )
    if proc.returncode != 0:
        msg = proc.stderr.strip() or proc.stdout.strip() or 'mysql command failed'
        raise ValueError(msg)
    return parse_mysql_xml(proc.stdout)


def parse_mysql_xml(output):
    output = output.strip()
    if not output:
        return []
    start = output.find('<?xml')
    if start > 0:
        output = output[start:]
    root = ET.fromstring(output)
    resultsets = [root] if root.tag == 'resultset' else root.findall('resultset')
    rows = []
    for resultset in resultsets:
        for row in resultset.findall('row'):
            item = {}
            for field in row.findall('field'):
                name = field.attrib.get('name')
                is_null = any(k.endswith('nil') and v == 'true' for k, v in field.attrib.items())
                item[name] = None if is_null else (field.text or '')
            rows.append(item)
    return rows


def mysql_columns(profile, table_name):
    rows = mysql_run(profile, (
        "SELECT COLUMN_NAME AS name, DATA_TYPE AS type, IS_NULLABLE AS nullable, "
        "COLUMN_DEFAULT AS dflt, COLUMN_KEY AS column_key, EXTRA AS extra "
        "FROM information_schema.COLUMNS "
        f"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = {sql_literal(table_name)} "
        "ORDER BY ORDINAL_POSITION"
    ))
    if not rows:
        raise ValueError('table was not found')
    return [{
        'name': r['name'],
        'type': r.get('type') or '',
        'notnull': r.get('nullable') == 'NO',
        'default': r.get('dflt'),
        'pk': 1 if r.get('column_key') == 'PRI' else 0,
        'extra': r.get('extra') or '',
    } for r in rows]


def table_key_for_row(row, pk_columns):
    return {name: row.get(name) for name in pk_columns}


def db_tables(profile):
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            rows = conn.execute(
                "SELECT name, type FROM sqlite_master "
                "WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' "
                "ORDER BY name"
            ).fetchall()
            tables = []
            for row in rows:
                table_sql = quote_identifier(row['name'])
                count = conn.execute(f'SELECT COUNT(*) AS c FROM {table_sql}').fetchone()['c']
                tables.append({'name': row['name'], 'type': row['type'], 'count': count})
            return tables

    rows = mysql_run(profile, (
        "SELECT TABLE_NAME AS name, TABLE_TYPE AS type, COALESCE(TABLE_ROWS, 0) AS count "
        "FROM information_schema.TABLES "
        "WHERE TABLE_SCHEMA = DATABASE() "
        "ORDER BY TABLE_NAME"
    ))
    return [{'name': r['name'], 'type': r['type'].lower(), 'count': int(r.get('count') or 0)} for r in rows]


def db_rows(profile, table, limit, offset):
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            meta = sqlite_table_meta(conn, table)
            columns = sqlite_columns(conn, table)
            table_sql = quote_identifier(table)
            total = conn.execute(f'SELECT COUNT(*) AS c FROM {table_sql}').fetchone()['c']
            rowid_available = True
            try:
                query = f'SELECT rowid AS __devhub_rowid__, * FROM {table_sql} LIMIT ? OFFSET ?'
                fetched = conn.execute(query, (limit, offset)).fetchall()
            except sqlite3.OperationalError:
                rowid_available = False
                fetched = conn.execute(f'SELECT * FROM {table_sql} LIMIT ? OFFSET ?', (limit, offset)).fetchall()
            rows = []
            for row in fetched:
                item = {key: normalize_sqlite_value(row[key]) for key in row.keys()}
                if rowid_available:
                    item['__devhub_key__'] = {'rowid': item.get('__devhub_rowid__')}
                rows.append(item)
            return {
                'table': meta,
                'columns': columns,
                'rows': rows,
                'total': total,
                'limit': limit,
                'offset': offset,
                'editable': meta['type'] == 'table' and rowid_available,
                'keyColumns': ['rowid'] if rowid_available else [],
            }

    columns = mysql_columns(profile, table)
    pk_columns = [c['name'] for c in columns if c['pk']]
    table_sql = mysql_identifier(table)
    total_rows = mysql_run(profile, f'SELECT COUNT(*) AS c FROM {table_sql}')
    total = int(total_rows[0]['c']) if total_rows else 0
    order_sql = ''
    if pk_columns:
        order_sql = ' ORDER BY ' + ', '.join(mysql_identifier(c) for c in pk_columns)
    fetched = mysql_run(profile, f'SELECT * FROM {table_sql}{order_sql} LIMIT {limit} OFFSET {offset}')
    rows = []
    for row in fetched:
        item = dict(row)
        item['__devhub_key__'] = table_key_for_row(row, pk_columns) if pk_columns else {}
        rows.append(item)
    return {
        'table': {'name': table, 'type': 'table'},
        'columns': columns,
        'rows': rows,
        'total': total,
        'limit': limit,
        'offset': offset,
        'editable': bool(pk_columns),
        'keyColumns': pk_columns,
    }


def db_update(profile, table, column, key, value):
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            meta = sqlite_table_meta(conn, table)
            if meta['type'] != 'table':
                raise ValueError('only tables can be edited')
            columns = sqlite_columns(conn, table)
            column_names = {c['name'] for c in columns}
            pk_columns = {c['name'] for c in columns if c['pk']}
            if column not in column_names:
                raise ValueError('column was not found')
            if column in pk_columns:
                raise ValueError('primary key columns cannot be edited')
            rowid = key.get('rowid') if isinstance(key, dict) else key
            sql = f'UPDATE {quote_identifier(table)} SET {quote_identifier(column)} = ? WHERE rowid = ?'
            cur = conn.execute(sql, (value, rowid))
            if cur.rowcount == 0:
                raise ValueError('row was not found')
        return

    columns = mysql_columns(profile, table)
    column_names = {c['name'] for c in columns}
    pk_columns = [c['name'] for c in columns if c['pk']]
    if column not in column_names:
        raise ValueError('column was not found')
    if not pk_columns:
        raise ValueError('table has no primary key')
    if column in pk_columns:
        raise ValueError('primary key columns cannot be edited')
    where = ' AND '.join(f'{mysql_identifier(c)} = {sql_literal(key.get(c))}' for c in pk_columns)
    mysql_run(profile, (
        f'UPDATE {mysql_identifier(table)} SET {mysql_identifier(column)} = {sql_literal(value)} '
        f'WHERE {where}'
    ))


def db_insert(profile, table):
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            meta = sqlite_table_meta(conn, table)
            if meta['type'] != 'table':
                raise ValueError('only tables can be edited')
            cur = conn.execute(f'INSERT INTO {quote_identifier(table)} DEFAULT VALUES')
            return cur.lastrowid

    mysql_columns(profile, table)
    mysql_run(profile, f'INSERT INTO {mysql_identifier(table)} () VALUES ()')
    return None


def db_delete(profile, table, key):
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            meta = sqlite_table_meta(conn, table)
            if meta['type'] != 'table':
                raise ValueError('only tables can be edited')
            rowid = key.get('rowid') if isinstance(key, dict) else key
            cur = conn.execute(f'DELETE FROM {quote_identifier(table)} WHERE rowid = ?', (rowid,))
            if cur.rowcount == 0:
                raise ValueError('row was not found')
        return

    columns = mysql_columns(profile, table)
    pk_columns = [c['name'] for c in columns if c['pk']]
    if not pk_columns:
        raise ValueError('table has no primary key')
    where = ' AND '.join(f'{mysql_identifier(c)} = {sql_literal(key.get(c))}' for c in pk_columns)
    mysql_run(profile, f'DELETE FROM {mysql_identifier(table)} WHERE {where}')


# ── Port manager ──────────────────────────────────────────────────────────────

def port_labels():
    labels = load_settings().get('port_labels', {})
    return labels if isinstance(labels, dict) else {}


def normalize_port_list(value, strict=False):
    if not isinstance(value, list):
        if strict:
            raise ValueError('ports must be a list')
        return []
    ports = []
    seen = set()
    for item in value:
        if isinstance(item, bool):
            port = None
        else:
            try:
                port = int(str(item).strip())
            except (TypeError, ValueError):
                port = None
        if port is None or port < 1 or port > 65535:
            if strict:
                raise ValueError('ports must be integers from 1 to 65535')
            continue
        if port in seen:
            continue
        seen.add(port)
        ports.append(port)
    return sorted(ports)


def protected_ports():
    return normalize_port_list(load_settings().get('protected_ports', []))


def save_protected_ports(ports):
    normalized = normalize_port_list(ports, strict=True)
    save_settings({'protected_ports': normalized})
    return normalized


def parse_port_name(name):
    match = re.search(r'(?:TCP\s+)?(.+):(\d+)\s+\(LISTEN\)$', name)
    if not match:
        return None
    host = match.group(1)
    return {'host': host, 'port': int(match.group(2))}


def list_ports_unix():
    proc = subprocess.run(
        ['lsof', '-nP', '-iTCP', '-sTCP:LISTEN'],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if proc.returncode not in (0, 1):
        raise ValueError(proc.stderr.strip() or 'failed to list ports')

    ports = []
    for line in proc.stdout.splitlines()[1:]:
        parts = line.split()
        if len(parts) < 9:
            continue
        parsed = parse_port_name(' '.join(parts[7:]))
        if not parsed:
            continue
        ports.append({
            'command': parts[0].replace('\\x20', ' '),
            'pid': int(parts[1]),
            'user': parts[2],
            'host': parsed['host'],
            'port': parsed['port'],
        })
    return ports


def list_ports_windows():
    proc = subprocess.run(
        ['netstat', '-ano', '-p', 'tcp'],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if proc.returncode != 0:
        raise ValueError(proc.stderr.strip() or 'failed to list ports')

    ports = []
    for line in proc.stdout.splitlines():
        parts = line.split()
        if len(parts) < 5 or parts[0].upper() != 'TCP' or parts[3].upper() != 'LISTENING':
            continue
        address = parts[1]
        if ':' not in address:
            continue
        host, port = address.rsplit(':', 1)
        try:
            pid = int(parts[4])
            port_num = int(port)
        except ValueError:
            continue
        ports.append({
            'command': '',
            'pid': pid,
            'user': '',
            'host': host,
            'port': port_num,
        })
    return ports


def list_open_ports():
    ports = list_ports_windows() if platform.system() == 'Windows' else list_ports_unix()
    labels = port_labels()
    protected = set(protected_ports())
    for item in ports:
        item['label'] = labels.get(str(item['port']), '')
        item['self'] = item['pid'] == os.getpid()
        item['protected'] = item['port'] in protected
    ports.sort(key=lambda item: (item['port'], item['pid']))
    return ports


def save_port_label(port, label):
    labels = port_labels()
    port_key = str(int(port))
    label = str(label or '').strip()
    if label:
        labels[port_key] = label
    else:
        labels.pop(port_key, None)
    save_settings({'port_labels': labels})


def kill_port_process(port, pid):
    port = int(port)
    pid = int(pid)
    if port in set(protected_ports()):
        raise ValueError(f'port {port} is protected')
    if pid == os.getpid():
        raise ValueError('devhub itself cannot be killed from this tool')
    matches = [p for p in list_open_ports() if p['port'] == port and p['pid'] == pid]
    if not matches:
        raise ValueError('port process was not found')
    if platform.system() == 'Windows':
        proc = subprocess.run(['taskkill', '/PID', str(pid), '/F'], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        if proc.returncode != 0:
            raise ValueError(proc.stderr.strip() or proc.stdout.strip() or 'taskkill failed')
    else:
        os.kill(pid, signal.SIGTERM)


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
            self.send_json(sanitize_settings(load_settings()))
            return

        if path == '/api/info':
            self.send_json({'base': BASE, 'port': PORT})
            return

        if path == '/api/db/tables':
            try:
                profile = connection_from_payload({'path': params.get('path', [None])[0]})
                self.send_json({'connection': public_connection(profile), 'tables': db_tables(profile)})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/db/rows':
            try:
                profile = connection_from_payload({'path': params.get('path', [None])[0]})
                table = params.get('table', [None])[0]
                limit = min(max(int(params.get('limit', [100])[0]), 1), 500)
                offset = max(int(params.get('offset', [0])[0]), 0)
                data = db_rows(profile, table, limit, offset)
                data['connection'] = public_connection(profile)
                self.send_json(data)
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/ports':
            try:
                self.send_json({'ports': list_open_ports(), 'protected_ports': protected_ports()})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
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
                allowed = {'disabled_tools', 'tool_order', 'editor', 'open_browser_on_start', 'db_connections', 'port_labels', 'protected_ports'}
                patch = {k: v for k, v in data.items() if k in allowed}
                if isinstance(patch.get('db_connections'), list):
                    patch['db_connections'] = [
                        sanitize_db_connection(profile)
                        for profile in patch['db_connections']
                        if isinstance(profile, dict)
                    ]
                if 'protected_ports' in patch:
                    patch['protected_ports'] = normalize_port_list(patch['protected_ports'], strict=True)
                save_settings(patch)
                self.send_json({'ok': True})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/ports/label':
            try:
                data = json.loads(self.read_body())
                save_port_label(data.get('port'), data.get('label', ''))
                self.send_json({'ok': True})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/ports/protected':
            try:
                data = json.loads(self.read_body())
                ports = save_protected_ports(data.get('ports', []))
                self.send_json({'ok': True, 'protected_ports': ports})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/ports/kill':
            try:
                data = json.loads(self.read_body())
                kill_port_process(data.get('port'), data.get('pid'))
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

        if path == '/api/db/tables':
            try:
                data = json.loads(self.read_body())
                profile = connection_from_payload(data)
                self.send_json({'connection': public_connection(profile), 'tables': db_tables(profile)})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/db/rows':
            try:
                data = json.loads(self.read_body())
                profile = connection_from_payload(data)
                table = data.get('table')
                limit = min(max(int(data.get('limit', 100)), 1), 500)
                offset = max(int(data.get('offset', 0)), 0)
                result = db_rows(profile, table, limit, offset)
                result['connection'] = public_connection(profile)
                self.send_json(result)
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/db/update':
            try:
                data = json.loads(self.read_body())
                profile = connection_from_payload(data)
                key = data.get('key') or {'rowid': data.get('rowid')}
                db_update(profile, data.get('table'), data.get('column'), key, data.get('value'))
                self.send_json({'ok': True})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/db/insert':
            try:
                data = json.loads(self.read_body())
                profile = connection_from_payload(data)
                inserted_id = db_insert(profile, data.get('table'))
                self.send_json({'ok': True, 'rowid': inserted_id})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/db/delete':
            try:
                data = json.loads(self.read_body())
                profile = connection_from_payload(data)
                key = data.get('key') or {'rowid': data.get('rowid')}
                db_delete(profile, data.get('table'), key)
                self.send_json({'ok': True})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
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
