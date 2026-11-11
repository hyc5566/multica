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
import pwd
import re
import shutil
import signal
import ssl
import stat
import subprocess
import sys
import time
import urllib.parse
import urllib.request
import uuid


class Refusal(Exception):
    pass


class ScopedSystemctl(list):
    def __init__(self, command, uid, gid, environment):
        super().__init__(command)
        self.uid, self.gid, self.environment = uid, gid, environment


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


def validate_observer_ca(path, digest):
    require(isinstance(path, str) and isinstance(digest, str) and re.fullmatch(r'[0-9a-f]{64}', digest) is not None, 'observer CA path and approved SHA-256 required')
    path_clean(path, 'file')
    require(sha(path) == digest, 'observer CA checksum mismatch')
    try:
        ssl.create_default_context(cafile=path)
    except (OSError, ssl.SSLError) as error:
        raise Refusal('observer CA bundle is not usable') from error
    return path


def known(value):
    return isinstance(value, str) and value.strip().lower() not in ('', 'unknown', 'none', 'null', 'dev', 'unavailable')


def run(args, timeout=30):
    try:
        privileges = ({'user': args.uid, 'group': args.gid, 'extra_groups': [], 'env': args.environment}
                      if isinstance(args, ScopedSystemctl) else {})
        p = subprocess.run(list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False, **privileges)
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


def require_visible_proc():
    try:
        with open('/proc/self/mountinfo', encoding='utf-8') as f:
            lines = f.readlines()
    except OSError as error:
        raise Refusal('cannot verify proc mount visibility') from error
    mounts = []
    for line in lines:
        parts = line.split()
        if '-' not in parts:
            continue
        separator = parts.index('-')
        if separator + 3 < len(parts) and len(parts) > 5 and parts[4] == '/proc' and parts[separator + 1] == 'proc':
            mounts.append(parts[5].split(',') + parts[separator + 3].split(','))
    require(len(mounts) == 1, 'cannot uniquely verify proc mount')
    options = [option for option in mounts[0] if option.startswith('hidepid=')]
    require(not options or all(option == 'hidepid=0' for option in options), 'proc mount hides other processes')


def executable_users(executable):
    """Find every visible process mapped to this inode, regardless of UID."""
    require_visible_proc()
    target = os.stat(executable)
    users = []
    try:
        names = os.listdir('/proc')
    except OSError as error:
        raise Refusal('cannot enumerate all processes') from error
    for name in names:
        if not name.isdecimal():
            continue
        proc = '/proc/' + name
        try:
            before = os.stat(proc)
            image = os.stat(proc + '/exe')
            after = os.stat(proc)
            require((before.st_dev, before.st_ino, before.st_uid) == (after.st_dev, after.st_ino, after.st_uid), 'process identity changed during inventory')
        except FileNotFoundError:
            # Kernel threads and zombies have no executable. A live userspace
            # process with an inaccessible image is not safe to ignore.
            try:
                with open(proc + '/stat', encoding='ascii') as f:
                    fields = f.read().rsplit(')', 1)[1].split()
                if fields[0] in ('Z', 'X') or int(fields[6]) & 0x00200000:
                    continue
            except FileNotFoundError:
                continue  # The process exited while being inspected.
            except (OSError, ValueError, IndexError) as error:
                raise Refusal('cannot inspect a process without an executable') from error
            raise Refusal('live process executable is unavailable')
        except OSError as error:
            raise Refusal('cannot inspect every process executable') from error
        if (image.st_dev, image.st_ino) == (target.st_dev, target.st_ino):
            users.append({'pid': int(name), 'uid': before.st_uid})
    return sorted(users, key=lambda item: item['pid'])


def require_exclusive_executable(executable, pid, uid):
    users = executable_users(executable)
    require(users == [{'pid': pid, 'uid': uid}], 'executable is shared or process inventory is ambiguous')
    return users


def exec_start_configuration(value):
    require(isinstance(value, str), 'invalid systemd ExecStart')
    match = re.fullmatch(r'\{\s*path=(.*?)\s*;\s*argv\[\]=(.*?)\s*;\s*ignore_errors=(yes|no)\s*;\s*[^{}]*\}', value)
    require(match is not None and all(match.groups()), 'ambiguous systemd ExecStart')
    return {'path': match.group(1), 'argv': match.group(2), 'ignore_errors': match.group(3)}


def cgroup_members(group):
    require(isinstance(group, str) and group.startswith('/') and group != '/' and os.path.normpath(group) == group, 'invalid systemd cgroup')
    root = '/sys/fs/cgroup'
    path = os.path.join(root, group.lstrip('/'))
    try:
        os.lstat(path)
    except FileNotFoundError:
        return []
    path_clean(path, 'dir')
    members = []
    def failed(error):
        raise Refusal('cannot inspect systemd cgroup') from error
    for base, directories, _ in os.walk(path, followlinks=False, onerror=failed):
        require(all(not os.path.islink(os.path.join(base, name)) for name in directories), 'symlink inside cgroup')
        try:
            with open(os.path.join(base, 'cgroup.procs'), encoding='ascii') as f:
                rows = f.read().splitlines()
        except FileNotFoundError:
            if not os.path.exists(base):
                continue
            raise Refusal('cgroup process list unavailable')
        require(all(row.isdecimal() for row in rows), 'invalid cgroup process list')
        members.extend(int(row) for row in rows)
    return members


def time_value(value):
    require(isinstance(value, str), 'runtime heartbeat missing')
    match = re.fullmatch(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})', value)
    require(match is not None, 'invalid RFC3339 timestamp')
    base, fraction, zone = match.groups()
    if zone != 'Z':
        require(int(zone[1:3]) <= 23 and int(zone[4:6]) <= 59, 'invalid timestamp timezone')
    normalized = base + ('.' + fraction[:6].ljust(6, '0') if fraction else '') + ('+00:00' if zone == 'Z' else zone)
    try:
        return datetime.datetime.fromisoformat(normalized)
    except ValueError:
        raise Refusal('invalid runtime heartbeat')


def task_marker_present():
    directory = os.getcwd()
    while True:
        marker = os.path.join(directory, '.multica', 'daemon_task_context.json')
        try:
            with open(marker, encoding='utf-8') as f:
                value = json.load(f)
            require(isinstance(value, dict) and value.get('managed_by') == 'multica-daemon-task', 'unknown daemon task marker')
            return True
        except FileNotFoundError:
            pass
        except (OSError, ValueError):
            raise Refusal('cannot verify daemon task context marker')
        parent = os.path.dirname(directory)
        if parent == directory:
            return False
        directory = parent


class Updater:
    def __init__(self, args):
        self.a = args
        self.owner_uid = getattr(args, 'owner_uid', os.getuid())
        self.owner_home = pwd.getpwuid(self.owner_uid).pw_dir
        self.gate = None
        self.gate_path = None
        self.profile_dir = None
        self.profile_inode = None
        self.profile_changed = False
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
        self.legacy_stopped_group = None

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

    def systemctl(self, *args):
        scope = getattr(self.a, 'systemd_scope', 'user')
        require(scope in ('user', 'system'), 'invalid systemd scope')
        command = ['systemctl'] + (['--user'] if scope == 'user' else ['--system']) + list(args)
        if scope == 'user' and os.geteuid() == 0:
            account = pwd.getpwuid(self.owner_uid)
            runtime = '/run/user/%d' % self.owner_uid
            path_clean(runtime, 'dir')
            runtime_info = os.lstat(runtime)
            bus = os.path.join(runtime, 'bus')
            bus_info = os.lstat(bus)
            require(runtime_info.st_uid == self.owner_uid and stat.S_IMODE(runtime_info.st_mode) & 0o022 == 0, 'user runtime directory owner or mode mismatch')
            require(stat.S_ISSOCK(bus_info.st_mode) and bus_info.st_uid == self.owner_uid, 'user bus socket owner or type mismatch')
            environment = {'HOME': self.owner_home, 'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8',
                           'XDG_RUNTIME_DIR': runtime, 'DBUS_SESSION_BUS_ADDRESS': 'unix:path=' + bus}
            return ScopedSystemctl(command, self.owner_uid, account.pw_gid, environment)
        return command

    def unit_pid(self):
        state = run(self.systemctl('show', self.a.unit, '--property=ActiveState', '--value')).decode().strip()
        pid = run(self.systemctl('show', self.a.unit, '--property=MainPID', '--value')).decode().strip()
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

    def inspect_legacy(self, h, *, expected_pid=None, idle=False, target_sha=None):
        require(h.get('status') == 'running' and h.get('profile') == self.a.profile and known(h.get('daemon_id')), 'legacy health identity mismatch')
        pid = h.get('pid')
        state, unit_pid = self.unit_pid()
        require(type(pid) is int and pid > 1 and state == 'active' and unit_pid == pid and (expected_pid is None or pid == expected_pid), 'legacy unit and health PID mismatch')
        gate = h.get('maintenance_gate')
        require(gate is None or (isinstance(gate, dict) and gate.get('supported') is False), 'legacy daemon unexpectedly reports gate support')
        if self.base:
            require(h.get('daemon_id') == self.base['daemon_id'] and self.workspaces(h) == self.base['workspaces'], 'legacy identity changed')
            require(sha('/proc/%d/exe' % pid) == (target_sha or self.base['sha256']), 'legacy executable changed')
        if idle:
            require(type(h.get('active_task_count')) is int and h['active_task_count'] == 0, 'legacy daemon has active or unknown tasks')
        return pid

    def wait_legacy(self, *, expected_pid=None, idle=False, target_sha=None):
        deadline = time.monotonic() + self.a.timeout
        while True:
            try:
                h = self.health()
                self.inspect_legacy(h, expected_pid=expected_pid, idle=idle, target_sha=target_sha)
                return h
            except Refusal:
                require(time.monotonic() < deadline, 'timed out waiting for matching legacy daemon')
                time.sleep(min(1, max(0, deadline - time.monotonic())))

    def check_legacy_entrypoint(self):
        expected = self.a.legacy_baseline
        path = expected['entrypoint']['path']
        path_clean(path, 'file')
        s = os.stat(path)
        actual = {'path': path, 'sha256': sha(path), 'mode': stat.S_IMODE(s.st_mode), 'uid': s.st_uid,
                  'gid': s.st_gid, 'device': s.st_dev, 'inode': s.st_ino, 'links': s.st_nlink}
        require(actual == expected['entrypoint'], 'legacy entrypoint changed since plan')
        if getattr(self.a, 'legacy_wrapper', None):
            ca = path_clean(expected['wrapper_ca_path'], 'file')
            require(sha(ca) == self.a.legacy_ca_sha256, 'legacy wrapper CA changed since plan')
        start = run(self.systemctl('show', self.a.unit, '--property=ExecStart', '--value')).decode('utf-8').strip()
        require(exec_start_configuration(start) == expected['exec_start_configuration'], 'systemd ExecStart configuration changed since plan')
        kill_mode = run(self.systemctl('show', self.a.unit, '--property=KillMode', '--value')).decode('utf-8').strip()
        group = run(self.systemctl('show', self.a.unit, '--property=ControlGroup', '--value')).decode('utf-8').strip()
        state, pid = self.unit_pid()
        require(kill_mode in ('control-group', 'mixed') and kill_mode == expected['kill_mode'], 'legacy unit KillMode changed')
        if state == 'active' and pid > 1:
            require(group == expected['control_group'], 'legacy unit cgroup changed')
        else:
            require(state in ('inactive', 'failed') and pid == 0 and group in ('', expected['control_group']) and not cgroup_members(expected['control_group']), 'legacy stopped cgroup not empty or changed')

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

    def version_as_owner(self, path):
        if self.owner_uid == os.geteuid():
            return version(path)
        try:
            account = pwd.getpwuid(self.owner_uid)
            proc = subprocess.run([path, 'version', '--output', 'json'], stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, timeout=30, check=False, user=self.owner_uid,
                                  group=account.pw_gid, extra_groups=[],
                                  env={'HOME': self.owner_home, 'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8'})
            require(proc.returncode == 0, 'binary version probe failed')
            data = json.loads(proc.stdout)
        except (OSError, ValueError, subprocess.TimeoutExpired):
            raise Refusal('binary version probe unavailable or invalid')
        require(isinstance(data, dict) and known(data.get('version')) and known(data.get('build_commit')) and data.get('daemon_maintenance_gate') == 'v1', 'binary version or gate capability unavailable')
        return data['version'], data['build_commit']

    def runtimes(self):
        result = {}
        ca = validate_observer_ca(getattr(self.a, 'observer_ca_cert_file', None), getattr(self.a, 'observer_ca_sha256', None))
        observer_env = {'HOME': self.owner_home, 'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8',
                        'MULTICA_CA_CERT_FILE': ca, 'SSL_CERT_FILE': ca, 'CURL_CA_BUNDLE': ca}
        for ws, ids in self.base['workspaces'].items():
            command = [self.a.cli, '--profile', self.a.profile, '--workspace-id', ws, 'runtime', 'list', '--output', 'json']
            try:
                kwargs = {}
                if self.owner_uid != os.geteuid():
                    account = pwd.getpwuid(self.owner_uid)
                    kwargs = {'user': self.owner_uid, 'group': account.pw_gid, 'extra_groups': []}
                proc = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, check=False,
                                      env=observer_env, **kwargs)
                require(proc.returncode == 0, 'observer CLI failed')
                rows = json.loads(proc.stdout)
            except (OSError, ValueError, subprocess.TimeoutExpired):
                raise Refusal('observer CLI unavailable or invalid')
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

    def profile_unchanged(self):
        try:
            s = os.lstat(self.profile_dir)
            valid = (os.path.realpath(os.path.dirname(self.gate)) == self.profile_dir and
                     stat.S_ISDIR(s.st_mode) and (s.st_dev, s.st_ino) == self.profile_inode and s.st_uid == self.owner_uid)
        except OSError:
            valid = False
        if not valid:
            self.profile_changed = True
            raise Refusal('profile state path changed or unavailable; claim blocking is unverified')

    def own_gate(self):
        self.profile_unchanged()
        s = os.lstat(self.gate_path)
        require(stat.S_ISDIR(s.st_mode) and (s.st_dev, s.st_ino) == self.gate_inode and s.st_uid == self.owner_uid, 'gate ownership changed')
        with open(os.path.join(self.gate_path, 'owner'), 'r', encoding='utf-8') as f:
            info = json.load(f)
        require(info == {'id': self.owner, 'pid': os.getpid()}, 'gate owner changed')

    def remove_gate(self):
        self.own_gate()
        # Removing the directory commits the handoff: work can resume immediately.
        # Defer signals until this fact is recorded, even if the final fsync fails.
        previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            os.unlink(os.path.join(self.gate_path, 'owner'))
            os.rmdir(self.gate_path)
            self.gate_released = True
            self.result['gate_released_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
            self.gate_inode = None
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        fsync_dir(self.profile_dir)

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

    def samples_legacy(self, prior, expected_pid):
        last = prior
        for _ in range(self.a.heartbeat_samples):
            deadline = time.monotonic() + self.a.timeout
            while True:
                time.sleep(self.a.heartbeat_interval)
                self.inspect_legacy(self.health(), expected_pid=expected_pid, target_sha=self.a.sha256)
                current = self.runtimes()
                if all(current[ws][rid] > last[ws][rid] for ws in last for rid in last[ws]):
                    last = current
                    break
                require(time.monotonic() < deadline, 'legacy runtime heartbeats did not advance')
        return last

    def replace(self, source, target):
        temp = target + '.update-' + self.owner
        try:
            if self.a.layout == 'launcher':
                os.link(source, temp, follow_symlinks=False)
            else:
                original = os.stat(target)
                fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, stat.S_IMODE(original.st_mode))
                with os.fdopen(fd, 'wb') as dst, open(source, 'rb') as src:
                    if (os.fstat(dst.fileno()).st_uid, os.fstat(dst.fileno()).st_gid) != (original.st_uid, original.st_gid):
                        os.fchown(dst.fileno(), original.st_uid, original.st_gid)
                    shutil.copyfileobj(src, dst)
                    dst.flush()
                    os.fsync(dst.fileno())
                shutil.copystat(target, temp, follow_symlinks=False)
                with open(temp, 'rb') as finalized:
                    os.fsync(finalized.fileno())
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
            if first and getattr(self.a, 'legacy_bootstrap', False):
                self.inspect_legacy(h, idle=True, expected_pid=pid)
            elif not first and getattr(self.a, 'legacy_target', False) and sha('/proc/%d/exe' % pid) == self.a.sha256:
                self.inspect_legacy(h, idle=True, expected_pid=pid, target_sha=self.a.sha256)
            elif not first and sha('/proc/%d/exe' % pid) == self.a.sha256:
                g = h.get('maintenance_gate')
                require(h.get('pid') == pid and isinstance(g, dict) and g.get('supported') is True and g.get('path') == self.gate and g.get('blocked') is True and g.get('ready') is True and g.get('claims_in_flight') == 0 and h.get('active_task_count') == 0, 'candidate is not safely drained')
            else:
                self.inspect(h, ready=True, expected_pid=pid)
            if first:
                require(pid == self.base['pid'], 'original daemon PID changed')
            else:
                require(sha('/proc/%d/exe' % pid) in (self.base['sha256'], self.a.sha256), 'unit executable is neither original nor staged')
        self.own_gate()
        if first and (getattr(self.a, 'legacy_bootstrap', False) or (getattr(self.a, 'legacy_target', False) and getattr(self.a, 'legacy_wrapper', None))):
            self.check_legacy_entrypoint()
        if first and getattr(self.a, 'legacy_bootstrap', False):
            self.inspect_legacy(self.health(), expected_pid=pid, idle=True)
        if first:
            path_info = os.stat(self.exe)
            require((path_info.st_dev, path_info.st_ino) == self.original_executable_identity, 'original executable path changed')
            require_exclusive_executable(self.exe, pid, self.owner_uid)
        if first and getattr(self.a, 'legacy_bootstrap', False):
            self.legacy_stopped_group = self.a.legacy_baseline['control_group']
        if first:
            # The stop call may partially succeed even when it reports failure.
            self.stopped = True
        run(self.systemctl('stop', self.a.unit), timeout=self.a.timeout)
        state, pid = self.unit_pid()
        require(state in ('inactive', 'failed') and pid == 0, 'unit did not stop')
        if self.legacy_stopped_group:
            require(not cgroup_members(self.legacy_stopped_group), 'legacy unit cgroup still has processes')

    def rollback(self):
        self.safe_stop()
        if self.legacy_stopped_group:
            require(not cgroup_members(self.legacy_stopped_group), 'legacy unit cgroup still has processes')
        if self.changed:
            require(self.backup and sha(self.backup) == self.backup_hash, 'backup checksum mismatch')
            source = self.original_launcher if self.a.layout == 'launcher' else self.backup
            require(sha(source) == self.backup_hash, 'original launcher changed')
            try:
                self.replace(source, self.destination)
            except OSError:
                raise Refusal('rollback restore failed')
        if getattr(self.a, 'legacy_wrapper', None) and (getattr(self.a, 'legacy_bootstrap', False) or getattr(self.a, 'legacy_target', False)):
            self.check_legacy_entrypoint()
        try:
            run(self.systemctl('start', self.a.unit), timeout=self.a.timeout)
        except Refusal:
            raise Refusal('rollback unit start failed')
        if getattr(self.a, 'legacy_bootstrap', False):
            restored = self.wait_legacy()
            self.result.update(status='legacy_restored', restored_pid=restored['pid'],
                               recovery='Legacy daemon cannot block claims or attest receipts; inspect and redrive interrupted or missed work.')
            self.remove_gate()
            return
        target = (self.base['version'], self.base['commit'], self.base['sha256'])
        restored = self.wait_ready(target)
        self.samples(target, self.runtimes(), restored['pid'])
        final_health = self.health()
        self.inspect(final_health, ready=True, target=target, expected_pid=restored['pid'])
        self.result.update(status='rolled_back_verified', restored_pid=restored['pid'],
            restored_process_started_at=(final_health.get('update_attestation') or {}).get('process_started_at'))
        self.remove_gate()

    def execute(self):
        require(not any(os.getenv(n) for n in ('MULTICA_TASK_ID', 'MULTICA_AGENT_ID', 'MULTICA_TASK_CONFIG_ROOT', 'MULTICA_DAEMON_PORT', 'MULTICA_QUICK_CREATE_TASK_ID')) and not task_marker_present(), 'updater cannot run in a daemon task context')
        require(getattr(self.a, 'systemd_scope', 'user') != 'system' or os.geteuid() == 0, 'system scope requires root')
        require(self.owner_uid == os.geteuid() or os.geteuid() == 0, 'operator cannot act for target owner')
        require(self.a.heartbeat_samples >= 3 and self.a.heartbeat_interval > 0 and self.a.timeout > 0, 'invalid heartbeat or timeout settings')
        require(re.fullmatch(r'[0-9a-fA-F]{64}', self.a.sha256) is not None, 'invalid SHA-256')
        self.a.sha256 = self.a.sha256.lower()
        legacy_target = getattr(self.a, 'legacy_target', False)
        if legacy_target:
            require(isinstance(getattr(self.a, 'expected_base', None), dict) and isinstance(getattr(self.a, 'legacy_baseline', None), dict), 'legacy rollback requires verified ledger and plan')
            require(self.a.legacy_baseline.get('binary_sha256') == self.a.sha256, 'legacy rollback target differs from baseline')
        else:
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
        legacy = getattr(self.a, 'legacy_bootstrap', False)
        if legacy:
            require(isinstance(getattr(self.a, 'expected_base', None), dict), 'legacy bootstrap requires a verified plan')
            require(g is None or (isinstance(g, dict) and g.get('supported') is False), 'legacy bootstrap requires no gate support')
            expected = os.path.join(self.owner_home, '.multica', 'profiles', self.a.profile) if self.a.profile else os.path.join(self.owner_home, '.multica')
            self.gate = os.path.join(expected, 'daemon.maintenance')
        else:
            require(isinstance(g, dict) and g.get('supported') is True and g.get('blocked') is False, 'gate unsupported or already blocked')
            self.gate = g.get('path')
        require(isinstance(self.gate, str) and os.path.basename(self.gate) == 'daemon.maintenance', 'invalid gate path')
        require(os.path.isabs(self.gate) and os.path.normpath(self.gate) == self.gate, 'invalid gate path')
        expected = os.path.join(self.owner_home, '.multica', 'profiles', self.a.profile) if self.a.profile else os.path.join(self.owner_home, '.multica')
        require(os.path.dirname(self.gate) == os.path.normpath(expected), 'profile directory mismatch')
        parent = path_clean(os.path.realpath(expected), 'dir')
        require(os.stat(parent).st_uid == self.owner_uid, 'profile directory not owned by user')
        self.profile_dir = parent
        s = os.stat(parent)
        self.profile_inode = (s.st_dev, s.st_ino)
        self.gate_path = os.path.join(parent, 'daemon.maintenance')
        pid = self.inspect_legacy(h) if legacy else self.inspect(h)
        if not legacy:
            require(known(h.get('cli_version')) and known(h.get('cli_commit')), 'baseline version or commit unavailable')
            require(re.fullmatch(r'[0-9a-f]{40}', h['cli_commit']) is not None, 'baseline must attest a full commit')
        exe = proc_exe(pid)
        self.exe = exe
        image_info, path_info = os.stat('/proc/%d/exe' % pid), os.stat(exe)
        require((image_info.st_dev, image_info.st_ino) == (path_info.st_dev, path_info.st_ino), 'running executable path changed')
        self.original_executable_identity = (path_info.st_dev, path_info.st_ino)
        require(os.stat(exe).st_uid == self.owner_uid, 'daemon executable is not owned by user')
        self.base = {'pid': pid, 'daemon_id': h['daemon_id'], 'version': h.get('cli_version'), 'commit': h.get('cli_commit') or None, 'sha256': sha('/proc/%d/exe' % pid), 'workspaces': self.workspaces(h)}
        if not legacy:
            require(self.version_as_owner(exe) == (self.base['version'], self.base['commit']), 'baseline executable version mismatch')
        expected_base = getattr(self.a, 'expected_base', None)
        if expected_base is not None:
            require(all(self.base.get(key) == expected_base.get(key) for key in ('pid', 'daemon_id', 'version', 'commit', 'sha256', 'workspaces')), 'baseline changed since plan')
            require(list(self.profile_inode) == expected_base.get('profile_inode'), 'profile inode changed since plan')
        require(self.a.no_shared_profiles, 'inventory every shared profile and attest with --no-shared-profiles')
        if self.a.layout == 'binary':
            require(not self.a.entrypoint and not self.a.binary, 'binary layout does not use entrypoint or binary')
            self.destination = exe
            real = self.a.staged
        else:
            require(self.a.entrypoint and self.a.binary, 'launcher layout requires entrypoint and binary')
            self.destination = path_clean(self.a.entrypoint, 'file')
            real = path_clean(self.a.binary, 'file')
            require(os.stat(self.destination).st_uid == self.owner_uid, 'entrypoint not owned by user')
            self.original_launcher = path_clean(os.path.join(os.path.dirname(exe), 'launcher'), 'file')
            require(os.path.samefile(self.destination, self.original_launcher), 'entrypoint does not match running version launcher')
            require(os.path.samefile(self.a.staged, os.path.join(os.path.dirname(real), 'launcher')), 'staged launcher is not paired with staged binary')
            require(self.version_as_owner(self.a.staged) == (self.a.version, self.a.commit), 'staged launcher version mismatch')
        require(sha(real) == self.a.sha256, 'staged binary checksum mismatch')
        if not legacy_target:
            require(self.version_as_owner(real) == (self.a.version, self.a.commit), 'staged binary version mismatch')
        require(os.stat(self.destination).st_uid == self.owner_uid, 'destination is not owned by user')
        require(not os.path.samefile(self.a.cli, self.destination) and not os.path.samefile(self.a.cli, self.a.staged), 'observation CLI must be independent of the replacement')
        self.baseline_heartbeats = self.runtimes()
        path_clean(os.path.dirname(self.a.evidence_dir), 'dir')
        require(os.path.isabs(self.a.evidence_dir) and os.path.normpath(self.a.evidence_dir) == self.a.evidence_dir, 'invalid evidence directory')
        os.mkdir(self.a.evidence_dir, 0o700)
        self.evidence = self.a.evidence_dir
        with open(os.path.join(self.evidence, 'heartbeats.jsonl'), 'x', encoding='utf-8'):
            pass
        self.profile_unchanged()
        lockpath = self.gate_path + '.lock'
        try:
            lockfd = os.open(lockpath, os.O_CREAT | os.O_EXCL | os.O_RDWR | os.O_NOFOLLOW, 0o600)
            if self.owner_uid != os.geteuid():
                os.fchown(lockfd, self.owner_uid, -1)
        except FileExistsError:
            lockfd = os.open(lockpath, os.O_RDWR | os.O_NOFOLLOW)
        self.lock = os.fdopen(lockfd, 'r+')
        require(os.fstat(lockfd).st_uid == self.owner_uid and stat.S_ISREG(os.fstat(lockfd).st_mode), 'gate lock invalid')
        try:
            fcntl.flock(lockfd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Refusal('another updater holds the gate lock')
        previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            self.profile_unchanged()
            os.mkdir(self.gate_path, 0o700)
            self.gate_created = True
            if self.owner_uid != os.geteuid():
                os.chown(self.gate_path, self.owner_uid, -1, follow_symlinks=False)
            st = os.lstat(self.gate_path)
            self.gate_inode = (st.st_dev, st.st_ino)
            with open(os.path.join(self.gate_path, 'owner'), 'x', encoding='utf-8') as f:
                json.dump({'id': self.owner, 'pid': os.getpid()}, f)
                f.flush()
                os.fsync(f.fileno())
            fsync_dir(self.gate_path)
            fsync_dir(parent)
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        if legacy:
            self.wait_legacy(expected_pid=pid, idle=True)
        else:
            self.wait_ready(expected_pid=pid)
        self.backup = os.path.join(self.evidence, 'original-entrypoint')
        with open(self.destination, 'rb') as src, open(self.backup, 'xb') as dst:
            shutil.copyfileobj(src, dst)
            dst.flush()
            os.fsync(dst.fileno())
        shutil.copystat(self.destination, self.backup, follow_symlinks=False)
        with open(self.backup, 'rb') as finalized:
            os.fsync(finalized.fileno())
        self.backup_hash = sha(self.backup)
        if self.a.layout == 'binary':
            require(self.backup_hash == self.base['sha256'], 'original executable changed before backup')
        if legacy and getattr(self.a, 'legacy_wrapper', None):
            self.check_legacy_entrypoint()
            wrapper_backup = os.path.join(self.evidence, 'original-wrapper')
            shutil.copy2(self.a.legacy_wrapper, wrapper_backup, follow_symlinks=False)
            original = os.stat(self.a.legacy_wrapper)
            copied = os.stat(wrapper_backup)
            if (copied.st_uid, copied.st_gid) != (original.st_uid, original.st_gid):
                os.chown(wrapper_backup, original.st_uid, original.st_gid)
            require(sha(wrapper_backup) == self.a.legacy_baseline['entrypoint']['sha256'], 'wrapper backup checksum mismatch')
            with open(wrapper_backup, 'rb') as finalized:
                os.fsync(finalized.fileno())
        with open(os.path.join(self.evidence, 'baseline.json'), 'x', encoding='utf-8') as f:
            json.dump({'daemon': self.base, 'runtime_last_seen': {ws: {rid: ts.isoformat() for rid, ts in values.items()} for ws, values in self.baseline_heartbeats.items()}, 'backup_sha256': self.backup_hash}, f, indent=2)
            f.write('\n')
            f.flush()
            os.fsync(f.fileno())
        fsync_dir(self.evidence)
        if legacy:
            self.inspect_legacy(self.health(), idle=True, expected_pid=pid)
        else:
            self.inspect(self.health(), ready=True, expected_pid=pid)
        require(sha('/proc/%d/exe' % pid) == self.base['sha256'] and sha(real) == self.a.sha256, 'executable changed before stop')
        self.safe_stop(first=True)
        path_info = os.stat(self.exe)
        require((path_info.st_dev, path_info.st_ino) == self.original_executable_identity, 'original executable path changed after stop')
        require(not executable_users(self.exe), 'original executable remains in use after unit stop')
        path_info = os.stat(self.exe)
        require((path_info.st_dev, path_info.st_ino) == self.original_executable_identity, 'original executable path changed during inventory')
        self.changed = True
        self.replace(self.a.staged, self.destination)
        if legacy and getattr(self.a, 'legacy_wrapper', None):
            self.check_legacy_entrypoint()
        run(self.systemctl('start', self.a.unit), timeout=self.a.timeout)
        if legacy_target:
            restored = self.wait_legacy(target_sha=self.a.sha256)
            self.samples_legacy(self.runtimes(), restored['pid'])
            if getattr(self.a, 'legacy_wrapper', None):
                self.check_legacy_entrypoint()
            self.inspect_legacy(self.health(), expected_pid=restored['pid'], target_sha=self.a.sha256)
            self.result.update(status='legacy_rollback_pending_manual', restored_pid=restored['pid'],
                               recovery='Legacy daemon cannot block claims or attest receipts; inspect and redrive interrupted or missed work.')
            self.remove_gate()
            return
        target = (self.a.version, self.a.commit, self.a.sha256)
        updated = self.wait_ready(target)
        self.samples(target, self.runtimes(), updated['pid'])
        final_health = self.health()
        self.inspect(final_health, ready=True, target=target, expected_pid=updated['pid'])
        attestation = final_health.get('update_attestation')
        started_at = attestation.get('process_started_at') if isinstance(attestation, dict) else None
        self.result.update(status='updated', daemon_id=self.base['daemon_id'], baseline=self.base, target={'version': self.a.version, 'commit': self.a.commit, 'sha256': self.a.sha256}, target_pid=updated['pid'], target_process_started_at=started_at, backup_sha256=self.backup_hash)
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
    p.add_argument('--observer-ca-cert-file', required=True)
    p.add_argument('--observer-ca-sha256', required=True)
    p.add_argument('--systemd-scope', choices=('user', 'system'), default='user')
    p.add_argument('--owner-uid', type=int, default=os.getuid())
    p.add_argument('--layout', required=True, choices=('binary', 'launcher'))
    p.add_argument('--entrypoint'); p.add_argument('--binary')
    p.add_argument('--no-shared-profiles', action='store_true', help='attest all profiles and units using the executable were inventoried and none share it')
    p.add_argument('--timeout', type=float, default=180)
    p.add_argument('--heartbeat-samples', type=int, default=3)
    p.add_argument('--heartbeat-interval', type=float, default=15)
    a = p.parse_args()
    u = Updater(a)
    code = perform(u)
    print(json.dumps(u.result, sort_keys=True))
    return code


def perform(u):
    previous_signals = {kind: signal.getsignal(kind) for kind in (signal.SIGINT, signal.SIGTERM)}
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        return perform_transaction(u)
    finally:
        try:
            lock = getattr(u, 'lock', None)
            if lock is not None:
                lock.close()
                u.lock = None
        finally:
            for kind, handler in previous_signals.items():
                signal.signal(kind, handler)


def perform_transaction(u):
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
                    u.result['rollback_error'] = (str(rollback_error) if isinstance(rollback_error, Refusal) else 'rollback filesystem operation failed') + '; automatic recovery stopped; inspect gate and profile before proceeding'
        elif u.gate_created:
            try:
                u.remove_gate()
            except (Refusal, OSError, ValueError):
                if u.gate_released:
                    u.report_released_error()
                else:
                    u.result['status'] = 'blocked'
                    u.result['cleanup_error'] = 'created gate could not be safely removed; inspect maintenance path before resuming'
        if u.profile_changed:
            u.result.update(status='blocked', profile_changed=True, claims_may_resume=True,
                            recovery='Profile state changed; the old gate may not protect the daemon. Re-establish identity and a gate before any further stop or rollback.')
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
    return code


if __name__ == '__main__':
    sys.exit(main())
