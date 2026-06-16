import json
import os

BASE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SETTINGS_DIR = os.path.join(BASE, 'settings')
CONFIG_PATH = os.path.join(SETTINGS_DIR, 'config.json')
CONFIG_EXAMPLE_PATH = os.path.join(SETTINGS_DIR, 'config.example.json')
ENVS_PATH = os.path.join(SETTINGS_DIR, 'envs.json')
ENVS_EXAMPLE_PATH = os.path.join(SETTINGS_DIR, 'envs.example.json')
TOOLS_SETTINGS_DIR = os.path.join(SETTINGS_DIR, 'tools')

def load_settings():
    defaults = {'port': 8765, 'editor': 'code', 'open_browser_on_start': True, 'protected_ports': [], 'db_local_only': True, 'terminal': {}}
    for name in ('server.example.json', 'server.json'):
        try:
            with open(os.path.join(SETTINGS_DIR, name), encoding='utf-8') as f:
                defaults.update(json.load(f))
        except FileNotFoundError:
            pass
    return defaults

def save_settings(patch):
    path = os.path.join(SETTINGS_DIR, 'server.json')
    current = {}
    try:
        with open(path, encoding='utf-8') as f:
            current = json.load(f)
    except FileNotFoundError:
        pass
    current.update(patch)
    tmp = path + '.tmp'
    with open(tmp, 'w', encoding='utf-8') as f:
        json.dump(current, f, indent=2, ensure_ascii=False)
        f.write('\n')
    os.replace(tmp, path)

def load_tool_settings(tool_id: str) -> dict:
    path = os.path.join(TOOLS_SETTINGS_DIR, f'{tool_id}.json')
    try:
        with open(path, encoding='utf-8') as f:
            return json.load(f)
    except FileNotFoundError:
        return {}

def save_tool_settings(tool_id: str, data: dict) -> None:
    os.makedirs(TOOLS_SETTINGS_DIR, exist_ok=True)
    path = os.path.join(TOOLS_SETTINGS_DIR, f'{tool_id}.json')
    tmp = path + '.tmp'
    with open(tmp, 'w', encoding='utf-8') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write('\n')
    os.replace(tmp, path)

def load_config():
    try:
        with open(CONFIG_PATH, encoding='utf-8') as f:
            return json.load(f)
    except FileNotFoundError:
        # first run: copy from example if available
        try:
            with open(CONFIG_EXAMPLE_PATH, encoding='utf-8') as f:
                cfg = json.load(f)
            save_config(cfg)
            return cfg
        except FileNotFoundError:
            pass
    except Exception:
        pass
    return {'scan_roots': [], 'excludes': [], 'pinned_repos': [], 'repo_order': [], 'hidden_repos': []}

def save_config(cfg):
    os.makedirs(SETTINGS_DIR, exist_ok=True)
    tmp = CONFIG_PATH + '.tmp'
    with open(tmp, 'w', encoding='utf-8') as f:
        json.dump(cfg, f, indent=2, ensure_ascii=False)
        f.write('\n')
    os.replace(tmp, CONFIG_PATH)

def load_envs():
    try:
        with open(ENVS_PATH, encoding='utf-8') as f:
            return json.load(f)
    except FileNotFoundError:
        try:
            with open(ENVS_EXAMPLE_PATH, encoding='utf-8') as f:
                envs = json.load(f)
            save_envs(envs)
            return envs
        except FileNotFoundError:
            pass
    except Exception:
        pass
    return {}

def save_envs(data):
    os.makedirs(SETTINGS_DIR, exist_ok=True)
    tmp = ENVS_PATH + '.tmp'
    with open(tmp, 'w', encoding='utf-8') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write('\n')
    os.replace(tmp, ENVS_PATH)
