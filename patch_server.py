import re

with open("server.py", "r") as f:
    content = f.read()

# 1. Add ROUTES
routes_pattern = r"(\'/ports/\':\s*os\.path\.join\(BASE, \'tools\', \'ports\', \'index\.html\'\),)"
routes_replacement = r"\1\n    '/git':       os.path.join(BASE, 'tools', 'git', 'index.html'),\n    '/git/':      os.path.join(BASE, 'tools', 'git', 'index.html'),"
content = re.sub(routes_pattern, routes_replacement, content)

# 2. Add TOOLS_SETTINGS_DIR and helpers
save_settings_pattern = r"(def save_settings\(patch\):[\s\S]*?json\.dump\(current, f, indent=2, ensure_ascii=False\)\n        f\.write\('\\n'\))"
helpers = """

TOOLS_SETTINGS_DIR = os.path.join(SETTINGS_DIR, 'tools')

def load_tool_settings(tool_id: str) -> dict:
    \"\"\"settings/tools/<tool_id>.json を読み込む。なければ空dictを返す。\"\"\"
    os.makedirs(TOOLS_SETTINGS_DIR, exist_ok=True)
    path = os.path.join(TOOLS_SETTINGS_DIR, f'{tool_id}.json')
    try:
        with open(path) as f:
            return json.load(f)
    except FileNotFoundError:
        return {}

def save_tool_settings(tool_id: str, data: dict) -> None:
    \"\"\"settings/tools/<tool_id>.json に上書き保存する。\"\"\"
    os.makedirs(TOOLS_SETTINGS_DIR, exist_ok=True)
    path = os.path.join(TOOLS_SETTINGS_DIR, f'{tool_id}.json')
    with open(path, 'w') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write('\\n')
"""
content = re.sub(save_settings_pattern, r"\1" + helpers, content)


# 3. Add _validated_repo_path and _validated_repo_path_from_body
all_repos_pattern = r"(def all_repos\(\):[\s\S]*?return repos\n\n)"
validation_helpers = """
def _validated_repo_path(params):
    raw = params.get('path', [None])[0]
    if not raw:
        return None
    valid_paths = {r['path'] for r in all_repos()}
    return raw if raw in valid_paths else None

def _validated_repo_path_from_body(data):
    raw = data.get('path') if isinstance(data, dict) else None
    if not raw:
        return None
    valid_paths = {r['path'] for r in all_repos()}
    return raw if raw in valid_paths else None

"""
content = re.sub(all_repos_pattern, r"\1" + validation_helpers, content)


with open("server.py", "w") as f:
    f.write(content)
