import os
import platform
import subprocess
import re
import signal
from backend.storage import load_settings, save_settings

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

def handle_get(handler, path, params):
    if path == '/api/ports':
        handler.send_json(list_open_ports())
        return
    handler.send_json({'error': 'not found'}, 404)

def handle_post(handler, path, data):
    if path == '/api/ports/label':
        save_port_label(data.get('port'), data.get('label', ''))
        handler.send_json({'ok': True})
        return

    if path == '/api/ports/protected':
        ports = save_protected_ports(data.get('ports', []))
        handler.send_json({'ok': True, 'protected_ports': ports})
        return

    if path == '/api/ports/kill':
        kill_port_process(data.get('port'), data.get('pid'))
        handler.send_json({'ok': True})
        return

    handler.send_json({'error': 'not found'}, 404)
