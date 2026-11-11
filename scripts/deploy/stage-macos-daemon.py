#!/usr/bin/env python3
"""Prepare an immutable candidate only; does not install, stop or start a daemon."""
import argparse
import hashlib
import json
import os
import plistlib
import re
import shutil
import stat
import subprocess
import sys


class Refusal(Exception):
    pass


def require(ok, message):
    if not ok:
        raise Refusal(message)


def clean_path(path, directory=False):
    require(os.path.isabs(path) and os.path.normpath(path) == path, 'paths must be absolute and normalized')
    current = '/'
    for part in path.split('/')[1:]:
        current = os.path.join(current, part)
        st = os.lstat(current)
        require(not stat.S_ISLNK(st.st_mode), 'symlink in asset or destination path')
    require(stat.S_ISDIR(st.st_mode) if directory else stat.S_ISREG(st.st_mode), 'unexpected path type')
    return path


def digest(path):
    h = hashlib.sha256()
    with open(path, 'rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def task_context():
    if any(os.environ.get(k) for k in ('MULTICA_TASK_ID', 'MULTICA_AGENT_ID', 'MULTICA_TASK_CONFIG_ROOT', 'MULTICA_DAEMON_PORT', 'MULTICA_QUICK_CREATE_TASK_ID')):
        return True
    path = os.getcwd()
    while True:
        marker = os.path.join(path, '.multica', 'daemon_task_context.json')
        if os.path.lexists(marker):
            return True
        parent = os.path.dirname(path)
        if path == parent:
            return False
        path = parent


def stage(a, *, platform=sys.platform, run=subprocess.run):
    require(platform == 'darwin', 'macOS is required')
    require(not task_context(), 'cannot run in a daemon task context')
    clean_path(a.binary)
    clean_path(a.destination, directory=True)
    clean_path(a.ca_cert)
    clean_path(a.working_dir, directory=True)
    clean_path(a.log_dir, directory=True)
    require(a.path_env and all(os.path.isabs(part) and os.path.normpath(part) == part for part in a.path_env.split(':')), 'PATH entries must be absolute and normalized')
    require(re.fullmatch(r'[0-9a-f]{64}', a.sha256) is not None, 'invalid SHA-256')
    require(re.fullmatch(r'[0-9a-f]{40}', a.commit) is not None, 'full lowercase commit required')
    require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', a.label) is not None, 'invalid launchd label')
    require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', a.profile) is not None, 'invalid profile')
    require(digest(a.binary) == a.sha256, 'candidate SHA-256 mismatch')
    result = run([a.binary, 'version', '--output', 'json'], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, check=False)
    require(result.returncode == 0, 'candidate version query failed')
    try:
        data = json.loads(result.stdout)
    except (ValueError, UnicodeDecodeError):
        raise Refusal('invalid candidate version response')
    require(isinstance(data, dict) and data.get('version') == a.version and data.get('build_commit') == a.commit, 'candidate identity mismatch')
    require(data.get('daemon_maintenance_gate') == 'v1' and data.get('daemon_lifecycle') == 'launchd-pinned-v1', 'candidate lacks required capabilities')
    folder = os.path.join(a.destination, a.commit + '-' + a.sha256)
    os.mkdir(folder, 0o700)  # Exclusive: a previous version directory is never overwritten.
    target = os.path.join(folder, 'multica')
    with open(a.binary, 'rb') as source, open(target, 'xb') as out:
        shutil.copyfileobj(source, out)
        out.flush()
        os.fsync(out.fileno())
    os.chmod(target, 0o500)
    require(digest(target) == a.sha256, 'copied candidate SHA-256 mismatch')
    marker = {'schema_version': 1, 'manager': 'launchd-pinned', 'label': a.label, 'binary': target, 'sha256': a.sha256, 'version': a.version, 'build_commit': a.commit}
    plist = {'Label': a.label, 'ProgramArguments': [target, '--profile', a.profile, 'daemon', 'start', '--foreground', '--no-auto-update', '--no-auto-reload'], 'RunAtLoad': True, 'KeepAlive': True, 'EnvironmentVariables': {'MULTICA_DAEMON_SUPERVISOR': 'launchd-pinned', 'MULTICA_DAEMON_AUTO_UPDATE': 'false', 'MULTICA_DAEMON_AUTO_RELOAD': 'false', 'MULTICA_ZH_TW_DESKTOP_SELF_RELOAD': '0', 'MULTICA_CA_CERT_FILE': a.ca_cert, 'PATH': a.path_env, 'HOME': os.path.expanduser('~')}, 'WorkingDirectory': a.working_dir, 'StandardOutPath': os.path.join(a.log_dir, a.label + '.stdout.log'), 'StandardErrorPath': os.path.join(a.log_dir, a.label + '.stderr.log')}
    with open(os.path.join(folder, 'launch-agent.plist'), 'xb') as out:
        plistlib.dump(plist, out)
        out.flush()
        os.fsync(out.fileno())
    with open(os.path.join(folder, 'daemon-management.json'), 'x', encoding='utf-8') as out:
        json.dump(marker, out, indent=2)
        out.write('\n')
        out.flush()
        os.fsync(out.fileno())
    for directory in (folder, a.destination):
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    return folder


def main():
    p = argparse.ArgumentParser(description=__doc__, epilog='Only prepares assets. It does not install the marker or LaunchAgent, prove a cold window, or authorize migration. Existing old daemons without a maintenance gate require a separately approved cold bootstrap procedure.')
    for name in ('binary', 'destination', 'sha256', 'version', 'commit', 'label', 'profile', 'ca-cert', 'path-env', 'working-dir', 'log-dir'):
        p.add_argument('--' + name, required=True)
    args = p.parse_args()
    try:
        folder = stage(args)
    except (Refusal, OSError, subprocess.TimeoutExpired) as e:
        print('Refused: ' + (str(e) if isinstance(e, Refusal) else 'filesystem or candidate execution failed; inspect destination before retry'), file=sys.stderr)
        return 1
    print('Prepared offline assets in ' + folder + '; no service or profile changed.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
