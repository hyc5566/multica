#!/usr/bin/env python3
"""Exercise real daemons against a local fixture API and isolated user units.

Requires systemd user services and two gate-aware CLI builds. No real agent,
account, database, release endpoint, or existing service is used.
"""
import argparse
import datetime as dt
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import threading
import time
import urllib.request
import uuid

WS = '11111111-1111-4111-8111-111111111111'
RID = '22222222-2222-4222-8222-222222222222'

def stamp():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def eventually(fn, timeout=25):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except (OSError, ValueError, KeyError) as exc:
            last = str(exc)
        time.sleep(.1)
    raise AssertionError(f'condition timed out: {last}')

class Fixture(http.server.ThreadingHTTPServer):
    daemon_threads = True
    def __init__(self, evidence):
        super().__init__(('127.0.0.1', 0), Handler)
        self.events = []
        self.row = None
        self.claims = 0
        self.hold = False
        self.entered = threading.Event()
        self.release = threading.Event()
        self.evidence = evidence
        self.thread = threading.Thread(target=self.serve_forever)
        self.thread.start()

    def close(self):
        self.release.set()
        self.shutdown()
        self.server_close()
        self.thread.join()
        self.evidence.write_text(json.dumps(self.events, indent=2) + '\n')

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def respond(self, data, code=200):
        body = json.dumps(data).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == '/api/daemon/workspaces':
            return self.respond([{'id': WS, 'name': 'Isolated maintenance lab'}])
        if self.path == '/api/runtimes':
            return self.respond([self.server.row] if self.server.row else [])
        if self.path.endswith('/runtime-profiles'):
            return self.respond({'workspace_id': WS, 'profiles': []})
        if self.path.endswith('/repos'):
            return self.respond({'workspace_id': WS, 'repos': [], 'repos_version': '1'})
        # WS is deliberately unavailable; the real daemon uses HTTP fallback.
        return self.respond({}, 404)

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))) or '{}')
        event = {'path': self.path, 'time': stamp()}
        if self.path == '/api/tokens/current/renew':
            return self.respond({'renewed': False})
        if self.path == '/api/daemon/register':
            self.server.row = {'id': RID, 'provider': 'claude', 'name': 'Lab fixture',
                'status': 'online', 'workspace_id': WS, 'daemon_id': data['daemon_id'],
                'last_seen_at': stamp(), 'metadata': {'cli_version': data['cli_version']}}
            event.update(daemon_id=data['daemon_id'], version=data['cli_version'])
            self.server.events.append(event)
            return self.respond({'runtimes': [self.server.row], 'repos': [], 'repos_version': '1'})
        if self.path == '/api/daemon/heartbeat':
            self.server.row['last_seen_at'] = stamp()
            event.update(runtime_id=data['runtime_id'], daemon_id=self.server.row['daemon_id'])
            self.server.events.append(event)
            return self.respond({})
        if self.path == '/api/daemon/tasks/claim':
            self.server.claims += 1
            event['claim'] = self.server.claims
            self.server.events.append(event)
            if self.server.hold:
                self.server.entered.set()
                self.server.release.wait(20)
            return self.respond({'tasks': []})
        if self.path == '/api/daemon/deregister':
            if self.server.row:
                self.server.row['status'] = 'offline'
            self.server.events.append(event)
        return self.respond({})

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--old-binary', required=True, type=Path)
    ap.add_argument('--new-binary', required=True, type=Path)
    ap.add_argument('--work-dir', required=True, type=Path)
    ap.add_argument('--boot-only', action='store_true')
    a = ap.parse_args()
    a.work_dir.mkdir(parents=True, exist_ok=False)
    results = []
    for layout in ('binary', 'launcher'):
        root = a.work_dir / layout
        root.mkdir()
        home = root / 'home'
        fixture_bin = root / 'fixture-bin'
        fixture_bin.mkdir()
        fake = fixture_bin / 'claude'
        fake.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo "2.1.322 (Claude Code)"; exit 0; fi\necho "real agent execution forbidden in lab" >&2\nexit 97\n')
        fake.chmod(0o755)
        # Resolve every other provider name to a fixture, never an installed CLI.
        names = Path(__file__).resolve().parents[1] / 'agent-cli-command-names.txt'
        for name in names.read_text().splitlines():
            name = name.strip()
            if name and not name.startswith('#') and name != 'claude':
                (fixture_bin / name).symlink_to('/bin/false')
        profile = 'hyc25-lab-' + layout + '-' + uuid.uuid4().hex[:8]
        conf = home / '.multica' / 'profiles' / profile
        conf.mkdir(parents=True)
        api = Fixture(root / 'server-events.json')
        (conf / 'config.json').write_text(json.dumps({'server_url': f'http://127.0.0.1:{api.server_port}',
            'token': 'lab-dummy-not-a-real-credential', 'workspace_id': WS,
            'disable_auto_update': True, 'disable_auto_reload': True}))
        old = root / 'old' / 'multica'
        new = root / 'new' / 'multica'
        old.parent.mkdir()
        new.parent.mkdir()
        shutil.copy2(a.old_binary, old)
        shutil.copy2(a.new_binary, new)
        entry = root / 'bin' / 'multica'
        entry.parent.mkdir()
        if layout == 'binary':
            shutil.copy2(old, entry)
        else:
            for binary in (old, new):
                launcher = binary.parent / 'launcher'
                launcher.write_text('#!/bin/sh\nexec ' + str(binary) + ' "$@"\n')
                launcher.chmod(0o755)
            os.link(old.parent / 'launcher', entry)
        # An independent observer stays executable even if the service's new build fails.
        observer = root / 'observer'
        shutil.copy2(old, observer)
        env = {'HOME': str(home), 'PATH': f'{fixture_bin}:/usr/bin:/bin', 'LANG': 'C.UTF-8',
            'SHELL': '/bin/sh', 'MULTICA_CLAUDE_PATH': str(fake),
            'XDG_RUNTIME_DIR': f'/run/user/{os.getuid()}',
            'TMPDIR': str(root), 'MULTICA_AGENT_AUTO_UPDATE': 'false',
            'MULTICA_GC_ENABLED': 'false', 'MULTICA_DAEMON_AUTO_UPDATE': 'false',
            'MULTICA_DAEMON_AUTO_RELOAD': 'false'}
        probe_source = Path(__file__).resolve().parents[2] / 'server/internal/daemon/agents_probe.go'
        for key in re.findall(r'probe\("(MULTICA_[A-Z_]+_PATH)"', probe_source.read_text()):
            if key != 'MULTICA_CLAUDE_PATH':
                env[key] = str(root / 'missing' / key)
        env['MULTICA_OMP_PATH'] = str(root / 'missing' / 'omp')
        unit = profile + '.service'
        unitfile = root / unit
        unitfile.write_text('[Unit]\nDescription=HYC-25 isolated daemon maintenance lab\n[Service]\nType=simple\n'
            + '\n'.join('Environment="' + k + '=' + v + '"' for k, v in env.items())
            + f'\nExecStart={entry} --profile {profile} daemon start --foreground --poll-interval 200ms --heartbeat-interval 400ms --no-auto-update --no-auto-reload\n'
            + f'StandardOutput=append:{root}/service.log\nStandardError=append:{root}/service.log\n')
        runtime_link = Path(env['XDG_RUNTIME_DIR']) / 'systemd' / 'user' / unit
        def command(args, check=True):
            return subprocess.run(args, env=env, capture_output=True, text=True, timeout=45, check=check)
        def health():
            p = conf / 'daemon.port'
            if not p.exists():
                return {}
            port = json.loads(p.read_text())['port']
            with urllib.request.urlopen(f'http://127.0.0.1:{port}/health', timeout=2) as response:
                return json.load(response)
        def running():
            h = health()
            return h if h.get('status') == 'running' else None
        try:
            assert not runtime_link.exists(), 'lab unit unexpectedly exists'
            command(['systemctl', '--user', 'link', '--runtime', str(unitfile)])
            command(['systemctl', '--user', 'daemon-reload'])
            command(['systemctl', '--user', 'start', unit])
            before = eventually(running)
            (root / 'before.json').write_text(json.dumps(before, indent=2) + '\n')
            assert before['cli_commit'] != 'unknown'
            eventually(lambda: api.claims >= 1)
            # Hold a real HTTP claim across gate publication. Readiness must wait.
            api.hold = True
            assert api.entered.wait(10), 'no claim in flight'
            gate = Path(before['maintenance_gate']['path'])
            gate.mkdir()
            held = health()
            assert held['maintenance_gate']['blocked'] and not held['maintenance_gate']['ready']
            assert held['maintenance_gate']['claims_in_flight'] == 1
            api.hold = False
            api.release.set()
            eventually(lambda: health()['maintenance_gate']['ready'])
            claim_count = api.claims
            beat_count = len([x for x in api.events if x['path'].endswith('/heartbeat')])
            eventually(lambda: len([x for x in api.events if x['path'].endswith('/heartbeat')]) >= beat_count + 3)
            assert api.claims == claim_count, 'claim passed persistent gate'
            gate.rmdir()
            eventually(lambda: api.claims > claim_count)
            result = {'layout': layout, 'inflight_drain': 'passed', 'claims_blocked_during_three_heartbeats': 'passed',
                'daemon_id': before['daemon_id'], 'runtimes': before['workspaces']}
            if not a.boot_only:
                updater = Path(__file__).resolve().parent / 'update-daemon.py'
                port = json.loads((conf / 'daemon.port').read_text())['port']
                common = ['python3', str(updater), profile, unit, '', sha(new),
                    '--cli', str(observer), '--health-url', f'http://127.0.0.1:{port}/health',
                    '--version', '0.8.1-zh-tw.902', '--commit', before['cli_commit'], '--layout', layout,
                    '--timeout', '15', '--heartbeat-samples', '3', '--heartbeat-interval', '0.5',
                    '--no-shared-profiles']
                common[4] = str(new if layout == 'binary' else new.parent / 'launcher')
                if layout == 'launcher':
                    common += ['--entrypoint', str(entry), '--binary', str(new)]
                invalid = list(common)
                invalid[5] = '0' * 64
                refused = command(invalid + ['--evidence-dir', str(root / 'invalid-evidence')], check=False)
                (root / 'invalid-hash-output.txt').write_text(refused.stdout + refused.stderr)
                assert refused.returncode != 0 and 'refused' in refused.stdout
                assert health()['pid'] == before['pid'] and not gate.exists()
                gate.mkdir()
                conflict = command(common + ['--evidence-dir', str(root / 'conflict-evidence')], check=False)
                (root / 'existing-gate-output.txt').write_text(conflict.stdout + conflict.stderr)
                assert conflict.returncode != 0 and gate.is_dir(), 'foreign gate was taken over'
                gate.rmdir()
                update = command(common + ['--evidence-dir', str(root / 'update-evidence')], check=False)
                (root / 'update-output.txt').write_text(update.stdout + update.stderr)
                assert update.returncode == 0, update.stdout + update.stderr
                current = eventually(running)
                assert current['daemon_id'] == before['daemon_id'] and current['cli_version'] == '0.8.1-zh-tw.902'
                assert current['workspaces'] == before['workspaces']
                released_count = api.claims
                eventually(lambda: api.claims > released_count)
                # A valid --version probe followed by startup failure forces verified rollback.
                bad = root / 'bad'
                bad.mkdir()
                badbinary = bad / 'multica'
                badbinary.write_text('#!/bin/sh\nif [ "$1" = --version ] || [ "$1" = version ]; then exec ' + str(new) + ' "$@"; fi\nexit 91\n')
                badbinary.chmod(0o755)
                badstage = badbinary
                if layout == 'launcher':
                    badstage = bad / 'launcher'
                    badstage.write_text('#!/bin/sh\nexec ' + str(badbinary) + ' "$@"\n')
                    badstage.chmod(0o755)
                failed = list(common)
                failed[4] = str(badstage)
                failed[5] = sha(badbinary)
                if layout == 'launcher':
                    failed[failed.index('--binary') + 1] = str(badbinary)
                rollback = command(failed + ['--evidence-dir', str(root / 'rollback-evidence')], check=False)
                (root / 'rollback-output.txt').write_text(rollback.stdout + rollback.stderr)
                assert rollback.returncode != 0, 'bad build unexpectedly succeeded'
                assert 'rolled_back_verified' in rollback.stdout, rollback.stdout + rollback.stderr
                restored = eventually(running)
                assert restored['daemon_id'] == before['daemon_id'] and restored['cli_version'] == '0.8.1-zh-tw.902'
                assert restored['workspaces'] == before['workspaces']
                assert sha(Path('/proc') / str(restored['pid']) / 'exe') == sha(new)
                assert not gate.exists(), 'successful rollback left gate'
                released_count = api.claims
                eventually(lambda: api.claims > released_count)
                result.update(update='passed', rollback='passed', original_identity_restored='passed',
                    invalid_hash_refused='passed', foreign_gate_preserved='passed', claims_resumed='passed')
            results.append(result)
        finally:
            api.release.set()
            command(['systemctl', '--user', 'stop', unit], check=False)
            if runtime_link.is_symlink() and runtime_link.resolve() == unitfile.resolve():
                runtime_link.unlink()
            command(['systemctl', '--user', 'daemon-reload'], check=False)
            command(['systemctl', '--user', 'reset-failed', unit], check=False)
            api.close()
    (a.work_dir / 'result.json').write_text(json.dumps({'time': stamp(), 'results': results}, indent=2) + '\n')
    print(json.dumps(results, indent=2))

if __name__ == '__main__':
    main()
