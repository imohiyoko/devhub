import os
import platform
import subprocess
import shutil
import sqlite3
import ipaddress
import json
import xml.etree.ElementTree as ET
from backend.storage import load_settings
from backend.controllers.base import sanitize_db_connection

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
    db_local_only = load_settings().get('db_local_only', True)
    if db_local_only and not is_local_db_host(host):
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

def normalize_search(value):
    if value is None:
        return ''
    return str(value).strip()[:200]

def escape_like_pattern(value):
    return str(value).replace('\\', '\\\\').replace('%', '\\%').replace('_', '\\_')

def sqlite_search_condition(columns, search):
    search = normalize_search(search)
    if not search:
        return '', []
    if not columns:
        return ' WHERE 0', []
    pattern = f'%{escape_like_pattern(search)}%'
    clauses = [
        f'CAST({quote_identifier(c["name"])} AS TEXT) LIKE ? ESCAPE \'\\\''
        for c in columns
    ]
    return ' WHERE (' + ' OR '.join(clauses) + ')', [pattern] * len(columns)

def mysql_search_condition(columns, search):
    search = normalize_search(search)
    if not search:
        return ''
    columns = searchable_columns(columns)
    if not columns:
        return ' WHERE 0'
    pattern = sql_literal(f'%{escape_like_pattern(search)}%')
    escape_sql = sql_literal('\\')
    clauses = [
        f'CAST({mysql_identifier(c["name"])} AS CHAR) LIKE {pattern} ESCAPE {escape_sql}'
        for c in columns
    ]
    return ' WHERE (' + ' OR '.join(clauses) + ')'

def searchable_columns(columns):
    skipped_types = {
        'binary', 'varbinary', 'blob', 'tinyblob', 'mediumblob', 'longblob',
        'geometry', 'point', 'linestring', 'polygon', 'multipoint',
        'multilinestring', 'multipolygon', 'geometrycollection',
    }
    return [
        c for c in columns
        if str(c.get('type') or '').lower() not in skipped_types
    ]

def matched_columns(columns, search):
    search = normalize_search(search).lower()
    if not search:
        return []
    return [
        c for c in columns
        if search in str(c.get('name') or '').lower()
        or search in str(c.get('type') or '').lower()
    ]

def row_search_sample(columns, row, search, limit=3):
    search = normalize_search(search).lower()
    if not search or not row:
        return []
    sample = []
    for col in columns:
        name = col['name']
        value = row.get(name)
        if value is None:
            continue
        normalized = normalize_sqlite_value(value)
        if search in str(normalized).lower():
            sample.append({'column': name, 'value': normalized})
            if len(sample) >= limit:
                break
    return sample

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

def db_table_names_types(profile):
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            rows = conn.execute(
                "SELECT name, type FROM sqlite_master "
                "WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' "
                "ORDER BY name"
            ).fetchall()
            return [{'name': row['name'], 'type': row['type']} for row in rows]

    rows = mysql_run(profile, (
        "SELECT TABLE_NAME AS name, TABLE_TYPE AS type "
        "FROM information_schema.TABLES "
        "WHERE TABLE_SCHEMA = DATABASE() "
        "ORDER BY TABLE_NAME"
    ))
    return [{'name': r['name'], 'type': r['type'].lower()} for r in rows]

def db_search(profile, column_search='', element_search=''):
    column_search = normalize_search(column_search)
    element_search = normalize_search(element_search)
    result = {'columnMatches': [], 'elementMatches': []}
    if not column_search and not element_search:
        return result

    tables = db_table_names_types(profile)
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            for table in tables:
                try:
                    columns = sqlite_columns(conn, table['name'])
                except (sqlite3.DatabaseError, ValueError):
                    continue

                if column_search:
                    cols = matched_columns(columns, column_search)
                    if cols:
                        result['columnMatches'].append({
                            'table': table['name'],
                            'type': table['type'],
                            'columns': cols,
                        })

                if element_search:
                    table_sql = quote_identifier(table['name'])
                    try:
                        where_sql, params = sqlite_search_condition(columns, element_search)
                        count = conn.execute(
                            f'SELECT COUNT(*) AS c FROM {table_sql}{where_sql}',
                            params,
                        ).fetchone()['c']
                        if count:
                            row = conn.execute(
                                f'SELECT * FROM {table_sql}{where_sql} LIMIT 1',
                                params,
                            ).fetchone()
                            result['elementMatches'].append({
                                'table': table['name'],
                                'type': table['type'],
                                'count': count,
                                'sample': row_search_sample(columns, dict(row) if row else {}, element_search),
                            })
                    except (sqlite3.DatabaseError, ValueError):
                        continue
        return result

    for table in tables:
        try:
            columns = mysql_columns(profile, table['name'])
        except ValueError:
            continue

        if column_search:
            cols = matched_columns(columns, column_search)
            if cols:
                result['columnMatches'].append({
                    'table': table['name'],
                    'type': table['type'],
                    'columns': cols,
                })

        if element_search:
            try:
                table_sql = mysql_identifier(table['name'])
                where_sql = mysql_search_condition(columns, element_search)
                count_rows = mysql_run(profile, f'SELECT COUNT(*) AS c FROM {table_sql}{where_sql}')
                count = int(count_rows[0]['c']) if count_rows else 0
                if count:
                    rows = mysql_run(profile, f'SELECT * FROM {table_sql}{where_sql} LIMIT 1')
                    result['elementMatches'].append({
                        'table': table['name'],
                        'type': table['type'],
                        'count': count,
                        'sample': row_search_sample(columns, rows[0] if rows else {}, element_search),
                    })
            except ValueError:
                continue
    return result

def db_rows(profile, table, limit, offset, search=''):
    if profile['driver'] == 'sqlite':
        with sqlite3.connect(profile['path']) as conn:
            conn.row_factory = sqlite3.Row
            meta = sqlite_table_meta(conn, table)
            columns = sqlite_columns(conn, table)
            table_sql = quote_identifier(table)
            where_sql, search_params = sqlite_search_condition(columns, search)
            total = conn.execute(
                f'SELECT COUNT(*) AS c FROM {table_sql}{where_sql}',
                search_params,
            ).fetchone()['c']
            rowid_available = True
            try:
                query = f'SELECT rowid AS __devhub_rowid__, * FROM {table_sql}{where_sql} LIMIT ? OFFSET ?'
                fetched = conn.execute(query, search_params + [limit, offset]).fetchall()
            except sqlite3.OperationalError:
                rowid_available = False
                fetched = conn.execute(
                    f'SELECT * FROM {table_sql}{where_sql} LIMIT ? OFFSET ?',
                    search_params + [limit, offset],
                ).fetchall()
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
    where_sql = mysql_search_condition(columns, search)
    total_rows = mysql_run(profile, f'SELECT COUNT(*) AS c FROM {table_sql}{where_sql}')
    total = int(total_rows[0]['c']) if total_rows else 0
    order_sql = ''
    if pk_columns:
        order_sql = ' ORDER BY ' + ', '.join(mysql_identifier(c) for c in pk_columns)
    fetched = mysql_run(profile, f'SELECT * FROM {table_sql}{where_sql}{order_sql} LIMIT {limit} OFFSET {offset}')
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

def handle_get(handler, path, params):
    if path == '/api/db/tables':
        profile = connection_from_payload({'path': params.get('path', [None])[0]})
        handler.send_json({'connection': sanitize_db_connection(profile), 'tables': db_tables(profile)})
        return

    if path == '/api/db/rows':
        profile = connection_from_payload({'path': params.get('path', [None])[0]})
        table = params.get('table', [None])[0]
        limit = min(max(int(params.get('limit', [100])[0]), 1), 500)
        offset = max(int(params.get('offset', [0])[0]), 0)
        search = params.get('search', [''])[0]
        data = db_rows(profile, table, limit, offset, search)
        data['connection'] = sanitize_db_connection(profile)
        handler.send_json(data)
        return

    handler.send_json({'error': 'not found'}, 404)

def handle_post(handler, path, data):
    if path == '/api/db/tables':
        profile = connection_from_payload(data)
        handler.send_json({'connection': sanitize_db_connection(profile), 'tables': db_tables(profile)})
        return

    if path == '/api/db/rows':
        profile = connection_from_payload(data)
        table = data.get('table')
        limit = min(max(int(data.get('limit', 100)), 1), 500)
        offset = max(int(data.get('offset', 0)), 0)
        search = data.get('search', '')
        res = db_rows(profile, table, limit, offset, search)
        res['connection'] = sanitize_db_connection(profile)
        handler.send_json(res)
        return

    if path == '/api/db/search':
        profile = connection_from_payload(data)
        col_search = data.get('columnSearch', '')
        elem_search = data.get('elementSearch', '')
        handler.send_json(db_search(profile, col_search, elem_search))
        return

    if path == '/api/db/update':
        profile = connection_from_payload(data)
        table = data.get('table')
        column = data.get('column')
        key = data.get('key')
        value = data.get('value')
        db_update(profile, table, column, key, value)
        handler.send_json({'ok': True})
        return

    if path == '/api/db/insert':
        profile = connection_from_payload(data)
        table = data.get('table')
        last_rowid = db_insert(profile, table)
        handler.send_json({'ok': True, 'lastrowid': last_rowid})
        return

    if path == '/api/db/delete':
        profile = connection_from_payload(data)
        table = data.get('table')
        key = data.get('key')
        db_delete(profile, table, key)
        handler.send_json({'ok': True})
        return

    handler.send_json({'error': 'not found'}, 404)
