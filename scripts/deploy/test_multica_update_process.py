"""Real child processes and loopback fixtures; never uses a real systemd unit."""
import importlib.util
import datetime
import json
import os
from pathlib import Path
import signal
import shutil
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from types import SimpleNamespace
from unittest import mock

spec = importlib.util.spec_from_file_location('daemon_process_fixture', Path(__file__).with_name('update-daemon.py'))
u = importlib.util.module_from_spec(spec)
spec.loader.exec_module(u)


class ProcessFixture(unittest.TestCase):
    def test_unreadable_cgroup_is_not_treated_as_empty(self):
        with mock.patch.object(u.os, 'lstat', side_effect=PermissionError('fixture')):
            with self.assertRaises(PermissionError):
                u.cgroup_members('/fixture.service')

    def test_observer_uses_approved_private_ca_in_both_execution_modes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cert, key = root / 'ca.pem', root / 'key.pem'
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(key),
                            '-out', str(cert), '-days', '1', '-subj', '/CN=fixture',
                            '-addext', 'subjectAltName=IP:127.0.0.1'], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            class Server(BaseHTTPRequestHandler):
                def do_GET(self):
                    body = json.dumps([{'id': 'runtime', 'daemon_id': 'daemon', 'status': 'online',
                                        'last_seen_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}]).encode()
                    self.send_response(200); self.end_headers(); self.wfile.write(body)
                def log_message(self, *args): pass
            server = ThreadingHTTPServer(('127.0.0.1', 0), Server)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            server.socket = context.wrap_socket(server.socket, server_side=True)
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            observer = root / 'observer'
            observer.write_text('#!/usr/bin/python3\nimport urllib.request\nprint(urllib.request.urlopen(%r).read().decode())\n' % ('https://127.0.0.1:%d/' % server.server_port))
            observer.chmod(0o755)
            owner = os.geteuid()
            account = SimpleNamespace(pw_dir=str(root), pw_gid=os.getgid())
            args = SimpleNamespace(owner_uid=owner, profile='', cli=str(observer),
                                   observer_ca_cert_file=str(cert), observer_ca_sha256=u.sha(str(cert)))
            try:
                with mock.patch.object(u.pwd, 'getpwuid', return_value=account), mock.patch.dict(os.environ, {}, clear=True):
                    operation = u.Updater(args)
                    operation.base = {'daemon_id': 'daemon', 'workspaces': {'workspace': ['runtime']}}
                    self.assertIn('runtime', operation.runtimes()['workspace'])
                    real_run = subprocess.run
                    calls = []
                    def owner_run(command, **kwargs):
                        calls.append(kwargs.copy())
                        for key in ('user', 'group', 'extra_groups'):
                            kwargs.pop(key, None)
                        return real_run(command, **kwargs)
                    with mock.patch.object(u.os, 'geteuid', return_value=owner + 1), mock.patch.object(u.subprocess, 'run', side_effect=owner_run):
                        self.assertIn('runtime', operation.runtimes()['workspace'])
                    self.assertEqual(calls[0]['user'], owner)
                    for name in ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE'):
                        self.assertEqual(calls[0]['env'][name], str(cert))
                    args.observer_ca_sha256 = '0' * 64
                    with self.assertRaises(u.Refusal):
                        operation.runtimes()
            finally:
                server.shutdown()
                server.server_close()
                thread.join()

    def test_normal_path_rejects_actual_pre_gate_binary_and_health(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, binary = root / 'legacy.c', root / 'legacy'
            source.write_text(r'''
#include <stdio.h>
int main(void) {
    puts("{\"version\":\"1.0.0\",\"commit\":\"unknown\"}");
    return 0;
}
''')
            subprocess.run(['gcc', str(source), '-o', str(binary)], check=True,
                           stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            with self.assertRaises(u.Refusal):
                u.version(str(binary))
            operation = u.Updater(SimpleNamespace(profile='', unit='fixture.service'))
            health = {'status': 'running', 'profile': '', 'daemon_id': 'daemon', 'pid': 123}
            with mock.patch.object(operation, 'unit_pid', return_value=('active', 123)):
                with self.assertRaisesRegex(u.Refusal, 'does not support maintenance gate'):
                    operation.inspect(health)

    def test_update_and_failed_boot_restore_with_three_heartbeats(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            home, bindir = root / 'home', root / 'bin'
            (home / '.multica').mkdir(parents=True)
            bindir.mkdir()
            rootstat = os.stat(root)
            self.assertEqual(rootstat.st_uid, os.getuid())
            source = root / 'fake.c'
            source.write_text(r'''
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
int main(int argc, char **argv) {
    if (argc > 1 && !strcmp(argv[1], "version")) {
#ifdef LEGACY
        puts("{\"version\":\"" VER "\",\"commit\":\"unknown\"}");
#else
        puts("{\"version\":\"" VER "\",\"build_commit\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"daemon_maintenance_gate\":\"v1\"}");
#endif
        return 0;
    }
    for (int i = 1; i < argc; i++) if (!strcmp(argv[i], "runtime")) {
        struct timespec now; struct tm utc; char stamp[32];
        clock_gettime(CLOCK_REALTIME, &now); gmtime_r(&now.tv_sec, &utc);
        strftime(stamp, sizeof(stamp), "%Y-%m-%dT%H:%M:%S", &utc);
        printf("[{\"id\":\"runtime\",\"daemon_id\":\"daemon\",\"status\":\"online\",\"last_seen_at\":\"%s.%09ldZ\"}]\n", stamp, now.tv_nsec);
        return 0;
    }
#ifdef BAD
    return 91;
#else
    for (;;) sleep(1);
#endif
}
''')
            current, candidate, next_candidate, bad, legacy = root / 'multica', root / 'candidate', root / 'next-candidate', root / 'bad', root / 'legacy'
            for path, version, is_bad in ((current, '1.0.0', False), (candidate, '2.0.0', False),
                                         (next_candidate, '3.0.0', False), (bad, '4.0.0', True), (legacy, '1.0.0', False)):
                command = ['gcc', '-O0', '-DVER="%s"' % version]
                if is_bad:
                    command.append('-DBAD=1')
                if path == legacy:
                    command.append('-DLEGACY=1')
                subprocess.run(command + [str(source), '-o', str(path)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            shutil.copyfile(legacy, current)
            wrapper = root / 'wrapper'
            ca = root / 'ca.pem'
            ca.write_bytes(b'fixture ca')
            wrapper.write_text('#!/bin/sh\n' + ''.join('export %s="%s"\n' % (name, ca) for name in
                ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')) + 'exec "' + str(current) + '" "$@"\n')
            wrapper.chmod(0o755)
            pidfile = root / 'pid'
            systemctl = bindir / 'systemctl'
            systemctl.write_text('''#!/usr/bin/python3
import os, pathlib, signal, subprocess, sys, urllib.request
root = pathlib.Path(os.environ['FIXTURE_ROOT'])
pidfile = root / 'pid'
args = sys.argv[2:]
if args[0] == 'show':
    pid = int(pidfile.read_text()) if pidfile.exists() else 0
    alive = pid and pathlib.Path('/proc/%d/exe' % pid).exists()
    state = 'active' if alive else ('failed' if pidfile.exists() else 'inactive')
    start = '{ path=%s ; argv[]=%s daemon ; ignore_errors=no ; start_time=%s ; stop_time=%s ; pid=%s ; code=(null) ; status=0 }' % (root / 'wrapper', root / 'wrapper', pid if alive else 0, 0 if alive else 5, pid if alive else 0)
    print(start if '--property=ExecStart' in args else ('control-group' if '--property=KillMode' in args else (('/fixture.service' if alive else '') if '--property=ControlGroup' in args else (state if '--property=ActiveState' in args else (pid if alive else 0)))))
elif args[0] == 'stop':
    urllib.request.urlopen('http://127.0.0.1:' + os.environ['FIXTURE_SERVER_PORT'] + '/miss').read()
    if pidfile.exists():
        pid = int(pidfile.read_text())
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        pidfile.unlink()
elif args[0] == 'start':
    p = subprocess.Popen([str(root / 'wrapper'), 'daemon'], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    pidfile.write_text(str(p.pid))
else:
    raise SystemExit(1)
''')
            systemctl.chmod(0o755)
            observer = root / 'observer'
            observer.write_text('''#!/usr/bin/python3
import datetime, json, sys
if len(sys.argv) > 1 and sys.argv[1] == 'version':
    print(json.dumps({'version': '3.0.0', 'build_commit': 'a' * 40, 'daemon_maintenance_gate': 'v1'}))
else:
    print(json.dumps([{'id':'runtime','daemon_id':'daemon','status':'online','last_seen_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}]))
''')
            observer.chmod(0o755)
            gate = home / '.multica' / 'daemon.maintenance'
            class Health(BaseHTTPRequestHandler):
                def do_GET(self):
                    try:
                        pid = int(pidfile.read_text())
                        exe = os.readlink('/proc/%d/exe' % pid)
                        version = json.loads(subprocess.check_output([exe, 'version', '--output', 'json']))
                        blocked = gate.is_dir()
                        data = {'status': 'running', 'profile': '', 'daemon_id': 'daemon', 'pid': pid,
                                'cli_version': version['version'], 'cli_commit': version.get('build_commit', ''),
                                'active_task_count': 0, 'workspaces': [{'id': 'workspace', 'runtimes': ['runtime']}]}
                        if (root / 'wrong-candidate-identity').exists() and version['version'] == '2.0.0':
                            data['daemon_id'] = 'wrong-daemon'
                        if version.get('daemon_maintenance_gate') == 'v1':
                            data['maintenance_gate'] = {'supported': True, 'path': str(gate), 'blocked': blocked,
                                                        'ready': blocked, 'claims_in_flight': 0}
                            fixture_state = json.loads(urllib.request.urlopen(fixture_url + '/status').read())
                            data['update_attestation'] = {'binary_sha256': u.sha('/proc/%d/exe' % pid),
                                'process_started_at': '2020-01-01T00:00:00.123456789Z',
                                'runtime_heartbeat_acks': {'runtime': 3}, 'task_receipts': fixture_state['receipts']}
                        payload = json.dumps(data).encode()
                        self.send_response(200)
                        self.end_headers()
                        self.wfile.write(payload)
                    except (OSError, ValueError, subprocess.SubprocessError):
                        self.send_response(503)
                        self.end_headers()
                def log_message(self, *args):
                    pass
            server = ThreadingHTTPServer(('127.0.0.1', 0), Health)
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            fixture_script = root / 'fixture-server.py'
            fixture_script.write_text('''import datetime, json
from http.server import BaseHTTPRequestHandler, HTTPServer
state = {'missed': 0, 'redriven': 0, 'receipts': []}
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/miss': state['missed'] += 1
        if self.path == '/redrive' and state['missed'] > state['redriven']:
            state['redriven'] += 1
            now = datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%S.%f') + '123Z'
            state['receipts'].append({'task_id': 'fixture-task-%d' % state['redriven'], 'runtime_id': 'runtime', 'completed_at': now, 'ack_at': now})
        body = json.dumps(state).encode()
        self.send_response(200); self.end_headers(); self.wfile.write(body)
    def log_message(self, *args): pass
server = HTTPServer(('127.0.0.1', 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
''')
            fixture_server = subprocess.Popen([sys.executable, str(fixture_script)], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
            fixture_port = fixture_server.stdout.readline().strip()
            fixture_url = 'http://127.0.0.1:' + fixture_port
            first = subprocess.Popen([str(wrapper), 'daemon'], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            pidfile.write_text(str(first.pid))
            env = {'PATH': str(bindir) + ':/usr/bin:/bin', 'FIXTURE_ROOT': str(root), 'FIXTURE_SERVER_PORT': fixture_port}
            account = SimpleNamespace(pw_dir=str(home), pw_gid=os.getgid())
            try:
                def fixture_executable_users(executable):
                    if not pidfile.exists():
                        return []
                    pid = int(pidfile.read_text())
                    try:
                        return [{'pid': pid, 'uid': os.getuid()}] if os.path.samefile('/proc/%d/exe' % pid, executable) else []
                    except FileNotFoundError:
                        return []
                with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(u.pwd, 'getpwuid', return_value=account), mock.patch.object(u.signal, 'signal'), mock.patch.object(u, 'cgroup_members', return_value=[]), mock.patch.object(u, 'executable_users', side_effect=fixture_executable_users), mock.patch.object(u.ssl, 'create_default_context'):
                    def operation(target, version, evidence, legacy_mode=False, legacy_target=False):
                        s = wrapper.stat()
                        legacy_baseline = {'entrypoint': {'path': str(wrapper), 'sha256': u.sha(str(wrapper)),
                            'mode': s.st_mode & 0o7777, 'uid': s.st_uid, 'gid': s.st_gid, 'device': s.st_dev,
                            'inode': s.st_ino, 'links': s.st_nlink}, 'exec_start': 'historical evidence',
                            'exec_start_configuration': {'path': str(wrapper), 'argv': str(wrapper) + ' daemon', 'ignore_errors': 'no'},
                            'kill_mode': 'control-group', 'control_group': '/fixture.service',
                            'wrapper_ca_path': str(ca),
                            'binary_sha256': u.sha(str(target)) if legacy_target else u.sha(str(current))}
                        expected_base = {'pid': int(pidfile.read_text()), 'daemon_id': 'daemon', 'version': '2.0.0' if legacy_target else '1.0.0',
                            'commit': 'a' * 40 if legacy_target else None, 'sha256': u.sha(str(current)), 'workspaces': {'workspace': ['runtime']},
                            'profile_inode': [os.stat(home / '.multica').st_dev, os.stat(home / '.multica').st_ino]}
                        args = SimpleNamespace(profile='', unit='fixture.service', systemd_scope='user', staged=str(target),
                                sha256=u.sha(str(target)), cli=str(observer), health_url='http://127.0.0.1:%d/health' % server.server_port,
                                version=version, commit=None if legacy_target else 'a' * 40, evidence_dir=str(evidence), layout='binary',
                                entrypoint=None, binary=None, no_shared_profiles=True, timeout=1.5,
                                heartbeat_samples=3, heartbeat_interval=0.05, legacy_bootstrap=legacy_mode,
                                legacy_baseline=legacy_baseline, legacy_wrapper=str(wrapper) if legacy_mode or legacy_target else None,
                                legacy_ca_sha256=u.sha(str(ca)),
                                observer_ca_cert_file=str(ca), observer_ca_sha256=u.sha(str(ca)),
                                legacy_target=legacy_target, expected_base=expected_base if legacy_mode or legacy_target else None)
                        instance = u.Updater(args)
                        return instance, u.perform(instance)
                    self.assertNotIn('maintenance_gate', json.loads(urllib.request.urlopen('http://127.0.0.1:%d/health' % server.server_port).read()))
                    original_pid = int(pidfile.read_text())
                    with mock.patch.object(u, 'executable_users', return_value=[{'pid': original_pid, 'uid': os.getuid()}, {'pid': original_pid + 1000000, 'uid': os.getuid() + 1}]):
                        shared, shared_code = operation(candidate, '2.0.0', root / 'evidence-shared', legacy_mode=True)
                    self.assertNotEqual(shared_code, 0)
                    self.assertEqual(shared.result['status'], 'refused', shared.result)
                    self.assertEqual(int(pidfile.read_text()), original_pid)
                    self.assertEqual(u.sha(str(current)), u.sha(str(legacy)))
                    self.assertFalse(gate.exists())
                    good, code = operation(candidate, '2.0.0', root / 'evidence-good', legacy_mode=True)
                    self.assertEqual(code, 0, good.result)
                    self.assertEqual(good.result['status'], 'updated')
                    self.assertEqual((root / 'evidence-good' / 'original-wrapper').read_bytes(), wrapper.read_bytes())
                    self.assertIsNone(fixture_server.poll())
                    missed = json.loads(urllib.request.urlopen(fixture_url + '/status').read())
                    self.assertGreaterEqual(missed['missed'], 1)
                    redriven = json.loads(urllib.request.urlopen(fixture_url + '/redrive').read())
                    self.assertEqual(redriven['redriven'], 1)
                    coordinator_spec = importlib.util.spec_from_file_location('fixture_coordinator', Path(__file__).with_name('multica_update.py'))
                    coordinator = importlib.util.module_from_spec(coordinator_spec)
                    coordinator_spec.loader.exec_module(coordinator)
                    receipt = coordinator.verify_receipt(good.health(), {'daemon_id': 'daemon', 'profile': '',
                        'commit': 'a' * 40, 'sha256': u.sha(str(current)), 'gate_released_at': good.result['gate_released_at'],
                        'runtime_ids': ['runtime']}, datetime.datetime.now(datetime.timezone.utc))
                    self.assertEqual(receipt['task_id'], 'fixture-task-1')
                    self.assertEqual(len((root / 'evidence-good' / 'heartbeats.jsonl').read_text().splitlines()), 3)
                    self.assertFalse(gate.exists())
                    rollback, code = operation(root / 'evidence-good' / 'original-entrypoint', None, root / 'evidence-manual-rollback', legacy_target=True)
                    self.assertEqual(code, 0, rollback.result)
                    self.assertEqual(rollback.result['status'], 'legacy_rollback_pending_manual')
                    self.assertEqual(u.sha(str(current)), u.sha(str(legacy)))
                    self.assertFalse(gate.exists())
                    repeat, code = operation(candidate, '2.0.0', root / 'evidence-repeat', legacy_mode=True)
                    self.assertEqual(code, 0, repeat.result)
                    again, code = operation(next_candidate, '3.0.0', root / 'evidence-again')
                    self.assertEqual(code, 0, again.result)
                    self.assertEqual(again.result['status'], 'updated')
                    self.assertEqual(u.sha(str(current)), u.sha(str(next_candidate)))
                    self.assertEqual(len((root / 'evidence-again' / 'heartbeats.jsonl').read_text().splitlines()), 3)
                    self.assertFalse(gate.exists())
                    # Exercise the real planner and transaction with distinct v3 observer and v2 target.
                    ledger_path = root / 'rollback-ledger'
                    previous_path = root / 'evidence-again' / 'original-entrypoint'
                    current_asset = {'path': str(current), 'sha256': u.sha(str(current)), 'version': '3.0.0',
                                     'commit': 'a' * 40, 'data_compatibility': 'multica-profile-v1'}
                    previous_asset = {'path': str(previous_path), 'sha256': u.sha(str(previous_path)), 'version': '2.0.0',
                                      'commit': 'a' * 40, 'data_compatibility': 'multica-profile-v1'}
                    config = {'owner_uid': os.geteuid(), 'profile': '', 'unit': 'fixture.service', 'systemd_scope': 'user',
                              'cli': str(observer), 'staged': str(next_candidate), 'search_roots': [str(root)],
                              'health_url': 'http://127.0.0.1:%d/health' % server.server_port, 'ledger': str(ledger_path),
                              'evidence_dir': str(root / 'unused-evidence'), 'asset_root': str(root),
                              'controller_machine_id_sha256': 'f' * 64, 'observer_ca_cert_file': str(ca),
                              'observer_ca_sha256': u.sha(str(ca))}
                    coordinator.write_ledger(str(ledger_path), {'schema': 1, 'status': 'accepted', 'current': current_asset,
                        'previous': previous_asset, 'assets': [current_asset, previous_asset],
                        'transaction': {'inventory': config, 'observer_sha256': u.sha(str(observer))}})
                    real_commonpath = os.path.commonpath
                    def fixture_commonpath(paths):
                        if tuple(paths) == (str(root), str(home / '.multica')):
                            return '/fixture-separate-profile'
                        return real_commonpath(paths)
                    original_updater = coordinator.updater.Updater
                    class FastUpdater(original_updater):
                        def __init__(self, args):
                            if hasattr(args, 'heartbeat_interval'):
                                args.heartbeat_interval = 0.05
                                args.timeout = 2
                            super().__init__(args)
                    with mock.patch.object(coordinator, 'target_machine_fingerprint', return_value='e' * 64), \
                         mock.patch.object(coordinator, 'scan_roots', return_value=[]), \
                         mock.patch.object(coordinator, 'scan_programs', return_value=[]), \
                         mock.patch.object(coordinator, 'process_inventory', side_effect=lambda uid: [{'pid': int(pidfile.read_text()), 'exe': str(current)}]), \
                         mock.patch.object(coordinator.updater, 'executable_users', side_effect=fixture_executable_users), \
                         mock.patch.object(coordinator.os.path, 'commonpath', side_effect=fixture_commonpath), \
                         mock.patch.object(coordinator.updater, 'Updater', FastUpdater):
                        plan, manifest = coordinator.rollback_plan(str(ledger_path), str(root / 'evidence-real-rollback'))
                        self.assertEqual(plan['version'], '2.0.0')
                        self.assertEqual(coordinator.apply_plan(plan, manifest, rollback=True)['status'], 'awaiting_acceptance')
                        self.assertEqual(u.sha(str(current)), u.sha(str(candidate)))
                        urllib.request.urlopen(fixture_url + '/redrive').read()
                        self.assertEqual(coordinator.accept_transaction(str(ledger_path))['status'], 'accepted')
                        observer_bytes = observer.read_bytes()
                        observer.write_bytes(observer_bytes + b'\n# tampered\n')
                        with self.assertRaises(coordinator.updater.Refusal):
                            coordinator.rollback_plan(str(ledger_path), str(root / 'evidence-tampered-observer'))
                        observer.write_bytes(observer_bytes)
                        plan_again, manifest_again = coordinator.rollback_plan(str(ledger_path), str(root / 'evidence-second-rollback'))
                        self.assertEqual(plan_again['version'], '3.0.0')
                        self.assertEqual(coordinator.apply_plan(plan_again, manifest_again, rollback=True)['status'], 'awaiting_acceptance')
                        self.assertEqual(u.sha(str(current)), u.sha(str(next_candidate)))
                        urllib.request.urlopen(fixture_url + '/redrive').read()
                        self.assertEqual(coordinator.accept_transaction(str(ledger_path))['status'], 'accepted')
                        bad_observer = root / 'bad-observer'
                        shutil.copy2(bad, bad_observer)
                        failed_config = dict(coordinator.read_ledger(str(ledger_path))['transaction']['inventory'])
                        failed_config.update(cli=str(bad_observer), staged=str(bad), evidence_dir=str(root / 'evidence-managed-failure'))
                        failed_manifest = {'version': '4.0.0', 'commit': 'a' * 40,
                            'data_compatibility': 'multica-profile-v1', 'assets': [{'role': 'binary',
                            'size': bad.stat().st_size, 'sha256': u.sha(str(bad))}]}
                        failed_plan = coordinator.build_plan(failed_config, failed_manifest, 'd' * 64)
                        managed_failure = coordinator.apply_plan(failed_plan, failed_manifest)
                        self.assertEqual(managed_failure['status'], 'rolled_back_pending_acceptance', managed_failure)
                        self.assertEqual(u.sha(str(current)), u.sha(str(next_candidate)))
                        pending_plan, _ = coordinator.rollback_plan(str(ledger_path), str(root / 'evidence-pending-failure'))
                        self.assertEqual(pending_plan['version'], '2.0.0')
                        self.assertEqual(coordinator.read_ledger(str(ledger_path))['status'], 'awaiting_rollback_acceptance')
                        urllib.request.urlopen(fixture_url + '/redrive').read()
                        real_sleep = time.sleep
                        with mock.patch.object(coordinator.time, 'sleep', side_effect=lambda seconds: real_sleep(min(seconds, 0.05))):
                            self.assertEqual(coordinator.accept_transaction(str(ledger_path))['status'], 'rolled_back')
                        plan_after_failure, _ = coordinator.rollback_plan(str(ledger_path), str(root / 'evidence-after-managed-failure'))
                        self.assertEqual(plan_after_failure['version'], '2.0.0')
                        bad = bad_observer  # Cleanup retired the failed staged copy.
                    failed, code = operation(bad, '4.0.0', root / 'evidence-bad')
                    self.assertNotEqual(code, 0)
                    self.assertEqual(failed.result['status'], 'rolled_back_verified', failed.result)
                    self.assertEqual(u.sha(str(current)), u.sha(str(next_candidate)))
                    self.assertEqual(len((root / 'evidence-bad' / 'heartbeats.jsonl').read_text().splitlines()), 3)
                    self.assertFalse(gate.exists())
                    u.run(failed.systemctl('stop', 'fixture.service'))
                    replacement = root / 'legacy-copy'
                    shutil.copy2(legacy, replacement)
                    os.replace(replacement, current)
                    u.run(failed.systemctl('start', 'fixture.service'))
                    failed_legacy, code = operation(bad, '4.0.0', root / 'evidence-legacy-bad', legacy_mode=True)
                    self.assertNotEqual(code, 0)
                    self.assertEqual(failed_legacy.result['status'], 'legacy_restored', failed_legacy.result)
                    self.assertEqual(u.sha(str(current)), u.sha(str(legacy)))
                    self.assertFalse(gate.exists())
                    self.assertIsNone(fixture_server.poll())
                    redriven = json.loads(urllib.request.urlopen(fixture_url + '/redrive').read())
                    self.assertGreaterEqual(redriven['redriven'], 2)
                    marker = root / 'wrong-candidate-identity'
                    marker.touch()
                    def live_cgroup(_):
                        return [int(pidfile.read_text())] if pidfile.exists() else []
                    with mock.patch.object(u, 'cgroup_members', side_effect=live_cgroup):
                        wrong, code = operation(candidate, '2.0.0', root / 'evidence-wrong-identity', legacy_mode=True)
                    self.assertNotEqual(code, 0)
                    self.assertEqual(wrong.result['status'], 'legacy_restored', wrong.result)
                    self.assertEqual(u.sha(str(current)), u.sha(str(legacy)))
                    marker.unlink()
                    with mock.patch.object(u, 'cgroup_members', return_value=[777]):
                        blocked, code = operation(candidate, '2.0.0', root / 'evidence-cgroup-blocked', legacy_mode=True)
                    self.assertNotEqual(code, 0)
                    self.assertEqual(blocked.result['status'], 'blocked')
                    self.assertIn('cgroup still has processes', blocked.result['error'])
                    self.assertEqual(u.sha(str(current)), u.sha(str(legacy)))
            finally:
                if pidfile.exists():
                    pid = int(pidfile.read_text())
                    try:
                        os.kill(pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                try:
                    first.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    first.terminate()
                    first.wait(timeout=2)
                server.shutdown()
                server.server_close()
                thread.join()
                fixture_server.terminate()
                fixture_server.wait(timeout=2)
                fixture_server.stdout.close()


if __name__ == '__main__':
    unittest.main()
