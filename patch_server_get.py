import re

with open("server.py", "r") as f:
    content = f.read()

get_settings_tool_pattern = """        if path == '/api/settings':
            self.send_json(sanitize_settings(load_settings()))
            return"""

get_settings_tool_replacement = """        if path == '/api/settings':
            self.send_json(sanitize_settings(load_settings()))
            return

        if path.startswith('/api/settings/tool/'):
            tool_id = path.split('/')[-1]
            if not re.fullmatch(r'[a-z0-9_-]+', tool_id):
                self.send_json({'error': 'invalid tool_id'}, 400)
                return
            self.send_json(load_tool_settings(tool_id))
            return"""

content = content.replace(get_settings_tool_pattern, get_settings_tool_replacement)

get_git_endpoints_pattern = """        if path == '/api/ls':"""
get_git_endpoints_replacement = """        if path.startswith('/api/git/'):
            repo_path = _validated_repo_path(params)
            if not repo_path:
                self.send_json({'error': 'invalid or missing repository path'}, 400)
                return

            if path == '/api/git/status':
                try:
                    res = subprocess.run(['git', 'status', '--porcelain=v1', '-u'], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/log':
                n = params.get('n', ['100'])[0]
                if not n.isdigit(): n = '100'
                try:
                    res = subprocess.run(['git', 'log', '--oneline', '--decorate', '--graph', f'-n{n}'], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/branches':
                try:
                    res = subprocess.run(['git', 'branch', '-a', '--format=%(refname:short)|%(HEAD)'], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/diff':
                file_path = params.get('file', [''])[0]
                staged = params.get('staged', ['0'])[0] == '1'
                cmd = ['git', 'diff']
                if staged:
                    cmd.append('--cached')
                cmd.extend(['--', file_path])
                try:
                    res = subprocess.run(cmd, cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

            if path == '/api/git/stash/list':
                try:
                    res = subprocess.run(['git', 'stash', 'list'], cwd=repo_path, capture_output=True, text=True, check=True)
                    self.send_json({'output': res.stdout})
                except subprocess.CalledProcessError as e:
                    self.send_json({'error': e.stderr}, 400)
                return

        if path == '/api/ls':"""

content = content.replace(get_git_endpoints_pattern, get_git_endpoints_replacement)


with open("server.py", "w") as f:
    f.write(content)
