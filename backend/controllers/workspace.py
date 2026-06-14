import os
import platform
import subprocess
import shutil
from backend.storage import load_settings
from backend.controllers.git import all_repos

def open_in_editor(path):
    settings = load_settings()
    editor = settings.get('editor', 'code')
    if platform.system() == 'Windows':
        resolved_editor = shutil.which(editor) or editor
        subprocess.Popen([resolved_editor, path])
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
    raw_path = params.get('path', ['~'])[0]

    # Windows: virtual drive listing
    if platform.system() == 'Windows' and raw_path == '__drives__':
        import string
        entries = []
        for letter in string.ascii_uppercase:
            drive = f"{letter}:\\"
            if os.path.exists(drive):
                entries.append({
                    'name': f"{letter}:",
                    'path': os.path.normpath(drive),
                    'is_git': False,
                    'in_workspace': False,
                })
        handler.send_json({'path': '__drives__', 'parent': None, 'entries': entries})
        return

    target = os.path.normpath(os.path.abspath(os.path.expanduser(raw_path)))
    if not os.path.isdir(target):
        handler.send_json({'error': 'not a directory'}, 400)
        return
    try:
        workspace_paths = {os.path.normcase(r['path']) for r in all_repos()}
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
                'in_workspace': os.path.normcase(norm_path) in workspace_paths,
            })
        parent_dir = os.path.dirname(target)
        parent = parent_dir if parent_dir != target else None
        # Windows: at drive root, allow navigating up to drive list
        if parent is None and platform.system() == 'Windows':
            parent = '__drives__'
        handler.send_json({'path': target, 'parent': parent, 'entries': entries})
    except PermissionError:
        handler.send_json({'error': 'permission denied'}, 403)
