#!/usr/bin/env python3
"""Offline, gate-aware systemd-user updater. Never run inside a daemon task.

The operator must first inventory *all* profiles and units sharing the executable
or launcher, including indirect links, and explicitly attest that no other
profile uses it. Launcher layout requires both launchers to be the hard
links installed beside their respective version binaries. Keep every backup.
"""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
import re
import shutil
import signal
import stat
import subprocess
import sys
import time
import urllib.parse
import urllib.request
import uuid


class Refusal(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Refusal(message)


def path_clean(value, kind=None):
    require(os.path.isabs(value) and os.path.normpath(value) == value, 'path must be absolute and normalized')
    p = '/'
    for part in value.split('/')[1:]:
        p = os.path.join(p, part)
        st = os.lstat(p)
        require(not stat.S_ISLNK(st.st_mode), 'symlink paths are not permitted')
    if kind == 'file':
        require(stat.S_ISREG(os.stat(value).st_mode), 'expected regular file')
    if kind == 'dir':
        require(stat.S_ISDIR(os.stat(value).st_mode), 'expected directory')
    return value


def sha(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def known(value):
    return isinstance(value, str) and value.strip().lower() not in ('', 'unknown', 'none', 'null', 'dev', 'unavailable')


def run(args, timeout=30):
    try:
        p = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise Refusal('required subprocess unavailable or timed out')
    require(p.returncode == 0, 'required subprocess failed')
    return p.stdout


def json_cmd(args):
    try:
        return json.loads(run(args))
    except (ValueError, UnicodeDecodeError):
        raise Refusal('invalid JSON from required subprocess')


def version(path):
    data = json_cmd([path, 'version', '--output', 'json'])
    require(isinstance(data, dict) and known(data.get('version')) and known(data.get('build_commit')), 'binary version or full build commit unavailable')
    require(data.get('daemon_maintenance_gate') == 'v1', 'staged binary does not attest maintenance gate support')
    return data['version'], data['build_commit']


def proc_exe(pid):
    require(isinstance(pid, int) and pid > 1, 'invalid daemon PID')
    path = os.readlink('/proc/%d/exe' % pid)
    require(not path.endswith(' (deleted)'), 'daemon executable is deleted')
    return path_clean(path, 'file')


def time_value(value):
    require(isinstance(value, str), 'runtime heartbeat missing')
    try:
        dt = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
        require(dt.tzinfo is not None, 'heartbeat has no timezone')
        return dt
    except ValueError:
        raise Refusal('invalid runtime heartbeat')


def task_marker_present():
    directory = os.getcwd()
    while True:
        marker = os.path.join(directory, '.multica', 'daemon_task_context.json')
        try:
            with open(marker, encoding='utf-8') as f:
                value = json.load(f)
            if isinstance(value, dict) and value.get('managed_by') == 'multica-daemon-task':
                return True
        except (OSError, ValueError):
            pass
        parent = os.path.dirname(directory)
        if parent == directory:
            return False
        directory = parent


class Updater:
    def __init__(self, args):
        self.a = args
        self.gate = None
        self.gate_inode = None
        self.owner = str(uuid.uuid4())
        self.lock = None
        self.stopped = False
        self.backup = None
        self.changed = False
        self.base = None
        self.exe = None
        self.result = {'status': 'refused'}
        self.evidence = None
        self.gate_released = False
        self.gate_created = False

    def health(self):
        try:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(self.a.health_url, timeout=3) as resp:
                require(resp.status == 200, 'health unavailable')
                data = json.load(resp)
            require(isinstance(data, dict), 'invalid health response')
            return data
        except (OSError, ValueError):
            raise Refusal('health unavailable or invalid')

    def unit_pid(self):
        state = run(['systemctl', '--user', 'show', self.a.unit, '--property=ActiveState', '--value']).decode().strip()
        pid = run(['systemctl', '--user', 'show', self.a.unit, '--property=MainPID', '--value']).decode().strip()
        require(pid.isdecimal(), 'invalid unit PID')
        return state, int(pid)

    def inspect(self, h, *, ready=False, expected_pid=None, target=None):
        require(h.get('status') == 'running' and h.get('profile') == self.a.profile and known(h.get('daemon_id')), 'health identity or status mismatch')
        pid = h.get('pid')
        require(type(pid) is int and pid > 1, 'invalid health PID')
        state, unitpid = self.unit_pid()
        require(state == 'active' and unitpid == pid and (expected_pid is None or expected_pid == pid), 'unit and health PID mismatch')
        g = h.get('maintenance_gate')
        require(isinstance(g, dict) and g.get('supported') is True, 'daemon does not support maintenance gate')
        require(g.get('path') == self.gate, 'maintenance gate path changed')
        if ready:
            require(g.get('blocked') is True and g.get('ready') is True and g.get('claims_in_flight') == 0 and h.get('active_task_count') == 0, 'maintenance gate not idle and ready')
            self.own_gate()
        if self.base:
            require(h.get('daemon_id') == self.base['daemon_id'], 'daemon ID changed')
            require(self.workspaces(h) == self.base['workspaces'], 'workspace or runtime set changed')
        if target:
            require(h.get('cli_version') == target[0] and h.get('cli_commit') == target[1], 'running version or commit mismatch')
            require(sha('/proc/%d/exe' % pid) == target[2], 'running executable checksum mismatch')
        return pid

    @staticmethod
    def workspaces(h):
        items = h.get('workspaces')
        require(isinstance(items, list) and items, 'workspace runtimes missing')
        out = {}
        for item in items:
            require(isinstance(item, dict) and known(item.get('id')) and isinstance(item.get('runtimes'), list), 'invalid workspace runtimes')
            ws = item['id']
            ids = item['runtimes']
            require(ws not in out and ids and all(known(x) for x in ids) and len(set(ids)) == len(ids), 'invalid runtime set')
            out[ws] = sorted(ids)
        return out

    def runtimes(self):
        result = {}
        for ws, ids in self.base['workspaces'].items():
            rows = json_cmd([self.a.cli, '--profile', self.a.profile, '--workspace-id', ws, 'runtime', 'list', '--output', 'json'])
            require(isinstance(rows, list), 'runtime list is invalid')
            selected = {}
            for row in rows:
                if isinstance(row, dict) and row.get('id') in ids:
                    rid = row['id']
                    require(rid not in selected and row.get('daemon_id') == self.base['daemon_id'] and row.get('status') == 'online', 'runtime identity or status mismatch')
                    selected[rid] = time_value(row.get('last_seen_at'))
            require(set(selected) == set(ids), 'runtime missing')
            result[ws] = selected
        return result

    def own_gate(self):
        s = os.lstat(self.gate)
        require(stat.S_ISDIR(s.st_mode) and (s.st_dev, s.st_ino) == self.gate_inode and s.st_uid == os.getuid(), 'gate ownership changed')
        with open(os.path.join(self.gate, 'owner'), 'r', encoding='utf-8') as f:
            info = json.load(f)
        require(info == {'id': self.owner, 'pid': os.getpid()}, 'gate owner changed')

    def remove_gate(self):
        self.own_gate()
        # Removing the directory commits the handoff: work can resume immediately.
        # Defer signals until this fact is recorded, even if the final fsync fails.
        previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            os.unlink(os.path.join(self.gate, 'owner'))
            os.rmdir(self.gate)
            self.gate_released = True
            self.gate_inode = None
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        fsync_dir(os.path.dirname(self.gate))

    def report_released_error(self):
        self.result.update(status=self.result['status'] + '_gate_released_error',
                           gate_present=False, claims_may_resume=True,
                           recovery='Gate was removed; reacquire and drain before any further stop or rollback.')

    def wait_ready(self, target=None, expected_pid=None):
        deadline = time.monotonic() + self.a.timeout
        while time.monotonic() < deadline:
            try:
                h = self.health()
                self.inspect(h, ready=True, expected_pid=expected_pid, target=target)
                return h
            except Refusal:
                time.sleep(1)
        raise Refusal('timed out waiting for matching idle daemon and gate')

    def samples(self, target, prior, expected_pid):
        last = prior
        for _ in range(self.a.heartbeat_samples):
            deadline = time.monotonic() + self.a.timeout
            while True:
                time.sleep(self.a.heartbeat_interval)
                self.inspect(self.health(), ready=True, target=target, expected_pid=expected_pid)
                current = self.runtimes()
                if all(current[ws][rid] > last[ws][rid] for ws in last for rid in last[ws]):
                    last = current
                    with open(os.path.join(self.evidence, 'heartbeats.jsonl'), 'a', encoding='utf-8') as f:
                        json.dump({'version': target[0], 'commit': target[1], 'runtime_last_seen': {ws: {rid: ts.isoformat() for rid, ts in values.items()} for ws, values in current.items()}}, f)
                        f.write('\n')
                        f.flush()
                        os.fsync(f.fileno())
                    break
                require(time.monotonic() < deadline, 'runtime heartbeats did not advance')
        return last

    def replace(self, source, target):
        temp = target + '.update-' + self.owner
        try:
            if self.a.layout == 'launcher':
                os.link(source, temp, follow_symlinks=False)
            else:
                fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, stat.S_IMODE(os.stat(target).st_mode))
                with os.fdopen(fd, 'wb') as dst, open(source, 'rb') as src:
                    shutil.copyfileobj(src, dst)
                    dst.flush()
                    os.fsync(dst.fileno())
            os.replace(temp, target)
            fsync_dir(os.path.dirname(target))
        finally:
            if os.path.exists(temp):
                os.unlink(temp)

    def safe_stop(self, *, first=False):
        self.own_gate()
        state, pid = self.unit_pid()
        if state in ('inactive', 'failed') and pid == 0:
            return
        require(state == 'active' and pid > 1, 'unit not safely stoppable')
        try:
            h = self.health()
        except Refusal:
            require(not first and sha('/proc/%d/exe' % pid) == self.a.sha256, 'unhealthy unit executable is not staged binary')
        else:
            self.inspect(h, ready=True, expected_pid=pid)
            if first:
                require(pid == self.base['pid'], 'original daemon PID changed')
            else:
                require(sha('/proc/%d/exe' % pid) in (self.base['sha256'], self.a.sha256), 'unit executable is neither original nor staged')
        self.own_gate()
        run(['systemctl', '--user', 'stop', self.a.unit], timeout=self.a.timeout)
        state, pid = self.unit_pid()
        require(state in ('inactive', 'failed') and pid == 0, 'unit did not stop')

    def rollback(self):
        self.safe_stop()
        if self.changed:
            require(self.backup and sha(self.backup) == self.backup_hash, 'backup checksum mismatch')
            source = self.original_launcher if self.a.layout == 'launcher' else self.backup
            require(sha(source) == self.backup_hash, 'original launcher changed')
            try:
                self.replace(source, self.destination)
            except OSError:
                raise Refusal('rollback restore failed')
        try:
            run(['systemctl', '--user', 'start', self.a.unit], timeout=self.a.timeout)
        except Refusal:
            raise Refusal('rollback unit start failed')
        target = (self.base['version'], self.base['commit'], self.base['sha256'])
        restored = self.wait_ready(target)
        self.samples(target, self.runtimes(), restored['pid'])
        self.inspect(self.health(), ready=True, target=target, expected_pid=restored['pid'])
        self.result['status'] = 'rolled_back_verified'
        self.remove_gate()

    def execute(self):
        require(not any(os.getenv(n) for n in ('MULTICA_TASK_ID', 'MULTICA_AGENT_ID', 'MULTICA_TASK_CONFIG_ROOT', 'MULTICA_DAEMON_PORT', 'MULTICA_QUICK_CREATE_TASK_ID')) and not task_marker_present(), 'updater cannot run in a daemon task context')
        require(self.a.heartbeat_samples >= 3 and self.a.heartbeat_interval > 0 and self.a.timeout > 0, 'invalid heartbeat or timeout settings')
        require(re.fullmatch(r'[0-9a-fA-F]{64}', self.a.sha256) is not None, 'invalid SHA-256')
        self.a.sha256 = self.a.sha256.lower()
        require(known(self.a.version) and known(self.a.commit), 'target version and commit required')
        require(re.fullmatch(r'[0-9a-f]{40}', self.a.commit) is not None, 'target must name a full commit')
        require(re.fullmatch(r'[A-Za-z0-9_.:@-]+\.service', self.a.unit) is not None, 'exact service unit required')
        url = urllib.parse.urlsplit(self.a.health_url)
        require(url.scheme == 'http' and url.hostname == '127.0.0.1' and url.port and url.path == '/health' and not url.query and not url.fragment and not url.username, 'health URL must be loopback HTTP /health')
        path_clean(self.a.cli, 'file')
        path_clean(self.a.staged, 'file')
        require(os.access(self.a.cli, os.X_OK) and os.access(self.a.staged, os.X_OK), 'CLI or staged file not executable')
        h = self.health()
        g = h.get('maintenance_gate')
        require(isinstance(g, dict) and g.get('supported') is True and g.get('blocked') is False, 'gate unsupported or already blocked')
        self.gate = g.get('path')
        require(isinstance(self.gate, str) and os.path.basename(self.gate) == 'daemon.maintenance', 'invalid gate path')
        parent = path_clean(os.path.dirname(self.gate), 'dir')
        expected = os.path.join(os.path.expanduser('~'), '.multica', 'profiles', self.a.profile) if self.a.profile else os.path.join(os.path.expanduser('~'), '.multica')
        require(parent == os.path.normpath(expected) and os.stat(parent).st_uid == os.getuid(), 'profile directory mismatch or not owned by user')
        pid = self.inspect(h)
        require(known(h.get('cli_version')) and known(h.get('cli_commit')), 'baseline version or commit unavailable')
        require(re.fullmatch(r'[0-9a-f]{40}', h['cli_commit']) is not None, 'baseline must attest a full commit')
        exe = proc_exe(pid)
        require(os.stat(exe).st_uid == os.getuid(), 'daemon executable is not owned by user')
        self.base = {'pid': pid, 'daemon_id': h['daemon_id'], 'version': h['cli_version'], 'commit': h['cli_commit'], 'sha256': sha('/proc/%d/exe' % pid), 'workspaces': self.workspaces(h)}
        require(version(exe) == (self.base['version'], self.base['commit']), 'baseline executable version mismatch')
        require(self.a.no_shared_profiles, 'inventory every shared profile and attest with --no-shared-profiles')
        if self.a.layout == 'binary':
            require(not self.a.entrypoint and not self.a.binary, 'binary layout does not use entrypoint or binary')
            self.destination = exe
            real = self.a.staged
        else:
            require(self.a.entrypoint and self.a.binary, 'launcher layout requires entrypoint and binary')
            self.destination = path_clean(self.a.entrypoint, 'file')
            real = path_clean(self.a.binary, 'file')
            require(os.stat(self.destination).st_uid == os.getuid(), 'entrypoint not owned by user')
            self.original_launcher = path_clean(os.path.join(os.path.dirname(exe), 'launcher'), 'file')
            require(os.path.samefile(self.destination, self.original_launcher), 'entrypoint does not match running version launcher')
            require(os.path.samefile(self.a.staged, os.path.join(os.path.dirname(real), 'launcher')), 'staged launcher is not paired with staged binary')
            require(version(self.a.staged) == (self.a.version, self.a.commit), 'staged launcher version mismatch')
        require(version(real) == (self.a.version, self.a.commit) and sha(real) == self.a.sha256, 'staged binary version or checksum mismatch')
        require(os.stat(self.destination).st_uid == os.getuid(), 'destination is not owned by user')
        require(not os.path.samefile(self.a.cli, self.destination) and not os.path.samefile(self.a.cli, self.a.staged), 'observation CLI must be independent of the replacement')
        self.baseline_heartbeats = self.runtimes()
        path_clean(os.path.dirname(self.a.evidence_dir), 'dir')
        require(os.path.isabs(self.a.evidence_dir) and os.path.normpath(self.a.evidence_dir) == self.a.evidence_dir, 'invalid evidence directory')
        os.mkdir(self.a.evidence_dir, 0o700)
        self.evidence = self.a.evidence_dir
        with open(os.path.join(self.evidence, 'heartbeats.jsonl'), 'x', encoding='utf-8'):
            pass
        lockpath = self.gate + '.lock'
        lockfd = os.open(lockpath, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        self.lock = os.fdopen(lockfd, 'r+')
        require(os.fstat(lockfd).st_uid == os.getuid() and stat.S_ISREG(os.fstat(lockfd).st_mode), 'gate lock invalid')
        try:
            fcntl.flock(lockfd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Refusal('another updater holds the gate lock')
        previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            os.mkdir(self.gate, 0o700)
            self.gate_created = True
            st = os.lstat(self.gate)
            self.gate_inode = (st.st_dev, st.st_ino)
            with open(os.path.join(self.gate, 'owner'), 'x', encoding='utf-8') as f:
                json.dump({'id': self.owner, 'pid': os.getpid()}, f)
                f.flush()
                os.fsync(f.fileno())
            fsync_dir(self.gate)
            fsync_dir(parent)
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        self.wait_ready(expected_pid=pid)
        self.backup = os.path.join(self.evidence, 'original-entrypoint')
        with open(self.destination, 'rb') as src, open(self.backup, 'xb') as dst:
            shutil.copyfileobj(src, dst)
            dst.flush()
            os.fsync(dst.fileno())
        self.backup_hash = sha(self.backup)
        if self.a.layout == 'binary':
            require(self.backup_hash == self.base['sha256'], 'original executable changed before backup')
        with open(os.path.join(self.evidence, 'baseline.json'), 'x', encoding='utf-8') as f:
            json.dump({'daemon': self.base, 'runtime_last_seen': {ws: {rid: ts.isoformat() for rid, ts in values.items()} for ws, values in self.baseline_heartbeats.items()}, 'backup_sha256': self.backup_hash}, f, indent=2)
            f.write('\n')
            f.flush()
            os.fsync(f.fileno())
        fsync_dir(self.evidence)
        self.inspect(self.health(), ready=True, expected_pid=pid)
        require(sha('/proc/%d/exe' % pid) == self.base['sha256'] and sha(real) == self.a.sha256, 'executable changed before stop')
        self.stopped = True
        self.safe_stop(first=True)
        self.changed = True
        self.replace(self.a.staged, self.destination)
        run(['systemctl', '--user', 'start', self.a.unit], timeout=self.a.timeout)
        target = (self.a.version, self.a.commit, self.a.sha256)
        updated = self.wait_ready(target)
        self.samples(target, self.runtimes(), updated['pid'])
        self.inspect(self.health(), ready=True, target=target, expected_pid=updated['pid'])
        self.result.update(status='updated', daemon_id=self.base['daemon_id'], baseline=self.base, target={'version': self.a.version, 'commit': self.a.commit, 'sha256': self.a.sha256}, backup_sha256=self.backup_hash)
        self.remove_gate()


def fsync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def interrupted(signum, frame):
    raise Refusal('interrupted by signal')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('profile'); p.add_argument('unit'); p.add_argument('staged'); p.add_argument('sha256')
    for arg in ('cli', 'health-url', 'version', 'commit', 'evidence-dir'):
        p.add_argument('--' + arg, required=True)
    p.add_argument('--layout', required=True, choices=('binary', 'launcher'))
    p.add_argument('--entrypoint'); p.add_argument('--binary')
    p.add_argument('--no-shared-profiles', action='store_true', help='attest all profiles and units using the executable were inventoried and none share it')
    p.add_argument('--timeout', type=float, default=180)
    p.add_argument('--heartbeat-samples', type=int, default=3)
    p.add_argument('--heartbeat-interval', type=float, default=15)
    a = p.parse_args()
    u = Updater(a)
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        u.execute()
        code = 0
    except (Refusal, OSError, ValueError) as error:
        u.result['error'] = str(error) if isinstance(error, Refusal) else 'filesystem or system operation failed'
        code = 1
        if u.gate_released:
            u.report_released_error()
        elif u.stopped:
            try:
                u.rollback()
            except (Refusal, OSError, ValueError) as rollback_error:
                if u.gate_released:
                    u.report_released_error()
                    u.result['rollback_error'] = 'rollback verified but gate release needs attention'
                else:
                    u.result['status'] = 'blocked'
                    u.result['rollback_error'] = (str(rollback_error) if isinstance(rollback_error, Refusal) else 'rollback filesystem operation failed') + '; gate retained'
        elif u.gate_created:
            try:
                u.remove_gate()
            except (Refusal, OSError, ValueError):
                if u.gate_released:
                    u.report_released_error()
                else:
                    u.result['status'] = 'blocked'
                    u.result['cleanup_error'] = 'created gate could not be safely removed; inspect maintenance path before resuming'
    if u.evidence:
        try:
            with open(os.path.join(u.evidence, 'result.json'), 'x', encoding='utf-8') as f:
                json.dump(u.result, f, indent=2)
                f.write('\n')
                f.flush()
                os.fsync(f.fileno())
            fsync_dir(u.evidence)
        except OSError:
            u.result['evidence_error'] = 'could not write result evidence'
            code = 1
    print(json.dumps(u.result, sort_keys=True))
    return code


if __name__ == '__main__':
    sys.exit(main())
