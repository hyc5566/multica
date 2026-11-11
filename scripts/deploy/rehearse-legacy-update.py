#!/usr/bin/env python3
"""Isolated Go-daemon rehearsal; all APIs and systemd operations are fixtures.

This does not test a real Multica Server, a real systemd unit, or an installed
agent. Both binaries are copied before execution. The fixture server is a
separate loopback process and every process launched here is reaped on exit.
"""
import argparse
import datetime
import hashlib
import http.server
import importlib.util
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import subprocess
import sys
import threading
import time
from types import SimpleNamespace
from unittest import mock
import urllib.request

WS = '11111111-1111-4111-8111-111111111111'
RID = '22222222-2222-4222-8222-222222222222'
AGENT = '33333333-3333-4333-8333-333333333333'
ISSUE = '44444444-4444-4444-8444-444444444444'


def stamp():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def digest(path):
    h = hashlib.sha256()
    with open(path, 'rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def write_json(path, data):
    Path(path).write_text(json.dumps(data, indent=2, sort_keys=True) + '\n', encoding='utf-8')


def wait_for(fn, description, timeout=45):
    end = time.monotonic() + timeout
    error = None
    while time.monotonic() < end:
        try:
            value = fn()
            if value:
                return value
        except (OSError, ValueError, KeyError, urllib.error.URLError) as exc:
            error = str(exc)
        time.sleep(0.1)
    raise RuntimeError('%s timed out: %s' % (description, error))


class FixtureServer(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, log):
        super().__init__(('127.0.0.1', 0), FixtureHandler)
        self.lock = threading.RLock()
        self.log = log
        self.row = None
        self.events = []
        self.queue = []
        self.claims = 0
        self.heartbeats = 0
        self.completions = {}
        self.hold = False
        self.claim_entered = False
        self.release = threading.Event()

    def record(self, kind, **details):
        event = {'time': stamp(), 'kind': kind, **details}
        with self.lock:
            self.events.append(event)
            with open(self.log, 'a', encoding='utf-8') as out:
                out.write(json.dumps(event, sort_keys=True) + '\n')

    def snapshot(self):
        with self.lock:
            return {'pid': os.getpid(), 'row': self.row, 'claims': self.claims,
                    'heartbeats': self.heartbeats, 'completions': dict(self.completions),
                    'claim_entered': self.claim_entered, 'queued': len(self.queue)}


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, value, status=200):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_GET(self):
        server = self.server
        server.record('get', path=self.path)
        if self.path == '/control/status':
            return self.reply(server.snapshot())
        if self.path == '/api/daemon/workspaces':
            return self.reply([{'id': WS, 'name': 'isolated fixture'}])
        if self.path == '/api/runtimes':
            return self.reply([server.row] if server.row else [])
        if self.path.endswith('/runtime-profiles'):
            return self.reply({'workspace_id': WS, 'profiles': []})
        if self.path.endswith('/repos'):
            return self.reply({'workspace_id': WS, 'repos': [], 'repos_version': '1'})
        match = re.fullmatch(r'/api/daemon/tasks/([^/]+)/status', self.path)
        if match:
            with server.lock:
                completion = server.completions.get(match.group(1))
                status = ('completed' if completion['action'] == 'complete' else 'failed') if completion else 'running'
            return self.reply({'status': status})
        return self.reply({}, 404)

    def do_POST(self):
        server = self.server
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if size > 1024 * 1024:
                return self.reply({'error': 'too large'}, 413)
            body = json.loads(self.rfile.read(size) or b'{}')
        except (ValueError, UnicodeError):
            return self.reply({'error': 'invalid JSON'}, 400)
        server.record('post', path=self.path, body=body)
        if self.path == '/control/reset':
            with server.lock:
                server.row, server.queue, server.claims, server.heartbeats = None, [], 0, 0
                server.completions, server.hold, server.claim_entered = {}, False, False
                server.release.set()
            return self.reply(server.snapshot())
        if self.path == '/control/hold':
            with server.lock:
                server.hold = True
                server.claim_entered = False
                server.release.clear()
            return self.reply(server.snapshot())
        if self.path == '/control/release':
            with server.lock:
                server.hold = False
                server.release.set()
            return self.reply(server.snapshot())
        if self.path == '/control/missed':
            server.record('maintenance_boundary_missed', task_id=body['id'], explanation='simulated missed dispatch; user redrive required')
            return self.reply({'recorded': True})
        if self.path == '/control/enqueue':
            task_id = body['id']
            task = {'id': task_id, 'workspace_id': WS, 'workspace_slug': 'fixture',
                    'runtime_id': RID, 'agent_id': AGENT, 'issue_id': ISSUE,
                    'issue_identifier': 'LAB-1', 'auth_token': 'mat_fixture_only',
                    'agent': {'id': AGENT, 'name': 'fixture-agent', 'instructions': 'Return fixture success.'}}
            with server.lock:
                server.queue.append(task)
            server.record('synthetic_task_enqueued', task_id=task_id, redrive_of=body.get('redrive_of'))
            return self.reply({'queued': task_id})
        if self.path == '/api/tokens/current/renew':
            return self.reply({'renewed': False})
        if self.path == '/api/daemon/register':
            with server.lock:
                server.row = {'id': RID, 'provider': 'claude', 'name': 'fixture', 'status': 'online',
                              'workspace_id': WS, 'daemon_id': body['daemon_id'],
                              'last_seen_at': stamp(), 'metadata': {'cli_version': body.get('cli_version')}}
            return self.reply({'runtimes': [server.row], 'repos': [], 'repos_version': '1'})
        if self.path == '/api/daemon/heartbeat':
            with server.lock:
                if server.row:
                    server.row['last_seen_at'] = stamp()
                server.heartbeats += 1
            return self.reply({})
        if self.path == '/api/daemon/tasks/claim' or re.fullmatch(r'/api/daemon/runtimes/[^/]+/tasks/claim', self.path):
            with server.lock:
                server.claims += 1
                held = server.hold
                if held:
                    server.claim_entered = True
            if held:
                server.release.wait(30)
            with server.lock:
                tasks = [server.queue.pop(0)] if server.queue else []
            return self.reply({'tasks': tasks} if self.path == '/api/daemon/tasks/claim' else {'task': tasks[0] if tasks else None})
        match = re.fullmatch(r'/api/daemon/tasks/([^/]+)/(start|complete|fail|messages|progress|usage|session|cancel-ack)', self.path)
        if match:
            task_id, action = match.groups()
            if action in ('complete', 'fail'):
                with server.lock:
                    server.completions[task_id] = {'action': action, 'body': body, 'time': stamp()}
            return self.reply({})
        if self.path == '/api/daemon/deregister' or re.fullmatch(r'/api/daemon/runtimes/[^/]+/(recover-orphans|tasks/[^/]+/prepare-lease)', self.path):
            return self.reply({})
        return self.reply({}, 404)


def fixture_worker(workdir):
    server = FixtureServer(Path(workdir) / 'fixture-events.jsonl')
    print(server.server_port, flush=True)
    try:
        server.serve_forever()
    finally:
        server.server_close()


def request(url, body=None):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, headers={'Content-Type': 'application/json'})
    with opener.open(req, timeout=5) as response:
        return json.load(response)


class FakeSystemd:
    """A private unit adapter; only exact child PIDs created here are signalled."""
    def __init__(self, unit, entry, profile, env, log, updater):
        self.unit, self.entry, self.profile, self.env, self.log, self.updater = unit, entry, profile, env, log, updater
        self.proc = None
        self.fail_next_start = False
        self.children = []
        self.group = '/fixture/' + unit

    def alive(self):
        return self.proc is not None and self.proc.poll() is None

    def start(self):
        if self.alive():
            raise self.updater.Refusal('fixture unit already active')
        bad_start = self.fail_next_start
        self.fail_next_start = False
        args = [str(self.entry), '--fixture-invalid-start'] if bad_start else [str(self.entry), '--profile', self.profile,
                'daemon', 'start', '--foreground', '--poll-interval', '200ms', '--heartbeat-interval', '400ms',
                '--no-auto-update', '--no-auto-reload']
        out = open(self.log, 'ab', buffering=0)
        try:
            self.proc = subprocess.Popen(args, env=self.env, stdin=subprocess.DEVNULL, stdout=out, stderr=out,
                                         start_new_session=True, close_fds=True)
        finally:
            out.close()
        self.children.append(self.proc)
        if bad_start:
            try:
                result = self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.stop()
                raise self.updater.Refusal('fixture bad-start process did not exit')
            if result == 0:
                raise self.updater.Refusal('fixture invalid startup unexpectedly succeeded')
            raise self.updater.Refusal('fixture injected candidate startup failure')

    def stop(self):
        proc = self.proc
        if proc is None or proc.poll() is not None:
            return
        # This is the private session created above, never an executable-name kill.
        if os.getpgid(proc.pid) != proc.pid:
            raise RuntimeError('fixture process group changed')
        os.killpg(proc.pid, signal.SIGTERM)
        try:
            proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            if proc.poll() is None and os.getpgid(proc.pid) == proc.pid:
                os.killpg(proc.pid, signal.SIGKILL)
            proc.wait(timeout=5)

    def close(self):
        self.stop()
        for child in self.children:
            if child.poll() is None:
                raise RuntimeError('fixture-owned child remained active')
            child.wait()

    def run(self, command, timeout=30):
        if command[:2] not in (['systemctl', '--user'], ['systemctl', '--system']):
            return self.real_run(command, timeout=timeout)
        action = command[2]
        if len(command) < 4 or command[3] != self.unit:
            raise self.updater.Refusal('fixture refuses non-owned unit')
        if action == 'stop':
            self.stop()
            return b''
        if action == 'start':
            self.start()
            return b''
        if action != 'show':
            raise self.updater.Refusal('unsupported fixture systemctl operation')
        live = self.alive()
        pid = self.proc.pid if live else 0
        props = {'--property=ActiveState': 'active' if live else 'inactive',
                 '--property=MainPID': str(pid), '--property=KillMode': 'control-group',
                 '--property=ControlGroup': self.group if live else '',
                 '--property=ExecStart': '{ path=%s ; argv[]=%s --profile %s daemon start ; ignore_errors=no ; start_time=%s ; stop_time=%s ; pid=%s ; code=(null) ; status=0 }' %
                 (self.entry, self.entry, self.profile, pid, 0 if live else 1, pid)}
        matches = [value for key, value in props.items() if key in command]
        if len(matches) != 1:
            raise self.updater.Refusal('unsupported fixture systemctl property')
        return (matches[0] + '\n').encode()

    def members(self, group):
        if group != self.group:
            raise self.updater.Refusal('fixture refuses foreign cgroup')
        return [self.proc.pid] if self.alive() else []


def health(conf):
    port = json.loads((conf / 'daemon.port').read_text())['port']
    return request('http://127.0.0.1:%d/health' % port)


def daemon_health(conf, unit):
    if not unit.alive():
        return None
    value = health(conf)
    return value if value.get('pid') == unit.proc.pid and value.get('status') == 'running' else None


def make_fixture_commands(root):
    bindir = root / 'fixture-bin'
    bindir.mkdir()
    agent = bindir / 'claude'
    agent.write_text('''#!/bin/sh
if [ "$1" = --version ]; then echo '2.1.322 (Claude Code)'; exit 0; fi
IFS= read -r ignored
printf '%s\\n' '{"type":"system","session_id":"fixture-session"}'
printf '%s\\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"fixture-session","result":"fixture task completed"}'
''', encoding='utf-8')
    agent.chmod(0o755)
    names = Path(__file__).resolve().parents[1] / 'agent-cli-command-names.txt'
    for name in names.read_text(encoding='utf-8').splitlines():
        name = name.strip()
        if name and not name.startswith('#') and name != 'claude':
            if not re.fullmatch(r'[A-Za-z0-9_.+-]+', name):
                raise RuntimeError('invalid fixture agent command name')
            path = bindir / name
            path.write_text('#!/bin/sh\nexit 127\n', encoding='utf-8')
            path.chmod(0o755)
    return bindir, agent


def fixture_env(home, root, bindir, agent):
    # Deliberately do not inherit tokens, task markers, proxy or agent settings.
    config, cache, data = home / '.config', home / '.cache', home / '.local' / 'share'
    for directory in (config, cache, data):
        directory.mkdir(parents=True, exist_ok=True)
    env = {'HOME': str(home), 'PATH': str(bindir) + ':/usr/bin:/bin', 'LANG': 'C.UTF-8', 'SHELL': '/bin/sh',
           'XDG_CONFIG_HOME': str(config), 'XDG_CACHE_HOME': str(cache), 'XDG_DATA_HOME': str(data),
           'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null',
           'TMPDIR': str(root), 'MULTICA_CLAUDE_PATH': str(agent),
           'MULTICA_AGENT_AUTO_UPDATE': 'false', 'MULTICA_GC_ENABLED': 'false',
           'MULTICA_DAEMON_AUTO_UPDATE': 'false', 'MULTICA_DAEMON_AUTO_RELOAD': 'false'}
    source = Path(__file__).resolve().parents[2] / 'server/internal/daemon/agents_probe.go'
    for name in re.findall(r'probe\("(MULTICA_[A-Z_]+_PATH)"', source.read_text(encoding='utf-8')):
        if name != 'MULTICA_CLAUDE_PATH':
            env[name] = str(root / 'missing' / name)
    env['MULTICA_OMP_PATH'] = str(root / 'missing' / 'omp')
    return env


def version(binary, env):
    result = subprocess.run([str(binary), 'version', '--output', 'json'], env=env,
                            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=20, check=True)
    return json.loads(result.stdout)


def attested_task(conf, unit, task_id):
    current = daemon_health(conf, unit)
    if not current:
        return None
    attestation = current.get('update_attestation') or {}
    return next((item for item in attestation.get('task_receipts', []) if item.get('task_id') == task_id), None)


def rehearse_case(root, old_binary, new_binary, fixture_url, fixture_pid, coordinator, *, fail_start):
    root.mkdir()
    home, assets = root / 'home', root / 'assets'
    home.mkdir()
    assets.mkdir()
    bindir, agent = make_fixture_commands(root)
    env = fixture_env(home, root, bindir, agent)
    profile = 'fixture-' + ('failure' if fail_start else 'success')
    conf = home / '.multica' / 'profiles' / profile
    conf.mkdir(parents=True)
    (conf / 'config.json').write_text(json.dumps({'server_url': fixture_url, 'token': 'fixture-dummy-token',
        'workspace_id': WS, 'workspaces_root': str(root / 'workspaces'),
        'disable_auto_update': True, 'disable_auto_reload': True}), encoding='utf-8')
    current, staged, observer = assets / 'multica-current', assets / 'candidate', assets / 'observer'
    for source, destination in ((old_binary, current), (new_binary, staged), (new_binary, observer)):
        shutil.copy2(source, destination)
        destination.chmod(0o755)
    old_info, new_info = version(current, env), version(staged, env)
    if 'daemon_maintenance_gate' in old_info or 'build_commit' in old_info:
        raise RuntimeError('old binary is not the expected pre-gate build')
    arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(os.uname().machine)
    if not arch or any(info.get('os') != 'linux' or info.get('arch') != arch for info in (old_info, new_info)):
        raise RuntimeError('binary platform does not match the fixture host')
    commit, ver = new_info.get('build_commit'), new_info.get('version')
    if new_info.get('daemon_maintenance_gate') != 'v1' or not isinstance(commit, str) or not re.fullmatch(r'[0-9a-f]{40}', commit or ''):
        raise RuntimeError('new binary lacks gate capability or full build commit')
    ca, key = assets / 'fixture-ca.pem', root / 'fixture-ca-key.pem'
    subprocess.run(['/usr/bin/openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(key),
                    '-out', str(ca), '-days', '1', '-subj', '/CN=isolated-fixture-ca'], env=env,
                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=30, check=True)
    entry = assets / 'entrypoint'
    entry.write_text('#!/bin/sh\n' + ''.join("export %s='%s'\n" % (name, ca) for name in
        ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')) + "exec '%s' \"$@\"\n" % current, encoding='utf-8')
    entry.chmod(0o755)
    before_stat, before_wrapper = entry.stat(), entry.read_bytes()
    unit = FakeSystemd('fixture.service', entry, profile, env, root / 'daemon.log', coordinator.updater)
    unit.real_run = coordinator.updater.run
    account = SimpleNamespace(pw_dir=str(home), pw_gid=os.getgid())
    actual_updater = coordinator.updater.Updater
    class FastUpdater(actual_updater):
        def __init__(self, args):
            if hasattr(args, 'heartbeat_interval'):
                args.heartbeat_interval = 0.25
                args.timeout = 25
            super().__init__(args)

        def systemctl(self, *args):
            # This adapter talks only to FakeSystemd, never a real user bus.
            return ['systemctl', '--user'] + list(args)
    config = {'owner_uid': os.geteuid(), 'profile': profile, 'unit': unit.unit, 'systemd_scope': 'user',
              'cli': str(observer), 'staged': str(staged), 'search_roots': [str(assets)],
              'ledger': str(root / 'ledger.json'), 'evidence_dir': str(assets / 'evidence'),
              'asset_root': str(assets), 'controller_machine_id_sha256': 'f' * 64,
              'legacy_wrapper': str(entry), 'ca_sha256': digest(ca),
              'observer_ca_cert_file': str(ca), 'observer_ca_sha256': digest(ca)}
    manifest = {'source': 'hyc5566/multica', 'platform': 'linux-amd64' if os.uname().machine == 'x86_64' else 'linux-arm64',
                'version': ver, 'commit': commit, 'data_compatibility': 'multica-profile-v1',
                'assets': [{'role': 'binary', 'url': 'https://github.com/hyc5566/multica/releases/download/v%s/fixture' % ver,
                            'size': staged.stat().st_size, 'sha256': digest(staged)}]}
    server = lambda: request(fixture_url + '/control/status')
    control = lambda action, body=None: request(fixture_url + '/control/' + action, body or {})
    control('reset')
    result = {'case': 'failed-start' if fail_start else 'success', 'fixture_server_pid': fixture_pid,
              'old_sha256': digest(current), 'candidate_sha256': digest(staged),
              'old_version_output': old_info, 'candidate_version_output': new_info,
              'boundary': 'fixture API, fake systemctl/cgroup and fake agent; cross-UID process set is fixture-only, not a host inventory; not real Server or systemd'}
    try:
        def fixture_executable_users(executable):
            if not unit.alive():
                return []
            image = os.stat('/proc/%d/exe' % unit.proc.pid)
            target = os.stat(executable)
            if (image.st_dev, image.st_ino) != (target.st_dev, target.st_ino):
                return []
            return [{'pid': unit.proc.pid, 'uid': os.stat('/proc/%d' % unit.proc.pid).st_uid}]
        with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(coordinator.pwd, 'getpwuid', return_value=account), \
             mock.patch.object(coordinator, 'target_machine_fingerprint', return_value='e' * 64), \
             mock.patch.object(coordinator, 'scan_roots', return_value=[]), mock.patch.object(coordinator, 'scan_programs', return_value=[]), \
             mock.patch.object(coordinator, 'process_inventory', side_effect=lambda uid: [{'pid': unit.proc.pid, 'exe': str(current)}] if unit.alive() else []), \
             mock.patch.object(coordinator.updater, 'run', side_effect=unit.run), \
             mock.patch.object(coordinator.updater, 'cgroup_members', side_effect=unit.members), \
             mock.patch.object(coordinator.updater, 'executable_users', side_effect=fixture_executable_users), \
             mock.patch.object(coordinator.updater, 'Updater', FastUpdater):
            unit.start()
            before = wait_for(lambda: daemon_health(conf, unit), 'legacy daemon health')
            write_json(root / 'before-health.json', before)
            if before.get('maintenance_gate', {}).get('supported') is True:
                raise RuntimeError('legacy health unexpectedly has a gate')
            if before.get('agents') != ['claude']:
                raise RuntimeError('unexpected fixture agent inventory')
            wait_for(lambda: server()['row'] and server()['claims'] >= 1, 'legacy registration and claim')
            config['health_url'] = 'http://127.0.0.1:%d/health' % json.loads((conf / 'daemon.port').read_text())['port']
            proposed = coordinator.build_plan(config, manifest, 'a' * 64, legacy_bootstrap=True, observe_legacy=True)
            config['legacy_baseline'] = proposed['legacy_baseline']
            plan = coordinator.build_plan(config, manifest, 'a' * 64, legacy_bootstrap=True)
            write_json(root / 'legacy-observation.json', proposed)
            write_json(root / 'legacy-plan.json', {'plan': plan, 'fingerprint': coordinator.fingerprint(plan)})
            missed = '66666666-6666-4666-8666-666666666666'
            control('missed', {'id': missed})
            unit.fail_next_start = fail_start
            outcome = coordinator.apply_plan(plan, manifest)
            write_json(root / 'apply-result.json', outcome)
            after = wait_for(lambda: daemon_health(conf, unit), 'post-transaction daemon')
            write_json(root / 'after-health.json', after)
            if after.get('daemon_id') != before.get('daemon_id') or after.get('workspaces') != before.get('workspaces') or after.get('profile') != profile:
                raise RuntimeError('daemon, workspace/runtime or profile identity changed')
            if entry.read_bytes() != before_wrapper or (entry.stat().st_ino, entry.stat().st_uid, entry.stat().st_gid, entry.stat().st_mode) != (before_stat.st_ino, before_stat.st_uid, before_stat.st_gid, before_stat.st_mode):
                raise RuntimeError('original wrapper bytes or metadata changed')
            if server()['pid'] != fixture_pid:
                raise RuntimeError('fixture Server process changed')
            if fail_start:
                if outcome.get('status') != 'recovery_required' or outcome.get('result', {}).get('status') != 'legacy_restored' or digest(current) != result['old_sha256']:
                    raise RuntimeError('injected failure did not restore original legacy binary')
                result.update(status='legacy_restored', daemon_id=after['daemon_id'], workspace_runtimes=after['workspaces'])
                return result
            if outcome.get('status') != 'awaiting_acceptance' or digest('/proc/%d/exe' % after['pid']) != result['candidate_sha256']:
                raise RuntimeError('new daemon did not reach receipt-pending state')
            first, retry = '55555555-5555-4555-8555-555555555555', '77777777-7777-4777-8777-777777777777'
            control('enqueue', {'id': first})
            completed = wait_for(lambda: server()['completions'].get(first), 'first synthetic task completion', 90)
            if completed['action'] != 'complete' or completed['body'].get('output') != 'fixture task completed' or completed['body'].get('session_id') != 'fixture-session':
                raise RuntimeError('first synthetic task failed: %s' % completed)
            first_receipt = wait_for(lambda: attested_task(conf, unit, first), 'daemon receipt for first task')
            control('enqueue', {'id': retry, 'redrive_of': missed})
            retried = wait_for(lambda: server()['completions'].get(retry), 'redriven synthetic task completion', 90)
            if retried['action'] != 'complete' or retried['body'].get('output') != 'fixture task completed' or retried['body'].get('session_id') != 'fixture-session':
                raise RuntimeError('redriven synthetic task failed: %s' % retried)
            retry_receipt = wait_for(lambda: attested_task(conf, unit, retry), 'daemon receipt for redriven task')
            write_json(root / 'task-receipts.json', {'synthetic_task': first_receipt, 'redriven_task': retry_receipt})
            write_json(root / 'accepted-health.json', health(conf))
            acceptance = coordinator.accept_transaction(config['ledger'])
            write_json(root / 'acceptance.json', acceptance)
            if acceptance.get('status') != 'accepted':
                raise RuntimeError('post-release receipt not accepted: %s' % acceptance)
            gate = Path(after['maintenance_gate']['path'])
            control('hold')
            wait_for(lambda: server()['claim_entered'], 'in-flight fixture claim')
            gate.mkdir(mode=0o700)
            blocked = wait_for(lambda: (h if (h := health(conf)).get('maintenance_gate', {}).get('blocked') and not h['maintenance_gate'].get('ready') else None), 'gate blocks in-flight claim')
            control('release')
            wait_for(lambda: health(conf).get('maintenance_gate', {}).get('ready'), 'claim drain')
            drained = health(conf)
            claims, beats = server()['claims'], server()['heartbeats']
            wait_for(lambda: server()['heartbeats'] >= beats + 3, 'three gated heartbeat acknowledgements')
            if server()['claims'] != claims:
                raise RuntimeError('claim passed the gate')
            unit.stop()
            if not gate.is_dir():
                raise RuntimeError('gate disappeared across stop')
            unit.start()
            restarted = wait_for(lambda: daemon_health(conf, unit), 'gated daemon restart')
            if not restarted.get('maintenance_gate', {}).get('blocked'):
                raise RuntimeError('restart did not retain gate')
            claims, beats = server()['claims'], server()['heartbeats']
            wait_for(lambda: server()['heartbeats'] >= beats + 3 and (health(conf).get('update_attestation') or {}).get('runtime_heartbeat_acks', {}).get(RID, 0) >= 3, 'three post-restart acknowledgements')
            if server()['claims'] != claims:
                raise RuntimeError('claim passed gate after restart')
            post_restart = health(conf)
            write_json(root / 'gate-checks.json', {'blocked_with_claim': blocked, 'drained': drained,
                                                  'restarted': restarted, 'post_restart_heartbeats': post_restart})
            gate.rmdir()
            wait_for(lambda: server()['claims'] > claims, 'claims resume')
            final = health(conf)
            write_json(root / 'final-health.json', final)
            result.update(status='accepted', daemon_id=final['daemon_id'], workspace_runtimes=final['workspaces'],
                          gate_drain='verified', gate_restart='verified', synthetic_task=first,
                          simulated_missed_task=missed, redriven_task=retry, acceptance=acceptance)
            return result
    finally:
        unit.close()


def start_fixture(root):
    log = open(root / 'fixture-server.stderr', 'ab', buffering=0)
    env = {'HOME': str(root), 'TMPDIR': str(root), 'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8'}
    try:
        proc = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), '--fixture-worker', str(root)],
                                env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=log,
                                text=True, bufsize=1, start_new_session=True)
    finally:
        log.close()
    selector = selectors.DefaultSelector()
    try:
        selector.register(proc.stdout, selectors.EVENT_READ)
        if not selector.select(10):
            raise RuntimeError('fixture Server did not report its port')
        line = proc.stdout.readline().strip()
        if not line.isdecimal() or not 1 <= int(line) <= 65535:
            raise RuntimeError('fixture Server reported invalid port')
        return proc, 'http://127.0.0.1:' + line
    except BaseException:
        stop_fixture(proc)
        raise
    finally:
        selector.close()


def stop_fixture(proc):
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
    else:
        proc.wait()
    if proc.stdout:
        proc.stdout.close()


def fixture_self_test(url, pid):
    control = lambda action, body=None: request(url + '/control/' + action, body or {})
    control('reset')
    registration = request(url + '/api/daemon/register', {'daemon_id': 'fixture-daemon', 'cli_version': 'fixture'})
    if registration['runtimes'][0]['id'] != RID:
        raise RuntimeError('fixture registration failed')
    request(url + '/api/daemon/heartbeat', {'runtime_id': RID})
    control('missed', {'id': 'missed-fixture-task'})
    control('enqueue', {'id': 'redriven-fixture-task', 'redrive_of': 'missed-fixture-task'})
    claim = request(url + '/api/daemon/tasks/claim', {'daemon_id': 'fixture-daemon', 'runtime_ids': [RID], 'max_tasks': 1})
    task = claim['tasks'][0]
    if task['id'] != 'redriven-fixture-task' or task['auth_token'] != 'mat_fixture_only':
        raise RuntimeError('fixture claim payload mismatch')
    if request(url + '/api/daemon/tasks/redriven-fixture-task/status')['status'] != 'running':
        raise RuntimeError('fixture pre-completion status mismatch')
    request(url + '/api/daemon/tasks/redriven-fixture-task/start', {})
    request(url + '/api/daemon/tasks/redriven-fixture-task/complete', {'output': 'fixture completion'})
    if request(url + '/api/daemon/tasks/redriven-fixture-task/status')['status'] != 'completed':
        raise RuntimeError('fixture post-completion status mismatch')
    result = request(url + '/control/status')
    if result['pid'] != pid or result['heartbeats'] != 1 or result['completions']['redriven-fixture-task']['action'] != 'complete':
        raise RuntimeError('fixture status mismatch')
    return {'status': 'fixture_self_test_passed', 'fixture_server_pid': pid,
            'limitation': 'Synthetic HTTP fixture only; no daemon, systemd, Server or agent was tested.'}


def legacy_smoke(root, binary, url, pid, coordinator):
    root.mkdir()
    home, assets = root / 'home', root / 'assets'
    home.mkdir()
    assets.mkdir()
    bindir, agent = make_fixture_commands(root)
    env = fixture_env(home, root, bindir, agent)
    profile = 'fixture-legacy-smoke'
    conf = home / '.multica' / 'profiles' / profile
    conf.mkdir(parents=True)
    (conf / 'config.json').write_text(json.dumps({'server_url': url, 'token': 'fixture-dummy-token',
        'workspace_id': WS, 'workspaces_root': str(root / 'workspaces'),
        'disable_auto_update': True, 'disable_auto_reload': True}), encoding='utf-8')
    executable, entry = assets / 'current', assets / 'entrypoint'
    shutil.copy2(binary, executable)
    executable.chmod(0o755)
    entry.write_text('#!/bin/sh\nexec ' + str(executable) + ' "$@"\n', encoding='utf-8')
    entry.chmod(0o755)
    info = version(executable, env)
    if 'daemon_maintenance_gate' in info or 'build_commit' in info:
        raise RuntimeError('old binary is not the expected pre-gate build')
    unit = FakeSystemd('fixture.service', entry, profile, env, root / 'daemon.log', coordinator.updater)
    try:
        request(url + '/control/reset', {})
        unit.start()
        before = wait_for(lambda: daemon_health(conf, unit), 'legacy smoke health')
        state = wait_for(lambda: (state if (state := request(url + '/control/status'))['row'] and state['claims'] >= 1 and state['heartbeats'] >= 1 else None), 'legacy smoke registration, claim and heartbeat')
        if state['pid'] != pid or (before.get('maintenance_gate') or {}).get('supported') is True:
            raise RuntimeError('legacy or fixture identity mismatch')
        if before.get('agents') != ['claude']:
            raise RuntimeError('unexpected fixture agent inventory')
        observer = subprocess.run([str(executable), '--profile', profile, '--workspace-id', WS, 'runtime', 'list', '--output', 'json'],
                                  env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  timeout=20, check=True)
        rows = json.loads(observer.stdout)
        if not any(row.get('id') == RID and row.get('daemon_id') == before['daemon_id'] for row in rows):
            raise RuntimeError('fixture observer runtime inventory mismatch')
        task_id = '88888888-8888-4888-8888-888888888888'
        request(url + '/control/enqueue', {'id': task_id})
        completion = wait_for(lambda: request(url + '/control/status')['completions'].get(task_id), 'legacy synthetic task completion', 45)
        if completion['action'] != 'complete' or completion['body'].get('output') != 'fixture task completed' or completion['body'].get('session_id') != 'fixture-session':
            raise RuntimeError('legacy synthetic task failed: %s' % completion)
        write_json(root / 'health.json', before)
        write_json(root / 'fixture-status.json', request(url + '/control/status'))
        return {'status': 'legacy_smoke_passed', 'fixture_server_pid': pid, 'daemon_id': before['daemon_id'],
                'binary_sha256': digest(executable), 'version_output': info, 'synthetic_task': task_id,
                'limitation': 'Real pre-gate daemon against fake API and fake systemctl; no candidate, receipt or production Server tested.'}
    finally:
        unit.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--old-binary', type=Path, help='pre-gate Go binary, copied before execution')
    parser.add_argument('--new-binary', type=Path, help='gate-aware candidate Go binary, copied before execution')
    parser.add_argument('--work-dir', type=Path, required=True, help='new absolute private laboratory directory')
    parser.add_argument('--self-test', action='store_true', help='exercise only the isolated fixture API, without binaries')
    parser.add_argument('--legacy-smoke', action='store_true', help='start only the old binary against the fixture API')
    args = parser.parse_args(argv)
    if args.self_test and args.legacy_smoke:
        parser.error('--self-test and --legacy-smoke are mutually exclusive')
    if args.legacy_smoke and (args.old_binary is None or args.new_binary is not None):
        parser.error('--legacy-smoke requires only --old-binary')
    if not args.self_test and not args.legacy_smoke and (args.old_binary is None or args.new_binary is None):
        parser.error('--old-binary and --new-binary are required except with --self-test')
    if args.self_test and (args.old_binary is not None or args.new_binary is not None):
        parser.error('--self-test does not execute binaries')
    root = args.work_dir
    if not root.is_absolute() or os.path.normpath(str(root)) != str(root) or root.exists():
        parser.error('--work-dir must be a new normalized absolute path')
    if not root.parent.is_dir() or root.parent.resolve() != root.parent:
        parser.error('--work-dir parent must exist without symlinks')
    root.mkdir(mode=0o700)
    original_cwd = os.getcwd()
    fixture = None
    summary = {'status': 'failed', 'started_at': stamp(), 'fixture_only': True}
    try:
        os.chdir(root)
        fixture, fixture_url = start_fixture(root)
        if args.self_test:
            summary.update(fixture_self_test(fixture_url, fixture.pid))
        else:
            for path in (args.old_binary,) if args.legacy_smoke else (args.old_binary, args.new_binary):
                if not path.is_absolute() or not path.is_file():
                    raise RuntimeError('binary paths must be absolute existing files')
            spec = importlib.util.spec_from_file_location('fixture_multica_update', Path(__file__).with_name('multica_update.py'))
            coordinator = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(coordinator)
            if args.legacy_smoke:
                summary.update(legacy_smoke(root / 'legacy-smoke', args.old_binary, fixture_url, fixture.pid, coordinator))
            else:
                success = rehearse_case(root / 'success', args.old_binary, args.new_binary, fixture_url, fixture.pid, coordinator, fail_start=False)
                failure = rehearse_case(root / 'failure', args.old_binary, args.new_binary, fixture_url, fixture.pid, coordinator, fail_start=True)
                summary.update(status='passed', fixture_server_pid=fixture.pid, cases=[success, failure],
                               limitation='Isolated fake API and fake systemctl; not real Server, installed systemd or production evidence.')
        return 0
    except Exception as error:
        summary['error'] = '%s: %s' % (type(error).__name__, error)
        return 1
    finally:
        if fixture is not None:
            try:
                control_url = fixture_url + '/control/release'
                request(control_url, {})
            except (OSError, ValueError):
                pass
            stop_fixture(fixture)
            summary['fixture_server_exit_code'] = fixture.returncode
        summary['finished_at'] = stamp()
        write_json(root / 'result.json', summary)
        os.chdir(original_cwd)
        print(json.dumps(summary, sort_keys=True))


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == '--fixture-worker':
        fixture_worker(sys.argv[2])
    else:
        sys.exit(main())
