#!/usr/bin/env python3
"""Offline, gate-aware systemd-user updater. Never run inside a daemon task.

The operator must first inventory *all* profiles and units sharing the executable
or launcher, including indirect links, and explicitly attest that no other
profile uses it. Launcher layout requires both launchers to be the hard
links installed beside their respective version binaries. Keep every backup.
"""
import argparse
import datetime
import errno
import fcntl
import hashlib
import json
import os
import pwd
import re
import shutil
import shlex
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


class SafeUserCommand(list):
    def __init__(self, command, environment):
        super().__init__(command)
        self.environment = environment


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
                      if isinstance(args, ScopedSystemctl) else {'env': args.environment} if isinstance(args, SafeUserCommand) else {})
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



ACCOUNT_UNIT_ANCILLARY = ('ExecCondition', 'ExecStartPre', 'ExecStartPost', 'ExecStop', 'ExecStopPost', 'ExecReload')


class AccountScope:
    """Evidence for a private binary installation and its explicit writes.

    This is deliberately not an owner assertion: every path component is
    inspected. Callers must revalidate unit properties around systemd actions.
    """
    def __init__(self, uid, asset_root, state_root, immutable_paths, transaction_paths,
                 unit, systemd_scope, exec_start, fragment, executable, profile, dropins, unit_object, ancillary, account_path_policy=None):
        require(type(uid) is int and uid == os.geteuid(), 'account scope requires the target effective UID')
        require(systemd_scope == 'user', 'account scope requires a user unit')
        require(isinstance(unit, str) and re.fullmatch(r'[A-Za-z0-9_.:@-]+\.service', unit), 'account scope requires an exact unit')
        self.uid, self.asset_root, self.state_root = uid, asset_root, state_root
        self.unverified_process_images = []
        self.process_managed_inodes = set()
        self.path_policy = account_path_policy
        if self.path_policy is not None:
            require(isinstance(self.path_policy, dict) and set(self.path_policy) == {'extended_home', 'sticky_parent'}, 'unknown account path policy')
            extended, parent = self.path_policy['extended_home'], self.path_policy['sticky_parent']
            require(all(isinstance(p, str) and os.path.isabs(p) and os.path.normpath(p) == p and p != '/' for p in (extended, parent)) and os.path.dirname(extended) == parent and os.path.commonpath((extended, asset_root)) == extended and asset_root != extended, 'invalid account path policy')
        self.fragment = fragment
        self.fragment_target = fragment
        self.registration = None
        self.registration_path = None
        self.standard_registration_path = None
        if self.path_policy is not None:
            home = pwd.getpwuid(uid).pw_dir
            standard_link = os.path.join(home, '.config', 'systemd', 'user', unit)
            self.standard_registration_path = standard_link
            require(os.path.islink(standard_link), 'account unit standard registration symlink required')
            self.registration_path = standard_link
        if os.path.islink(fragment):
            require(self.registration_path == fragment, 'invalid account unit registration link')
        if self.registration_path is not None:
            info = os.lstat(self.registration_path)
            require(info.st_uid == uid and stat.S_ISLNK(info.st_mode), 'account unit registration owner unsafe')
            target = os.readlink(self.registration_path)
            require(os.path.isabs(target) and os.path.normpath(target) == target and os.path.commonpath((extended, target)) == extended and target != extended, 'invalid account unit registration target')
            require(fragment in (self.registration_path, target), 'account unit registration does not match fragment')
            self.fragment_target = target
            self.registration = self._registration_snapshot()
        self.unit = unit
        self.immutable_paths = tuple(sorted(set(immutable_paths)))
        transaction_paths = set(transaction_paths)
        for path in self.immutable_paths:
            parent = os.path.dirname(path)
            while parent != asset_root and os.path.commonpath((asset_root, parent)) == asset_root:
                transaction_paths.add(parent)
                parent = os.path.dirname(parent)
        self.transaction_paths = tuple(sorted(transaction_paths))
        self.static_transaction_paths = self.transaction_paths
        self.fragment = fragment
        self.executable = executable
        self.exec_start = exec_start_configuration(exec_start)
        require(self.exec_start['path'] == executable and self.exec_start['ignore_errors'] == 'no', 'account unit ExecStart entrypoint mismatch')
        require(dropins == '', 'account unit DropInPaths are unsupported')
        require(isinstance(ancillary, dict) and set(ancillary) == set(ACCOUNT_UNIT_ANCILLARY) and all(value == '' for value in ancillary.values()), 'account unit ancillary commands are unsupported')
        require(isinstance(unit_object, str) and re.fullmatch(r'/org/freedesktop/systemd1/unit/[A-Za-z0-9_]+', unit_object), 'invalid account unit object')
        self.unit_object, self.ancillary = unit_object, dict(ancillary)
        try:
            argv = shlex.split(self.exec_start['argv'])
        except ValueError as error:
            raise Refusal('invalid account unit argv') from error
        expected_argv = [executable] + (['--profile', profile] if profile else []) + ['daemon', 'start', '--foreground']
        require(argv == expected_argv, 'account unit argv or profile mismatch')
        self.profile, self.argv, self.dropins = profile, argv, dropins
        require(executable in self.immutable_paths, 'account executable must be an immutable input')
        for path in (asset_root, state_root, fragment, executable, *self.immutable_paths, *self.transaction_paths):
            require(isinstance(path, str) and os.path.isabs(path) and os.path.normpath(path) == path and path != '/', 'account path must be absolute and normalized')
        require(os.path.commonpath((asset_root, state_root)) not in (asset_root, state_root), 'account state must be independent of asset root')
        for path in self.immutable_paths:
            require(os.path.commonpath((asset_root, path)) == asset_root and path != asset_root, 'account input outside asset root')
        for path in self.transaction_paths:
            require(any(os.path.commonpath((root, path)) == root and path != root for root in (asset_root, state_root)), 'account transaction path outside private roots')
        self._dynamic_directories = {}
        for path in self.static_transaction_paths:
            if os.path.lexists(path) and stat.S_ISDIR(self._identity(path).st_mode):
                self._dynamic_directories[path] = self._fields(self._identity(path))[:5]
        self._boundaries = self._boundary_snapshot()
        self._inputs = {path: self._file_snapshot(path) for path in self.immutable_paths}
        self._fragment = self._file_snapshot(self.fragment_target)
        self._scan_assets()

    def _identity(self, path):
        try:
            info = os.lstat(path)
        except OSError as error:
            raise Refusal('account path unavailable: ' + path) from error
        require(not stat.S_ISLNK(info.st_mode), 'account path is a symlink: ' + path)
        return info

    @staticmethod
    def _fields(info):
        return [info.st_dev, info.st_ino, info.st_uid, info.st_gid,
                stat.S_IMODE(info.st_mode), info.st_nlink]

    def _acl_snapshot(self, path, info):
        # Linux stores nontrivial access and default POSIX ACLs as xattrs.
        # Refuse them all, including named entries, masks and default ACLs.
        try:
            for name in ('system.posix_acl_access', 'system.posix_acl_default'):
                try:
                    os.getxattr(path, name, follow_symlinks=False)
                except OSError as error:
                    if error.errno != errno.ENODATA:
                        raise
                else:
                    raise Refusal('account ACL unsupported: ' + path)
            after = os.lstat(path)
        except OSError as error:
            raise Refusal('account ACL inspection unavailable: ' + path) from error
        require(self._fields(info) == self._fields(after) and info.st_ctime_ns == after.st_ctime_ns, 'account path changed during ACL inspection: ' + path)
        return ['owner-group-other', stat.S_IMODE(info.st_mode) & 0o777]

    def _registration_snapshot(self):
        path = self.registration_path
        self._ancestors(os.path.dirname(path))
        try:
            before = os.lstat(path)
            target = os.readlink(path)
            after = os.lstat(path)
        except OSError as error:
            raise Refusal('account unit registration unavailable') from error
        require(stat.S_ISLNK(before.st_mode) and before.st_uid == self.uid and self._fields(before) == self._fields(after) and before.st_ctime_ns == after.st_ctime_ns and target == self.fragment_target, 'account unit registration changed')
        require(os.readlink(path) == target, 'account unit registration changed')
        return self._fields(before) + [before.st_ctime_ns, target]

    def _ancestors(self, path):
        result = {}
        current = '/'
        for part in ['', *path.strip('/').split('/')]:
            if part:
                current = os.path.join(current, part)
            info = self._identity(current)
            require(stat.S_ISDIR(info.st_mode), 'account ancestor is not a directory: ' + current)
            acl = self._acl_snapshot(current, info)
            policy = self.path_policy
            if policy is not None and current == policy['sticky_parent']:
                require(info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o1775, 'account sticky parent owner or permissions unsafe: ' + current)
            elif policy is not None and current == policy['extended_home']:
                require(info.st_uid == self.uid and not (info.st_mode & 0o7022), 'account extended home owner or permissions unsafe: ' + current)
            else:
                require(info.st_uid in (0, self.uid) and not (info.st_mode & 0o022), 'account ancestor owner or permissions unsafe: ' + current)
            result[current] = self._fields(info)[:5] + [acl]
        return result

    def _boundary_snapshot(self):
        result = {}
        paths = [self.asset_root, self.state_root, os.path.dirname(self.fragment), os.path.dirname(self.fragment_target)]
        if self.path_policy is not None:
            paths.append(self.path_policy['extended_home'])
        if self.registration_path is not None:
            paths.append(os.path.dirname(self.registration_path))
        paths.extend(os.path.dirname(path) for path in self.immutable_paths)
        paths.extend(path for path in self.static_transaction_paths if os.path.lexists(path) and stat.S_ISDIR(self._identity(path).st_mode))
        for path in paths:
            result.update(self._ancestors(path))
        for path in (self.asset_root, self.state_root):
            info = self._identity(path)
            require(info.st_uid == self.uid and stat.S_IMODE(info.st_mode) == 0o700, 'account private root must be owner 0700: ' + path)
        return result

    def _file_snapshot(self, path):
        self._ancestors(os.path.dirname(path))
        info = self._identity(path)
        self._acl_snapshot(path, info)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == self.uid and info.st_nlink == 1 and not (info.st_mode & 0o7022), 'account file owner, type, permissions or links unsafe: ' + path)
        try:
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd, 'rb') as stream:
                before = os.fstat(stream.fileno())
                require(not os.listxattr(stream.fileno()), 'account file extended attributes unsupported: ' + path)
                require(self._fields(before) == self._fields(info), 'account file changed during inspection: ' + path)
                digest = hashlib.sha256()
                for block in iter(lambda: stream.read(1024 * 1024), b''):
                    digest.update(block)
                after = os.fstat(stream.fileno())
            final = self._identity(path)
            signature = lambda item: self._fields(item) + [item.st_size, item.st_mtime_ns, item.st_ctime_ns]
            require(signature(info) == signature(before) == signature(after) == signature(final), 'account file changed during inspection: ' + path)
            return signature(info) + [digest.hexdigest()]
        except OSError as error:
            raise Refusal('cannot inspect account file: ' + path) from error

    def _scan_assets(self):
        allowed = set(self.immutable_paths) | set(self.transaction_paths)
        def fail(error):
            raise Refusal('cannot inspect account asset root') from error
        for base, directories, files in os.walk(self.asset_root, followlinks=False, onerror=fail):
            for name in directories + files:
                path = os.path.join(base, name)
                info = self._identity(path)
                self._acl_snapshot(path, info)
                require(info.st_uid == self.uid and not (info.st_mode & 0o7022), 'account asset owner or permissions unsafe: ' + path)
                if stat.S_ISDIR(info.st_mode):
                    require(path in allowed or any(item.startswith(path + '/') for item in allowed), 'unknown account asset directory: ' + path)
                else:
                    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and path in allowed, 'unknown or shared account asset: ' + path)

    def evidence(self):
        return {'scope': 'account', 'uid': self.uid, 'asset_root': self.asset_root,
                'process_uid_policy': 'ruid-owner-esfs-same-foreign-eacces-unverified-v1',
                'state_root': self.state_root, 'unit': self.unit, 'systemd_scope': 'user',
                'exec_start': self.exec_start, 'profile': self.profile, 'argv': self.argv, 'dropins': self.dropins,
                'unit_object': self.unit_object, 'ancillary': self.ancillary, 'fragment': self.fragment,
                'fragment_identity': self._fragment, 'fragment_target': self.fragment_target, 'registration_path': self.registration_path, 'registration': self.registration, 'account_path_policy': self.path_policy, 'boundaries': self._boundaries,
                'immutable': self._inputs, 'transaction_paths': list(self.static_transaction_paths),
                'dynamic_directories': self._dynamic_directories}

    def revalidate(self):
        require(self._boundary_snapshot() == self._boundaries, 'account boundary changed')
        require((self.registration is not None and self._registration_snapshot() == self.registration) or
                (self.registration is None and (self.standard_registration_path is None or not os.path.islink(self.standard_registration_path))), 'account unit registration changed')
        for path, expected in self._dynamic_directories.items():
            info = self._identity(path)
            require(stat.S_ISDIR(info.st_mode) and self._fields(info)[:5] == expected, 'account transaction directory changed: ' + path)
        require(self._file_snapshot(self.fragment_target) == self._fragment, 'account unit fragment changed')
        for path, expected in self._inputs.items():
            require(self._file_snapshot(path) == expected, 'account immutable input changed: ' + path)
        self._scan_assets()

    def note_created_directory(self, path):
        require(path in self.static_transaction_paths and path not in self._dynamic_directories, 'account directory creation not authorized')
        info = self._identity(path)
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == self.uid and stat.S_IMODE(info.st_mode) == 0o700,
                'account created directory is not private')
        current = self._boundary_snapshot()
        require(all(current.get(key) == value for key, value in self._boundaries.items()), 'account boundary changed')
        self._boundaries = current
        self._dynamic_directories[path] = self._fields(info)[:5]
        self.revalidate()

    def note_removed_directory(self, path):
        require(path in self._dynamic_directories and not os.path.lexists(path), 'account directory removal not authorized')
        del self._dynamic_directories[path]
        current = self._boundary_snapshot()
        require(all(current.get(key) == value for key, value in self._boundaries.items() if key != path), 'account boundary changed')
        self._boundaries = current
        self.revalidate()

    def authorize_temporary(self, path):
        self.revalidate()
        require(isinstance(path, str) and os.path.isabs(path) and os.path.normpath(path) == path and
                any(os.path.commonpath((root, path)) == root and path != root for root in (self.asset_root, self.state_root)),
                'account temporary path outside private roots')
        require(not os.path.lexists(path), 'account temporary path already exists')
        self._ancestors(os.path.dirname(path))
        self.transaction_paths = tuple(sorted(set(self.transaction_paths) | {path}))

    def revalidate_while_leased(self, target, temp=None, temp_snapshot=None):
        require(target in self._inputs and (temp is None or temp in self.transaction_paths), 'account lease paths are not authorized')
        require(self._boundary_snapshot() == self._boundaries, 'account boundary changed')
        require((self.registration is not None and self._registration_snapshot() == self.registration) or
                (self.registration is None and (self.standard_registration_path is None or not os.path.islink(self.standard_registration_path))), 'account unit registration changed')
        require(self._file_snapshot(self.fragment_target) == self._fragment, 'account unit fragment changed')
        for path, expected in self._inputs.items():
            if path != target:
                require(self._file_snapshot(path) == expected, 'account immutable input changed: ' + path)
        self._scan_assets()
        if temp is not None:
            require(self._file_snapshot(temp) == temp_snapshot, 'account replacement copy changed')

    def acknowledge_replacement(self, path, digest):
        require(path in self._inputs and path in self.transaction_paths, 'account replacement is not authorized')
        require(self._boundary_snapshot() == self._boundaries, 'account boundary changed')
        require((self.registration is not None and self._registration_snapshot() == self.registration) or
                (self.registration is None and (self.standard_registration_path is None or not os.path.islink(self.standard_registration_path))), 'account unit registration changed')
        require(self._file_snapshot(self.fragment_target) == self._fragment, 'account unit fragment changed')
        for other, expected in self._inputs.items():
            if other != path:
                require(self._file_snapshot(other) == expected, 'account immutable input changed: ' + other)
        replacement = self._file_snapshot(path)
        require(replacement[-1] == digest, 'account replacement checksum mismatch')
        self._scan_assets()
        self._inputs[path] = replacement

    def add_immutable(self, path):
        require(path in self.static_transaction_paths, 'account immutable path is not authorized')
        self.revalidate()
        value = self._file_snapshot(path)
        if path in self._inputs:
            require(self._inputs[path] == value, 'account immutable input changed: ' + path)
        else:
            self._inputs[path] = value
            self.immutable_paths = tuple(sorted(set(self.immutable_paths) | {path}))
            current = self._boundary_snapshot()
            require(all(current.get(key) == value for key, value in self._boundaries.items()), 'account boundary changed')
            self._boundaries = current

    def acknowledge_removal(self, path, digest):
        require(path in self._inputs and path in self.transaction_paths and self._inputs[path][-1] == digest,
                'account cleanup target was not authorized')
        require(not os.path.lexists(path), 'account cleanup target still exists')
        del self._inputs[path]
        self.revalidate()

    def revalidate_unit(self, unit, systemd_scope, exec_start, fragment, dropins, unit_object, ancillary):
        self.revalidate()
        require(unit == self.unit and systemd_scope == 'user' and fragment == self.fragment and dropins == self.dropins and unit_object == self.unit_object and ancillary == self.ancillary and
                exec_start_configuration(exec_start) == self.exec_start, 'account unit changed since plan')

    def check_mutation(self, path):
        require(path in self.transaction_paths, 'account transaction path not authorized: ' + str(path))
        self.revalidate()
        self._ancestors(os.path.dirname(path))
        if os.path.lexists(path):
            info = self._identity(path)
            require(info.st_uid == self.uid and not (info.st_mode & 0o7022) and
                    (stat.S_ISDIR(info.st_mode) or (stat.S_ISREG(info.st_mode) and info.st_nlink == 1)),
                    'account transaction path unsafe: ' + path)


class ExclusiveFileLease:
    """Hold a Linux write lease while replacing or unlinking an old inode."""
    def __init__(self, path, uid):
        self.path, self.uid = path, uid
        self.fd = None
        self.broken = False
        self.acquired = False
        self.previous_handler = None

    def _break(self, signum, frame):
        self.broken = True

    def __enter__(self):
        require(sys.platform.startswith('linux') and hasattr(fcntl, 'F_SETLEASE') and hasattr(fcntl, 'F_GETLEASE'), 'account file leases unsupported')
        try:
            self.previous_handler = signal.signal(signal.SIGIO, self._break)
            self.fd = os.open(self.path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
            info = os.fstat(self.fd)
            require(stat.S_ISREG(info.st_mode) and info.st_uid == self.uid and info.st_nlink == 1, 'account lease target unsafe')
            fcntl.fcntl(self.fd, fcntl.F_SETLEASE, fcntl.F_WRLCK)
            self.acquired = True
            self.verify()
            return self
        except (OSError, ValueError) as error:
            self.__exit__(None, None, None)
            raise Refusal('account exclusive file lease unavailable') from error
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def verify(self, *, renamed=False):
        require(self.fd is not None and not self.broken, 'account file lease break requested')
        try:
            require(fcntl.fcntl(self.fd, fcntl.F_GETLEASE) == fcntl.F_WRLCK, 'account file lease lost')
            if renamed:
                return
            opened, named = os.fstat(self.fd), os.lstat(self.path)
            require(stat.S_ISREG(named.st_mode) and (opened.st_dev, opened.st_ino, opened.st_uid, opened.st_mode, opened.st_nlink) ==
                    (named.st_dev, named.st_ino, named.st_uid, named.st_mode, named.st_nlink), 'account lease target changed')
        except OSError as error:
            raise Refusal('account file lease verification failed') from error

    def digest(self):
        self.verify()
        digest = hashlib.sha256()
        os.lseek(self.fd, 0, os.SEEK_SET)
        while True:
            block = os.read(self.fd, 1024 * 1024)
            if not block:
                break
            digest.update(block)
        self.verify()
        return digest.hexdigest()

    def __exit__(self, exc_type, exc, traceback):
        try:
            if self.fd is not None:
                try:
                    if self.acquired:
                        fcntl.fcntl(self.fd, fcntl.F_SETLEASE, fcntl.F_UNLCK)
                        self.acquired = False
                finally:
                    os.close(self.fd)
                    self.fd = None
        finally:
            if self.previous_handler is not None:
                signal.signal(signal.SIGIO, self.previous_handler)
                self.previous_handler = None



def _account_guard(guard):
    require(isinstance(guard, AccountScope), 'account process inventory requires verified private scope')
    guard.revalidate()


def _process_account_identity(proc, info):
    try:
        with open(proc + '/status', encoding='ascii') as f:
            rows = [line for line in f if line.startswith('Uid:')]
        require(len(rows) == 1, 'process UID status unavailable')
        values = rows[0].split()[1:]
        require(len(values) == 4 and all(value.isdecimal() for value in values), 'invalid process UID status')
        with open(proc + '/stat', encoding='ascii') as f:
            fields = f.read().rsplit(')', 1)[1].split()
        require(len(fields) > 19 and fields[19].isdecimal(), 'invalid process start time')
        return (info.st_dev, info.st_ino, info.st_uid, tuple(int(value) for value in values), fields[19])
    except (OSError, ValueError, IndexError) as error:
        raise Refusal('cannot verify account process UID and start time') from error


def _account_process_stable(proc, original):
    try:
        after = os.stat(proc)
    except OSError as error:
        raise Refusal('account process identity unavailable') from error
    require(_process_account_identity(proc, after) == original, 'account process identity changed during inventory')


def _account_image_access(proc, original, operation):
    _account_process_stable(proc, original)
    try:
        return operation(proc + '/exe')
    finally:
        _account_process_stable(proc, original)


def _account_unverified(scope, proc, identity, error, operation, image=None):
    if scope is None:
        return False
    uids = identity[3]
    if not (error.errno == errno.EACCES and uids[0] == scope.uid and
            uids[1] == uids[2] == uids[3] and uids[1] != scope.uid):
        return False
    item = {'pid': int(os.path.basename(proc)), 'proc_device': identity[0],
            'proc_inode': identity[1], 'proc_owner': identity[2], 'uids': list(uids),
            'start_time': identity[4], 'operation': operation,
            'status': 'external privilege identity; image, managed inode and App relationship unverified'}
    if image is not None:
        inode = (image.st_dev, image.st_ino)
        require(inode not in scope.process_managed_inodes, 'executable is shared or process inventory is ambiguous')
        item['observed_image_inode'] = list(inode)
    if item not in scope.unverified_process_images:
        scope.unverified_process_images.append(item)
    return True


def process_inventory(account_scope=None):
    """Inspect visible process identities without excluding other owners."""
    if account_scope is not None:
        _account_guard(account_scope)
    require_visible_proc()
    try:
        names = os.listdir('/proc')
    except OSError as error:
        raise Refusal('cannot enumerate all processes') from error
    result = []
    for name in names:
        if not name.isdecimal():
            continue
        proc = '/proc/' + name
        try:
            before = os.stat(proc)
            identity_before = _process_account_identity(proc, before) if account_scope is not None else None
            foreign = identity_before is not None and account_scope.uid not in identity_before[3]
            try:
                image = os.stat(proc + '/exe') if account_scope is None else _account_image_access(proc, identity_before, os.stat)
            except PermissionError as error:
                if not foreign and not _account_unverified(account_scope, proc, identity_before, error, 'stat'):
                    raise
                continue
            if foreign:
                try:
                    _account_image_access(proc, identity_before, os.readlink)
                    image_after = _account_image_access(proc, identity_before, os.stat)
                except PermissionError:
                    _account_process_stable(proc, identity_before)
                    continue
                require((image.st_dev, image.st_ino) == (image_after.st_dev, image_after.st_ino), 'process executable changed during inventory')
                _account_process_stable(proc, identity_before)
                continue
            try:
                executable = os.readlink(proc + '/exe') if account_scope is None else _account_image_access(proc, identity_before, os.readlink)
            except PermissionError as error:
                if account_scope is None or not _account_unverified(account_scope, proc, identity_before, error, 'readlink', image):
                    raise
                continue
            require(not executable.endswith(' (deleted)'), 'process has deleted executable')
            identity = executable.casefold()
            if identity.endswith('.appimage'):
                require('multica' in identity, 'cannot identify AppImage process')
            if 'electron' in os.path.basename(identity):
                with open(proc + '/cmdline', 'rb') as f:
                    command = f.read()
                identity += ' ' + os.fsdecode(command).casefold()
                require(bool(command) and 'multica' in identity, 'cannot identify Electron process')
            app = 'multica' in identity and ('.appimage' in identity or 'electron' in os.path.basename(executable).casefold() or os.path.basename(executable).casefold() in ('multica-desktop', '@multicadesktop') or '.app/' in identity)
            try:
                image_after = os.stat(proc + '/exe') if account_scope is None else _account_image_access(proc, identity_before, os.stat)
            except PermissionError as error:
                if account_scope is None or not _account_unverified(account_scope, proc, identity_before, error, 'stat', image):
                    raise
                require(not app, 'desktop application running; update through the application')
                continue
            after = os.stat(proc)
            require((image.st_dev, image.st_ino) == (image_after.st_dev, image_after.st_ino), 'process executable changed during inventory')
            require((before.st_dev, before.st_ino, before.st_uid) == (after.st_dev, after.st_ino, after.st_uid), 'process identity changed during inventory')
            if identity_before is not None:
                _account_process_stable(proc, identity_before)
        except FileNotFoundError:
            try:
                with open(proc + '/stat', encoding='ascii') as f:
                    fields = f.read().rsplit(')', 1)[1].split()
                if fields[0] in ('Z', 'X') or int(fields[6]) & 0x00200000:
                    continue
            except FileNotFoundError:
                continue
            except (OSError, ValueError, IndexError) as error:
                raise Refusal('cannot inspect a process without an executable') from error
            raise Refusal('live process executable or identity is unavailable')
        except OSError as error:
            raise Refusal('cannot inspect every process executable or identity') from error
        if 'multica' in identity or executable.casefold().endswith('.appimage') or 'electron' in os.path.basename(executable).casefold():
            result.append({'pid': int(name), 'uid': before.st_uid if account_scope is None else account_scope.uid, 'exe': executable,
                           'device': image.st_dev, 'inode': image.st_ino, 'app': app})
    return sorted(result, key=lambda item: item['pid'])


def require_no_running_app(account_scope=None):
    processes = process_inventory() if account_scope is None else process_inventory(account_scope)
    require(not any(item['app'] for item in processes), 'desktop application running; update through the application')


def executable_users(executable, account_scope=None):
    """Find processes mapped to the inode within the verified scope."""
    if account_scope is not None:
        _account_guard(account_scope)
    require_visible_proc()
    target = os.stat(executable)
    if account_scope is not None:
        target_inode = (target.st_dev, target.st_ino)
        require(not any(item.get('observed_image_inode') == list(target_inode) for item in account_scope.unverified_process_images), 'executable is shared or process inventory is ambiguous')
        account_scope.process_managed_inodes.add(target_inode)
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
            identity_before = _process_account_identity(proc, before) if account_scope is not None else None
            foreign = identity_before is not None and account_scope.uid not in identity_before[3]
            try:
                image = os.stat(proc + '/exe') if account_scope is None else _account_image_access(proc, identity_before, os.stat)
            except PermissionError as error:
                if not foreign and not _account_unverified(account_scope, proc, identity_before, error, 'stat'):
                    raise
                continue
            after = os.stat(proc)
            require((before.st_dev, before.st_ino, before.st_uid) == (after.st_dev, after.st_ino, after.st_uid), 'process identity changed during inventory')
            if identity_before is not None:
                _account_process_stable(proc, identity_before)
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


def require_exclusive_executable(executable, pid, uid, account_scope=None):
    if account_scope is not None:
        require(isinstance(account_scope, AccountScope) and account_scope.uid == uid, 'account process owner mismatch')
    users = executable_users(executable) if account_scope is None else executable_users(executable, account_scope)
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
        if getattr(self.a, 'inventory_scope', 'host') == 'account' or getattr(self.a, 'account_guard', None) is not None:
            require(scope == 'user' and self.owner_uid == os.geteuid(), 'account user bus owner mismatch')
            return SafeUserCommand(command, self.account_bus_environment())
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

    def account_bus_environment(self):
        runtime = '/run/user/%d' % self.owner_uid
        path_clean(runtime, 'dir')
        runtime_info = os.lstat(runtime)
        bus = os.path.join(runtime, 'bus')
        bus_info = os.lstat(bus)
        require(runtime_info.st_uid == self.owner_uid and not (runtime_info.st_mode & 0o022), 'account user runtime owner or mode mismatch')
        require(stat.S_ISSOCK(bus_info.st_mode) and bus_info.st_uid == self.owner_uid, 'account user bus socket owner or type mismatch')
        return {'HOME': self.owner_home, 'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8',
                'XDG_RUNTIME_DIR': runtime, 'DBUS_SESSION_BUS_ADDRESS': 'unix:path=' + bus}

    def account_unit_property(self, name):
        data = run(self.systemctl('show', self.a.unit, '--all', '--property=' + name)).decode('utf-8')
        lines = data.splitlines()
        require(len(lines) == 1 and lines[0].startswith(name + '='), 'account unit property unavailable: ' + name)
        return lines[0][len(name) + 1:]

    def account_unit_ancillary(self):
        prefix = ['busctl', '--user', '--json=short']
        destination = 'org.freedesktop.systemd1'
        environment = self.account_bus_environment()
        unit = json_cmd(SafeUserCommand(prefix + ['call', destination, '/org/freedesktop/systemd1',
                                  'org.freedesktop.systemd1.Manager', 'GetUnit', 's', self.a.unit], environment))
        require(isinstance(unit, dict) and unit.get('type') == 'o' and isinstance(unit.get('data'), list) and
                len(unit['data']) == 1 and isinstance(unit['data'][0], str) and
                re.fullmatch(r'/org/freedesktop/systemd1/unit/[A-Za-z0-9_]+', unit['data'][0]), 'account unit object unavailable')
        object_path = unit['data'][0]
        result = {}
        for prop in ACCOUNT_UNIT_ANCILLARY:
            value = json_cmd(SafeUserCommand(prefix + ['get-property', destination, object_path, 'org.freedesktop.systemd1.Service', prop], environment))
            require(isinstance(value, dict) and value.get('type') == 'a(sasbttttuii)' and value.get('data') == [], 'account unit ancillary command unavailable or nonempty: ' + prop)
            result[prop] = ''
        return object_path, result

    def account_check(self, path=None):
        guard = getattr(self.a, 'account_guard', None)
        if guard is None:
            return
        start = self.account_unit_property('ExecStart')
        fragment = self.account_unit_property('FragmentPath')
        dropins = self.account_unit_property('DropInPaths')
        unit_object, ancillary = self.account_unit_ancillary()
        guard.revalidate_unit(self.a.unit, self.a.systemd_scope, start, fragment, dropins, unit_object, ancillary)
        if path is not None:
            guard.check_mutation(path)

    def exclusive_file(self, path, binary=False):
        self.account_check(path)
        if getattr(self.a, 'account_guard', None) is None:
            return open(path, 'xb' if binary else 'x', **({} if binary else {'encoding': 'utf-8'}))
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        return os.fdopen(fd, 'wb' if binary else 'w', **({} if binary else {'encoding': 'utf-8'}))

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
        self.account_check(os.path.join(self.gate_path, 'owner'))
        # Removing the directory commits the handoff: work can resume immediately.
        # Defer signals until this fact is recorded, even if the final fsync fails.
        previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            os.unlink(os.path.join(self.gate_path, 'owner'))
            self.account_check(self.gate_path)
            os.rmdir(self.gate_path)
            self.gate_released = True
            if getattr(self.a, 'account_guard', None) is not None:
                self.a.account_guard.note_removed_directory(self.gate_path)
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
                    self.account_check(os.path.join(self.evidence, 'heartbeats.jsonl'))
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
        guard = getattr(self.a, 'account_guard', None)
        if guard is not None:
            self.account_check(target)
            return self.replace_account(source, target, guard)
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

    def replace_account(self, source, target, guard):
        require(self.a.layout == 'binary', 'account replacement requires binary layout')
        guard.check_mutation(target)
        temp = target + '.update-' + self.owner
        guard.authorize_temporary(temp)
        expected_source = self.backup_hash if source == self.backup else self.a.sha256
        expected_target = self.a.sha256 if source == self.backup else self.base['sha256']
        require(sha(source) == expected_source, 'account replacement source changed')
        original = os.lstat(target)
        require(stat.S_ISREG(original.st_mode) and not (original.st_mode & 0o7000), 'account executable special mode unsupported')
        created = committed = False
        try:
            with open(source, 'rb') as src:
                try:
                    require(not os.listxattr(src.fileno()), 'account executable extended attributes unsupported')
                except OSError as error:
                    raise Refusal('account executable attributes unavailable') from error
                fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC,
                             stat.S_IMODE(original.st_mode))
                created = True
                with os.fdopen(fd, 'wb') as dst:
                    require(os.fstat(dst.fileno()).st_gid == original.st_gid, 'account replacement group mismatch')
                    os.fchmod(dst.fileno(), stat.S_IMODE(original.st_mode))
                    shutil.copyfileobj(src, dst)
                    dst.flush()
                    os.fsync(dst.fileno())
            require(sha(temp) == expected_source, 'account replacement copy checksum mismatch')
            self.account_check(target)
            guard.check_mutation(target)
            temp_snapshot = guard._file_snapshot(temp)
            with ExclusiveFileLease(target, self.owner_uid) as lease:
                require(lease.digest() == expected_target, 'account lease target checksum changed')
                leased = os.fstat(lease.fd)
                require((leased.st_dev, leased.st_ino, leased.st_mode, leased.st_gid) ==
                        (original.st_dev, original.st_ino, original.st_mode, original.st_gid), 'account replacement target changed')
                try:
                    require(not os.listxattr(lease.fd), 'account executable extended attributes unsupported')
                except OSError as error:
                    raise Refusal('account executable attributes unavailable') from error
                guard.revalidate_while_leased(target, temp, temp_snapshot)
                lease.verify()
                os.replace(temp, target)
                created = False
                committed = True
                self.changed = True
                lease.verify(renamed=True)
            guard.acknowledge_replacement(target, expected_source)
            fsync_dir(os.path.dirname(target))
        except BaseException:
            if committed:
                guard.acknowledge_replacement(target, expected_source)
            raise
        finally:
            if created:
                guard.check_mutation(temp)
                os.unlink(temp)

    def safe_stop(self, *, first=False):
        self.account_check()
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
            guard = getattr(self.a, 'account_guard', None)
            require_no_running_app() if guard is None else require_no_running_app(guard)
            path_info = os.stat(self.exe)
            require((path_info.st_dev, path_info.st_ino) == self.original_executable_identity, 'original executable path changed')
            if guard is None:
                require_exclusive_executable(self.exe, pid, self.owner_uid)
            else:
                require_exclusive_executable(self.exe, pid, self.owner_uid, guard)
                self.account_check()
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
            self.account_check()
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
        self.account_check(self.a.evidence_dir)
        os.mkdir(self.a.evidence_dir, 0o700)
        self.evidence = self.a.evidence_dir
        if getattr(self.a, 'account_guard', None) is not None:
            self.a.account_guard.note_created_directory(self.a.evidence_dir)
        with self.exclusive_file(os.path.join(self.evidence, 'heartbeats.jsonl')):
            pass
        self.profile_unchanged()
        lockpath = self.gate_path + '.lock'
        self.account_check(lockpath)
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
            self.account_check(self.gate_path)
            os.mkdir(self.gate_path, 0o700)
            self.gate_created = True
            if getattr(self.a, 'account_guard', None) is not None:
                self.a.account_guard.note_created_directory(self.gate_path)
            if self.owner_uid != os.geteuid():
                os.chown(self.gate_path, self.owner_uid, -1, follow_symlinks=False)
            st = os.lstat(self.gate_path)
            self.gate_inode = (st.st_dev, st.st_ino)
            with self.exclusive_file(os.path.join(self.gate_path, 'owner')) as f:
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
        self.account_check()
        with open(self.destination, 'rb') as src, self.exclusive_file(self.backup, binary=True) as dst:
            if getattr(self.a, 'account_guard', None) is not None:
                source_info = os.fstat(src.fileno())
                require(not (source_info.st_mode & 0o7000), 'account backup source has special mode')
                os.fchmod(dst.fileno(), stat.S_IMODE(source_info.st_mode))
            shutil.copyfileobj(src, dst)
            dst.flush()
            os.fsync(dst.fileno())
        if getattr(self.a, 'account_guard', None) is None:
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
        with self.exclusive_file(os.path.join(self.evidence, 'baseline.json')) as f:
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
        guard = getattr(self.a, 'account_guard', None)
        users = executable_users(self.exe) if guard is None else executable_users(self.exe, guard)
        require(not users, 'original executable remains in use after unit stop')
        path_info = os.stat(self.exe)
        require((path_info.st_dev, path_info.st_ino) == self.original_executable_identity, 'original executable path changed during inventory')
        require_no_running_app() if guard is None else require_no_running_app(guard)
        self.account_check()
        if guard is None:
            self.changed = True
        self.replace(self.a.staged, self.destination)
        if legacy and getattr(self.a, 'legacy_wrapper', None):
            self.check_legacy_entrypoint()
        self.account_check()
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
            with u.exclusive_file(os.path.join(u.evidence, 'result.json')) as f:
                json.dump(u.result, f, indent=2)
                f.write('\n')
                f.flush()
                os.fsync(f.fileno())
            fsync_dir(u.evidence)
        except (Refusal, OSError):
            u.result['evidence_error'] = 'could not write result evidence'
            code = 1
    return code


if __name__ == '__main__':
    sys.exit(main())
