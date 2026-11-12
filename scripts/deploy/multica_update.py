#!/usr/bin/env python3
"""Prepare, plan and gate a Multica update; verify dispatch before cleanup."""
import argparse
import datetime
import fcntl
import tempfile
import time
import hashlib
import importlib.util
import json
import os
import plistlib
import pwd
import stat
import subprocess
import platform
import re
import shutil
import shlex
import ssl
import sys
from urllib.parse import urlsplit
import urllib.request
import uuid


def load_updater():
    if '_daemon_updater' in sys.modules:
        return sys.modules['_daemon_updater']
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'update-daemon.py')
    spec = importlib.util.spec_from_file_location('_daemon_updater', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


updater = load_updater()


class PendingReceipt(updater.Refusal):
    pass

# Set by the single-file release generator. Unbound development builds refuse.
EXPECTED_VERSION = None
EXPECTED_COMMIT = None
UPDATER_PROTOCOL = 1


def release_url(url, version):
    if not isinstance(url, str):
        return False
    parsed = urlsplit(url)
    if parsed.scheme != 'https' or parsed.query or parsed.fragment:
        return False
    parts = parsed.path.split('/')
    if parsed.netloc == 'github.com':
        return (len(parts) == 7 and parts[1:5] == ['hyc5566', 'multica', 'releases', 'download']
                and parts[5] in (version, 'v' + version, 'zh-tw-v' + version)
                and re.fullmatch(r'[A-Za-z0-9._+-]+', parts[6]) is not None)
    if parsed.netloc == '10.1.24.90:45671':
        return (len(parts) == 4 and parts[1:3] == ['downloads', version]
                and re.fullmatch(r'[A-Za-z0-9._+-]+', parts[3]) is not None)
    return False


def validate_manifest(manifest):
    require = updater.require
    require(isinstance(manifest, dict), 'manifest must be an object')
    require(manifest.get('source') == 'hyc5566/multica', 'unapproved source')
    require(manifest.get('data_compatibility') == 'multica-profile-v1', 'unsupported data compatibility')
    machine = {'x86_64': 'linux-amd64', 'aarch64': 'linux-arm64'}.get(platform.machine().lower()) if platform.system() == 'Linux' else None
    require(machine is not None and manifest.get('platform') == machine, 'release platform does not match this machine')
    version = manifest.get('version')
    require(updater.known(version) and re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', version) is not None, 'invalid release version')
    require(isinstance(manifest.get('commit'), str) and re.fullmatch(r'[0-9a-f]{40}', manifest['commit']) is not None, 'full commit required')
    assets = manifest.get('assets')
    require(isinstance(assets, list) and len(assets) > 0, 'manifest assets required')
    roles = set()
    for asset in assets:
        require(isinstance(asset, dict), 'invalid asset')
        require(asset.get('role') in ('binary', 'launcher') and asset['role'] not in roles, 'unique binary/launcher asset role required')
        roles.add(asset['role'])
        url = asset.get('url')
        require(isinstance(url, str), 'asset HTTPS URL required')
        require(release_url(url, version), 'asset URL does not match approved origin and version')
        if roles != {asset['role']}:
            require(urlsplit(url).netloc == origin, 'assets must share an HTTPS origin')
        origin = urlsplit(url).netloc
        require(type(asset.get('size')) is int and asset['size'] > 0, 'asset size required')
        require(isinstance(asset.get('sha256'), str) and re.fullmatch(r'[0-9a-f]{64}', asset['sha256']) is not None, 'asset SHA-256 required')
    require('binary' in roles, 'binary asset required')
    return manifest


def fingerprint(plan):
    data = json.dumps(plan, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode('utf-8')
    return hashlib.sha256(data).hexdigest()


def path_present(path):
    try:
        os.lstat(path)
        return True
    except FileNotFoundError:
        return False


def scan_roots(roots):
    """Find desktop bundles and launch metadata within the declared boundary."""
    found = []
    def failed(error):
        raise updater.Refusal('cannot fully inspect an application search root') from error
    for root in roots:
        if not path_present(root):
            continue
        updater.path_clean(root, 'dir')
        for base, directories, files in os.walk(root, followlinks=False, onerror=failed):
            for name in list(directories):
                path = os.path.join(base, name)
                if os.path.islink(path):
                    raise updater.Refusal('symlink inside search root makes inventory ambiguous')
                if name.lower().endswith('.app'):
                    info = os.path.join(path, 'Contents', 'Info.plist')
                    try:
                        updater.path_clean(info, 'file')
                        with open(info, 'rb') as f:
                            bundle = plistlib.load(f)
                    except (OSError, ValueError, plistlib.InvalidFileException) as error:
                        raise updater.Refusal('cannot inspect app bundle identity') from error
                    if 'multica' in str(bundle.get('CFBundleIdentifier', '')).lower():
                        found.append(path)
                    directories.remove(name)
            for name in files:
                path = os.path.join(base, name)
                lower = name.lower()
                if lower.endswith('.appimage') and 'multica' in lower:
                    found.append(path)
                elif lower.endswith('.desktop'):
                    if os.path.islink(path):
                        raise updater.Refusal('symlinked desktop metadata makes inventory ambiguous')
                    try:
                        with open(path, encoding='utf-8') as f:
                            text = f.read(1024 * 1024 + 1)
                        updater.require(len(text) <= 1024 * 1024, 'desktop metadata too large')
                    except (OSError, UnicodeError) as error:
                        raise updater.Refusal('cannot inspect desktop metadata') from error
                    if 'multica' in text.casefold():
                        found.append(path)
    return sorted(set(found))


def process_inventory(owner_uid, account_scope=None):
    return updater.process_inventory() if account_scope is None else updater.process_inventory(account_scope)


def scan_programs(roots):
    found = []
    def failed(error):
        raise updater.Refusal('cannot fully inspect a program search root') from error
    for root in roots:
        if not path_present(root):
            continue
        updater.path_clean(root, 'dir')
        for base, directories, files in os.walk(root, followlinks=False, onerror=failed):
            for name in directories:
                if os.path.islink(os.path.join(base, name)):
                    raise updater.Refusal('symlink inside search root makes inventory ambiguous')
            for name in files:
                if name.lower() in ('multica', 'multica.exe'):
                    path = os.path.join(base, name)
                    updater.path_clean(path, 'file')
                    found.append(path)
    return sorted(set(found))


DARWIN_MULTICA_BUNDLE_IDS = ('ai.multica.desktop.zh-tw.local', 'ai.multica.desktop', 'ai.multica.desktop.dev')
DARWIN_APPROVED_BUNDLE_ID = 'ai.multica.desktop.zh-tw.local'


def darwin_bundle_for_path(path):
    parts = os.path.abspath(path).split(os.sep)
    for i in range(1, len(parts)):
        if parts[i].lower().endswith('.app'):
            return os.sep.join(parts[:i + 1]) or os.sep
    return None


def darwin_bundle_id(bundle):
    updater.path_clean(bundle, 'dir')
    info = updater.path_clean(os.path.join(bundle, 'Contents', 'Info.plist'), 'file')
    try:
        with open(info, 'rb') as f:
            data = plistlib.load(f)
    except (OSError, ValueError, plistlib.InvalidFileException) as error:
        raise updater.Refusal('unknown: cannot inspect application bundle identity') from error
    updater.require(isinstance(data, dict) and isinstance(data.get('CFBundleIdentifier'), str) and data['CFBundleIdentifier'].strip(), 'unknown: application bundle identity unavailable')
    return data['CFBundleIdentifier']


def darwin_scan_roots(roots):
    found = {}
    def failed(error):
        raise updater.Refusal('unknown: cannot inspect application search root') from error
    for root in roots:
        if not path_present(root):
            continue
        updater.path_clean(root, 'dir')
        for base, directories, _ in os.walk(root, followlinks=False, onerror=failed):
            for name in list(directories):
                path = os.path.join(base, name)
                updater.require(not os.path.islink(path), 'unknown: symlink inside search root')
                if name.lower().endswith('.app'):
                    bundle_id = darwin_bundle_id(path)
                    if 'multica' in bundle_id.casefold() or 'multica' in name.casefold():
                        found[path] = bundle_id
                    directories.remove(name)
    return found


def darwin_command(args):
    try:
        result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False, timeout=30)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise updater.Refusal('unknown: required macOS inventory query failed') from error
    updater.require(result.returncode == 0 and not result.stderr.strip(), 'unknown: required macOS inventory query failed')
    try:
        return result.stdout.decode('utf-8', errors='strict')
    except UnicodeDecodeError as error:
        raise updater.Refusal('unknown: invalid macOS inventory output') from error


def _darwin_inspect(owner_uid, search_roots, cli_hint):
    require = updater.require
    require(type(owner_uid) is int and owner_uid == os.geteuid(), 'unknown: owner UID does not match operator')
    require(isinstance(search_roots, list) and search_roots and all(isinstance(x, str) and os.path.isabs(x) and os.path.normpath(x) == x for x in search_roots), 'unknown: explicit normalized search roots required')
    for root in search_roots:
        require(path_present(root), 'unknown: declared search root does not exist')
        updater.path_clean(root, 'dir')
    require(isinstance(cli_hint, str) and os.path.isabs(cli_hint) and os.path.normpath(cli_hint) == cli_hint, 'unknown: normalized absolute CLI hint required')
    require(path_present(cli_hint), 'unknown: CLI hint does not exist')
    real_cli = os.path.realpath(cli_hint)
    updater.path_clean(real_cli, 'file')
    home = pwd.getpwuid(owner_uid).pw_dir
    roots = sorted(set(search_roots + ['/Applications', '/System/Applications', os.path.join(home, 'Applications')]))
    bundles = darwin_scan_roots(roots)
    for bundle_id in (*DARWIN_MULTICA_BUNDLE_IDS, '*multica*'):
        query = 'kMDItemCFBundleIdentifier == "' + bundle_id + '"'
        output = darwin_command(['/usr/bin/mdfind', '-0', query])
        for path in filter(None, output.split('\0')):
            require(os.path.isabs(path) and path.lower().endswith('.app'), 'unknown: unexpected Spotlight result')
            actual = os.path.realpath(path)
            actual_id = darwin_bundle_id(actual)
            require(('multica' in actual_id.casefold()) if bundle_id == '*multica*' else actual_id == bundle_id, 'unknown: Spotlight and bundle identity disagree')
            bundles[actual] = actual_id
    cli_bundle = darwin_bundle_for_path(real_cli)
    cli_id = darwin_bundle_id(cli_bundle) if cli_bundle else None
    if cli_bundle and ('multica' in cli_id.casefold() or 'multica' in cli_bundle.casefold()):
        bundles[cli_bundle] = cli_id
    output = darwin_command(['/bin/ps', '-U', str(owner_uid), '-o', 'pid=,comm='])
    processes = []
    for line in output.splitlines():
        fields = line.strip().split(None, 1)
        require(len(fields) == 2 and fields[0].isdigit(), 'unknown: malformed process inventory')
        pid, executable = int(fields[0]), fields[1]
        if not os.path.isabs(executable):
            require('multica' not in executable.casefold(), 'unknown: Multica process executable path unavailable')
            continue
        resolved = os.path.realpath(executable)
        bundle = darwin_bundle_for_path(resolved)
        if bundle:
            bundle_id = darwin_bundle_id(bundle)
            if 'multica' in bundle_id.casefold() or 'multica' in bundle.casefold():
                updater.path_clean(resolved, 'file')
                bundles[bundle] = bundle_id
                processes.append({'pid': pid, 'exe': resolved, 'bundle': bundle})
        elif 'multica' in resolved.casefold():
            updater.path_clean(resolved, 'file')
            processes.append({'pid': pid, 'exe': resolved, 'bundle': None})
    foreign = any(bundle_id != DARWIN_APPROVED_BUNDLE_ID for bundle_id in bundles.values())
    standalone_process = any(item['bundle'] is None for item in processes)
    if foreign or len(bundles) > 1 or (bundles and (cli_bundle not in bundles or standalone_process)):
        status = 'mixed'
    elif bundles:
        status = 'use_app'
    else:
        status = 'unsupported'
    return {'status': status, 'owner_uid': owner_uid, 'search_roots': roots, 'cli_hint': cli_hint,
            'cli_resolved': real_cli, 'applications': [{'path': path, 'bundle_id': value} for path, value in sorted(bundles.items())], 'processes': processes}


def darwin_inspect(owner_uid, search_roots, cli_hint):
    try:
        return _darwin_inspect(owner_uid, search_roots, cli_hint)
    except (OSError, ValueError, KeyError) as error:
        raise updater.Refusal('unknown: macOS inventory could not be completed') from error


def inspect_install(owner_uid, search_roots, cli_hint):
    """Inspect the declared owner installation boundary without writes or network."""
    require = updater.require
    if platform.system() == 'Darwin':
        return darwin_inspect(owner_uid, search_roots, cli_hint)
    require(platform.system() == 'Linux', 'unknown: this platform inventory is unsupported')
    require(type(owner_uid) is int and owner_uid == os.geteuid(), 'unknown: owner UID does not match operator')
    require(isinstance(search_roots, list) and search_roots and all(isinstance(x, str) and os.path.isabs(x) and os.path.normpath(x) == x for x in search_roots), 'unknown: explicit normalized search roots required')
    require(isinstance(cli_hint, str) and os.path.isabs(cli_hint), 'unknown: absolute CLI hint required')
    require(os.path.normpath(cli_hint) == cli_hint, 'unknown: CLI hint must be normalized')
    if path_present(cli_hint):
        updater.path_clean(cli_hint, 'file')
    else:
        updater.path_clean(os.path.dirname(cli_hint), 'dir')
    home = pwd.getpwuid(owner_uid).pw_dir
    roots = sorted(set(search_roots + ['/usr/share/applications', '/usr/local/share/applications', '/var/lib/flatpak/exports/share/applications', '/var/lib/snapd/desktop/applications', '/opt',
                                       os.path.join(home, 'Applications'), os.path.join(home, '.local/share/applications'), os.path.join(home, '.local/share/flatpak/exports/share/applications')]))
    apps = scan_roots(roots)
    programs = scan_programs(roots)
    identities = {(os.stat(path).st_dev, os.stat(path).st_ino) for path in programs}
    if os.path.exists(cli_hint):
        hint = os.stat(cli_hint)
        identities.add((hint.st_dev, hint.st_ino))
    processes = process_inventory(owner_uid)
    app_processes = [p for p in processes if p.get('app', 'multica' in p['exe'].lower() and ('.app/' in p['exe'].lower() or p['exe'].lower().endswith('.appimage') or 'electron' in p['exe'].lower()))]
    cli_app = '.app/' in cli_hint.lower() or cli_hint.lower().endswith('.appimage')
    if apps or app_processes or cli_app:
        status = 'app'
        if not cli_app:
            status = 'mixed'
    else:
        status = 'standalone' if len(identities) <= (1 if os.path.exists(cli_hint) else 0) else 'mixed'
    return {'status': status, 'owner_uid': owner_uid, 'search_roots': roots, 'cli_hint': cli_hint,
            'applications': apps, 'programs': programs, 'processes': processes, 'app_processes': app_processes}


def validate_staged_asset(path, asset, version, commit, owner_uid):
    updater.path_clean(path, 'file')
    updater.require(os.path.getsize(path) == asset['size'] and updater.sha(path) == asset['sha256'], 'staged asset size or SHA-256 mismatch')
    probe = updater.Updater(argparse.Namespace(owner_uid=owner_uid))
    updater.require(probe.version_as_owner(path) == (version, commit), 'staged asset release identity mismatch')


def target_machine_fingerprint():
    with open('/etc/machine-id', 'rb') as f:
        raw = f.read(128)
    value = raw.strip()
    updater.require(re.fullmatch(rb'[0-9a-f]{32}', value) is not None and value != b'0' * 32, 'machine identity unavailable')
    return hashlib.sha256(value).hexdigest()


def launcher_ca_path(launcher, expected_sha256):
    updater.require(isinstance(expected_sha256, str) and re.fullmatch(r'[0-9a-f]{64}', expected_sha256), 'approved CA SHA-256 required for launcher layout')
    updater.path_clean(launcher, 'file')
    with open(launcher, encoding='utf-8') as f:
        text = f.read(1024 * 1024 + 1)
    updater.require(len(text) <= 1024 * 1024, 'launcher is too large')
    names = ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')
    found = {}
    for line in text.splitlines():
        if not line.startswith('export '):
            continue
        try:
            words = shlex.split(line)
        except ValueError as error:
            raise updater.Refusal('launcher shell syntax cannot be verified') from error
        for word in words[1:]:
            key, sep, value = word.partition('=')
            if key in names:
                updater.require(sep and key not in found and os.path.isabs(value) and '$' not in value and '`' not in value, 'launcher CA configuration is ambiguous')
                found[key] = value
    updater.require(set(found) == set(names) and len(set(found.values())) == 1, 'launcher CA configuration is missing or inconsistent')
    path = next(iter(found.values()))
    updater.path_clean(path, 'file')
    updater.require(updater.sha(path) == expected_sha256, 'launcher CA bundle differs from approved hash')
    try:
        ssl.create_default_context(cafile=path)
    except (OSError, ssl.SSLError) as error:
        raise updater.Refusal('launcher CA bundle is not usable') from error
    return path


def legacy_wrapper_path(wrapper, binary, ca_sha256):
    """Recognize only the fixed CA-export and exec wrapper form."""
    updater.require(re.fullmatch(r'/[A-Za-z0-9_./:+-]+', binary) is not None, 'unsupported legacy binary path')
    updater.path_clean(wrapper, 'file')
    with open(wrapper, encoding='utf-8') as f:
        lines = f.read(1024 * 1024 + 1).splitlines()
    updater.require(lines and lines[0] in ('#!/bin/sh', '#!/usr/bin/env bash') and sum(map(len, lines)) <= 1024 * 1024, 'unsupported legacy wrapper')
    if lines[0] == '#!/usr/bin/env bash':
        updater.require(len(lines) == 10 and lines[1] == 'export MULTICA_DAEMON_AUTO_UPDATE=${MULTICA_DAEMON_AUTO_UPDATE:-true} MULTICA_DAEMON_AUTO_UPDATE_INTERVAL=${MULTICA_DAEMON_AUTO_UPDATE_INTERVAL:-24h}' and lines[2] == 'export MULTICA_DAEMON_AUTO_RELOAD=true MULTICA_DAEMON_DYNAMIC_PORT=true', 'unsupported official legacy wrapper')
        try:
            commands = [shlex.split(line, comments=False, posix=True) for line in lines[3:]]
        except ValueError as error:
            raise updater.Refusal('unsupported legacy wrapper syntax') from error
        updater.require(all(not any(c in line for c in '$`;|&<>()') for line in lines[3:-1]), 'unsafe legacy wrapper literal')
        updater.require(len(commands) == 7 and commands[-1] == ['exec', binary, '$@'], 'unsupported official wrapper commands')
        def assignments(command, names):
            updater.require(len(command) == len(names) + 1 and command[0] == 'export', 'unsupported official wrapper export')
            result = {}
            for part, name in zip(command[1:], names):
                key, sep, value = part.partition('=')
                updater.require(sep and key == name and value, 'unsupported official wrapper export')
                result[key] = value
            return result
        manifest = assignments(commands[0], ['MULTICA_ZH_TW_RELEASE_MANIFEST_URL'])['MULTICA_ZH_TW_RELEASE_MANIFEST_URL']
        updater.require(manifest == 'https://10.1.24.90:45671/downloads/latest.json', 'unapproved wrapper release URL')
        locations = assignments(commands[1], ['MULTICA_ZH_TW_LAUNCHER', 'MULTICA_ZH_TW_INSTALL_ROOT'])
        install_root = locations['MULTICA_ZH_TW_INSTALL_ROOT']
        updater.require(locations['MULTICA_ZH_TW_LAUNCHER'] == wrapper and re.fullmatch(r'/[A-Za-z0-9_./:+-]+', install_root) is not None and os.path.commonpath((install_root, wrapper)) == install_root and os.path.commonpath((install_root, binary)) == install_root, 'wrapper installation root mismatch')
        for command, name in zip(commands[2:5], ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')):
            value = assignments(command, [name])[name]
            updater.require(re.fullmatch(r'/[A-Za-z0-9_./:+-]+', value) is not None, 'unsafe wrapper CA path')
        urls = assignments(commands[5], ['MULTICA_SERVER_URL', 'MULTICA_APP_URL'])
        updater.require(set(urls.values()) == {'https://10.1.24.90:45671'}, 'unapproved wrapper service URL')
        return launcher_ca_path(wrapper, ca_sha256)
    commands = []
    for line in lines[1:]:
        if not line.strip():
            continue
        try:
            commands.append(shlex.split(line, comments=False, posix=True))
        except ValueError as error:
            raise updater.Refusal('unsupported legacy wrapper syntax') from error
    if commands and commands[0] == ['set', '-eu']:
        commands.pop(0)
    updater.require(len(commands) == 4 and commands[-1] == ['exec', binary, '$@'], 'unsupported legacy wrapper commands')
    names = ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')
    values = {}
    for command in commands[:3]:
        updater.require(len(command) == 2 and command[0] == 'export', 'unsupported legacy wrapper export')
        key, separator, value = command[1].partition('=')
        updater.require(separator and key in names and key not in values and re.fullmatch(r'/[A-Za-z0-9_./:+-]+', value) is not None, 'unsupported legacy wrapper export')
        values[key] = value
    updater.require(set(values) == set(names) and len(set(values.values())) == 1, 'legacy wrapper CA exports differ')
    return launcher_ca_path(wrapper, ca_sha256)


def build_plan(config, manifest, manifest_sha256, *, legacy_bootstrap=False, legacy_target=False, observe_legacy=False, rollback_observer=False):
    """Build a read-only plan from independently inspected local resources."""
    require = updater.require
    require(isinstance(config, dict), 'inventory must be an object')
    required = ('owner_uid', 'profile', 'unit', 'systemd_scope', 'cli', 'health_url',
                'staged', 'search_roots', 'evidence_dir', 'ledger', 'asset_root', 'controller_machine_id_sha256')
    require(all(key in config for key in required), 'inventory is missing required fields')
    controller = config['controller_machine_id_sha256']
    require(isinstance(controller, str) and re.fullmatch(r'[0-9a-f]{64}', controller) is not None, 'controller machine fingerprint required')
    machine = target_machine_fingerprint()
    require(controller != machine, 'controller and target are the same physical machine')
    inventory_scope = config.get('inventory_scope', 'host')
    require(inventory_scope in ('host', 'account'), 'invalid inventory scope')
    if inventory_scope == 'account':
        require(platform.system() == 'Linux', 'account inventory requires Linux')
        require(config.get('layout', 'binary') == 'binary', 'account inventory only supports binary layout')
    uid = config['owner_uid']
    require(type(uid) is int and uid >= 0, 'invalid owner UID')
    require(uid == os.geteuid() or os.geteuid() == 0, 'operator cannot act for target owner')
    require(config['systemd_scope'] in ('user', 'system'), 'invalid unit scope')
    require(config['systemd_scope'] != 'system' or os.geteuid() == 0, 'system scope requires root')
    require(isinstance(config['profile'], str) and re.fullmatch(r'[A-Za-z0-9_-]*', config['profile']) is not None, 'invalid profile')
    require(isinstance(config['unit'], str) and re.fullmatch(r'[A-Za-z0-9_.:@-]+\.service', config['unit']) is not None, 'exact service unit required')
    health_url = urlsplit(config['health_url'])
    require(health_url.scheme == 'http' and health_url.hostname == '127.0.0.1' and health_url.port and health_url.path == '/health' and not health_url.query and not health_url.fragment and not health_url.username, 'health URL must be loopback HTTP /health')
    roots = config['search_roots']
    require(isinstance(roots, list) and roots and all(isinstance(root, str) and os.path.isabs(root) and os.path.normpath(root) == root for root in roots), 'explicit application search roots required')
    if inventory_scope == 'account':
        for root in roots:
            require(path_present(root), 'account search root does not exist: ' + root)
            updater.path_clean(root, 'dir')
            require(os.lstat(root).st_uid == uid, 'account search root must be owned by target: ' + root)
    home = pwd.getpwuid(uid).pw_dir
    personal_roots = [os.path.join(home, 'Applications'), os.path.join(home, '.local/share/applications'), os.path.join(home, '.local/share/flatpak/exports/share/applications')]
    global_roots = ['/usr/share/applications', '/usr/local/share/applications', '/var/lib/flatpak/exports/share/applications', '/var/lib/snapd/desktop/applications', '/opt']
    roots = sorted(set(roots + personal_roots + (global_roots if inventory_scope == 'host' else [config['asset_root']])))
    apps = scan_roots(roots)
    require(not apps, 'desktop application found; update through the application')
    assets = {a['role']: a for a in manifest['assets']}
    layout = config.get('layout', 'binary')
    require(layout in ('binary', 'launcher'), 'invalid installation layout')
    require(layout == 'binary' or ('launcher' in assets and isinstance(config.get('entrypoint'), str) and isinstance(config.get('staged_binary'), str)), 'launcher layout requires launcher asset, entrypoint and staged_binary')
    for key in ('cli', 'staged'):
        updater.path_clean(config[key], 'file')
        require(os.access(config[key], os.X_OK), 'CLI and staged binary must be executable')
    staged_binary = config['staged'] if layout == 'binary' else config['staged_binary']
    if legacy_target:
        require(os.path.getsize(staged_binary) == assets['binary']['size'] and updater.sha(staged_binary) == assets['binary']['sha256'], 'legacy rollback binary hash mismatch')
    else:
        validate_staged_asset(staged_binary, assets['binary'], manifest['version'], manifest['commit'], uid)
    if rollback_observer:
        ledger_for_observer = read_ledger(config['ledger'])
        require(isinstance(ledger_for_observer, dict) and updater.sha(config['cli']) == ledger_for_observer.get('transaction', {}).get('observer_sha256'), 'rollback observer changed')
        probe = updater.Updater(argparse.Namespace(owner_uid=uid))
        observer_version, observer_commit = probe.version_as_owner(config['cli'])
        require(updater.known(observer_version) and isinstance(observer_commit, str) and re.fullmatch(r'[0-9a-f]{40}', observer_commit) is not None, 'rollback observer identity unavailable')
    else:
        validate_staged_asset(config['cli'], assets['binary'], manifest['version'], manifest['commit'], uid)
    if layout == 'launcher':
        validate_staged_asset(config['staged'], assets['launcher'], manifest['version'], manifest['commit'], uid)
        updater.path_clean(config['entrypoint'], 'file')
        require(all(os.stat(path).st_uid == uid for path in (staged_binary, config['staged'], config['entrypoint'])), 'launcher assets must be owned by daemon owner')
        require(os.path.samefile(config['staged'], os.path.join(os.path.dirname(staged_binary), 'launcher')), 'staged launcher is not paired with staged binary')
    args_config = dict(config)
    args_config.pop('layout', None)
    args_config.pop('entrypoint', None)
    args = argparse.Namespace(**args_config, layout=layout, sha256=assets['binary']['sha256'],
                               version=manifest['version'], commit=manifest['commit'], timeout=180,
                               heartbeat_samples=3, heartbeat_interval=15, no_shared_profiles=True,
                               entrypoint=config.get('entrypoint'), binary=staged_binary if layout == 'launcher' else None)
    u = updater.Updater(args)
    health = u.health()
    gate = health.get('maintenance_gate')
    expected = os.path.join(home, '.multica', 'profiles', config['profile']) if config['profile'] else os.path.join(home, '.multica')
    if legacy_bootstrap:
        require(gate is None or (isinstance(gate, dict) and gate.get('supported') is False), 'legacy bootstrap requires a daemon without gate support')
        u.gate = os.path.join(expected, 'daemon.maintenance')
    else:
        require(isinstance(gate, dict) and gate.get('supported') is True, 'cold-window-required')
        require(gate.get('blocked') is False, 'maintenance gate is already blocked')
        u.gate = gate.get('path')
        require(u.gate == os.path.join(expected, 'daemon.maintenance'), 'profile gate path mismatch')
    profile_path = updater.path_clean(os.path.realpath(expected), 'dir')
    profile_stat = os.stat(profile_path)
    require(profile_stat.st_uid == uid and not os.path.lexists(os.path.join(profile_path, 'daemon.maintenance')), 'profile owner or gate mismatch')
    if legacy_bootstrap:
        pid = health.get('pid')
        state, unit_pid = u.unit_pid()
        require(health.get('status') == 'running' and health.get('profile') == config['profile'] and updater.known(health.get('daemon_id')), 'legacy health identity mismatch')
        require(type(pid) is int and pid > 1 and state == 'active' and unit_pid == pid, 'legacy unit and health PID mismatch')
    else:
        pid = u.inspect(health)
    exe = updater.proc_exe(pid)
    require(os.stat('/proc/%d' % pid).st_uid == uid and os.stat(exe).st_uid == uid, 'daemon owner mismatch')
    image = os.stat('/proc/%d/exe' % pid)
    installed = os.stat(exe)
    require((image.st_dev, image.st_ino) == (installed.st_dev, installed.st_ino), 'running executable path changed')
    asset_root = updater.path_clean(config['asset_root'], 'dir')
    account_guard = None
    ledger = read_ledger(config['ledger'])
    if isinstance(ledger, dict) and isinstance(ledger.get('transaction'), dict):
        previous_inventory = ledger['transaction'].get('inventory')
        if isinstance(previous_inventory, dict):
            previous_scope = previous_inventory.get('inventory_scope', 'host')
            require(previous_scope == inventory_scope, 'ledger inventory scope changed')
    if inventory_scope == 'account':
        require(uid == os.geteuid() and config['systemd_scope'] == 'user', 'account inventory requires the target effective UID and user unit')
        require(os.path.realpath(expected) == expected, 'account profile state symlink is unsupported')
        require(updater.path_clean(expected, 'dir') == profile_path, 'account profile state must have a canonical path')
        require(os.path.commonpath((asset_root, config['ledger'])) == asset_root, 'account ledger must be inside private asset root')
        immutable = [exe, config['cli'], staged_binary]
        account_recorded_inputs = []
        if ledger is not None:
            require(isinstance(ledger.get('assets'), list), 'ledger managed assets missing')
            immutable.extend(item['path'] for item in ledger['assets'] if isinstance(item, dict) and isinstance(item.get('path'), str) and path_present(item['path']))
            evidence_records = ledger.get('account_evidence_files', [])
            require(isinstance(evidence_records, list), 'invalid account evidence records')
            for row in evidence_records:
                names = {'heartbeat': 'heartbeats.jsonl', 'baseline': 'baseline.json', 'result': 'result.json'}
                require(isinstance(row, dict) and set(row) == {'role', 'path', 'sha256'} and row['role'] in names and
                        isinstance(row['path'], str) and os.path.isabs(row['path']) and os.path.normpath(row['path']) == row['path'] and
                        os.path.commonpath((asset_root, row['path'])) == asset_root and os.path.basename(row['path']) == names[row['role']] and
                        isinstance(row['sha256'], str) and re.fullmatch(r'[0-9a-f]{64}', row['sha256']), 'invalid account evidence record')
                updater.path_clean(row['path'], 'file')
                require(updater.sha(row['path']) == row['sha256'], 'recorded account evidence changed')
                immutable.append(row['path'])
            for key in ('account_inputs', 'account_obsolete_inputs'):
                rows = ledger.get(key, [])
                require(isinstance(rows, list), 'invalid account input records')
                for row in rows:
                    require(isinstance(row, dict) and set(row) == {'role', 'path', 'sha256'} and
                            row['role'] in ('observer', 'ca', 'baseline') and isinstance(row['path'], str) and
                            os.path.isabs(row['path']) and os.path.normpath(row['path']) == row['path'] and
                            os.path.commonpath((asset_root, row['path'])) == asset_root and
                            isinstance(row['sha256'], str) and re.fullmatch(r'[0-9a-f]{64}', row['sha256']), 'invalid account input record')
                    if path_present(row['path']):
                        updater.path_clean(row['path'], 'file')
                        require(updater.sha(row['path']) == row['sha256'], 'recorded account input changed')
                        account_recorded_inputs.append(row['path'])
                        immutable.append(row['path'])
                    else:
                        require(key == 'account_obsolete_inputs' and ledger.get('status') in ('cleaning', 'cleanup_failed'), 'recorded account input missing')
        require('account_auxiliary_paths' not in config, 'account auxiliary path assertions are unsupported')
        for role in ('observer_ca_cert_file', 'baseline_manifest'):
            path = config.get(role)
            if path is not None:
                require(isinstance(path, str) and os.path.isabs(path) and os.path.commonpath((asset_root, path)) == asset_root,
                        'account input must be inside private asset root')
                immutable.append(path)
        prior_evidence = []
        if ledger is not None and isinstance(ledger.get('transaction'), dict):
            previous_inventory = ledger['transaction'].get('inventory', {})
            previous_dir = previous_inventory.get('evidence_dir') if isinstance(previous_inventory, dict) else None
            if isinstance(previous_dir, str) and os.path.isabs(previous_dir) and os.path.commonpath((asset_root, previous_dir)) == asset_root:
                prior_evidence = [os.path.join(previous_dir, name) for name in ('heartbeats.jsonl', 'original-entrypoint', 'baseline.json', 'result.json') if path_present(os.path.join(previous_dir, name))]
                immutable.extend(prior_evidence)
        directories = []
        if ledger is not None:
            records = ledger.get('account_directories', [])
            require(isinstance(records, list), 'invalid account directory records')
            for record in records:
                require(isinstance(record, dict) and set(record) == {'path', 'identity'} and isinstance(record['path'], str) and
                        os.path.isabs(record['path']) and os.path.normpath(record['path']) == record['path'] and
                        os.path.commonpath((asset_root, record['path'])) == asset_root and record['path'] != asset_root and
                        isinstance(record['identity'], list), 'invalid account directory record')
                info = os.lstat(record['path'])
                require(stat.S_ISDIR(info.st_mode) and updater.AccountScope._fields(info)[:5] == record['identity'], 'account recorded directory changed')
                directories.append(record['path'])
        evidence_dir = config['evidence_dir']
        transaction_paths = [exe, config['ledger'], config['ledger'] + '.lock', evidence_dir, *directories,
                             *[os.path.join(evidence_dir, name) for name in ('heartbeats.jsonl', 'original-entrypoint', 'baseline.json', 'result.json')],
                             os.path.join(profile_path, 'daemon.maintenance'), os.path.join(profile_path, 'daemon.maintenance.lock'),
                             os.path.join(profile_path, 'daemon.maintenance', 'owner'), *immutable]
        exec_start = u.account_unit_property('ExecStart')
        fragment = u.account_unit_property('FragmentPath')
        dropins = u.account_unit_property('DropInPaths')
        unit_object, ancillary = u.account_unit_ancillary()
        account_guard = updater.AccountScope(uid, asset_root, profile_path, immutable, transaction_paths,
                                             config['unit'], config['systemd_scope'], exec_start, fragment, exe,
                                             config['profile'], dropins, unit_object, ancillary, config.get('account_path_policy'))
    executable_processes = (updater.require_exclusive_executable(exe, pid, uid) if account_guard is None else
                            updater.require_exclusive_executable(exe, pid, uid, account_guard))
    managed_paths = [exe, config['evidence_dir'], staged_binary]
    if layout == 'launcher':
        managed_paths.extend([staged_binary, config['staged'], config['entrypoint']])
    require(all(os.path.isabs(path) and os.path.commonpath((asset_root, path)) == asset_root for path in managed_paths), 'program and evidence must be inside managed asset root')
    require(os.path.commonpath((asset_root, profile_path)) != asset_root and os.path.commonpath((asset_root, profile_path)) != profile_path, 'managed program root must be separate from profile state')
    if layout == 'launcher':
        original_launcher = updater.path_clean(os.path.join(os.path.dirname(exe), 'launcher'), 'file')
        require(os.path.samefile(config['entrypoint'], original_launcher), 'entrypoint does not match running launcher')
        old_ca = launcher_ca_path(original_launcher, config.get('ca_sha256'))
        new_ca = launcher_ca_path(config['staged'], config.get('ca_sha256'))
    processes = process_inventory(uid) if account_guard is None else process_inventory(uid, account_guard)
    require(not any(item.get('app', False) for item in processes), 'desktop application running; update through the application')
    related = [item for item in processes if item.get('uid', uid) == uid and 'multica' in item['exe'].casefold()]
    require(len(related) == 1 and related[0]['pid'] == pid and os.path.samefile(related[0]['exe'], exe), 'running Multica process inventory is ambiguous')
    if not legacy_bootstrap:
        require(u.version_as_owner(exe) == (health.get('cli_version'), health.get('cli_commit')), 'running executable identity mismatch')
    require(not os.path.samefile(config['cli'], exe) and not os.path.samefile(config['cli'], config['staged']), 'observer CLI must be independent')
    updater.validate_observer_ca(config.get('observer_ca_cert_file'), config.get('observer_ca_sha256'))
    u.base = {'daemon_id': health['daemon_id'], 'workspaces': u.workspaces(health)}
    runtime = u.runtimes()
    require(runtime, 'runtime inventory required')
    known_paths = [exe, config['cli'], staged_binary] + (account_recorded_inputs if account_guard is not None else [])
    if legacy_bootstrap and config.get('legacy_wrapper'):
        known_paths.append(updater.path_clean(config['legacy_wrapper'], 'file'))
    if ledger is not None:
        require(isinstance(ledger.get('assets'), list), 'ledger managed assets missing')
        shared = ledger.get('shared_assets', [])
        require(isinstance(shared, list) and all(isinstance(item, dict) and item.get('role') in ('wrapper', 'ca') for item in shared), 'invalid shared asset inventory')
        asset_hashes = {item.get('path'): item.get('sha256') for item in ledger['assets'] if isinstance(item, dict)}
        require(len({item.get('role') for item in shared}) == len(shared) and all(asset_hashes.get(item.get('path')) == item.get('sha256') for item in shared), 'shared asset missing from ledger')
        for item in ledger['assets']:
            require(isinstance(item, dict) and isinstance(item.get('path'), str) and isinstance(item.get('sha256'), str), 'invalid ledger asset')
            managed = item['path']
            require(os.path.isabs(managed) and os.path.normpath(managed) == managed and os.path.commonpath((asset_root, managed)) == asset_root and managed != asset_root, 'ledger asset outside managed root')
            updater.path_clean(managed, 'file')
            require(updater.sha(managed) == item['sha256'], 'ledger asset hash mismatch')
            known_paths.append(managed)
    known_ids = {(os.stat(path).st_dev, os.stat(path).st_ino) for path in known_paths}
    programs = scan_programs(roots)
    require(all((os.stat(path).st_dev, os.stat(path).st_ino) in known_ids for path in programs), 'unrecognized additional Multica installation found')
    legacy_details = None
    if legacy_bootstrap:
        require(ledger is None, 'legacy bootstrap requires an unmanaged installation')
        approved = config.get('legacy_baseline')
        require(observe_legacy or isinstance(approved, dict), 'approved legacy baseline inventory required')
        wrapper = config.get('legacy_wrapper')
        require(wrapper is None or (layout == 'binary' and isinstance(wrapper, str)), 'legacy wrapper requires binary layout')
        if wrapper is not None:
            require(os.path.commonpath((asset_root, wrapper)) == asset_root, 'legacy wrapper outside managed root')
            require(os.stat(wrapper).st_uid == uid and os.access(wrapper, os.X_OK), 'legacy wrapper owner or mode mismatch')
            legacy_ca = legacy_wrapper_path(wrapper, exe, config.get('ca_sha256'))
            require(os.path.commonpath((asset_root, legacy_ca)) == asset_root, 'legacy wrapper CA outside managed root')
        entrypoint = wrapper or (config['entrypoint'] if layout == 'launcher' else exe)
        entry_stat = os.stat(entrypoint)
        require(layout != 'binary' or os.stat(exe).st_nlink == 1, 'legacy binary hard links require a separately supported layout')
        require(wrapper is None or entry_stat.st_nlink == 1, 'legacy wrapper hard links are unsupported')
        exec_start = updater.run(u.systemctl('show', config['unit'], '--property=ExecStart', '--value')).decode('utf-8').strip()
        exec_config = updater.exec_start_configuration(exec_start)
        kill_mode = updater.run(u.systemctl('show', config['unit'], '--property=KillMode', '--value')).decode('utf-8').strip()
        group = updater.run(u.systemctl('show', config['unit'], '--property=ControlGroup', '--value')).decode('utf-8').strip()
        require(kill_mode in ('control-group', 'mixed') and pid in updater.cgroup_members(group), 'legacy unit cgroup cannot be safely drained')
        require(exec_config['path'] == entrypoint, 'unsupported or ambiguous legacy systemd entrypoint')
        legacy_details = {'machine_id_sha256': machine, 'unit': config['unit'], 'systemd_scope': config['systemd_scope'],
                          'pid': pid, 'daemon_id': health['daemon_id'], 'binary_sha256': updater.sha('/proc/%d/exe' % pid),
                          'entrypoint': {'path': entrypoint, 'sha256': updater.sha(entrypoint), 'mode': stat.S_IMODE(entry_stat.st_mode),
                                         'uid': entry_stat.st_uid, 'gid': entry_stat.st_gid, 'device': entry_stat.st_dev,
                                         'inode': entry_stat.st_ino, 'links': entry_stat.st_nlink}, 'exec_start': exec_start,
                          'exec_start_configuration': exec_config, 'kill_mode': kill_mode, 'control_group': group, 'data_compatibility': 'multica-profile-v1',
                          **({'wrapper_ca_path': legacy_ca} if wrapper is not None else {})}
        if not observe_legacy:
            require({k: v for k, v in approved.items() if k != 'exec_start'} == {k: v for k, v in legacy_details.items() if k != 'exec_start'}, 'legacy baseline disagrees with running installation')
        baseline_compatibility = legacy_details['data_compatibility']
    elif ledger is None:
        baseline_path = config.get('baseline_manifest')
        baseline_digest = config.get('baseline_manifest_sha256')
        require(isinstance(baseline_path, str) and isinstance(baseline_digest, str) and re.fullmatch(r'[0-9a-f]{64}', baseline_digest) is not None, 'baseline manifest and approved SHA required for adoption')
        updater.path_clean(baseline_path, 'file')
        with open(baseline_path, 'rb') as f:
            raw = f.read(1024 * 1024 + 1)
        require(len(raw) <= 1024 * 1024 and hashlib.sha256(raw).hexdigest() == baseline_digest, 'baseline manifest checksum mismatch')
        baseline = validate_manifest(json.loads(raw))
        baseline_binary = next(a for a in baseline['assets'] if a['role'] == 'binary')
        require((baseline['version'], baseline['commit'], baseline_binary['sha256'], baseline_binary['size']) == (health['cli_version'], health['cli_commit'], updater.sha('/proc/%d/exe' % pid), os.stat(exe).st_size), 'baseline manifest disagrees with running binary')
        if layout == 'launcher':
            baseline_launcher = next((a for a in baseline['assets'] if a['role'] == 'launcher'), None)
            require(isinstance(baseline_launcher, dict) and updater.sha(original_launcher) == baseline_launcher['sha256'] and os.stat(original_launcher).st_size == baseline_launcher['size'], 'baseline launcher does not match approved manifest')
        baseline_compatibility = baseline['data_compatibility']
    else:
        baseline_compatibility = ledger.get('current', {}).get('data_compatibility')
        if layout == 'launcher':
            require(original_launcher in ledger.get('current', {}).get('associated_paths', []), 'current launcher missing from ledger')
        require(baseline_compatibility == 'multica-profile-v1', 'ledger baseline compatibility unknown')
    return {'schema': 1, 'manifest_sha256': manifest_sha256, 'version': manifest['version'],
            'commit': manifest['commit'], 'data_compatibility': manifest['data_compatibility'], 'baseline_compatibility': baseline_compatibility, 'target_sha256': assets['binary']['sha256'],
            'inventory': config, 'ledger_sha256': updater.sha(config['ledger']) if ledger is not None else None, 'target_machine_id_sha256': machine, 'search_roots': roots, 'applications': apps, 'programs': programs, 'processes': processes,
            'unit_pid': pid, 'executable_processes': executable_processes,
            **({'account_scope_evidence': account_guard.evidence(), 'account_lease_required': True,
                'unverified_process_images': sorted(account_guard.unverified_process_images, key=lambda item: json.dumps(item, sort_keys=True))} if account_guard is not None else {}),
            'process_visibility_requirement': ('Native host /proc view required; unreadable images and their App, managed inode and writer relationships remain unverified. A kernel lease is required before replacing a protected inode.' if account_guard is not None else 'Run from a verified native host shell; the operator must confirm a complete host /proc view during target inventory.'),
            'profile_path': profile_path, 'profile_inode': [profile_stat.st_dev, profile_stat.st_ino],
            'launcher_ca_paths': [old_ca, new_ca] if layout == 'launcher' else [], 'original_launcher': original_launcher if layout == 'launcher' else None, 'running_exe': exe, 'observer_sha256': updater.sha(config['cli']), 'running_sha256': updater.sha('/proc/%d/exe' % pid),
            'running_version': health.get('cli_version'), 'running_commit': health.get('cli_commit') or None,
            'daemon_id': health['daemon_id'], 'process_started_at': (health.get('update_attestation') or {}).get('process_started_at'), 'workspaces': u.base['workspaces'],
            **({'legacy_target': True, 'legacy_baseline': ledger['previous']['legacy_baseline'],
                'legacy_risk': 'legacy daemon cannot block claims; work may need redrive'} if legacy_target else {}),
            **({'legacy_bootstrap': True, 'legacy_baseline': legacy_details,
                'legacy_risk': 'old daemon cannot block claims; work may be interrupted or missed and must be redriven; legacy automatic updates are not blocked',
                'backup_required': 'preserve original entrypoint bytes, metadata and link identity before stopping'} if legacy_bootstrap else {})}


def write_ledger(path, data):
    parent = updater.path_clean(os.path.dirname(path), 'dir')
    updater.require(os.path.isabs(path) and os.path.normpath(path) == path and os.path.basename(path) not in ('', '.', '..'), 'invalid ledger path')
    if path_present(path):
        updater.path_clean(path, 'file')
        updater.require(os.stat(path).st_uid == os.geteuid(), 'ledger owner mismatch')
    fd, temp = tempfile.mkstemp(prefix='.multica-ledger-', dir=parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as f:
            json.dump(data, f, sort_keys=True)
            f.write('\n')
            f.flush()
            os.fsync(f.fileno())
        os.replace(temp, path)
        updater.fsync_dir(parent)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def read_ledger(path):
    if not path_present(path):
        return None
    updater.path_clean(path, 'file')
    st = os.stat(path)
    updater.require(st.st_uid == os.geteuid() and stat.S_IMODE(st.st_mode) & 0o077 == 0, 'ledger must be owner-private')
    with open(path, encoding='utf-8') as f:
        ledger = json.load(f)
    updater.require(isinstance(ledger, dict) and ledger.get('schema') == 1, 'invalid ledger')
    return ledger


def account_guard_from_evidence(evidence, config, pending_missing=None):
    updater.require(isinstance(evidence, dict) and evidence.get('scope') == 'account', 'account scope evidence missing')
    if pending_missing is not None:
        evidence = json.loads(json.dumps(evidence))
        updater.require(pending_missing in evidence.get('immutable', {}) and not path_present(pending_missing), 'invalid interrupted account cleanup')
        del evidence['immutable'][pending_missing]
    home = pwd.getpwuid(config['owner_uid']).pw_dir
    expected_state = os.path.join(home, '.multica', 'profiles', config['profile']) if config['profile'] else os.path.join(home, '.multica')
    updater.require(evidence.get('state_root') == expected_state and evidence.get('asset_root') == config.get('asset_root'), 'account private root binding mismatch')
    args = argparse.Namespace(owner_uid=config['owner_uid'], unit=config['unit'], systemd_scope=config['systemd_scope'], inventory_scope='account')
    operation = updater.Updater(args)
    try:
        start = operation.account_unit_property('ExecStart')
        fragment = operation.account_unit_property('FragmentPath')
        dropins = operation.account_unit_property('DropInPaths')
        unit_object, ancillary = operation.account_unit_ancillary()
        guard = updater.AccountScope(config['owner_uid'], config['asset_root'], evidence['state_root'],
                                     list(evidence['immutable']), evidence['transaction_paths'], config['unit'],
                                     config['systemd_scope'], start, fragment, evidence['exec_start']['path'],
                                     config['profile'], dropins, unit_object, ancillary, config.get('account_path_policy'))
    except (KeyError, TypeError, UnicodeError) as error:
        raise updater.Refusal('invalid account scope evidence') from error
    updater.require(guard.evidence() == evidence, 'account scope evidence changed')
    return guard


def write_account_ledger(path, data, guard):
    guard.check_mutation(path)
    old = guard._file_snapshot(path) if path_present(path) else None
    temp = path + '.tmp-' + uuid.uuid4().hex
    guard.authorize_temporary(temp)
    created = False
    try:
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        created = True
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(data, stream, sort_keys=True)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        guard.check_mutation(path)
        updater.require((guard._file_snapshot(path) if path_present(path) else None) == old, 'account ledger changed during write')
        guard.check_mutation(temp)
        os.replace(temp, path)
        created = False
        updater.fsync_dir(os.path.dirname(path))
    finally:
        if created:
            guard.check_mutation(temp)
            os.unlink(temp)


def record_account_inputs(ledger, config):
    recorded = ledger.get('account_inputs', [])
    obsolete = ledger.get('account_obsolete_inputs', [])
    updater.require(isinstance(recorded, list) and isinstance(obsolete, list), 'invalid account input records')
    mapping = [('observer', config['cli']), ('ca', config.get('observer_ca_cert_file')), ('baseline', config.get('baseline_manifest'))]
    for role, path in mapping:
        if path is None:
            continue
        updater.require(isinstance(path, str) and os.path.isabs(path) and os.path.commonpath((config['asset_root'], path)) == config['asset_root'], 'account input outside private asset root')
        digest = updater.sha(path)
        for item in recorded + obsolete:
            if item.get('path') == path:
                updater.require(item.get('sha256') == digest, 'recorded account input changed')
        recorded = [item for item in recorded if not (item.get('role') == role and item.get('path') == path)]
        obsolete = [item for item in obsolete if not (item.get('role') == role and item.get('path') == path)]
        recorded.insert(0, {'role': role, 'path': path, 'sha256': digest})
        same_role = [item for item in recorded if item.get('role') == role]
        for item in same_role[2:]:
            recorded.remove(item)
            obsolete.append(item)
    ledger['account_inputs'] = recorded
    ledger['account_obsolete_inputs'] = obsolete


def record_account_evidence(ledger, guard):
    transaction = ledger.get('transaction')
    inventory = transaction.get('inventory') if isinstance(transaction, dict) else None
    if not isinstance(inventory, dict):
        return
    directory = inventory.get('evidence_dir')
    if not isinstance(directory, str):
        return
    records = ledger.setdefault('account_evidence_files', [])
    updater.require(isinstance(records, list), 'invalid account evidence records')
    for role, name in (('heartbeat', 'heartbeats.jsonl'), ('baseline', 'baseline.json'), ('result', 'result.json')):
        path = os.path.join(directory, name)
        if not path_present(path):
            continue
        guard.add_immutable(path)
        entry = {'role': role, 'path': path, 'sha256': updater.sha(path)}
        prior = [item for item in records if isinstance(item, dict) and item.get('path') == path]
        updater.require(not prior or prior == [entry], 'recorded account evidence changed')
        if not prior:
            records.append(entry)


def save_transaction_ledger(path, ledger, guard):
    if guard is None:
        write_ledger(path, ledger)
    else:
        record_account_evidence(ledger, guard)
        ledger['account_directories'] = [{'path': path, 'identity': identity} for path, identity in sorted(guard._dynamic_directories.items())
                                         if os.path.commonpath((guard.asset_root, path)) == guard.asset_root and path != guard.asset_root]
        transaction = ledger.get('transaction')
        updater.require(isinstance(transaction, dict) and isinstance(transaction.get('inventory'), dict), 'account transaction inventory missing')
        transaction['account_scope_evidence'] = guard.evidence()
        account_guard_from_evidence(transaction['account_scope_evidence'], transaction['inventory'])
        write_account_ledger(path, ledger, guard)


def apply_plan(plan, manifest, *, rollback=False):
    """Execute one planned transaction; retain all files until acceptance."""
    cfg = plan['inventory']
    scope = cfg.get('inventory_scope', 'host')
    updater.require(scope in ('host', 'account') and
                    ((scope == 'account') == ('account_scope_evidence' in plan)),
                    'plan scope evidence mismatch')
    account_guard = (account_guard_from_evidence(plan.get('account_scope_evidence'), cfg)
                     if scope == 'account' else None)
    ledger_path = cfg['ledger']
    updater.path_clean(os.path.dirname(ledger_path), 'dir')
    lock_path = ledger_path + '.lock'
    if account_guard is not None:
        account_guard.check_mutation(lock_path)
    fd = os.open(lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'r+') as lock:
        updater.require(os.fstat(lock.fileno()).st_uid == os.geteuid() and stat.S_ISREG(os.fstat(lock.fileno()).st_mode), 'invalid transaction lock')
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise updater.Refusal('another transaction is active') from error
        refreshed = (build_plan(cfg, manifest, plan['manifest_sha256'], legacy_bootstrap=True)
                     if plan.get('legacy_bootstrap') else
                     build_plan(cfg, manifest, plan['manifest_sha256'], legacy_target=bool(plan.get('legacy_target')), rollback_observer=True)
                     if rollback else build_plan(cfg, manifest, plan['manifest_sha256']))
        updater.require(fingerprint(refreshed) == fingerprint(plan), 'plan fingerprint changed')
        ledger = read_ledger(ledger_path)
        adopting = ledger is None
        if ledger is not None:
            allowed = ('accepted', 'awaiting_acceptance', 'awaiting_rollback_acceptance') if rollback else ('accepted',)
            updater.require(ledger.get('status') in allowed, 'previous transaction is pending or requires recovery')
            current = ledger.get('current')
            updater.require(isinstance(current, dict) and current.get('sha256') == plan['running_sha256'] and current.get('version') == plan['running_version'] and current.get('commit') == plan['running_commit'], 'running installation disagrees with ledger')
        else:
            ledger = {'schema': 1, 'status': 'accepted', 'assets': [], 'previous': None,
                      'current': {'path': plan['running_exe'], 'sha256': plan['running_sha256'],
                                  'version': plan['running_version'], 'commit': plan['running_commit'], 'data_compatibility': plan['baseline_compatibility']}}
        if rollback:
            previous = ledger.get('previous')
            current = ledger.get('current')
            updater.require(isinstance(previous, dict) and isinstance(current, dict) and current.get('data_compatibility') == previous.get('data_compatibility') == 'multica-profile-v1', 'rollback data compatibility is unverified')
            updater.require((plan['version'], plan['commit'], plan['target_sha256']) == (previous.get('version'), previous.get('commit'), previous.get('sha256')), 'rollback target is not the immediately previous release')
        if account_guard is not None:
            record_account_inputs(ledger, cfg)
        if (plan['running_version'], plan['running_commit'], plan['running_sha256']) == (plan['version'], plan['commit'], plan['target_sha256']):
            if adopting:
                ledger['assets'] = [ledger['current']]
                if cfg.get('layout', 'binary') == 'binary':
                    staged = cfg.get('staged', ledger['current']['path'])
                    if staged != ledger['current']['path']:
                        updater.path_clean(staged, 'file')
                        updater.require(updater.sha(staged) == plan['target_sha256'], 'adopted staged binary checksum mismatch')
                        ledger['current']['associated_paths'] = [staged]
                        ledger['assets'].append({'path': staged, 'sha256': plan['target_sha256']})
                if cfg.get('layout', 'binary') == 'launcher':
                    launcher = plan['original_launcher']
                    ledger['current']['associated_paths'] = [launcher]
                    ledger['assets'].append({'path': launcher, 'sha256': updater.sha(launcher)})
                ledger['status'] = 'awaiting_baseline_acceptance'
                ledger['transaction'] = {'baseline_adoption': True, 'inventory': cfg, 'daemon_id': plan['daemon_id'], 'profile': cfg['profile'],
                    'workspaces': plan['workspaces'], 'runtime_ids': [rid for ids in plan['workspaces'].values() for rid in ids],
                    'gate_released_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                    'target_pid': plan['unit_pid'], 'process_started_at': plan['process_started_at'],
                    'observer_sha256': plan['observer_sha256']}
                save_transaction_ledger(ledger_path, ledger, account_guard)
                return {'status': 'awaiting_baseline_acceptance', 'ledger': ledger_path}
            if account_guard is not None:
                if not isinstance(ledger.get('transaction'), dict):
                    ledger['transaction'] = {'inventory': cfg}
                save_transaction_ledger(ledger_path, ledger, account_guard)
            return {'status': 'no_change'}
        old = ledger['current']
        if plan.get('legacy_bootstrap'):
            old['legacy_baseline'] = plan['legacy_baseline']
            if cfg.get('legacy_wrapper'):
                ledger['shared_assets'] = [{'role': role, 'path': path, 'sha256': updater.sha(path)} for role, path in
                    (('wrapper', cfg['legacy_wrapper']), ('ca', plan['legacy_baseline']['wrapper_ca_path']))]
        shared = ledger.get('shared_assets', [])
        for item in shared:
            updater.path_clean(item['path'], 'file')
            updater.require(updater.sha(item['path']) == item['sha256'], 'shared asset changed')
        shared_by_role = {item['role']: item for item in shared}
        ledger.update(status='applying', transaction={'plan_fingerprint': fingerprint(plan), 'target_version': plan['version'],
                       'target_commit': plan['commit'], 'target_sha256': plan['target_sha256'],
                       'evidence_dir': cfg['evidence_dir'], 'inventory': cfg})
        save_transaction_ledger(ledger_path, ledger, account_guard)
        layout = cfg.get('layout', 'binary')
        args = argparse.Namespace(profile=cfg['profile'], unit=cfg['unit'], systemd_scope=cfg['systemd_scope'], owner_uid=cfg['owner_uid'],
                    staged=cfg['staged'], sha256=plan['target_sha256'], cli=cfg['cli'], health_url=cfg['health_url'],
                    version=plan['version'], commit=plan['commit'], evidence_dir=cfg['evidence_dir'], layout=layout,
                    entrypoint=cfg.get('entrypoint'), binary=cfg.get('staged_binary'), no_shared_profiles=True,
                    timeout=180, heartbeat_samples=3, heartbeat_interval=15,
                    observer_ca_cert_file=cfg.get('observer_ca_cert_file'), observer_ca_sha256=cfg.get('observer_ca_sha256'),
                    legacy_bootstrap=plan.get('legacy_bootstrap', False), legacy_baseline=plan.get('legacy_baseline'), legacy_wrapper=cfg.get('legacy_wrapper') or shared_by_role.get('wrapper', {}).get('path'),
                    legacy_ca_sha256=cfg.get('ca_sha256') or shared_by_role.get('ca', {}).get('sha256'),
                    legacy_target=plan.get('legacy_target', False), account_guard=account_guard, inventory_scope=cfg.get('inventory_scope', 'host'),
                    expected_base={'pid': plan['unit_pid'], 'daemon_id': plan['daemon_id'],
                        'version': plan['running_version'], 'commit': plan['running_commit'],
                        'sha256': plan['running_sha256'], 'workspaces': plan['workspaces'],
                        'profile_inode': plan['profile_inode']})
        operation = updater.Updater(args)
        code = updater.perform(operation)
        if code == 0 and operation.result.get('status') == 'legacy_rollback_pending_manual':
            backup = os.path.join(cfg['evidence_dir'], 'original-entrypoint')
            updater.path_clean(backup, 'file')
            updater.require(updater.sha(backup) == old['sha256'], 'rollback saved binary checksum mismatch')
            restored = dict(ledger['previous'], path=plan['running_exe'])
            ledger['previous'] = dict(old, path=backup)
            ledger['current'] = restored
            assets = {item['path']: item for item in ledger['assets']}
            assets[backup] = dict(ledger['previous'])
            assets[restored['path']] = dict(restored)
            ledger['assets'] = list(assets.values())
            ledger['status'] = 'legacy_rollback_pending_manual'
            ledger['transaction'].update(result=operation.result, inventory=cfg, daemon_id=plan['daemon_id'], workspaces=plan['workspaces'])
            save_transaction_ledger(ledger_path, ledger, account_guard)
            return {'status': 'legacy_rollback_pending_manual', 'ledger': ledger_path, 'result': operation.result}
        if code != 0 or operation.result.get('status') != 'updated':
            if operation.result.get('status') == 'rolled_back_verified' and operation.result.get('restored_pid'):
                try:
                    restored = operation.health()
                    operation.inspect(restored, target=(old['version'], old['commit'], old['sha256']),
                                      expected_pid=operation.result['restored_pid'])
                    updater.require(updater.sha(old['path']) == old['sha256'], 'restored entry differs from the original release')
                    stamp = (restored.get('update_attestation') or {}).get('process_started_at')
                    updater.require(stamp == operation.result.get('restored_process_started_at'), 'restored process changed after gate release')
                    entries = {asset['path']: asset for asset in ledger['assets']}
                    entries[old['path']] = old
                    # Keep the failed candidate until the restored release has
                    # a new successful dispatch receipt. It is still managed.
                    for candidate in [cfg['staged']] + ([cfg['staged_binary']] if layout == 'launcher' else []):
                        updater.path_clean(candidate, 'file')
                        entries[candidate] = {'path': candidate, 'sha256': updater.sha(candidate)}
                    ledger.update(status='awaiting_rollback_acceptance', current=old, assets=list(entries.values()))
                    ledger['transaction'] = {'baseline_adoption': True, 'rollback_result': operation.result,
                        'inventory': cfg, 'daemon_id': plan['daemon_id'], 'profile': cfg['profile'],
                        'workspaces': plan['workspaces'],
                        'runtime_ids': [rid for ids in plan['workspaces'].values() for rid in ids],
                        'gate_released_at': operation.result['gate_released_at'],
                        'target_pid': restored['pid'], 'process_started_at': stamp,
                        'observer_sha256': plan['observer_sha256']}
                    save_transaction_ledger(ledger_path, ledger, account_guard)
                    return {'status': 'rolled_back_pending_acceptance', 'ledger': ledger_path,
                            'result': operation.result}
                except (updater.Refusal, OSError, ValueError, AttributeError, KeyError):
                    pass
            ledger['status'] = 'recovery_required'
            ledger['transaction']['result'] = operation.result
            save_transaction_ledger(ledger_path, ledger, account_guard)
            return {'status': 'recovery_required', 'result': operation.result}
        backup = os.path.join(cfg['evidence_dir'], 'original-entrypoint')
        if layout == 'binary':
            updater.require(updater.sha(backup) == old['sha256'], 'saved previous binary checksum mismatch')
            previous = dict(old, path=backup)
            current = {'path': plan['running_exe'], 'sha256': plan['target_sha256'],
                       'version': plan['version'], 'commit': plan['commit'], 'data_compatibility': plan['data_compatibility']}
            retained = [previous, current]
            staged = cfg['staged']
            if staged != current['path']:
                updater.path_clean(staged, 'file')
                updater.require(updater.sha(staged) == plan['target_sha256'], 'staged binary changed after update')
                current['associated_paths'] = [staged]
            if shared:
                shared_paths = [item['path'] for item in shared]
                previous['associated_paths'] = list(dict.fromkeys(previous.get('associated_paths', []) + shared_paths))
                current['associated_paths'] = list(dict.fromkeys(current.get('associated_paths', []) + shared_paths))
            associated = set(previous.get('associated_paths', [])) | set(current.get('associated_paths', []))
            for path in sorted(associated - {previous['path'], current['path']}):
                updater.path_clean(path, 'file')
                retained.append({'path': path, 'sha256': updater.sha(path)})
        else:
            old_launcher = os.path.join(os.path.dirname(plan['running_exe']), 'launcher')
            old_launcher_sha = updater.sha(old_launcher)
            updater.require(updater.sha(backup) == old_launcher_sha, 'saved previous launcher checksum mismatch')
            updater.require(updater.sha(plan['running_exe']) == old['sha256'], 'previous binary changed')
            new_launcher_sha = updater.sha(cfg['staged'])
            previous = dict(old, path=plan['running_exe'], associated_paths=[old_launcher, backup])
            current = {'path': cfg['staged_binary'], 'sha256': plan['target_sha256'],
                       'version': plan['version'], 'commit': plan['commit'], 'data_compatibility': plan['data_compatibility'], 'associated_paths': [cfg['staged']]}
            retained = [previous, current, {'path': old_launcher, 'sha256': old_launcher_sha},
                        {'path': backup, 'sha256': old_launcher_sha}, {'path': cfg['staged'], 'sha256': new_launcher_sha}]
        retained_paths = {item['path'] for item in retained}
        replaced_paths = {old['path']} | set(old.get('associated_paths', []))
        older = [a for a in ledger['assets'] if a.get('path') not in retained_paths | replaced_paths]
        ledger.update(status='awaiting_acceptance', current=current, previous=previous,
                      assets=older + retained)
        try:
            updated_health = operation.health()
            operation.inspect(updated_health)
            started_at = updated_health.get('update_attestation', {}).get('process_started_at')
            updater.require(updated_health.get('pid') == operation.result.get('target_pid') and started_at == operation.result.get('target_process_started_at'), 'updated process changed after gate release')
        except (updater.Refusal, OSError, ValueError, AttributeError):
            started_at = None
        heartbeat_path = os.path.join(cfg['evidence_dir'], 'heartbeats.jsonl')
        updater.path_clean(heartbeat_path, 'file')
        heartbeat_sha = updater.sha(heartbeat_path)
        ledger['transaction'].update(inventory=cfg, workspaces=plan['workspaces'], process_started_at=started_at,
                    target_pid=operation.result.get('target_pid'), heartbeat_sha256=heartbeat_sha, observer_sha256=plan['observer_sha256'],
                    daemon_id=plan['daemon_id'], profile=cfg['profile'],
                    runtime_ids=[rid for ids in plan['workspaces'].values() for rid in ids],
                    gate_released_at=operation.result['gate_released_at'], result=operation.result)
        save_transaction_ledger(ledger_path, ledger, account_guard)
        return {'status': 'awaiting_acceptance', 'daemon_id': plan['daemon_id'], 'ledger': ledger_path}


def rollback_plan(ledger_path, evidence_dir):
    ledger = read_ledger(ledger_path)
    updater.require(isinstance(ledger, dict) and ledger.get('status') in ('accepted', 'awaiting_acceptance', 'awaiting_rollback_acceptance'), 'rollback has no usable ledger')
    current, previous = ledger.get('current'), ledger.get('previous')
    updater.require(isinstance(current, dict) and isinstance(previous, dict), 'no immediate previous release')
    updater.require(current.get('data_compatibility') == previous.get('data_compatibility') == 'multica-profile-v1', 'rollback data compatibility is unverified')
    transaction = ledger.get('transaction')
    updater.require(isinstance(transaction, dict) and isinstance(transaction.get('inventory'), dict), 'rollback inventory is missing')
    cfg = dict(transaction['inventory'])
    scope = cfg.get('inventory_scope', 'host')
    if scope == 'account':
        updater.require(cfg.get('ledger') == ledger_path, 'account ledger path changed')
    updater.require(scope in ('host', 'account') and ((scope == 'account') == ('account_scope_evidence' in transaction)), 'transaction scope evidence mismatch')
    if scope == 'account':
        account_guard_from_evidence(transaction['account_scope_evidence'], cfg)
    cfg['ledger'] = ledger_path
    cfg['evidence_dir'] = evidence_dir
    path = previous['path']
    updater.path_clean(path, 'file')
    updater.require(updater.sha(path) == previous['sha256'], 'previous binary checksum mismatch')
    manifest = {'version': previous['version'], 'commit': previous['commit'],
                'data_compatibility': previous['data_compatibility'],
                'assets': [{'role': 'binary', 'size': os.stat(path).st_size, 'sha256': previous['sha256']}]}
    if cfg.get('layout', 'binary') == 'launcher':
        launcher = os.path.join(os.path.dirname(path), 'launcher')
        assets = {a['path']: a for a in ledger['assets']}
        updater.require(launcher in previous.get('associated_paths', []) and launcher in assets, 'previous launcher is not recorded')
        updater.path_clean(launcher, 'file')
        updater.require(updater.sha(launcher) == assets[launcher]['sha256'], 'previous launcher checksum mismatch')
        cfg['staged'], cfg['staged_binary'] = launcher, path
        manifest['assets'].append({'role': 'launcher', 'size': os.stat(launcher).st_size, 'sha256': assets[launcher]['sha256']})
    else:
        cfg['staged'] = path
    # This identity comes from the previously accepted, private ledger rather
    # than an operator-supplied target; build_plan rechecks every live input.
    digest = fingerprint({'ledger_current': current, 'ledger_previous': previous})
    if previous.get('legacy_baseline') is not None:
        updater.require(cfg.get('layout', 'binary') == 'binary', 'legacy rollback supports only binary layout')
        return build_plan(cfg, manifest, digest, legacy_target=True, rollback_observer=True), manifest
    return build_plan(cfg, manifest, digest, rollback_observer=True), manifest


def cleanup_managed_assets(ledger, asset_root, account_guard=None, account_check=None, before_remove=None, after_remove=None):
    """Remove only obsolete, explicitly recorded files with matching digests."""
    updater.path_clean(asset_root, 'dir')
    updater.require(isinstance(ledger, dict) and ledger.get('schema') == 1, 'invalid transaction ledger')
    assets = ledger.get('assets')
    current = ledger.get('current')
    previous = ledger.get('previous')
    updater.require(isinstance(assets, list) and isinstance(current, dict), 'invalid ledger assets')
    keep = {current.get('path')}
    shared = ledger.get('shared_assets', [])
    updater.require(isinstance(shared, list) and all(isinstance(item, dict) and isinstance(item.get('path'), str) for item in shared), 'invalid shared asset inventory')
    keep.update(item['path'] for item in shared)
    updater.require(isinstance(current.get('associated_paths', []), list), 'invalid current associated paths')
    keep.update(current.get('associated_paths', []))
    if previous is not None:
        updater.require(isinstance(previous, dict), 'invalid previous asset')
        keep.add(previous.get('path'))
        updater.require(isinstance(previous.get('associated_paths', []), list), 'invalid previous associated paths')
        keep.update(previous.get('associated_paths', []))
    updater.require(None not in keep and all(isinstance(path, str) for path in keep), 'invalid retained paths')
    entries = {}
    for asset in assets:
        updater.require(isinstance(asset, dict) and isinstance(asset.get('path'), str) and isinstance(asset.get('sha256'), str) and re.fullmatch(r'[0-9a-f]{64}', asset['sha256']) is not None, 'invalid managed asset')
        path = asset['path']
        updater.require(os.path.isabs(path) and os.path.normpath(path) == path and os.path.commonpath((asset_root, path)) == asset_root and path != asset_root and path not in entries, 'invalid managed asset path')
        entries[path] = asset
    updater.require(keep <= entries.keys(), 'retained asset is not in ledger')
    for item in shared:
        updater.require(entries[item['path']]['sha256'] == item.get('sha256'), 'shared asset ledger mismatch')
        updater.path_clean(item['path'], 'file')
        updater.require(updater.sha(item['path']) == item['sha256'], 'shared asset changed before cleanup')
    # Validate the full deletion set before removing any file.
    remove = []
    for path, asset in entries.items():
        if path in keep:
            continue
        if not path_present(path):
            continue  # A prior cleanup attempt may have already removed it.
        updater.path_clean(path, 'file')
        updater.require(updater.sha(path) == asset['sha256'], 'obsolete asset hash mismatch')
        remove.append(path)
    for path in remove:
        updater.path_clean(path, 'file')
        updater.require(updater.sha(path) == entries[path]['sha256'], 'obsolete asset changed during cleanup')
        if account_guard is None:
            os.unlink(path)
        else:
            if account_check is not None:
                account_check()
            account_guard.check_mutation(path)
            if before_remove is not None:
                before_remove(path, entries[path]['sha256'])
            removed = False
            try:
                with updater.ExclusiveFileLease(path, account_guard.uid) as lease:
                    updater.require(lease.digest() == entries[path]['sha256'], 'account cleanup asset changed')
                    account_guard.revalidate_while_leased(path)
                    lease.verify()
                    os.unlink(path)
                    removed = True
                    lease.verify(renamed=True)
            finally:
                if removed:
                    account_guard.acknowledge_removal(path, entries[path]['sha256'])
                    if after_remove is not None:
                        after_remove(path, entries[path]['sha256'])
    return [entries[path] for path in entries if path in keep]


def cleanup_account_inputs(ledger, guard, account_check, before_remove, after_remove):
    records = ledger.get('account_obsolete_inputs', [])
    updater.require(isinstance(records, list), 'invalid obsolete account inputs')
    protected = {item.get('path') for item in ledger.get('account_inputs', []) + ledger.get('assets', [])}
    for item in list(records):
        path, digest = item['path'], item['sha256']
        if path in protected:
            records.remove(item)
            continue
        if not path_present(path):
            records.remove(item)
            continue
        account_check()
        guard.check_mutation(path)
        before_remove(path, digest)
        removed = False
        try:
            with updater.ExclusiveFileLease(path, guard.uid) as lease:
                updater.require(lease.digest() == digest, 'obsolete account input changed')
                guard.revalidate_while_leased(path)
                lease.verify()
                os.unlink(path)
                removed = True
                lease.verify(renamed=True)
        finally:
            if removed:
                guard.acknowledge_removal(path, digest)
                after_remove(path, digest)
    ledger['account_obsolete_inputs'] = [item for item in ledger.get('account_obsolete_inputs', []) if path_present(item['path']) and item['path'] not in protected]


def verify_receipt(health, transaction, now):
    """Verify a server-acknowledged task against a recorded update transaction."""
    require = updater.require
    require(isinstance(health, dict) and isinstance(transaction, dict), 'invalid acceptance evidence')
    require(health.get('status') == 'running' and health.get('daemon_id') == transaction.get('daemon_id'), 'daemon identity mismatch')
    require(health.get('profile') == transaction.get('profile') and health.get('cli_commit') == transaction.get('commit'), 'profile or commit mismatch')
    attestation = health.get('update_attestation')
    require(isinstance(attestation, dict) and attestation.get('binary_sha256') == transaction.get('sha256'), 'running binary hash mismatch')
    released = updater.time_value(transaction.get('gate_released_at'))
    started = updater.time_value(attestation.get('process_started_at'))
    started_before_release = started < released or (started == released and attestation.get('process_started_at') == transaction.get('gate_released_at'))
    require(started_before_release and released <= now, 'invalid process or gate release time')
    runtime_ids = transaction.get('runtime_ids')
    require(isinstance(runtime_ids, list) and runtime_ids and all(updater.known(x) for x in runtime_ids), 'original runtime inventory missing')
    acknowledgements = attestation.get('runtime_heartbeat_acks')
    require(isinstance(acknowledgements, dict) and all(type(acknowledgements.get(rid)) is int and acknowledgements[rid] >= 3 for rid in runtime_ids), 'three server-acknowledged heartbeats per runtime required')
    receipts = attestation.get('task_receipts')
    require(isinstance(receipts, list), 'task receipt evidence missing')
    for receipt in receipts:
        if not isinstance(receipt, dict) or not updater.known(receipt.get('task_id')) or receipt.get('runtime_id') not in runtime_ids:
            continue
        try:
            completed = updater.time_value(receipt.get('completed_at'))
            acknowledged = updater.time_value(receipt.get('ack_at'))
        except updater.Refusal:
            continue
        completion_acknowledged = completed < acknowledged or (completed == acknowledged and receipt.get('completed_at') == receipt.get('ack_at'))
        ack_not_future = acknowledged < now or (acknowledged == now and receipt.get('ack_at') == now.isoformat())
        if released < completed and completion_acknowledged and ack_not_future:
            return receipt
    raise PendingReceipt('no successful post-release dispatch receipt for the original runtimes')


def verify_heartbeat_evidence(tx, current):
    path = os.path.join(tx['inventory']['evidence_dir'], 'heartbeats.jsonl')
    updater.path_clean(path, 'file')
    updater.require(updater.sha(path) == tx.get('heartbeat_sha256'), 'heartbeat evidence changed')
    with open(path, encoding='utf-8') as f:
        lines = f.readlines(1024 * 1024 + 1)
    updater.require(len(lines) >= 3 and sum(len(line) for line in lines) <= 1024 * 1024, 'three heartbeat samples required')
    previous = None
    for line in lines:
        row = json.loads(line)
        updater.require(row.get('version') == current['version'] and row.get('commit') == current['commit'], 'heartbeat release mismatch')
        values = row.get('runtime_last_seen')
        updater.require(isinstance(values, dict) and set(values) == set(tx['workspaces']), 'heartbeat workspace set mismatch')
        sample = {}
        for workspace, ids in tx['workspaces'].items():
            updater.require(isinstance(values[workspace], dict) and set(values[workspace]) == set(ids), 'heartbeat runtime set mismatch')
            for runtime_id in ids:
                sample[(workspace, runtime_id)] = updater.time_value(values[workspace][runtime_id])
        if previous is not None:
            updater.require(all(sample[key] > previous[key] for key in sample), 'runtime heartbeat did not advance')
        previous = sample


def accept_transaction(ledger_path):
    """Confirm a live observer's acknowledged task, then clean recorded assets."""
    updater.path_clean(os.path.dirname(ledger_path), 'dir')
    before_lock = read_ledger(ledger_path)
    updater.require(isinstance(before_lock, dict), 'transaction ledger missing')
    before_tx = before_lock.get('transaction')
    updater.require(isinstance(before_tx, dict) and isinstance(before_tx.get('inventory'), dict), 'transaction inventory missing')
    before_cfg = before_tx['inventory']
    scope = before_cfg.get('inventory_scope', 'host')
    if scope == 'account':
        updater.require(before_cfg.get('ledger') == ledger_path, 'account ledger path changed')
    updater.require(scope in ('host', 'account'), 'invalid inventory scope')
    updater.require((scope == 'account') == ('account_scope_evidence' in before_tx), 'transaction scope evidence mismatch')
    pending = before_tx.get('account_cleanup_pending') if scope == 'account' else None
    missing_pending = None
    if pending is not None:
        updater.require(before_lock.get('status') in ('cleaning', 'cleanup_failed') and isinstance(pending, dict) and set(pending) == {'path', 'sha256'}, 'invalid account cleanup checkpoint')
        candidates = before_lock.get('assets', []) + before_lock.get('account_obsolete_inputs', [])
        updater.require(any(isinstance(item, dict) and item.get('path') == pending['path'] and item.get('sha256') == pending['sha256'] for item in candidates), 'unknown account cleanup checkpoint')
        if not path_present(pending['path']):
            missing_pending = pending['path']
    account_guard = account_guard_from_evidence(before_tx.get('account_scope_evidence'), before_cfg, missing_pending) if scope == 'account' else None
    if account_guard is not None:
        account_guard.check_mutation(ledger_path + '.lock')
    fd = os.open(ledger_path + '.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'r+') as lock:
        updater.require(os.fstat(lock.fileno()).st_uid == os.geteuid() and stat.S_ISREG(os.fstat(lock.fileno()).st_mode), 'invalid transaction lock')
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise updater.Refusal('another transaction is active') from error
        ledger = read_ledger(ledger_path)
        updater.require(ledger == before_lock, 'transaction ledger changed while locking')
        updater.require(isinstance(ledger, dict) and ledger.get('status') in ('awaiting_acceptance', 'awaiting_baseline_acceptance', 'awaiting_rollback_acceptance', 'cleanup_failed', 'cleaning'), 'no pending transaction')
        tx = ledger.get('transaction')
        updater.require(isinstance(tx, dict) and isinstance(tx.get('inventory'), dict), 'transaction inventory missing')
        cfg = tx['inventory']
        if missing_pending is not None:
            ledger['assets'] = [item for item in ledger['assets'] if item.get('path') != missing_pending]
            ledger['account_obsolete_inputs'] = [item for item in ledger.get('account_obsolete_inputs', []) if item.get('path') != missing_pending]
            tx.pop('account_cleanup_pending', None)
            save_transaction_ledger(ledger_path, ledger, account_guard)
        updater.require(cfg['owner_uid'] == os.geteuid() or os.geteuid() == 0, 'operator cannot act for target owner')
        updater.require(tx.get('process_started_at'), 'post-update process start evidence missing')
        args = argparse.Namespace(profile=cfg['profile'], unit=cfg['unit'], systemd_scope=cfg['systemd_scope'], owner_uid=cfg['owner_uid'],
                    cli=cfg['cli'], health_url=cfg['health_url'], observer_ca_cert_file=cfg.get('observer_ca_cert_file'), observer_ca_sha256=cfg.get('observer_ca_sha256'), account_guard=account_guard, inventory_scope=cfg.get('inventory_scope', 'host'))
        updater.path_clean(cfg['cli'], 'file')
        updater.require(updater.sha(cfg['cli']) == tx.get('observer_sha256'), 'observer CLI changed since plan')
        operation = updater.Updater(args)
        health = operation.health()
        gate = health.get('maintenance_gate')
        updater.require(isinstance(gate, dict) and gate.get('supported') is True and gate.get('blocked') is False, 'gate unavailable or blocked')
        home = pwd.getpwuid(cfg['owner_uid']).pw_dir
        profile_dir = os.path.join(home, '.multica', 'profiles', cfg['profile']) if cfg['profile'] else os.path.join(home, '.multica')
        updater.require(gate.get('path') == os.path.join(profile_dir, 'daemon.maintenance'), 'profile gate path mismatch')
        operation.gate = gate['path']
        operation.base = {'daemon_id': tx['daemon_id'], 'workspaces': tx['workspaces']}
        current = ledger['current']
        pid = operation.inspect(health, target=(current['version'], current['commit'], current['sha256']))
        updater.require(pid == tx.get('target_pid') and os.stat('/proc/%d' % pid).st_uid == cfg['owner_uid'], 'live daemon PID or owner mismatch')
        if tx.get('baseline_adoption') is True:
            last = operation.runtimes()
            samples = []
            for _ in range(3):
                deadline = time.monotonic() + 180
                while True:
                    time.sleep(15)
                    operation.inspect(operation.health(), target=(current['version'], current['commit'], current['sha256']), expected_pid=pid)
                    sample = operation.runtimes()
                    if all(sample[ws][rid] > last[ws][rid] for ws in last for rid in last[ws]):
                        samples.append({ws: {rid: value.isoformat() for rid, value in rows.items()} for ws, rows in sample.items()})
                        last = sample
                        break
                    updater.require(time.monotonic() < deadline, 'baseline runtime heartbeats did not advance')
            tx['baseline_heartbeats'] = samples
            health = operation.health()
            operation.inspect(health, target=(current['version'], current['commit'], current['sha256']), expected_pid=pid)
        else:
            verify_heartbeat_evidence(tx, current)
        updater.require(updater.proc_exe(pid) == current['path'], 'live executable path mismatch')
        operation.runtimes()
        attestation = health.get('update_attestation')
        updater.require(isinstance(attestation, dict) and attestation.get('process_started_at') == tx['process_started_at'], 'process restarted since update')
        evidence = {'daemon_id': tx['daemon_id'], 'profile': tx['profile'], 'commit': current['commit'],
                    'sha256': current['sha256'], 'runtime_ids': tx['runtime_ids'], 'gate_released_at': tx['gate_released_at']}
        try:
            receipt = verify_receipt(health, evidence, datetime.datetime.now(datetime.timezone.utc))
        except PendingReceipt as error:
            if ledger['status'] in ('awaiting_acceptance', 'awaiting_baseline_acceptance', 'awaiting_rollback_acceptance'):
                return {'status': ledger['status'], 'reason': str(error)}
            raise
        previous = ledger['previous']
        if previous is not None:
            updater.path_clean(previous['path'], 'file')
            updater.require(updater.sha(previous['path']) == previous['sha256'], 'previous binary checksum mismatch')
        asset_map = {a['path']: a for a in ledger['assets']}
        for path in (previous or {}).get('associated_paths', []) + current.get('associated_paths', []):
            updater.require(path in asset_map, 'associated program asset is not in ledger')
            updater.path_clean(path, 'file')
            updater.require(updater.sha(path) == asset_map[path]['sha256'], 'associated program asset checksum mismatch')
        ledger['status'] = 'cleaning'
        ledger['transaction']['receipt'] = receipt
        save_transaction_ledger(ledger_path, ledger, account_guard)
        def before_remove(path, digest):
            tx['account_cleanup_pending'] = {'path': path, 'sha256': digest}
            save_transaction_ledger(ledger_path, ledger, account_guard)
        def after_remove(path, digest):
            ledger['assets'] = [item for item in ledger['assets'] if item.get('path') != path]
            ledger['account_obsolete_inputs'] = [item for item in ledger.get('account_obsolete_inputs', []) if item.get('path') != path]
            tx.pop('account_cleanup_pending', None)
            save_transaction_ledger(ledger_path, ledger, account_guard)
        try:
            ledger['assets'] = cleanup_managed_assets(ledger, cfg['asset_root'], account_guard, operation.account_check, before_remove, after_remove) if account_guard is not None else cleanup_managed_assets(ledger, cfg['asset_root'])
            if account_guard is not None:
                cleanup_account_inputs(ledger, account_guard, operation.account_check, before_remove, after_remove)
        except (updater.Refusal, OSError, ValueError):
            ledger['status'] = 'cleanup_failed'
            save_transaction_ledger(ledger_path, ledger, account_guard)
            raise
        ledger['status'] = 'accepted'
        save_transaction_ledger(ledger_path, ledger, account_guard)
        return {'status': 'rolled_back' if tx.get('rollback_result') else 'accepted',
                'task_id': receipt['task_id'], 'runtime_id': receipt['runtime_id']}


class SameOriginRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        old = urlsplit(req.full_url)
        new = urlsplit(newurl)
        updater.require(new.scheme == 'https' and new.netloc == old.netloc and release_url(newurl, EXPECTED_VERSION), 'manifest redirect changes approved origin or version path')
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def read_manifest(args):
    if args.manifest:
        updater.path_clean(args.manifest, 'file')
        with open(args.manifest, 'rb') as f:
            raw = f.read(1024 * 1024 + 1)
    else:
        updater.require(updater.known(EXPECTED_VERSION) and release_url(args.manifest_url, EXPECTED_VERSION), 'unapproved manifest URL')
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), SameOriginRedirect())
        with opener.open(args.manifest_url, timeout=30) as response:
            raw = response.read(1024 * 1024 + 1)
    updater.require(len(raw) <= 1024 * 1024 and hashlib.sha256(raw).hexdigest() == args.manifest_sha256, 'manifest checksum mismatch')
    manifest = validate_manifest(json.loads(raw))
    if args.manifest_url:
        updater.require(all(urlsplit(a['url']).netloc == urlsplit(args.manifest_url).netloc for a in manifest['assets']), 'manifest and assets must share an origin')
    return manifest


def download_assets(manifest, destination, opener=None):
    """Prepare a new asset directory; publish it only after every hash matches."""
    updater.require(isinstance(destination, str) and os.path.isabs(destination) and os.path.normpath(destination) == destination, 'asset directory must be absolute and normalized')
    parent = updater.path_clean(os.path.dirname(destination), 'dir')
    home = pwd.getpwuid(os.geteuid()).pw_dir
    profile = os.path.join(home, '.multica')
    common = os.path.commonpath((destination, profile))
    updater.require(common not in (destination, profile), 'asset directory must be outside profile state')
    updater.require(not path_present(destination), 'asset directory already exists')
    opener = opener or urllib.request.build_opener(urllib.request.ProxyHandler({}), SameOriginRedirect())
    os.mkdir(destination, 0o700)
    reservation = os.lstat(destination)
    temporary = None
    published = False
    installed_inode = None
    try:
        temporary = tempfile.mkdtemp(prefix='.multica-assets-', dir=parent)
        result = {}
        for asset in manifest['assets']:
            name = 'multica' if asset['role'] == 'binary' else 'launcher'
            path = os.path.join(temporary, name + '.partial')
            digest = hashlib.sha256()
            size = 0
            with opener.open(asset['url'], timeout=30) as response, open(path, 'xb') as output:
                updater.require(getattr(response, 'status', 200) == 200, 'asset download failed')
                while True:
                    block = response.read(min(1024 * 1024, asset['size'] - size + 1))
                    if not block:
                        break
                    size += len(block)
                    updater.require(size <= asset['size'], 'download exceeds approved size')
                    output.write(block)
                    digest.update(block)
                output.flush()
                os.fsync(output.fileno())
            updater.require(size == asset['size'] and digest.hexdigest() == asset['sha256'], 'download size or checksum mismatch')
            os.chmod(path, 0o755)
            with open(path, 'rb') as finalized:
                os.fsync(finalized.fileno())
            result[asset['role']] = name
        # No candidate gets its final name until every asset passed validation.
        for name in result.values():
            os.rename(os.path.join(temporary, name + '.partial'), os.path.join(temporary, name))
        updater.fsync_dir(temporary)
        current = os.lstat(destination)
        updater.require((current.st_dev, current.st_ino) == (reservation.st_dev, reservation.st_ino) and stat.S_ISDIR(current.st_mode), 'asset reservation changed')
        staging_stat = os.lstat(temporary)
        installed_inode = (staging_stat.st_dev, staging_stat.st_ino)
        os.rename(temporary, destination)
        temporary = None
        updater.fsync_dir(parent)
        published = True
        return {role: os.path.join(destination, name) for role, name in result.items()}
    finally:
        if temporary is not None:
            shutil.rmtree(temporary)
        if not published:
            try:
                current = os.lstat(destination)
                current_inode = (current.st_dev, current.st_ino)
                if current_inode == (reservation.st_dev, reservation.st_ino):
                    os.rmdir(destination)
                elif installed_inode is not None and current_inode == installed_inode:
                    shutil.rmtree(destination)
            except FileNotFoundError:
                pass


def result_exit_code(result):
    if result['status'] in ('awaiting_acceptance', 'awaiting_baseline_acceptance', 'awaiting_rollback_acceptance', 'rolled_back_pending_acceptance', 'legacy_rollback_pending_manual'):
        return 2
    return 0 if result['status'] in ('accepted', 'no_change', 'adopted') else 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--inspect', action='store_true', help='read-only installation inventory')
    parser.add_argument('--owner-uid', type=int)
    parser.add_argument('--search-root', action='append', default=[])
    parser.add_argument('--cli-hint')
    source = parser.add_mutually_exclusive_group(required=False)
    source.add_argument('--manifest', help='local release manifest')
    source.add_argument('--manifest-url', help='HTTPS release manifest URL')
    parser.add_argument('--manifest-sha256', help='independently approved manifest SHA-256')
    parser.add_argument('--download-assets', action='store_true')
    parser.add_argument('--asset-dir')
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--legacy-bootstrap', action='store_true', help='plan first adoption of an inventoried daemon without a maintenance gate')
    parser.add_argument('--observe-legacy', action='store_true', help='observe a proposed legacy baseline without approving or applying it')
    parser.add_argument('--accept', action='store_true')
    parser.add_argument('--rollback', action='store_true')
    parser.add_argument('--rollback-evidence-dir')
    parser.add_argument('--ledger', help='private transaction ledger for acceptance')
    parser.add_argument('--fingerprint')
    parser.add_argument('--inventory', help='local private inventory hints JSON')
    args = parser.parse_args(argv)
    try:
        if args.observe_legacy:
            updater.require(not args.legacy_bootstrap and not args.apply and not args.accept and not args.rollback and not args.inspect and not args.download_assets, 'legacy observation is read-only')
        if args.legacy_bootstrap:
            updater.require(not args.accept and not args.rollback and not args.inspect and not args.download_assets, 'legacy bootstrap requires a release plan')
        updater.require(not any(os.getenv(n) for n in ('MULTICA_TASK_ID', 'MULTICA_AGENT_ID', 'MULTICA_TASK_CONFIG_ROOT', 'MULTICA_DAEMON_PORT', 'MULTICA_QUICK_CREATE_TASK_ID')) and not updater.task_marker_present(), 'updater cannot run in a daemon task context')
        if platform.system() == 'Darwin':
            try:
                result = darwin_inspect(args.owner_uid, args.search_root, args.cli_hint)
            except updater.Refusal as error:
                result = {'status': 'unknown', 'error': str(error)}
            print(json.dumps(result, sort_keys=True))
            return 1
        if args.rollback:
            updater.require(not args.accept and not args.inspect and not args.manifest and not args.manifest_url and isinstance(args.ledger, str) and isinstance(args.rollback_evidence_dir, str), 'rollback requires ledger and new evidence directory')
            plan, rollback_manifest = rollback_plan(args.ledger, args.rollback_evidence_dir)
            digest = fingerprint(plan)
            if args.apply:
                updater.require(args.fingerprint == digest, 'rollback plan fingerprint changed')
                result = apply_plan(plan, rollback_manifest, rollback=True)
                print(json.dumps(result, sort_keys=True))
                return result_exit_code(result)
            print(json.dumps({'status': 'plan', 'fingerprint': digest, 'plan': plan}, sort_keys=True))
            return 0
        if args.accept:
            updater.require(not args.apply and not args.inspect and not args.download_assets and not args.manifest and not args.manifest_url and isinstance(args.ledger, str), 'accept requires only a ledger path')
            result = accept_transaction(args.ledger)
            print(json.dumps(result, sort_keys=True))
            return result_exit_code(result)
        if args.inspect:
            updater.require(not args.manifest and not args.manifest_url and not args.apply and not args.download_assets, 'inspect does not accept update options')
            result = inspect_install(args.owner_uid, args.search_root, args.cli_hint)
            print(json.dumps(result, sort_keys=True))
            return 0 if result['status'] == 'standalone' else 1
        updater.require(bool(args.manifest) != bool(args.manifest_url), 'exactly one manifest source required')
        updater.require(isinstance(args.manifest_sha256, str) and re.fullmatch(r'[0-9a-f]{64}', args.manifest_sha256) is not None, 'manifest SHA-256 required')
        manifest = read_manifest(args)
        updater.require(updater.known(EXPECTED_VERSION) and re.fullmatch(r'[0-9a-f]{40}', EXPECTED_COMMIT or '') is not None, 'release identity not embedded')
        updater.require(manifest['version'] == EXPECTED_VERSION and manifest['commit'] == EXPECTED_COMMIT, 'manifest does not match embedded release')
        if args.download_assets:
            updater.require(not args.apply and not args.inventory and isinstance(args.asset_dir, str), 'download-assets requires only a new asset directory')
            result = download_assets(manifest, args.asset_dir)
            print(json.dumps({'status': 'prepared', 'assets': result}, sort_keys=True))
            return 0
        updater.require(isinstance(args.inventory, str), 'private inventory hints are required')
        updater.path_clean(args.inventory, 'file')
        with open(args.inventory, encoding='utf-8') as f:
            config = json.load(f)
        if args.observe_legacy:
            plan = build_plan(config, manifest, args.manifest_sha256, legacy_bootstrap=True, observe_legacy=True)
            print(json.dumps({'status': 'observed', 'legacy_baseline': plan['legacy_baseline'],
                              'notice': 'Observation is not approval. Compare with approved inventory before planning.'}, sort_keys=True))
            return 0
        plan = build_plan(config, manifest, args.manifest_sha256, legacy_bootstrap=True) if args.legacy_bootstrap else build_plan(config, manifest, args.manifest_sha256)
        digest = fingerprint(plan)
        if args.apply:
            updater.require(args.fingerprint == digest, 'plan fingerprint changed')
            result = apply_plan(plan, manifest)
            print(json.dumps(result, sort_keys=True))
            return result_exit_code(result)
        print(json.dumps({'status': 'plan', 'fingerprint': digest, 'plan': plan}, sort_keys=True))
        return 0
    except (updater.Refusal, OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({'status': 'refused', 'error': str(error)}))
        return 1
    return 1


if __name__ == '__main__':
    sys.exit(main())
