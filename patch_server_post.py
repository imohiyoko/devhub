import re

with open("server.py", "r") as f:
    content = f.read()

post_settings_tool_pattern = """        if path == '/api/settings':
            try:
                data = json.loads(self.read_body())
                allowed = {'disabled_tools', 'tool_order', 'editor', 'open_browser_on_start', 'db_connections', 'port_labels', 'protected_ports', 'terminal'}"""
post_settings_tool_replacement = """        if path.startswith('/api/settings/tool/'):
            tool_id = path.split('/')[-1]
            if not re.fullmatch(r'[a-z0-9_-]+', tool_id):
                self.send_json({'error': 'invalid tool_id'}, 400)
                return
            try:
                data = json.loads(self.read_body())
                if not isinstance(data, dict):
                    self.send_json({'error': 'invalid'}, 400)
                    return
                save_tool_settings(tool_id, data)
                self.send_json({'ok': True})
            except Exception as e:
                self.send_json({'error': str(e)}, 400)
            return

        if path == '/api/settings':
            try:
                data = json.loads(self.read_body())
                allowed = {'disabled_tools', 'tool_order', 'editor', 'open_browser_on_start', 'db_connections', 'port_labels', 'protected_ports', 'terminal'}"""

content = content.replace(post_settings_tool_pattern, post_settings_tool_replacement)


post_git_endpoints_pattern = """        if path == '/api/ports/label':"""
post_git_endpoints_replacement = """        if path.startswith('/api/git/'):
            try:
                data = json.loads(self.read_body())
            except Exception:
                data = {}

            repo_path = _validated_repo_path_from_body(data)
            if not repo_path:
                self.send_json({'error': 'invalid or missing repository path'}, 400)
                return

            if path == '/api/git/stage':
                files = data.get('files', [])
                if not files:
                    self.send_json({'error': 'no files specified'}, 400)
                    return
                try:
                    res = subprocess.run(['git', 'add', '--'] + files, cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/unstage':
                files = data.get('files', [])
                if not files:
                    self.send_json({'error': 'no files specified'}, 400)
                    return
                try:
                    res = subprocess.run(['git', 'restore', '--staged', '--'] + files, cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/commit':
                message = data.get('message', '')
                if not message:
                    self.send_json({'error': 'no message specified'}, 400)
                    return
                try:
                    res = subprocess.run(['git', 'commit', '-m', message], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/push':
                try:
                    res = subprocess.run(['git', 'push'], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/pull':
                try:
                    res = subprocess.run(['git', 'pull'], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/checkout':
                branch = data.get('branch', '')
                if not branch or not re.fullmatch(r'^[a-zA-Z0-9_\-\./]+$', branch):
                    self.send_json({'error': 'invalid branch name'}, 400)
                    return
                try:
                    res = subprocess.run(['git', 'checkout', branch], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/branch/create':
                branch = data.get('branch', '')
                if not branch or not re.fullmatch(r'^[a-zA-Z0-9_\-\./]+$', branch):
                    self.send_json({'error': 'invalid branch name'}, 400)
                    return
                try:
                    res = subprocess.run(['git', 'checkout', '-b', branch], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/stash':
                action = data.get('action', '')
                cmd = ['git', 'stash']
                if action in ['push', 'pop', 'drop']:
                    cmd.append(action)
                    if action in ['pop', 'drop']:
                        idx = data.get('index')
                        if idx is not None:
                            cmd.append(f'stash@{{{idx}}}')
                else:
                    self.send_json({'error': 'invalid action'}, 400)
                    return

                try:
                    res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'ok': True, 'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

        if path == '/api/ports/label':"""

content = content.replace(post_git_endpoints_pattern, post_git_endpoints_replacement)


with open("server.py", "w") as f:
    f.write(content)
