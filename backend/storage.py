import json
import os
import sqlite3
import sys
from contextlib import closing
from datetime import datetime

BASE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SETTINGS_DIR = os.path.join(BASE, 'settings')
CONFIG_PATH = os.path.join(SETTINGS_DIR, 'config.json')
CONFIG_EXAMPLE_PATH = os.path.join(SETTINGS_DIR, 'config.example.json')
ENVS_PATH = os.path.join(SETTINGS_DIR, 'envs.json')
ENVS_EXAMPLE_PATH = os.path.join(SETTINGS_DIR, 'envs.example.json')
LAUNCHES_PATH = os.path.join(SETTINGS_DIR, 'launches.json')
SERVER_PATH = os.path.join(SETTINGS_DIR, 'server.json')
SERVER_EXAMPLE_PATH = os.path.join(SETTINGS_DIR, 'server.example.json')
TOOLS_SETTINGS_DIR = os.path.join(SETTINGS_DIR, 'tools')

# Single SQLite file backs all app state (config / settings / envs / launches /
# tool settings). It is local runtime state, gitignored via *.db. Config-shaped
# documents live as JSON in the `kv` table (whole-document round-trip, mirroring
# the old JSON files). Launches use one row per record (so a record carries its
# own JSON), but save_launches() replaces the whole table in a single
# transaction; callers still serialize load->mutate->save under _REGISTRY_LOCK.
DB_PATH = os.path.join(SETTINGS_DIR, 'devhub.db')

# DB paths that have already had init_db()+migration applied this process, keyed
# by path so tests pointing DB_PATH at a temp file get re-initialized.
_initialized = set()


def _now():
    return datetime.now().astimezone().isoformat(timespec='seconds')


def _conn():
    os.makedirs(os.path.dirname(DB_PATH), exist_ok=True)
    conn = sqlite3.connect(DB_PATH, timeout=5.0)
    conn.row_factory = sqlite3.Row
    conn.execute('PRAGMA journal_mode=WAL')
    conn.execute('PRAGMA foreign_keys=ON')
    conn.execute('PRAGMA busy_timeout=5000')
    return conn


def init_db():
    with closing(_conn()) as conn:
        with conn:
            conn.execute(
                'CREATE TABLE IF NOT EXISTS kv ('
                ' key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT)'
            )
            conn.execute(
                'CREATE TABLE IF NOT EXISTS meta ('
                ' key TEXT PRIMARY KEY, value TEXT)'
            )
            conn.execute(
                'CREATE TABLE IF NOT EXISTS launches ('
                ' launch_id TEXT PRIMARY KEY,'
                ' data TEXT NOT NULL,'
                ' launched_at TEXT)'
            )


def _ensure_db():
    """Initialize (and one-time migrate) the DB at the current DB_PATH."""
    if DB_PATH in _initialized:
        return
    init_db()
    try:
        migrate_json_to_sqlite()
    except Exception as e:
        # Migration is best-effort: a failure must not block app startup. Surface
        # it to stderr and return WITHOUT marking the DB initialized, so a later
        # call retries (the migration is idempotent and rolls back on failure).
        print(f"storage: JSON->SQLite migration failed: {e}", file=sys.stderr)
        return
    _initialized.add(DB_PATH)


# --- low-level KV helpers ----------------------------------------------------

def _kv_get(conn, key):
    row = conn.execute('SELECT value FROM kv WHERE key = ?', (key,)).fetchone()
    return json.loads(row['value']) if row else None


def _kv_set(conn, key, value):
    conn.execute(
        'INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?) '
        'ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at',
        (key, json.dumps(value, ensure_ascii=False), _now()),
    )


def _read_json_file(path):
    with open(path, encoding='utf-8') as f:
        return json.load(f)


# --- one-time JSON -> SQLite migration --------------------------------------

def migrate_json_to_sqlite():
    """Best-effort import of legacy settings/*.json on first run. Idempotent via
    the meta.migrated flag. Does not delete or rename the source files."""
    with closing(_conn()) as conn:
        done = conn.execute("SELECT value FROM meta WHERE key = 'migrated'").fetchone()
        if done:
            return
        # 1) Config-shaped state (config/settings/envs/tools) + the migrated flag
        #    commit together. Launches are imported separately below so one bad
        #    launch record can't roll back the config migration.
        with conn:
            for key, path in (
                ('config', CONFIG_PATH),
                ('settings', SERVER_PATH),
                ('envs', ENVS_PATH),
            ):
                try:
                    _kv_set(conn, key, _read_json_file(path))
                except (FileNotFoundError, OSError, json.JSONDecodeError):
                    pass
            try:
                for name in os.listdir(TOOLS_SETTINGS_DIR):
                    if name.endswith('.json') and not name.endswith('.example.json'):
                        try:
                            _kv_set(conn, 'tool:' + name[:-len('.json')],
                                    _read_json_file(os.path.join(TOOLS_SETTINGS_DIR, name)))
                        except (OSError, json.JSONDecodeError):
                            pass
            except OSError:
                pass
            conn.execute("INSERT OR REPLACE INTO meta (key, value) VALUES ('migrated', '1')")

        # 2) Launches: separate transaction, skip individual bad records so one
        #    malformed entry doesn't drop the rest.
        try:
            legacy = _read_json_file(LAUNCHES_PATH)
            launches = legacy.get('launches', []) if isinstance(legacy, dict) else []
        except (FileNotFoundError, OSError, json.JSONDecodeError):
            launches = []
        with conn:
            for rec in launches:
                if not isinstance(rec, dict) or not rec.get('launch_id'):
                    continue
                try:
                    conn.execute(
                        'INSERT OR REPLACE INTO launches (launch_id, data, launched_at) VALUES (?, ?, ?)',
                        (rec.get('launch_id'), json.dumps(rec, ensure_ascii=False), rec.get('launched_at')),
                    )
                except sqlite3.DatabaseError:
                    continue


# --- settings (server.json equivalent) --------------------------------------

def load_settings():
    _ensure_db()
    defaults = {'port': 8765, 'editor': 'code', 'open_browser_on_start': True,
                'protected_ports': [], 'db_local_only': True, 'terminal': {}}
    # Committed example file is the base layer (defaults <- example <- stored).
    try:
        defaults.update(_read_json_file(SERVER_EXAMPLE_PATH))
    except (FileNotFoundError, OSError, json.JSONDecodeError):
        pass
    with closing(_conn()) as conn:
        stored = _kv_get(conn, 'settings')
    if isinstance(stored, dict):
        defaults.update(stored)
    return defaults


def save_settings(patch):
    _ensure_db()
    with closing(_conn()) as conn:
        with conn:
            current = _kv_get(conn, 'settings') or {}
            current.update(patch)
            _kv_set(conn, 'settings', current)


# --- per-tool settings -------------------------------------------------------

def load_tool_settings(tool_id: str) -> dict:
    _ensure_db()
    with closing(_conn()) as conn:
        stored = _kv_get(conn, 'tool:' + tool_id)
    return stored if isinstance(stored, dict) else {}


def save_tool_settings(tool_id: str, data: dict) -> None:
    _ensure_db()
    with closing(_conn()) as conn:
        with conn:
            _kv_set(conn, 'tool:' + tool_id, data)


# --- git tool config (config.json equivalent) -------------------------------

def load_config():
    _ensure_db()
    with closing(_conn()) as conn:
        stored = _kv_get(conn, 'config')
    if stored is not None:
        return stored
    # First run: seed from the committed example if present.
    try:
        cfg = _read_json_file(CONFIG_EXAMPLE_PATH)
        save_config(cfg)
        return cfg
    except (FileNotFoundError, OSError, json.JSONDecodeError):
        pass
    return {'scan_roots': [], 'excludes': [], 'pinned_repos': [], 'repo_order': [], 'hidden_repos': []}


def save_config(cfg):
    _ensure_db()
    with closing(_conn()) as conn:
        with conn:
            _kv_set(conn, 'config', cfg)


# --- environments (envs.json equivalent) ------------------------------------

def load_envs():
    _ensure_db()
    with closing(_conn()) as conn:
        stored = _kv_get(conn, 'envs')
    if stored is not None:
        return stored
    try:
        envs = _read_json_file(ENVS_EXAMPLE_PATH)
        save_envs(envs)
        return envs
    except (FileNotFoundError, OSError, json.JSONDecodeError):
        pass
    return {}


def save_envs(data):
    _ensure_db()
    with closing(_conn()) as conn:
        with conn:
            _kv_set(conn, 'envs', data)


# --- launch registry ---------------------------------------------------------

def load_launches():
    """Runtime registry of environments launched via env-launcher. Returns the
    same {'launches': [...]} shape the JSON file used, reconstructed from rows."""
    _ensure_db()
    with closing(_conn()) as conn:
        rows = conn.execute(
            'SELECT data FROM launches ORDER BY launched_at, rowid'
        ).fetchall()
    launches = []
    for row in rows:
        try:
            launches.append(json.loads(row['data']))
        except json.JSONDecodeError as err:
            raise ValueError('failed to decode launch record') from err
    return {'launches': launches}


def save_launches(data):
    _ensure_db()
    launches = data.get('launches', []) if isinstance(data, dict) else []
    with closing(_conn()) as conn:
        with conn:
            conn.execute('DELETE FROM launches')
            for rec in launches:
                conn.execute(
                    'INSERT OR REPLACE INTO launches (launch_id, data, launched_at) VALUES (?, ?, ?)',
                    (rec.get('launch_id'), json.dumps(rec, ensure_ascii=False), rec.get('launched_at')),
                )


# Initialize the default DB at import so server.py's module-level load_settings()
# (and the first request) operate on a ready database.
_ensure_db()
