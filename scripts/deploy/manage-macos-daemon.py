#!/usr/bin/env python3
"""Gate-protected macOS LaunchAgent handoff. Run as the profile owner, outside a daemon task."""
import argparse
import datetime
import functools
import fcntl
import hashlib
import importlib.util
import json
import os
import plistlib
import re
import signal
import stat
import subprocess
import sys
import time
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

spec = importlib.util.spec_from_file_location('macos_stage', Path(__file__).with_name('stage-macos-daemon.py'))
stage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stage)
Refusal = stage.Refusal
require = stage.require


def command(argv, *, timeout=30, check=True):
    try:
        p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise Refusal('system command failed or timed out')
    if check:
        require(p.returncode == 0, 'system command failed')
    return p


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def atomic(path, data):
    temp = path + '.new-' + uuid.uuid4().hex
    fd = os.open(temp, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, 'wb') as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(temp, path)
        sync_dir(os.path.dirname(path))
    finally:
        if os.path.lexists(temp):
            os.unlink(temp)


def block_signals(fn):
    @functools.wraps(fn)
    def wrapped(*args, **kwargs):
        old = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            return fn(*args, **kwargs)
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, old)
    return wrapped


def workspace_map(h):
    rows = h.get('workspaces')
    require(isinstance(rows, list) and rows, 'workspace runtimes missing')
    result = {}
    for row in rows:
        require(isinstance(row, dict) and isinstance(row.get('id'), str) and row['id'] and isinstance(row.get('runtimes'), list) and row['runtimes'], 'invalid workspace runtimes')
        ids = row['runtimes']
        require(all(isinstance(x, str) and x for x in ids) and len(set(ids)) == len(ids) and row['id'] not in result, 'invalid runtime set')
        result[row['id']] = sorted(ids)
    return result


def timestamp(s):
    try:
        value = datetime.datetime.fromisoformat(s.replace('Z', '+00:00'))
        require(value.tzinfo is not None, 'heartbeat lacks timezone')
        return value
    except (AttributeError, ValueError):
        raise Refusal('invalid heartbeat')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise Refusal('health redirect refused')


class MacOS:
    def __init__(self, a):
        self.a = a
        self.domain = 'gui/%d' % os.getuid()

    def health(self):
        hint = None
        try:
            path = os.path.join(os.path.realpath(self.a.profile_dir), 'daemon.port')
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd, 'rb') as f:
                st = os.fstat(f.fileno())
                if stat.S_ISREG(st.st_mode) and st.st_uid == os.getuid() and st.st_size <= 4096:
                    record = json.load(f)
                    if type(record.get('port')) is int and 1 <= record['port'] <= 65535 and type(record.get('pid')) is int and record['pid'] > 1:
                        hint = record
        except (OSError, ValueError, AttributeError):
            pass
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        candidates = []
        if hint:
            candidates.append(('http://127.0.0.1:%d/health' % hint['port'], hint['pid']))
        candidates.append((self.a.health_url, None))
        for url, expected_pid in candidates:
            try:
                with opener.open(url, timeout=3) as response:
                    require(response.status == 200, 'health unavailable')
                    data = json.load(response)
                if isinstance(data, dict) and data.get('profile') == self.a.profile and (expected_pid is None or data.get('pid') == expected_pid):
                    return data
            except (OSError, ValueError, Refusal):
                continue
        raise Refusal('health unavailable or profile mismatch')

    def process(self, pid):
        try:
            return self._process(pid)
        except (Refusal, OSError):
            # A graceful exit can race lsof or the final identity probe. Only
            # an independent, successful absence check permits the handoff.
            p = command(['/bin/ps', '-p', str(pid), '-o', 'pid='], check=False)
            if p.returncode == 1 and not p.stdout.strip() and not p.stderr.strip():
                return None
            raise

    def _process(self, pid):
        require(type(pid) is int and pid > 1, 'invalid PID')
        def ps_identity():
            p = command(['/bin/ps', '-p', str(pid), '-o', 'uid=', '-o', 'lstart='], check=False)
            if p.returncode == 1 and not p.stdout.strip() and not p.stderr.strip():
                return None
            require(p.returncode == 0 and p.stdout.strip(), 'cannot determine process state')
            fields = p.stdout.decode('utf-8', 'strict').strip().split(None, 1)
            require(len(fields) == 2 and fields[0] == str(os.getuid()), 'PID owner mismatch')
            return fields[1]
        started = ps_identity()
        if started is None:
            return None
        executable = command(['/bin/ps', '-p', str(pid), '-o', 'comm=']).stdout.decode('utf-8', 'strict').strip()
        require(executable.startswith('/'), 'process executable unavailable')
        lines = command(['/usr/sbin/lsof', '-a', '-p', str(pid), '-d', 'txt', '-FpfinD']).stdout.decode('utf-8', 'strict').splitlines()
        records = []
        current = None
        for line in lines:
            if line.startswith('f'):
                current = {}
                records.append(current)
            elif current is not None and line[:1] in ('n', 'i', 'D'):
                require(line[0] not in current, 'ambiguous executable record')
                current[line[0]] = line[1:]
        matches = [r for r in records if 'n' in r and os.path.realpath(r['n']) == os.path.realpath(executable)]
        require(len(matches) == 1 and all(k in matches[0] for k in ('i', 'D')), 'executable inode evidence unavailable')
        try:
            ino = int(matches[0]['i'], 10)
            dev = int(matches[0]['D'], 16)
        except ValueError:
            raise Refusal('invalid executable inode evidence')
        fd = os.open(executable, os.O_RDONLY)
        try:
            before = os.fstat(fd)
            require(stat.S_ISREG(before.st_mode) and (before.st_dev, before.st_ino) == (dev, ino), 'running executable inode differs from path')
            h = hashlib.sha256()
            while True:
                chunk = os.read(fd, 1024 * 1024)
                if not chunk: break
                h.update(chunk)
            after = os.fstat(fd)
            pathstat = os.stat(executable)
            require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns) and (pathstat.st_dev, pathstat.st_ino) == (dev, ino), 'executable changed during inspection')
        finally:
            os.close(fd)
        require(ps_identity() == started and command(['/bin/ps', '-p', str(pid), '-o', 'comm=']).stdout.decode('utf-8', 'strict').strip() == executable, 'process changed during inspection')
        return (started, executable, dev, ino, h.hexdigest())

    def version(self, path):
        return command([path, 'version', '--output', 'json'])

    def app_absent(self):
        p = command(['/bin/ps', '-axo', 'uid=,command='])
        for raw in p.stdout.decode('utf-8', 'strict').splitlines():
            fields = raw.strip().split(None, 1)
            if len(fields) == 2 and fields[0] == str(os.getuid()) and (fields[1] == self.a.app_executable or fields[1].startswith(self.a.app_executable + ' ')):
                return False
        return True

    def job_pid(self):
        p = command(['/bin/launchctl', 'print', self.domain + '/' + self.a.label], check=False)
        if p.returncode:
            # Confirm the GUI domain itself exists before interpreting absence.
            command(['/bin/launchctl', 'print', self.domain])
            require(b'Could not find service' in p.stderr, 'cannot determine LaunchAgent state')
            return None
        match = re.search(rb'^\s*pid\s*=\s*(\d+)\s*$', p.stdout, re.MULTILINE)
        return int(match.group(1)) if match else 0

    def terminate(self, pid, identity):
        require(self.process(pid) == identity, 'process changed before SIGTERM')
        os.kill(pid, signal.SIGTERM)

    def bootout(self):
        command(['/bin/launchctl', 'bootout', self.domain + '/' + self.a.label], timeout=self.a.timeout)

    def bootstrap(self, plist):
        command(['/bin/launchctl', 'bootstrap', self.domain, plist], timeout=self.a.timeout)

    def start_app_daemon(self, executable, environment):
        # The old App remains closed until verified; this starts its original CLI with the same profile.
        subprocess.Popen([executable, '--profile', self.a.profile, 'daemon', 'start', '--foreground', '--no-auto-update', '--no-auto-reload'], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True, close_fds=True, env=environment)

    def runtimes(self, base):
        out = {}
        for ws, ids in base['workspaces'].items():
            p = command([self.a.cli, '--profile', self.a.profile, '--workspace-id', ws, 'runtime', 'list', '--output', 'json'])
            try:
                rows = json.loads(p.stdout)
            except ValueError:
                raise Refusal('invalid runtime response')
            require(isinstance(rows, list), 'invalid runtime response')
            found = {}
            for row in rows:
                if isinstance(row, dict) and row.get('id') in ids:
                    rid = row['id']
                    require(rid not in found and row.get('daemon_id') == base['daemon_id'] and row.get('status') == 'online', 'runtime identity mismatch')
                    found[rid] = timestamp(row.get('last_seen_at'))
            require(set(found) == set(ids), 'runtime set incomplete')
            out[ws] = found
        return out

    def sleep(self, seconds):
        time.sleep(seconds)


class Transaction:
    def __init__(self, a, os_adapter):
        self.a, self.os = a, os_adapter
        self.token = uuid.uuid4().hex
        self.gate = None
        self.changed = False
        self.old_stopped = False
        self.released = False
        self.profile_changed = False
        self.base = None
        self.old_marker = None
        self.old_plist = None
        self.result = 'refused'

    def profile_ok(self):
        try:
            s = os.stat(self.real_profile)
            valid = os.path.realpath(self.a.profile_dir) == self.real_profile and (s.st_dev, s.st_ino, s.st_uid) == self.profile_identity
        except OSError:
            valid = False
        if not valid:
            self.profile_changed = True
            raise Refusal('profile changed; gate protection unverified and claims may resume')

    def own_gate(self):
        self.profile_ok()
        s = os.lstat(self.gate)
        require(stat.S_ISDIR(s.st_mode) and (s.st_dev, s.st_ino, s.st_uid) == self.gate_identity, 'gate ownership changed')
        with open(os.path.join(self.gate, 'owner'), encoding='utf-8') as f:
            require(json.load(f) == {'id': self.token, 'pid': os.getpid()}, 'gate owner changed')

    @block_signals
    def gate_create(self):
        self.profile_ok()
        os.mkdir(self.gate, 0o700)
        s = os.lstat(self.gate)
        self.gate_identity = (s.st_dev, s.st_ino, s.st_uid)
        with open(os.path.join(self.gate, 'owner'), 'x', encoding='utf-8') as f:
            json.dump({'id': self.token, 'pid': os.getpid()}, f)
            f.flush()
            os.fsync(f.fileno())
        sync_dir(self.gate)
        sync_dir(self.real_profile)

    @block_signals
    def release(self):
        self.own_gate()
        os.unlink(os.path.join(self.gate, 'owner'))
        os.rmdir(self.gate)
        self.released = True
        sync_dir(self.real_profile)

    def check(self, h, target=None, ready=False, pid=None, managed=False):
        require(isinstance(h, dict) and h.get('status') == 'running' and h.get('profile') == self.a.profile and isinstance(h.get('daemon_id'), str) and h['daemon_id'], 'health identity mismatch')
        actual = h.get('pid')
        require(type(actual) is int and actual > 1 and (pid is None or pid == actual), 'PID mismatch')
        proc = self.os.process(actual)
        require(proc is not None, 'daemon process missing')
        gate = h.get('maintenance_gate')
        require(isinstance(gate, dict) and gate.get('supported') is True and gate.get('path') == self.reported_gate, 'maintenance gate unsupported or changed')
        if ready:
            self.own_gate()
            require(gate.get('blocked') is True and gate.get('ready') is True and gate.get('claims_in_flight') == 0 and h.get('active_task_count') == 0, 'gate not drained')
        if self.base:
            require(h['daemon_id'] == self.base['daemon_id'] and h.get('server_url') == self.base['server_url'] and workspace_map(h) == self.base['workspaces'], 'daemon, server or workspace identity changed')
        if target:
            require(h.get('cli_version') == target['version'] and h.get('cli_commit') == target['build_commit'] and proc[4] == target['sha256'] and os.path.realpath(proc[1]) == os.path.realpath(target['binary']), 'running executable identity mismatch')
        if managed:
            require(self.os.job_pid() == actual and h.get('lifecycle_manager') == 'launchd-pinned' and h.get('desktop_protocol') == 1, 'launchd ownership mismatch')
        return actual, proc

    def wait_health(self, target, managed):
        end = time.monotonic() + self.a.timeout
        while time.monotonic() < end:
            try:
                h = self.os.health()
                self.check(h, target=target, ready=True, managed=managed)
                return h
            except (Refusal, OSError):
                self.os.sleep(1)
        raise Refusal('timed out waiting for verified daemon')

    def verify(self, target, managed):
        h = self.wait_health(target, managed)
        pid, _ = self.check(h, target=target, ready=True, managed=managed)
        previous = self.os.runtimes(self.base)
        for _ in range(3):
            end = time.monotonic() + self.a.timeout
            while True:
                self.os.sleep(self.a.interval)
                self.check(self.os.health(), target=target, ready=True, pid=pid, managed=managed)
                now = self.os.runtimes(self.base)
                if all(now[w][r] > previous[w][r] for w in previous for r in previous[w]):
                    previous = now
                    with open(os.path.join(self.evidence, 'heartbeats.jsonl'), 'a', encoding='utf-8') as f:
                        json.dump({'pid': pid, 'version': target['version'], 'samples': {w: {r: str(v) for r, v in values.items()} for w, values in now.items()}}, f)
                        f.write('\n')
                        f.flush()
                        os.fsync(f.fileno())
                    break
                require(time.monotonic() < end, 'runtime heartbeat did not advance')
        self.check(self.os.health(), target=target, ready=True, pid=pid, managed=managed)

    def wait_stopped(self, pid):
        end = time.monotonic() + self.a.timeout
        while self.os.process(pid) is not None and time.monotonic() < end:
            self.os.sleep(1)
        require(self.os.process(pid) is None, 'old PID still exists; refusing bootstrap')

    def stop_original(self):
        pid, identity = self.check(self.os.health(), ready=True, pid=self.base['pid'], managed=self.old_managed)
        require(identity == self.base['process'], 'old process identity changed')
        self.own_gate()
        if self.old_managed:
            self.os.bootout()
            require(self.os.job_pid() is None, 'old job remains registered')
        else:
            require(self.os.app_absent(), 'Desktop App is still running')
            self.os.terminate(pid, identity)
        self.wait_stopped(pid)
        self.old_stopped = True

    def stop_candidate(self):
        self.own_gate()
        pid = self.os.job_pid()
        if pid is None:
            return
        if pid:
            proc = self.os.process(pid)
            require(proc is not None and os.path.realpath(proc[1]) == os.path.realpath(self.target['binary']) and proc[4] == self.target['sha256'], 'unknown launchd process; refusing rollback stop')
            h = self.os.health()
            g = h.get('maintenance_gate', {})
            require(h.get('pid') == pid and h.get('profile') == self.a.profile and h.get('status') == 'running' and g.get('supported') is True and g.get('path') == self.reported_gate and g.get('blocked') is True and g.get('ready') is True and g.get('claims_in_flight') == 0 and h.get('active_task_count') == 0 and h.get('cli_version') == self.target['version'] and h.get('cli_commit') == self.target['build_commit'] and h.get('lifecycle_manager') == 'launchd-pinned' and h.get('desktop_protocol') == 1, 'candidate is not proven drained; refusing rollback stop')
            self.own_gate()
            require(self.os.job_pid() == pid and self.os.process(pid) == proc, 'candidate changed before rollback stop')
        self.os.bootout()
        require(self.os.job_pid() is None, 'candidate job remains registered')
        if pid:
            self.wait_stopped(pid)

    def rollback(self):
        require(self.old_stopped, 'original stop not confirmed; manual inspection required')
        self.stop_candidate()
        self.profile_ok()
        with open(self.marker_path, 'rb') as f:
            require(f.read() == self.marker_bytes, 'marker changed during rollback')
        require(stage.digest(self.old_target['binary']) == self.old_target['sha256'], 'original executable changed; refusing rollback')
        if self.old_managed:
            atomic(self.plist_path, self.old_plist)
            atomic(self.marker_path, self.old_marker)
            self.os.bootstrap(self.plist_path)
            self.verify(self.old_target, True)
        else:
            environment = {key: self.plist_env[key] for key in ('HOME', 'PATH', 'MULTICA_CA_CERT_FILE')}
            environment.update({'MULTICA_DAEMON_AUTO_UPDATE': 'false', 'MULTICA_DAEMON_AUTO_RELOAD': 'false', 'MULTICA_LAUNCHED_BY': 'desktop', 'MULTICA_ZH_TW_DESKTOP_SELF_RELOAD': '1'})
            self.os.start_app_daemon(self.old_target['binary'], environment)
            self.verify(self.old_target, False)
            self.profile_ok()
            with open(self.marker_path, 'rb') as f:
                require(f.read() == self.marker_bytes, 'marker changed; refusing removal')
            os.unlink(self.marker_path)
            sync_dir(self.real_profile)
            if os.path.exists(self.plist_path):
                with open(self.plist_path, 'rb') as f:
                    require(f.read() == self.new_plist, 'LaunchAgent plist changed')
                os.unlink(self.plist_path)
                sync_dir(os.path.dirname(self.plist_path))
        self.release()
        self.result = 'rolled_back_verified'

    def execute(self):
        a = self.a
        require(sys.platform == 'darwin' or getattr(self.os, 'fixture', False), 'macOS required')
        require(not stage.task_context() or getattr(self.os, 'fixture', False), 'daemon task context forbidden')
        require(a.timeout > 0 and a.interval > 0, 'invalid timeout or heartbeat interval')
        require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', a.profile) and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', a.label), 'invalid profile or label')
        expected_profile = os.path.join(os.path.expanduser('~'), '.multica', 'profiles', a.profile)
        require(os.path.normpath(a.profile_dir) == expected_profile and os.path.isabs(a.profile_dir), 'profile directory must be the default owner profile path')
        self.real_profile = os.path.realpath(a.profile_dir)
        stage.clean_path(self.real_profile, directory=True)
        s = os.stat(self.real_profile)
        require(s.st_uid == os.getuid(), 'profile not owned by operator')
        self.profile_identity = (s.st_dev, s.st_ino, s.st_uid)
        self.marker_path = os.path.join(self.real_profile, 'daemon-management.json')
        self.gate = os.path.join(self.real_profile, 'daemon.maintenance')
        self.reported_gate = os.path.join(a.profile_dir, 'daemon.maintenance')
        self.plist_path = a.plist_path
        expected_plist = os.path.join(os.path.expanduser('~'), 'Library', 'LaunchAgents', a.label + '.plist')
        require(self.plist_path == expected_plist, 'LaunchAgent must use the owner login LaunchAgents directory')
        stage.clean_path(os.path.dirname(self.plist_path), directory=True)
        stage.clean_path(a.cli)
        stage.clean_path(a.candidate_dir, directory=True)
        stage.clean_path(os.path.join(a.candidate_dir, 'daemon-management.json'))
        stage.clean_path(os.path.join(a.candidate_dir, 'launch-agent.plist'))
        with open(os.path.join(a.candidate_dir, 'daemon-management.json'), 'rb') as f:
            self.marker_bytes = f.read()
        with open(os.path.join(a.candidate_dir, 'launch-agent.plist'), 'rb') as f:
            self.new_plist = f.read()
        self.target = json.loads(self.marker_bytes)
        require(set(self.target) == {'schema_version', 'manager', 'label', 'binary', 'sha256', 'version', 'build_commit'} and self.target['schema_version'] == 1 and self.target['manager'] == 'launchd-pinned' and self.target['label'] == a.label, 'invalid staged marker')
        require(self.target['binary'] == os.path.join(a.candidate_dir, 'multica') and re.fullmatch('[0-9a-f]{64}', self.target['sha256']) and re.fullmatch('[0-9a-f]{40}', self.target['build_commit']), 'invalid staged identity')
        stage.clean_path(self.target['binary'])
        require(stage.digest(self.target['binary']) == self.target['sha256'], 'candidate hash mismatch')
        p = self.os.version(self.target['binary'])
        info = json.loads(p.stdout)
        require(info.get('version') == self.target['version'] and info.get('build_commit') == self.target['build_commit'] and info.get('daemon_maintenance_gate') == 'v1' and info.get('daemon_lifecycle') == 'launchd-pinned-v1', 'candidate capability or identity mismatch')
        plist = plistlib.loads(self.new_plist)
        require(set(plist) == {'Label', 'ProgramArguments', 'RunAtLoad', 'KeepAlive', 'EnvironmentVariables', 'WorkingDirectory', 'StandardOutPath', 'StandardErrorPath'}, 'unexpected LaunchAgent keys')
        require(plist.get('Label') == a.label and plist.get('ProgramArguments') == [self.target['binary'], '--profile', a.profile, 'daemon', 'start', '--foreground', '--no-auto-update', '--no-auto-reload'] and plist.get('RunAtLoad') is True and plist.get('KeepAlive') is True and all(plist.get('EnvironmentVariables', {}).get(k) == v for k, v in {'MULTICA_DAEMON_SUPERVISOR': 'launchd-pinned', 'MULTICA_DAEMON_AUTO_UPDATE': 'false', 'MULTICA_DAEMON_AUTO_RELOAD': 'false', 'MULTICA_ZH_TW_DESKTOP_SELF_RELOAD': '0', 'HOME': os.path.expanduser('~')}.items()), 'invalid staged LaunchAgent')
        env = plist['EnvironmentVariables']
        self.plist_env = env
        require(set(env) == {'MULTICA_DAEMON_SUPERVISOR', 'MULTICA_DAEMON_AUTO_UPDATE', 'MULTICA_DAEMON_AUTO_RELOAD', 'MULTICA_ZH_TW_DESKTOP_SELF_RELOAD', 'HOME', 'PATH', 'MULTICA_CA_CERT_FILE'}, 'unexpected LaunchAgent environment')
        stage.clean_path(env['MULTICA_CA_CERT_FILE'])
        require(env['MULTICA_CA_CERT_FILE'] == a.original_ca_cert and stage.digest(a.original_ca_cert) == a.original_ca_sha256 and env['PATH'] == a.original_path_env, 'candidate CA or PATH differs from approved original')
        require(env['PATH'] and all(os.path.isabs(part) and os.path.normpath(part) == part for part in env['PATH'].split(':')), 'invalid LaunchAgent PATH')
        stage.clean_path(plist['WorkingDirectory'], directory=True)
        for key in ('StandardOutPath', 'StandardErrorPath'):
            require(os.path.isabs(plist[key]) and os.path.normpath(plist[key]) == plist[key], 'invalid log path')
            stage.clean_path(os.path.dirname(plist[key]), directory=True)
        self.old_managed = a.mode != 'migrate'
        if self.old_managed:
            stage.clean_path(self.marker_path)
            stage.clean_path(self.plist_path)
            with open(self.marker_path, 'rb') as f: self.old_marker = f.read()
            with open(self.plist_path, 'rb') as f: self.old_plist = f.read()
            self.old_target = json.loads(self.old_marker)
            require(set(self.old_target) == {'schema_version', 'manager', 'label', 'binary', 'sha256', 'version', 'build_commit'} and self.old_target.get('schema_version') == 1 and self.old_target.get('manager') == 'launchd-pinned' and self.old_target.get('label') == a.label and re.fullmatch('[0-9a-f]{64}', self.old_target.get('sha256', '')) and re.fullmatch('[0-9a-f]{40}', self.old_target.get('build_commit', '')), 'existing marker invalid')
        else:
            require(a.app_quit_confirmed and a.app_executable and os.path.isabs(a.app_executable) and os.path.normpath(a.app_executable) == a.app_executable and self.os.app_absent(), 'quit Desktop and confirm exact executable before migration')
            require(not os.path.lexists(self.marker_path) and not os.path.lexists(self.plist_path) and self.os.job_pid() is None, 'existing marker or job')
        h = self.os.health()
        g = h.get('maintenance_gate')
        require(isinstance(g, dict) and g.get('supported') is True and g.get('blocked') is False and g.get('path') == self.reported_gate, 'old daemon gate unsupported or blocked')
        pid, process = self.check(h, managed=self.old_managed)
        require(re.fullmatch('[0-9a-f]{40}', h.get('cli_commit', '')) and isinstance(h.get('cli_version'), str) and h['cli_version'], 'old daemon identity unavailable')
        url = urllib.parse.urlsplit(h.get('server_url', ''))
        require(url.scheme in ('http', 'https') and url.hostname and not url.username and not url.password and not url.query and not url.fragment, 'invalid original server URL')
        self.base = {'pid': pid, 'process': process, 'daemon_id': h['daemon_id'], 'server_url': h['server_url'], 'workspaces': workspace_map(h)}
        if not self.old_managed:
            self.old_target = {'binary': process[1], 'sha256': process[4], 'version': h['cli_version'], 'build_commit': h['cli_commit']}
        self.check(h, target=self.old_target, managed=self.old_managed)
        old_version = json.loads(self.os.version(process[1]).stdout)
        require(old_version.get('version') == self.old_target['version'] and old_version.get('build_commit') == self.old_target['build_commit'] and old_version.get('daemon_maintenance_gate') == 'v1', 'original executable version or gate capability mismatch')
        require(self.os.process(pid) == process, 'original executable changed during version check')
        baseline_runtimes = self.os.runtimes(self.base)
        stage.clean_path(os.path.dirname(a.evidence_dir), directory=True)
        require(os.path.isabs(a.evidence_dir) and os.path.normpath(a.evidence_dir) == a.evidence_dir, 'invalid evidence directory')
        os.mkdir(a.evidence_dir, 0o700)
        self.evidence = a.evidence_dir
        if self.old_managed:
            old_plist = plistlib.loads(self.old_plist)
            require(set(old_plist) == {'Label', 'ProgramArguments', 'RunAtLoad', 'KeepAlive', 'EnvironmentVariables', 'WorkingDirectory', 'StandardOutPath', 'StandardErrorPath'}, 'original plist contains unsupported fields')
            old_env = old_plist.get('EnvironmentVariables', {})
            require(set(old_env) <= {'MULTICA_DAEMON_SUPERVISOR', 'MULTICA_DAEMON_AUTO_UPDATE', 'MULTICA_DAEMON_AUTO_RELOAD', 'MULTICA_ZH_TW_DESKTOP_SELF_RELOAD', 'HOME', 'PATH', 'MULTICA_CA_CERT_FILE'}, 'original plist contains unapproved environment fields')
            require(old_plist.get('Label') == a.label and old_plist.get('ProgramArguments', [None])[0] == self.old_target['binary'], 'original plist identity mismatch')
            atomic(os.path.join(self.evidence, 'original-marker.json'), self.old_marker)
            atomic(os.path.join(self.evidence, 'original-launch-agent.plist'), self.old_plist)
        safe_base = dict(self.base)
        safe_base['server_url_sha256'] = hashlib.sha256(safe_base.pop('server_url').encode()).hexdigest()
        atomic(os.path.join(self.evidence, 'baseline.json'), json.dumps({'baseline': safe_base, 'old_target': self.old_target, 'target': self.target, 'runtime_last_seen': {w: {r: str(v) for r, v in values.items()} for w, values in baseline_runtimes.items()}, 'ca_sha256': a.original_ca_sha256}, indent=2).encode())
        atomic(os.path.join(self.evidence, 'heartbeats.jsonl'), b'')
        sync_dir(os.path.dirname(self.evidence))
        lockfd = os.open(self.gate + '.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        self.lock = os.fdopen(lockfd, 'r+')
        require(stat.S_ISREG(os.fstat(lockfd).st_mode) and os.fstat(lockfd).st_uid == os.getuid(), 'invalid lock')
        try: fcntl.flock(lockfd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: raise Refusal('another updater is active')
        self.gate_create()
        self.wait_health(self.old_target, self.old_managed)
        self.check(self.os.health(), target=self.old_target, ready=True, pid=pid, managed=self.old_managed)
        # Marker is installed before the App-owned daemon is stopped.
        self.profile_ok()
        if self.old_managed:
            with open(self.marker_path, 'rb') as f:
                require(f.read() == self.old_marker, 'existing marker changed')
            atomic(self.marker_path, self.marker_bytes)
        else:
            with open(self.marker_path, 'xb') as f:
                f.write(self.marker_bytes)
                f.flush()
                os.fsync(f.fileno())
            sync_dir(self.real_profile)
        self.changed = True
        self.stop_original()
        self.own_gate()
        require(stage.digest(self.target['binary']) == self.target['sha256'], 'candidate changed before bootstrap')
        with open(self.marker_path, 'rb') as f:
            require(f.read() == self.marker_bytes, 'marker changed before bootstrap')
        if not self.old_managed:
            require(self.os.app_absent(), 'Desktop App restarted before bootstrap')
        require(self.os.job_pid() is None, 'unexpected LaunchAgent before bootstrap')
        atomic(self.plist_path, self.new_plist)
        self.os.bootstrap(self.plist_path)
        self.verify(self.target, True)
        with open(self.marker_path, 'rb') as f:
            require(f.read() == self.marker_bytes, 'marker changed before release')
        self.release()
        self.result = 'updated_verified'


def main():
    p = argparse.ArgumentParser(description=__doc__, epilog='Legacy daemons without the gate are refused. A separately approved cold bootstrap is required; this command never accepts cold attestation.')
    p.add_argument('mode', choices=('migrate', 'update', 'rollback'), help='rollback selects a previously staged version as the candidate')
    for name in ('profile', 'profile-dir', 'label', 'plist-path', 'candidate-dir', 'cli', 'health-url', 'evidence-dir', 'original-ca-cert', 'original-ca-sha256', 'original-path-env'):
        p.add_argument('--' + name, required=True)
    p.add_argument('--app-executable', help='exact Desktop executable path, required for migrate')
    p.add_argument('--app-quit-confirmed', action='store_true')
    p.add_argument('--timeout', type=float, default=180)
    p.add_argument('--interval', type=float, default=15)
    a = p.parse_args()
    u = urllib.parse.urlsplit(a.health_url)
    if not (u.scheme == 'http' and u.hostname == '127.0.0.1' and u.port and u.path == '/health' and not u.username and not u.query and not u.fragment):
        p.error('health URL must be loopback HTTP /health')
    t = Transaction(a, MacOS(a))
    def interrupted(signum, frame):
        raise Refusal('interrupted; inspect recovery state')
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    code = 0
    try:
        t.execute()
    except (Refusal, OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as e:
        print('Operation failed: ' + (str(e) if isinstance(e, Refusal) else 'filesystem or system error'), file=sys.stderr)
        code = 1
        if t.released:
            t.result = 'gate_released_error_claims_may_resume'
            print('Gate was removed; claims may resume. Reacquire and drain before any further stop.', file=sys.stderr)
        elif t.profile_changed:
            t.result = 'blocked_profile_changed_claims_may_resume'
            print('Profile changed; gate protection is unverified and claims may resume. No further stop attempted.', file=sys.stderr)
            code = 2
        elif t.changed:
            try:
                t.rollback()
            except (Refusal, OSError, ValueError, KeyError, TypeError):
                if t.profile_changed:
                    t.result = 'blocked_profile_changed_claims_may_resume'
                    print('Profile changed; gate protection is unverified and claims may resume.', file=sys.stderr)
                elif t.released:
                    t.result = 'gate_released_error_claims_may_resume'
                    print('Gate was removed; claims may resume. Reacquire and drain before any further stop.', file=sys.stderr)
                else:
                    t.result = 'blocked_rollback_unverified'
                    print('Rollback unverified; preserve the gate and marker and inspect before recovery.', file=sys.stderr)
                code = 2
            else:
                print('Original daemon restored and verified.', file=sys.stderr)
        elif t.gate and os.path.lexists(t.gate):
            t.result = 'blocked_gate_preserved'
            print('Gate preserved; inspect ownership before recovery.', file=sys.stderr)
    if getattr(t, 'evidence', None):
        try:
            atomic(os.path.join(t.evidence, 'result.json'), json.dumps({'status': t.result, 'gate_released': t.released, 'profile_changed': t.profile_changed, 'claims_may_resume': t.released or t.profile_changed}, indent=2).encode())
        except OSError:
            print('Could not write result evidence.', file=sys.stderr)
            code = 2
    if code == 0:
        print(t.result)
    return code


if __name__ == '__main__':
    sys.exit(main())
