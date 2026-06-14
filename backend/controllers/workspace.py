import os
import platform
import subprocess
from backend.storage import load_settings
from backend.controllers.git import all_repos

def open_in_editor(path):
    settings = load_settings()
    editor = settings.get('editor', 'code')
    if platform.system() == 'Windows':
        subprocess.Popen(f'"{editor}" "{path}"', shell=True)
    elif platform.system() == 'Darwin' and editor in ('code', 'cursor', 'windsurf'):
        _DARWIN_APP = {'code': 'Visual Studio Code', 'cursor': 'Cursor', 'windsurf': 'Windsurf'}
        subprocess.Popen(['open', '-a', _DARWIN_APP[editor], path])
    else:
        subprocess.Popen([editor, path])

def handle_open(handler, params):
    target = params.get('path', [None])[0]
    if not target or not os.path.isdir(target):
        handler.send_json({'error': 'invalid path'}, 400)
        return
    open_in_editor(target)
    handler.send_json({'ok': True})

def handle_ls(handler, params):
    target = os.path.normpath(os.path.abspath(os.path.expanduser(params.get('path', ['~'])[0])))
    if not os.path.isdir(target):
        handler.send_json({'error': 'not a directory'}, 400)
        return
    try:
        workspace_paths = {r['path'] for r in all_repos()}
        entries = []
        for e in sorted(os.scandir(target), key=lambda x: x.name):
            if not e.is_dir() or e.name.startswith('.'):
                continue
            norm_path = os.path.normpath(os.path.abspath(e.path))
            is_git = os.path.exists(os.path.join(norm_path, '.git'))
            entries.append({
                'name': e.name,
                'path': norm_path,
                'is_git': is_git,
                'in_workspace': norm_path in workspace_paths,
            })
        parent_dir = os.path.dirname(target)
        parent = parent_dir if parent_dir != target else None
        handler.send_json({'path': target, 'parent': parent, 'entries': entries})
    except PermissionError:
        handler.send_json({'error': 'permission denied'}, 403)
