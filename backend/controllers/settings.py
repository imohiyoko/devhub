import re
from backend.storage import (
    load_config, save_config,
    load_settings, save_settings,
    load_tool_settings, save_tool_settings
)
from backend.controllers.base import sanitize_settings, sanitize_db_connection
from backend.controllers.ports import normalize_port_list

def handle_get(handler, path):
    if path == '/api/config':
        handler.send_json(load_config())
        return

    if path == '/api/settings':
        handler.send_json(sanitize_settings(load_settings()))
        return

    if path.startswith('/api/settings/tool/'):
        tool_id = path.split('/')[-1]
        if not re.fullmatch(r'[a-z0-9_-]+', tool_id):
            handler.send_json({'error': 'invalid tool_id'}, 400)
            return
        handler.send_json(load_tool_settings(tool_id))
        return

    handler.send_json({'error': 'not found'}, 404)

def handle_post(handler, path, data):
    if path == '/api/config':
        cfg = load_config()
        for key in ('scan_roots', 'excludes', 'pinned_repos', 'repo_order', 'hidden_repos'):
            if key in data:
                cfg[key] = data[key]
        save_config(cfg)
        handler.send_json({'ok': True})
        return

    if path == '/api/settings':
        allowed = {'disabled_tools', 'tool_order', 'editor', 'open_browser_on_start', 'db_connections', 'port_labels', 'protected_ports', 'terminal'}
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
        handler.send_json({'ok': True})
        return

    if path.startswith('/api/settings/tool/'):
        tool_id = path.split('/')[-1]
        if not re.fullmatch(r'[a-z0-9_-]+', tool_id):
            handler.send_json({'error': 'invalid tool_id'}, 400)
            return
        if not isinstance(data, dict):
            handler.send_json({'error': 'invalid'}, 400)
            return
        save_tool_settings(tool_id, data)
        handler.send_json({'ok': True})
        return

    handler.send_json({'error': 'not found'}, 404)
