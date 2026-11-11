import contextlib
import datetime
import importlib.util
import io
import http.server
import ssl
import subprocess
import threading
import urllib.request
import json
import os
import plistlib
from pathlib import Path
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('multica_update', HERE / 'multica_update.py')
u = importlib.util.module_from_spec(spec)
spec.loader.exec_module(u)


class PreparationTests(unittest.TestCase):
    def test_executable_inventory_scans_other_uids_and_refuses_unreadable(self):
        target = type('Stat', (), {'st_dev': 7, 'st_ino': 11})()
        other_image = type('Stat', (), {'st_dev': 7, 'st_ino': 12})()
        def stat(path):
            if path == '/program': return target
            if path in ('/proc/101', '/proc/202', '/proc/303'):
                return type('Owner', (), {'st_dev': 1, 'st_ino': int(path.rsplit('/', 1)[1]), 'st_uid': 1000 if path == '/proc/101' else 1001})()
            if path in ('/proc/101/exe', '/proc/202/exe'): return target
            if path == '/proc/303/exe': return other_image
            raise AssertionError(path)
        with mock.patch.object(u.updater.os, 'listdir', return_value=['101', '202', '303', 'self']), mock.patch.object(u.updater.os, 'stat', side_effect=stat):
            self.assertEqual(u.updater.executable_users('/program'), [{'pid': 101, 'uid': 1000}, {'pid': 202, 'uid': 1001}])
            with self.assertRaisesRegex(u.updater.Refusal, 'shared'):
                u.updater.require_exclusive_executable('/program', 101, 1000)
        def denied(path):
            if path == '/proc/303/exe': raise PermissionError('fixture')
            return stat(path)
        with mock.patch.object(u.updater.os, 'listdir', return_value=['101', '202', '303']), mock.patch.object(u.updater.os, 'stat', side_effect=denied):
            with self.assertRaisesRegex(u.updater.Refusal, 'every process'):
                u.updater.executable_users('/program')
        hidden_mount = '1 0 0:5 / /proc rw,nosuid - proc proc rw,hidepid=2\n'
        with mock.patch('builtins.open', mock.mock_open(read_data=hidden_mount)):
            with self.assertRaisesRegex(u.updater.Refusal, 'hides other processes'):
                u.updater.require_visible_proc()

    def test_legacy_wrapper_template_rejects_extra_shell(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            wrapper, ca = root / 'wrapper', root / 'ca.pem'
            ca.write_bytes(b'fixture ca')
            lines = ['#!/bin/sh', 'set -eu'] + ["export %s='%s'" % (name, ca) for name in
                ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')] + ["exec '/opt/multica' \"$@\""]
            wrapper.write_text('\n'.join(lines) + '\n')
            with mock.patch.object(u.ssl, 'create_default_context'):
                self.assertEqual(u.legacy_wrapper_path(str(wrapper), '/opt/multica', u.updater.sha(str(ca))), str(ca))
                wrapper.write_text('\n'.join(lines[:-1] + ['touch /unexpected'] + lines[-1:]) + '\n')
                with self.assertRaises(u.updater.Refusal):
                    u.legacy_wrapper_path(str(wrapper), '/opt/multica', u.updater.sha(str(ca)))

    def test_official_legacy_wrapper_is_strictly_recognized(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'bin').mkdir()
            (root / 'releases').mkdir()
            wrapper, binary, ca = root / 'bin' / 'multica', root / 'releases' / 'multica', root / 'releases' / 'ca.pem'
            ca.write_bytes(b'fixture ca')
            lines = ['#!/usr/bin/env bash',
                     'export MULTICA_DAEMON_AUTO_UPDATE=${MULTICA_DAEMON_AUTO_UPDATE:-true} MULTICA_DAEMON_AUTO_UPDATE_INTERVAL=${MULTICA_DAEMON_AUTO_UPDATE_INTERVAL:-24h}',
                     'export MULTICA_DAEMON_AUTO_RELOAD=true MULTICA_DAEMON_DYNAMIC_PORT=true',
                     'export MULTICA_ZH_TW_RELEASE_MANIFEST_URL=https://10.1.24.90:45671/downloads/latest.json',
                     'export MULTICA_ZH_TW_LAUNCHER=%s MULTICA_ZH_TW_INSTALL_ROOT=%s' % (wrapper, root)]
            lines += ['export %s=%s' % (name, ca) for name in ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')]
            lines += ['export MULTICA_SERVER_URL=https://10.1.24.90:45671 MULTICA_APP_URL=https://10.1.24.90:45671', 'exec %s "$@"' % binary]
            wrapper.write_text('\n'.join(lines) + '\n')
            with mock.patch.object(u.ssl, 'create_default_context'):
                self.assertEqual(u.legacy_wrapper_path(str(wrapper), str(binary), u.updater.sha(str(ca))), str(ca))
                lines[3] += ';touch /unexpected'
                wrapper.write_text('\n'.join(lines) + '\n')
                with self.assertRaises(u.updater.Refusal):
                    u.legacy_wrapper_path(str(wrapper), str(binary), u.updater.sha(str(ca)))

    def test_legacy_plan_binds_live_inventory_and_rejects_mismatches(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            home, programs = root / 'home', root / 'programs'
            (home / '.multica').mkdir(parents=True)
            programs.mkdir()
            binary, observer, staged = [programs / name for name in ('multica', 'observer', 'staged')]
            for path in (binary, observer, staged):
                path.write_bytes(path.name.encode())
                path.chmod(0o755)
            sha = u.updater.sha
            info = binary.stat()
            start = '{ path=%s ; argv[]=%s daemon ; ignore_errors=no ; start_time=1 ; stop_time=0 ; pid=123 ; code=(null) ; status=0 }' % (binary, binary)
            baseline = {'machine_id_sha256': 'e' * 64, 'unit': 'legacy.service', 'systemd_scope': 'user',
                        'pid': 123, 'daemon_id': 'daemon', 'binary_sha256': sha(str(binary)),
                        'entrypoint': {'path': str(binary), 'sha256': sha(str(binary)), 'mode': 0o755,
                                       'uid': info.st_uid, 'gid': info.st_gid, 'device': info.st_dev,
                                       'inode': info.st_ino, 'links': info.st_nlink}, 'exec_start': start,
                        'exec_start_configuration': {'path': str(binary), 'argv': str(binary) + ' daemon', 'ignore_errors': 'no'}, 'kill_mode': 'control-group', 'control_group': '/fixture.service',
                        'data_compatibility': 'multica-profile-v1'}
            config = {'owner_uid': os.geteuid(), 'profile': '', 'unit': 'legacy.service', 'systemd_scope': 'user',
                      'cli': str(observer), 'staged': str(staged), 'search_roots': [str(programs)],
                      'health_url': 'http://127.0.0.1:1234/health', 'ledger': str(root / 'ledger'),
                      'evidence_dir': str(programs / 'evidence'), 'asset_root': str(programs),
                      'controller_machine_id_sha256': 'f' * 64, 'legacy_baseline': baseline}
            class FakeUpdater:
                def __init__(self, args): pass
                def health(self):
                    return {'status': 'running', 'profile': '', 'pid': 123, 'daemon_id': 'daemon',
                            'cli_version': '1.0.0', 'cli_commit': ''}
                def unit_pid(self): return ('active', 123)
                def workspaces(self, health): return {'workspace': ['runtime']}
                def runtimes(self): return {'workspace': {'runtime': 'time'}}
                def systemctl(self, *args): return list(args)
            real_stat = u.os.stat
            def stat_file(path, *args, **kwargs):
                return info if path in ('/proc/123', '/proc/123/exe') else real_stat(path, *args, **kwargs)
            def hash_file(path):
                return sha(str(binary) if path == '/proc/123/exe' else path)
            with mock.patch.object(u.pwd, 'getpwuid', return_value=type('User', (), {'pw_dir': str(home)})()), \
                 mock.patch.object(u, 'target_machine_fingerprint', return_value='e' * 64), \
                 mock.patch.object(u, 'scan_roots', return_value=[]), mock.patch.object(u, 'scan_programs', return_value=[]), \
                 mock.patch.object(u, 'process_inventory', return_value=[{'pid': 123, 'exe': str(binary)}]), \
                 mock.patch.object(u, 'validate_staged_asset'), mock.patch.object(u.updater, 'Updater', FakeUpdater), \
                 mock.patch.object(u.updater, 'validate_observer_ca'), \
                 mock.patch.object(u.updater, 'proc_exe', return_value=str(binary)), \
                 mock.patch.object(u.updater, 'run', side_effect=lambda args: (b'control-group' if '--property=KillMode' in args else b'/fixture.service' if '--property=ControlGroup' in args else start.encode())), \
                 mock.patch.object(u.updater, 'cgroup_members', return_value=[123]), \
                 mock.patch.object(u.updater, 'executable_users', return_value=[{'pid': 123, 'uid': os.geteuid()}]), \
                 mock.patch.object(u.updater, 'sha', side_effect=hash_file), mock.patch.object(u.os, 'stat', side_effect=stat_file):
                plan = u.build_plan(config, self.manifest(), 'a' * 64, legacy_bootstrap=True)
                observed_config = dict(config)
                del observed_config['legacy_baseline']
                observed = u.build_plan(observed_config, self.manifest(), 'a' * 64, legacy_bootstrap=True, observe_legacy=True)
                self.assertEqual(observed['legacy_baseline'], baseline)
                with self.assertRaises(u.updater.Refusal):
                    u.build_plan(observed_config, self.manifest(), 'a' * 64, legacy_bootstrap=True)
                self.assertIsNone(plan['running_commit'])
                self.assertEqual(plan['legacy_baseline'], baseline)
                self.assertIn('interrupted or missed', plan['legacy_risk'])
                self.assertNotEqual(u.fingerprint(plan), u.fingerprint(dict(plan, legacy_risk='different')))
                for key, value in (('pid', 124), ('binary_sha256', '0' * 64), ('exec_start_configuration', {})):
                    with self.subTest(key=key), self.assertRaises(u.updater.Refusal):
                        u.build_plan(dict(config, legacy_baseline=dict(baseline, **{key: value})), self.manifest(), 'a' * 64, legacy_bootstrap=True)
                with mock.patch.object(u.updater, 'run', side_effect=lambda args: (b'process' if '--property=KillMode' in args else b'/fixture.service' if '--property=ControlGroup' in args else start.encode())):
                    with self.assertRaisesRegex(u.updater.Refusal, 'cgroup'):
                        u.build_plan(config, self.manifest(), 'a' * 64, legacy_bootstrap=True)
                with self.assertRaises(u.updater.Refusal):
                    u.build_plan(config, self.manifest(), 'a' * 64)
                wrapper, ca = programs / 'wrapper', programs / 'ca.pem'
                ca.write_bytes(b'fixture ca')
                wrapper.write_text('#!/bin/sh\n' + ''.join("export %s='%s'\n" % (name, ca) for name in
                    ('MULTICA_CA_CERT_FILE', 'SSL_CERT_FILE', 'CURL_CA_BUNDLE')) + "exec '%s' \"$@\"\n" % binary)
                wrapper.chmod(0o755)
                w = wrapper.stat()
                entry = {'path': str(wrapper), 'sha256': sha(str(wrapper)), 'mode': 0o755, 'uid': w.st_uid,
                         'gid': w.st_gid, 'device': w.st_dev, 'inode': w.st_ino, 'links': w.st_nlink}
                wrapper_start = '{ path=%s ; argv[]=%s daemon ; ignore_errors=no ; start_time=1 ; pid=123 }' % (wrapper, wrapper)
                wrapper_baseline = dict(baseline, entrypoint=entry, exec_start=wrapper_start, exec_start_configuration={'path': str(wrapper), 'argv': str(wrapper) + ' daemon', 'ignore_errors': 'no'}, wrapper_ca_path=str(ca))
                wrapper_config = dict(config, legacy_baseline=wrapper_baseline, legacy_wrapper=str(wrapper), ca_sha256=sha(str(ca)))
                with mock.patch.object(u.updater, 'run', side_effect=lambda args: (b'control-group' if '--property=KillMode' in args else b'/fixture.service' if '--property=ControlGroup' in args else wrapper_start.encode())), mock.patch.object(u.ssl, 'create_default_context'):
                    wrapped = u.build_plan(wrapper_config, self.manifest(), 'a' * 64, legacy_bootstrap=True)
                    self.assertEqual(wrapped['legacy_baseline']['entrypoint'], entry)

    def test_legacy_cli_is_explicit_and_read_only(self):
        manifest = self.manifest()
        with tempfile.TemporaryDirectory() as temporary:
            inventory = Path(temporary) / 'inventory.json'
            inventory.write_text('{}')
            plan = {'legacy_bootstrap': True}
            args = ['--manifest', '/manifest', '--manifest-sha256', 'a' * 64,
                    '--inventory', str(inventory), '--legacy-bootstrap']
            with mock.patch.object(u.updater, 'task_marker_present', return_value=False), \
                 mock.patch.object(u, 'EXPECTED_VERSION', manifest['version']), \
                 mock.patch.object(u, 'EXPECTED_COMMIT', manifest['commit']), \
                 mock.patch.object(u, 'read_manifest', return_value=manifest), \
                 mock.patch.object(u, 'build_plan', return_value=plan) as build, \
                 mock.patch.dict(os.environ, {}, clear=True), contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(u.main(args), 0)
                build.assert_called_once_with({}, manifest, 'a' * 64, legacy_bootstrap=True)
                self.assertEqual(json.loads(output.getvalue())['fingerprint'], u.fingerprint(plan))
                self.assertEqual(u.main(args + ['--apply']), 1)
                self.assertEqual(u.main(args[:-1] + ['--observe-legacy', '--apply']), 1)
                build.return_value = {'legacy_baseline': {'pid': 123}}
                self.assertEqual(u.main(args[:-1] + ['--observe-legacy']), 0)
                self.assertTrue(build.call_args.kwargs['observe_legacy'])

    def manifest(self):
        return {'source': 'hyc5566/multica', 'data_compatibility': 'multica-profile-v1', 'platform': {'x86_64': 'linux-amd64', 'aarch64': 'linux-arm64'}[u.platform.machine().lower()], 'version': '0.8.1-zh-tw.3', 'commit': 'a' * 40,
                'assets': [{'role': 'binary', 'url': 'https://github.com/hyc5566/multica/releases/download/v0.8.1-zh-tw.3/a', 'size': 2, 'sha256': 'b' * 64}]}

    def test_source_and_asset_constraints(self):
        for mutate in (lambda m: m.update(source='other/repo'),
                       lambda m: m.update(commit='abc'),
                       lambda m: m.update(platform='wrong-arch'),
                       lambda m: m.update(data_compatibility='other-schema'),
                       lambda m: m['assets'][0].update(url='http://example.com/a'),
                       lambda m: m['assets'][0].update(size=True),
                       lambda m: m['assets'][0].update(sha256='x' * 64)):
            manifest = self.manifest()
            mutate(manifest)
            with self.assertRaises(u.updater.Refusal):
                u.validate_manifest(manifest)
        u.validate_manifest(self.manifest())

    def test_release_url_and_redirect_origin(self):
        version = '0.8.1-zh-tw.3'
        self.assertTrue(u.release_url('https://10.1.24.90:45671/downloads/' + version + '/binary', version))
        self.assertFalse(u.release_url('http://10.1.24.90:45671/downloads/' + version + '/binary', version))
        self.assertFalse(u.release_url('https://10.1.24.90:45671/downloads/other/binary', version))
        request = u.urllib.request.Request('https://github.com/hyc5566/multica/releases/download/v0.8.1-zh-tw.3/manifest')
        with self.assertRaises(u.updater.Refusal):
            u.SameOriginRedirect().redirect_request(request, None, 302, '', {}, 'https://evil.test/file')

    def test_dry_run_and_apply_refuse_without_writes(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'manifest.json'
            path.write_text(json.dumps(self.manifest()))
            before = {p.name: p.read_bytes() for p in Path(root).iterdir()}
            digest = u.hashlib.sha256(path.read_bytes()).hexdigest()
            with mock.patch.object(u.updater, 'task_marker_present', return_value=False), mock.patch.object(u, 'EXPECTED_VERSION', self.manifest()['version']), mock.patch.object(u, 'EXPECTED_COMMIT', self.manifest()['commit']), mock.patch.dict(os.environ, {}, clear=True), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(u.main(['--manifest', str(path), '--manifest-sha256', digest]), 1)
                self.assertEqual(u.main(['--manifest', str(path), '--manifest-sha256', digest, '--apply', '--fingerprint', 'a' * 64]), 1)
            self.assertEqual(before, {p.name: p.read_bytes() for p in Path(root).iterdir()})

    def test_self_task_rejected(self):
        with mock.patch.dict(os.environ, {'MULTICA_TASK_ID': 'test'}, clear=True), contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(u.main(['--manifest', '/nonexistent', '--manifest-sha256', 'a' * 64]), 1)
        self.assertIn('daemon task context', output.getvalue())

    def test_nondefault_bundle_and_desktop_detected(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            bundle = root / 'alternate' / 'Client.app' / 'Contents'
            bundle.mkdir(parents=True)
            (bundle / 'Info.plist').write_bytes(plistlib.dumps({'CFBundleIdentifier': 'io.multica.desktop'}))
            desktop = root / 'launch.desktop'
            desktop.write_text('[Desktop Entry]\nName=Multica\n')
            self.assertEqual(set(u.scan_roots([str(root)])), {str(bundle.parent), str(desktop)})

    def test_scan_refuses_symlinked_subdirectory(self):
        with tempfile.TemporaryDirectory() as root:
            (Path(root) / 'link').symlink_to(root)
            with self.assertRaises(u.updater.Refusal):
                u.scan_roots([root])

    def test_unrelated_electron_and_appimage_are_not_multica(self):
        with tempfile.TemporaryDirectory() as root:
            cli = Path(root) / 'multica'
            cli.write_bytes(b'binary')
            processes = [{'pid': 111, 'exe': '/opt/Code/electron'}, {'pid': 222, 'exe': '/opt/Other.AppImage'}]
            with mock.patch.object(u, 'scan_roots', return_value=[]), mock.patch.object(u, 'scan_programs', return_value=[str(cli)]), mock.patch.object(u, 'process_inventory', return_value=processes):
                result = u.inspect_install(os.geteuid(), [root], str(cli))
            self.assertEqual(result['status'], 'standalone')
            self.assertEqual(result['app_processes'], [])

    def test_process_inventory_detects_actual_executable(self):
        def stat(path):
            return type('Stat', (), {'st_uid': 42})()
        with mock.patch.object(u.os, 'listdir', return_value=['12']), mock.patch.object(u.os, 'stat', side_effect=stat), mock.patch.object(u.os, 'readlink', return_value='/custom/Multica.AppImage'):
            self.assertEqual(u.process_inventory(42), [{'pid': 12, 'exe': '/custom/Multica.AppImage'}])

    def test_darwin_spotlight_running_bundle_and_symlinked_cli(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            search = root / 'search'
            search.mkdir()
            app = root / 'other-location' / 'Renamed.app'
            contents = app / 'Contents'
            (contents / 'MacOS').mkdir(parents=True)
            (contents / 'Resources' / 'bin').mkdir(parents=True)
            (contents / 'Info.plist').write_bytes(plistlib.dumps({'CFBundleIdentifier': 'ai.multica.desktop.zh-tw.local'}))
            executable = contents / 'MacOS' / 'Multica'
            executable.write_bytes(b'fixture')
            binary = contents / 'Resources' / 'bin' / 'multica'
            binary.write_bytes(b'fixture')
            cli = root / 'cli'
            cli.symlink_to(binary)
            chrome = root / 'Chrome.app' / 'Contents'
            (chrome / 'MacOS').mkdir(parents=True)
            (chrome / 'Info.plist').write_bytes(plistlib.dumps({'CFBundleIdentifier': 'com.google.Chrome'}))
            (chrome / 'MacOS' / 'Chrome').write_bytes(b'fixture')
            def run(args, **kwargs):
                if args[0] == '/usr/bin/mdfind':
                    output = (str(app) + '\0').encode() if 'zh-tw.local' in args[-1] else b''
                else:
                    output = ('111 ' + str(executable) + '\n222 ' + str(chrome / 'MacOS' / 'Chrome') + '\n').encode()
                return type('Result', (), {'returncode': 0, 'stdout': output, 'stderr': b''})()
            with mock.patch.object(u.platform, 'system', return_value='Darwin'), mock.patch.object(u.pwd, 'getpwuid', return_value=type('User', (), {'pw_dir': str(root)})()), mock.patch.object(u.subprocess, 'run', side_effect=run):
                result = u.inspect_install(os.geteuid(), [str(search)], str(cli))
                self.assertEqual(result['status'], 'use_app')
                self.assertEqual(result['cli_resolved'], str(binary))
                self.assertEqual(result['applications'][0]['path'], str(app))
                self.assertEqual(result['processes'], [{'pid': 111, 'exe': str(executable), 'bundle': str(app)}])
                def process_only(args, **kwargs):
                    if args[0] == '/usr/bin/mdfind':
                        return type('Result', (), {'returncode': 0, 'stdout': b'', 'stderr': b''})()
                    return run(args, **kwargs)
                with mock.patch.object(u.subprocess, 'run', side_effect=process_only):
                    self.assertEqual(u.inspect_install(os.geteuid(), [str(search)], str(cli))['status'], 'use_app')
                with mock.patch.object(u.updater, 'task_marker_present', return_value=False), mock.patch.dict(os.environ, {}, clear=True), contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(u.main(['--inspect', '--owner-uid', str(os.geteuid()), '--search-root', str(search), '--cli-hint', str(cli)]), 1)
                    self.assertEqual(json.loads(output.getvalue())['status'], 'use_app')
                with mock.patch.object(u.updater, 'task_marker_present', return_value=False), mock.patch.dict(os.environ, {}, clear=True), contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(u.main(['--manifest', '/not-read', '--apply', '--owner-uid', str(os.geteuid()), '--search-root', str(search), '--cli-hint', str(cli)]), 1)
                    self.assertEqual(json.loads(output.getvalue())['status'], 'use_app')

    def test_darwin_foreign_app_unsupported_and_query_failure(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            search = root / 'search'
            search.mkdir()
            cli = root / 'cli'
            cli.write_bytes(b'fixture')
            app = search / 'Multica.app' / 'Contents'
            app.mkdir(parents=True)
            info = app / 'Info.plist'
            info.write_bytes(plistlib.dumps({'CFBundleIdentifier': 'ai.multica.desktop'}))
            success = type('Result', (), {'returncode': 0, 'stdout': b'', 'stderr': b''})()
            failure = type('Result', (), {'returncode': 1, 'stdout': b'', 'stderr': b'permission denied'})()
            with mock.patch.object(u.platform, 'system', return_value='Darwin'), mock.patch.object(u.pwd, 'getpwuid', return_value=type('User', (), {'pw_dir': str(root)})()), mock.patch.object(u.subprocess, 'run', return_value=success):
                self.assertEqual(u.inspect_install(os.geteuid(), [str(search)], str(cli))['status'], 'mixed')
                info.write_bytes(plistlib.dumps({'CFBundleIdentifier': 'com.example.other'}))
                (app.parent).rename(search / 'Other.app')
                self.assertEqual(u.inspect_install(os.geteuid(), [str(search)], str(cli))['status'], 'unsupported')
                outside = root / 'unlisted' / 'Renamed.app' / 'Contents'
                outside.mkdir(parents=True)
                (outside / 'Info.plist').write_bytes(plistlib.dumps({'CFBundleIdentifier': 'org.example.multica'}))
                def foreign_spotlight(args, **kwargs):
                    found = (str(outside.parent) + '\0').encode() if args[0] == '/usr/bin/mdfind' and '*multica*' in args[-1] else b''
                    return type('Result', (), {'returncode': 0, 'stdout': found, 'stderr': b''})()
                with mock.patch.object(u.subprocess, 'run', side_effect=foreign_spotlight):
                    self.assertEqual(u.inspect_install(os.geteuid(), [str(search)], str(cli))['status'], 'mixed')
                with mock.patch.object(u.subprocess, 'run', side_effect=PermissionError('fixture')):
                    with self.assertRaisesRegex(u.updater.Refusal, 'unknown'):
                        u.inspect_install(os.geteuid(), [str(search)], str(cli))
                with mock.patch.object(u.subprocess, 'run', return_value=failure):
                    with self.assertRaisesRegex(u.updater.Refusal, 'unknown'):
                        u.inspect_install(os.geteuid(), [str(search)], str(cli))
                    with mock.patch.object(u.updater, 'task_marker_present', return_value=False), mock.patch.dict(os.environ, {}, clear=True), contextlib.redirect_stdout(io.StringIO()) as output:
                        self.assertEqual(u.main(['--inspect', '--owner-uid', str(os.geteuid()), '--search-root', str(search), '--cli-hint', str(cli)]), 1)
                        self.assertEqual(json.loads(output.getvalue())['status'], 'unknown')
                with self.assertRaisesRegex(u.updater.Refusal, 'unknown'):
                    u.inspect_install(os.geteuid(), [str(root / 'missing')], str(cli))

    def test_read_only_plan_rechecks_inventory(self):
        class FakeUpdater:
            def __init__(self, args):
                self.gate = None
            def health(self):
                return {'maintenance_gate': {'supported': True, 'blocked': False, 'path': '/home/owner/.multica/daemon.maintenance'},
                        'cli_version': '1.0.0', 'cli_commit': 'c' * 40, 'daemon_id': 'daemon'}
            def inspect(self, health):
                return 123
            def workspaces(self, health):
                return {'workspace': ['runtime']}
            def runtimes(self):
                return {'workspace': {'runtime': 'time'}}
            def version_as_owner(self, path):
                return ('1.0.0', 'c' * 40)
        config = {'owner_uid': 42, 'profile': '', 'unit': 'multica.service', 'systemd_scope': 'user',
                  'cli': '/observer', 'staged': '/program/stage', 'search_roots': ['/apps'],
                  'health_url': 'http://127.0.0.1:1234/health', 'ledger': '/ledger', 'evidence_dir': '/program/evidence', 'asset_root': '/program', 'controller_machine_id_sha256': 'f' * 64}
        info = type('Stat', (), {'st_uid': 42, 'st_dev': 1, 'st_ino': 2})()
        with mock.patch.object(u.os, 'geteuid', return_value=42), mock.patch.object(u, 'target_machine_fingerprint', return_value='e' * 64), mock.patch.object(u.pwd, 'getpwuid', return_value=type('User', (), {'pw_dir': '/home/owner'})()), mock.patch.object(u, 'scan_roots', return_value=[]), mock.patch.object(u, 'scan_programs', return_value=[]), mock.patch.object(u, 'read_ledger', return_value={'current': {'data_compatibility': 'multica-profile-v1'}, 'assets': []}), mock.patch.object(u, 'process_inventory', return_value=[{'pid': 123, 'exe': '/program/multica'}]), mock.patch.multiple(u.updater, executable_users=mock.Mock(return_value=[{'pid': 123, 'uid': 42}]), path_clean=mock.Mock(side_effect=lambda p, *a: p), Updater=FakeUpdater, proc_exe=mock.Mock(return_value='/program/multica'), version=mock.Mock(return_value=('1.0.0', 'c' * 40)), sha=mock.Mock(return_value='d' * 64), validate_observer_ca=mock.Mock()), mock.patch.object(u, 'validate_staged_asset'), mock.patch.object(u.os, 'stat', return_value=info), mock.patch.object(u.os, 'access', return_value=True), mock.patch.object(u.os.path, 'lexists', return_value=False), mock.patch.object(u.os.path, 'samefile', side_effect=lambda a, b: a == b):
            plan = u.build_plan(config, self.manifest(), 'e' * 64)
            self.assertEqual(plan['unit_pid'], 123)
            with self.assertRaises(u.updater.Refusal):
                u.build_plan(dict(config, staged='/outside/stage'), self.manifest(), 'e' * 64)
            self.assertNotEqual(u.fingerprint(plan), u.fingerprint(dict(plan, unit_pid=124)))
            with mock.patch.object(u.os, 'geteuid', return_value=0):
                system_plan = u.build_plan(dict(config, systemd_scope='system'), self.manifest(), 'e' * 64)
                user_plan = u.build_plan(config, self.manifest(), 'e' * 64)
            self.assertEqual(system_plan['inventory']['owner_uid'], 42)
            self.assertEqual(user_plan['inventory']['systemd_scope'], 'user')
            with self.assertRaises(u.updater.Refusal):
                u.build_plan(dict(config, controller_machine_id_sha256='e' * 64), self.manifest(), 'e' * 64)
            with self.assertRaises(u.updater.Refusal):
                u.build_plan(dict(config, controller_machine_id_sha256=''), self.manifest(), 'e' * 64)
            with mock.patch.object(u, 'scan_roots', return_value=['/apps/Multica.app']):
                with self.assertRaises(u.updater.Refusal):
                    u.build_plan(config, self.manifest(), 'e' * 64)

    def test_apply_records_pending_before_acceptance(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            oldfile = root / 'running'
            oldfile.write_bytes(b'old')
            staged = root / 'staged'
            staged.write_bytes(b'new')
            evidence = root / 'evidence'
            oldhash = u.updater.sha(str(oldfile))
            config = {'owner_uid': os.geteuid(), 'ledger': str(root / 'ledger.json'), 'evidence_dir': str(evidence), 'profile': '',
                      'unit': 'example.service', 'systemd_scope': 'user', 'staged': str(staged), 'cli': '/observer',
                      'health_url': 'http://127.0.0.1:1234/health'}
            plan = {'inventory': config, 'manifest_sha256': 'a' * 64, 'running_exe': str(oldfile),
                    'running_sha256': oldhash, 'running_version': '1.0.0', 'running_commit': 'b' * 40,
                    'version': '2.0.0', 'commit': 'c' * 40, 'data_compatibility': 'multica-profile-v1', 'baseline_compatibility': 'multica-profile-v1', 'target_sha256': u.updater.sha(str(staged)),
                    'observer_sha256': 'e' * 64, 'daemon_id': 'daemon', 'workspaces': {'workspace': ['runtime']}, 'unit_pid': 123, 'profile_inode': [1, 2]}
            class FakeUpdater:
                def __init__(self, args):
                    self.result = {}
            def perform(operation):
                self.assertEqual(json.loads((root / 'ledger.json').read_text())['status'], 'applying')
                evidence.mkdir()
                (evidence / 'original-entrypoint').write_bytes(b'old')
                (evidence / 'heartbeats.jsonl').write_text('{}\n')
                oldfile.write_bytes(b'new')
                operation.result = {'status': 'updated', 'gate_released_at': '2026-11-12T00:00:00+00:00'}
                return 0
            with mock.patch.object(u, 'build_plan', return_value=plan), mock.patch.object(u.updater, 'Updater', FakeUpdater), mock.patch.object(u.updater, 'perform', side_effect=perform):
                result = u.apply_plan(plan, self.manifest())
            ledger = json.loads((root / 'ledger.json').read_text())
            self.assertEqual(result['status'], 'awaiting_acceptance')
            self.assertEqual(ledger['status'], 'awaiting_acceptance')
            self.assertEqual(ledger['previous']['sha256'], oldhash)
            self.assertTrue((evidence / 'original-entrypoint').exists())

    def test_two_updates_keep_recovery_until_cleanup(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            binary = root / 'multica'
            binary.write_bytes(b'v1')
            wrapper, ca = root / 'wrapper', root / 'ca.pem'
            wrapper.write_bytes(b'wrapper')
            ca.write_bytes(b'ca')
            ledger_path = root / 'ledger'
            class FakeUpdater:
                def __init__(self, args):
                    self.args = args
                    self.result = {}
            def perform(operation):
                evidence = Path(operation.args.evidence_dir)
                evidence.mkdir()
                (evidence / 'original-entrypoint').write_bytes(binary.read_bytes())
                (evidence / 'heartbeats.jsonl').write_text('{}\n')
                binary.write_bytes(operation.args.version.encode())
                operation.result = {'status': 'updated', 'gate_released_at': '2020-01-01T00:00:00+00:00'}
                return 0
            for n in (2, 3, 4):
                old = binary.read_bytes()
                new = ('v%d' % n).encode()
                staged_dir = root / ('staged%d' % n)
                staged_dir.mkdir()
                staged = staged_dir / 'multica'
                staged.write_bytes(new)
                config = {'owner_uid': os.geteuid(), 'ledger': str(ledger_path), 'evidence_dir': str(root / ('evidence%d' % n)),
                          'profile': '', 'unit': 'test.service', 'systemd_scope': 'user', 'staged': str(staged),
                          'cli': '/observer', 'health_url': 'http://127.0.0.1:1234/health',
                          'legacy_wrapper': str(wrapper), 'legacy_baseline': {'wrapper_ca_path': str(ca)}}
                plan = {'inventory': config, 'manifest_sha256': 'a' * 64, 'running_exe': str(binary),
                        'running_sha256': u.hashlib.sha256(old).hexdigest(), 'running_version': 'v%d' % (n - 1),
                        'running_commit': str(n - 1) * 40, 'version': 'v%d' % n, 'commit': str(n) * 40,
                        'target_sha256': u.hashlib.sha256(new).hexdigest(), 'data_compatibility': 'multica-profile-v1',
                        'baseline_compatibility': 'multica-profile-v1', 'observer_sha256': 'e' * 64,
                        'daemon_id': 'daemon', 'workspaces': {'workspace': ['runtime']}, 'unit_pid': 123, 'profile_inode': [1, 2]}
                if n == 2:
                    plan.update(legacy_bootstrap=True, legacy_baseline={'wrapper_ca_path': str(ca)})
                else:
                    del config['legacy_wrapper']
                    del config['legacy_baseline']
                with mock.patch.object(u, 'build_plan', return_value=plan), mock.patch.object(u.updater, 'Updater', FakeUpdater), mock.patch.object(u.updater, 'perform', side_effect=perform):
                    self.assertEqual(u.apply_plan(plan, {})['status'], 'awaiting_acceptance')
                ledger = u.read_ledger(str(ledger_path))
                if n in (2, 3):
                    # Candidate copies are managed with their release so they
                    # cannot block the next inventory as a second installation.
                    associated = {a['path'] for a in ledger['assets']}
                    self.assertIn(str(staged), associated)
                    ledger['assets'] = u.cleanup_managed_assets(ledger, str(root))
                    ledger['status'] = 'accepted'  # Isolate the rotation from receipt verification.
                    u.write_ledger(str(ledger_path), ledger)
            first = root / 'evidence2' / 'original-entrypoint'
            second = root / 'evidence4' / 'original-entrypoint'
            self.assertFalse(first.exists())
            self.assertTrue(second.exists())
            u.cleanup_managed_assets(ledger, str(root))
            self.assertFalse(first.exists())
            self.assertTrue(second.exists() and binary.exists())
            self.assertFalse((root / 'staged2' / 'multica').exists())
            self.assertTrue((root / 'staged3' / 'multica').exists())
            self.assertTrue((root / 'staged4' / 'multica').exists())
            self.assertTrue(wrapper.exists() and ca.exists())
            self.assertTrue({str(wrapper), str(ca)}.issubset(set(ledger['current']['associated_paths'])))

    def test_launcher_apply_retains_both_binaries_and_paired_launchers(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            old_dir, new_dir = root / 'old', root / 'new'
            old_dir.mkdir()
            new_dir.mkdir()
            old_binary, old_launcher = old_dir / 'multica', old_dir / 'launcher'
            new_binary, new_launcher = new_dir / 'multica', new_dir / 'launcher'
            for path, content in ((old_binary, b'old binary'), (old_launcher, b'old launcher'),
                                  (new_binary, b'new binary'), (new_launcher, b'new launcher')):
                path.write_bytes(content)
            evidence = root / 'evidence'
            config = {'owner_uid': os.geteuid(), 'ledger': str(root / 'ledger'), 'evidence_dir': str(evidence),
                      'profile': '', 'unit': 'test.service', 'systemd_scope': 'user', 'staged': str(new_launcher),
                      'staged_binary': str(new_binary), 'entrypoint': str(root / 'entrypoint'), 'layout': 'launcher',
                      'cli': '/observer', 'health_url': 'http://127.0.0.1:1234/health'}
            plan = {'inventory': config, 'manifest_sha256': 'a' * 64, 'running_exe': str(old_binary),
                    'running_sha256': u.updater.sha(str(old_binary)), 'running_version': '1.0.0', 'running_commit': 'b' * 40,
                    'version': '2.0.0', 'commit': 'c' * 40, 'data_compatibility': 'multica-profile-v1', 'baseline_compatibility': 'multica-profile-v1', 'target_sha256': u.updater.sha(str(new_binary)),
                    'observer_sha256': 'e' * 64, 'daemon_id': 'daemon', 'workspaces': {'workspace': ['runtime']}, 'unit_pid': 123, 'profile_inode': [1, 2]}
            class FakeUpdater:
                def __init__(self, args):
                    self.result = {}
            def perform(operation):
                evidence.mkdir()
                (evidence / 'original-entrypoint').write_bytes(b'old launcher')
                (evidence / 'heartbeats.jsonl').write_text('{}\n')
                operation.result = {'status': 'updated', 'gate_released_at': '2020-01-01T00:00:00+00:00'}
                return 0
            with mock.patch.object(u, 'build_plan', return_value=plan), mock.patch.object(u.updater, 'Updater', FakeUpdater), mock.patch.object(u.updater, 'perform', side_effect=perform):
                self.assertEqual(u.apply_plan(plan, self.manifest())['status'], 'awaiting_acceptance')
            ledger = u.read_ledger(str(root / 'ledger'))
            self.assertEqual(ledger['previous']['path'], str(old_binary))
            self.assertEqual(ledger['current']['path'], str(new_binary))
            self.assertIn(str(old_launcher), ledger['previous']['associated_paths'])
            self.assertIn(str(new_launcher), ledger['current']['associated_paths'])

    def test_heartbeat_evidence_needs_three_advancing_samples_per_runtime(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'heartbeats.jsonl'
            current = {'version': '2.0.0', 'commit': 'a' * 40}
            def row(t1, t2):
                return {'version': current['version'], 'commit': current['commit'], 'runtime_last_seen': {'ws': {'r1': t1, 'r2': t2}}}
            one, two, three = ('2020-01-01T00:00:0%d+00:00' % n for n in (1, 2, 3))
            rows = [row(one, one), row(two, two), row(three, two)]
            path.write_text(''.join(json.dumps(item) + '\n' for item in rows))
            tx = {'inventory': {'evidence_dir': root}, 'workspaces': {'ws': ['r1', 'r2']}, 'heartbeat_sha256': u.updater.sha(str(path))}
            with self.assertRaises(u.updater.Refusal):
                u.verify_heartbeat_evidence(tx, current)
            rows[2] = row(three, three)
            path.write_text(''.join(json.dumps(item) + '\n' for item in rows))
            with self.assertRaises(u.updater.Refusal):
                u.verify_heartbeat_evidence(tx, current)
            tx['heartbeat_sha256'] = u.updater.sha(str(path))
            u.verify_heartbeat_evidence(tx, current)
            rows = [row('2020-01-01T00:00:01.%09dZ' % n, '2020-01-01T00:00:01.%09dZ' % n) for n in (1000, 2000, 3000)]
            path.write_text(''.join(json.dumps(item) + '\n' for item in rows))
            tx['heartbeat_sha256'] = u.updater.sha(str(path))
            u.verify_heartbeat_evidence(tx, current)
            rows[2] = row('2020-01-01T00:00:01.000002999Z', '2020-01-01T00:00:01.000002999Z')
            path.write_text(''.join(json.dumps(item) + '\n' for item in rows))
            tx['heartbeat_sha256'] = u.updater.sha(str(path))
            with self.assertRaises(u.updater.Refusal):
                u.verify_heartbeat_evidence(tx, current)

    def test_accept_requires_live_task_receipt_and_retains_two_versions(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            current_file, previous_file = root / 'current', root / 'previous'
            current_file.write_bytes(b'new')
            previous_file.write_bytes(b'old')
            current = {'path': str(current_file), 'sha256': u.updater.sha(str(current_file)), 'version': '2.0.0', 'commit': 'a' * 40}
            previous = {'path': str(previous_file), 'sha256': u.updater.sha(str(previous_file)), 'version': '1.0.0', 'commit': 'b' * 40}
            cfg = {'owner_uid': os.geteuid(), 'profile': '', 'unit': 'test.service', 'systemd_scope': 'user',
                   'cli': str(current_file), 'health_url': 'http://127.0.0.1:1234/health', 'asset_root': str(root)}
            tx = {'inventory': cfg, 'daemon_id': 'daemon', 'profile': '', 'runtime_ids': ['runtime'],
                  'workspaces': {'workspace': ['runtime']}, 'gate_released_at': '2020-01-01T00:00:01+00:00',
                  'process_started_at': '2020-01-01T00:00:00+00:00', 'observer_sha256': current['sha256']}
            cfg['evidence_dir'] = str(root)
            heartbeat = root / 'heartbeats.jsonl'
            heartbeat.write_text(''.join(json.dumps({'version': current['version'], 'commit': current['commit'], 'runtime_last_seen': {'workspace': {'runtime': '2020-01-01T00:00:0%d+00:00' % n}}}) + '\n' for n in (1, 2, 3)))
            tx['heartbeat_sha256'] = u.updater.sha(str(heartbeat))
            tx['target_pid'] = 456
            ledger_path = root / 'ledger'
            u.write_ledger(str(ledger_path), {'schema': 1, 'status': 'awaiting_acceptance', 'current': current,
                                               'previous': previous, 'assets': [current, previous], 'transaction': tx})
            receipt = {'task_id': 'task', 'runtime_id': 'runtime', 'completed_at': '2020-01-01T00:00:02+00:00',
                       'ack_at': '2020-01-01T00:00:03+00:00'}
            health = {'status': 'running', 'daemon_id': 'daemon', 'profile': '', 'cli_commit': current['commit'],
                      'maintenance_gate': {'supported': True, 'blocked': False, 'path': os.path.join(u.pwd.getpwuid(os.geteuid()).pw_dir, '.multica', 'daemon.maintenance')},
                      'update_attestation': {'binary_sha256': current['sha256'], 'process_started_at': tx['process_started_at'],
                                             'heartbeat_acks': 3, 'runtime_heartbeat_acks': {'runtime': 3}, 'task_receipts': []}}
            class FakeUpdater:
                def __init__(self, args):
                    pass
                def health(self):
                    return health
                def inspect(self, *args, **kwargs):
                    return 456
                def runtimes(self):
                    return {'workspace': {'runtime': 'online'}}
            actual_stat = os.stat
            def stat_path(path, *args, **kwargs):
                if path == '/proc/456':
                    return type('Owner', (), {'st_uid': os.geteuid()})()
                return actual_stat(path, *args, **kwargs)
            with mock.patch.object(u.updater, 'Updater', FakeUpdater), mock.patch.object(u.updater, 'proc_exe', return_value=str(current_file)), mock.patch.object(u.os, 'stat', side_effect=stat_path):
                pending = u.accept_transaction(str(ledger_path))
                self.assertEqual(pending['status'], 'awaiting_acceptance')
                self.assertEqual(u.result_exit_code(pending), 2)
                self.assertEqual(u.read_ledger(str(ledger_path))['status'], 'awaiting_acceptance')
                health['update_attestation']['task_receipts'] = [receipt]
                with mock.patch.object(u, 'cleanup_managed_assets', side_effect=PermissionError('fixture')):
                    with self.assertRaises(PermissionError):
                        u.accept_transaction(str(ledger_path))
                self.assertEqual(u.read_ledger(str(ledger_path))['status'], 'cleanup_failed')
                self.assertEqual(u.accept_transaction(str(ledger_path))['status'], 'accepted')
            self.assertEqual(u.read_ledger(str(ledger_path))['status'], 'accepted')
            self.assertTrue(current_file.exists() and previous_file.exists())

    def test_perform_releases_lock_and_restores_signal_handlers(self):
        with tempfile.TemporaryDirectory() as root:
            lockfile = Path(root) / 'gate.lock'
            prior = {sig: u.updater.signal.getsignal(sig) for sig in (u.updater.signal.SIGINT, u.updater.signal.SIGTERM)}
            for attempt in range(2):
                operation = u.updater.Updater(type('Args', (), {})())
                def execute():
                    operation.lock = open(lockfile, 'a+')
                    u.updater.fcntl.flock(operation.lock.fileno(), u.updater.fcntl.LOCK_EX | u.updater.fcntl.LOCK_NB)
                    if attempt == 1:
                        raise u.updater.Refusal('fixture failure')
                with mock.patch.object(operation, 'execute', side_effect=execute):
                    self.assertEqual(u.updater.perform(operation), attempt)
                self.assertIsNone(operation.lock)
                self.assertEqual({sig: u.updater.signal.getsignal(sig) for sig in prior}, prior)
            with open(lockfile, 'a+') as final:
                u.updater.fcntl.flock(final.fileno(), u.updater.fcntl.LOCK_EX | u.updater.fcntl.LOCK_NB)

    def test_gate_busy_does_not_take_over(self):
        operation = u.updater.Updater(type('Args', (), {})())
        with mock.patch.object(operation, 'execute', side_effect=u.updater.Refusal('another updater holds the gate lock')), mock.patch.object(operation, 'remove_gate') as remove, mock.patch.object(u.updater.signal, 'signal'):
            self.assertEqual(u.updater.perform(operation), 1)
        remove.assert_not_called()
        self.assertEqual(operation.result['status'], 'refused')

    def test_boot_failure_attempts_verified_rollback(self):
        operation = u.updater.Updater(type('Args', (), {})())
        def failed_boot():
            operation.stopped = True
            raise u.updater.Refusal('target failed to start')
        def rollback():
            operation.result['status'] = 'rolled_back_verified'
        with mock.patch.object(operation, 'execute', side_effect=failed_boot), mock.patch.object(operation, 'rollback', side_effect=rollback) as restore, mock.patch.object(u.updater.signal, 'signal'):
            self.assertEqual(u.updater.perform(operation), 1)
        restore.assert_called_once()
        self.assertEqual(operation.result['status'], 'rolled_back_verified')

    def test_update_faults_leave_ledger_in_recovery(self):
        for status in ('blocked', 'rolled_back_verified'):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as root:
                root = Path(root)
                binary = root / 'binary'
                binary.write_bytes(b'old')
                config = {'owner_uid': os.geteuid(), 'ledger': str(root / 'ledger'), 'evidence_dir': str(root / 'evidence'),
                          'profile': '', 'unit': 'test.service', 'systemd_scope': 'user', 'staged': '/stage', 'cli': '/observer',
                          'health_url': 'http://127.0.0.1:1234/health'}
                plan = {'inventory': config, 'manifest_sha256': 'a' * 64, 'running_exe': str(binary),
                        'running_sha256': u.updater.sha(str(binary)), 'running_version': '1.0.0', 'running_commit': 'b' * 40,
                        'version': '2.0.0', 'commit': 'c' * 40, 'target_sha256': 'd' * 64,
                        'data_compatibility': 'multica-profile-v1', 'baseline_compatibility': 'multica-profile-v1',
                        'observer_sha256': 'e' * 64, 'daemon_id': 'daemon', 'workspaces': {'workspace': ['runtime']}, 'unit_pid': 123, 'profile_inode': [1, 2]}
                class FakeUpdater:
                    def __init__(self, args):
                        self.result = {'status': status}
                with mock.patch.object(u, 'build_plan', return_value=plan), mock.patch.object(u.updater, 'Updater', FakeUpdater), mock.patch.object(u.updater, 'perform', return_value=1):
                    self.assertEqual(u.apply_plan(plan, self.manifest())['status'], 'recovery_required')
                self.assertEqual(u.read_ledger(str(root / 'ledger'))['status'], 'recovery_required')
                self.assertEqual(binary.read_bytes(), b'old')

    def test_verified_automatic_rollback_waits_for_dispatch_acceptance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary, candidate = root / 'multica', root / 'candidate'
            binary.write_bytes(b'old')
            candidate.write_bytes(b'failed candidate')
            config = {'owner_uid': os.geteuid(), 'ledger': str(root / 'ledger'),
                'evidence_dir': str(root / 'evidence'), 'profile': '', 'unit': 'test.service',
                'systemd_scope': 'user', 'staged': str(candidate), 'cli': '/observer',
                'health_url': 'http://127.0.0.1:1234/health'}
            plan = {'inventory': config, 'manifest_sha256': 'a' * 64,
                'running_exe': str(binary), 'running_sha256': u.updater.sha(str(binary)),
                'running_version': '1.0.0', 'running_commit': 'b' * 40,
                'version': '2.0.0', 'commit': 'c' * 40,
                'target_sha256': u.updater.sha(str(candidate)),
                'data_compatibility': 'multica-profile-v1', 'baseline_compatibility': 'multica-profile-v1',
                'observer_sha256': 'e' * 64, 'daemon_id': 'daemon', 'workspaces': {'ws': ['runtime']},
                'unit_pid': 123, 'profile_inode': [1, 2]}
            stamp = '2020-01-01T00:00:00+00:00'
            class RestoredUpdater:
                def __init__(self, args):
                    self.result = {'status': 'rolled_back_verified', 'restored_pid': 124,
                        'restored_process_started_at': stamp, 'gate_released_at': stamp}
                def health(self):
                    return {'pid': 124, 'update_attestation': {'process_started_at': stamp}}
                def inspect(self, health, *, target, expected_pid):
                    self_target = (plan['running_version'], plan['running_commit'], plan['running_sha256'])
                    if target != self_target or expected_pid != 124:
                        raise u.updater.Refusal('fixture identity mismatch')
            with mock.patch.object(u, 'build_plan', return_value=plan), \
                    mock.patch.object(u.updater, 'Updater', RestoredUpdater), \
                    mock.patch.object(u.updater, 'perform', return_value=1):
                result = u.apply_plan(plan, {})
            ledger = u.read_ledger(config['ledger'])
            self.assertEqual(result['status'], 'rolled_back_pending_acceptance')
            self.assertEqual(ledger['status'], 'awaiting_rollback_acceptance')
            self.assertEqual(ledger['current']['sha256'], plan['running_sha256'])
            self.assertIsNone(ledger['previous'])
            self.assertTrue(candidate.exists(), 'keep failed assets until restored dispatch is verified')
            self.assertEqual(u.result_exit_code(result), 2)
            self.assertEqual(u.result_exit_code({'status': 'rolled_back'}), 1,
                'a restored installation does not turn the failed update into success')

    def test_existing_install_can_initialize_ledger_without_restart(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            binary = root / 'binary'
            binary.write_bytes(b'verified existing program')
            digest = u.updater.sha(str(binary))
            plan = {'inventory': {'ledger': str(root / 'ledger'), 'profile': ''}, 'manifest_sha256': 'd' * 64,
                    'running_exe': str(binary), 'running_sha256': digest, 'running_version': '1.0.0',
                    'running_commit': 'a' * 40, 'version': '1.0.0', 'commit': 'a' * 40, 'target_sha256': digest,
                    'baseline_compatibility': 'multica-profile-v1', 'daemon_id': 'daemon', 'workspaces': {'ws': ['runtime']}, 'unit_pid': 123, 'process_started_at': '2020-01-01T00:00:00+00:00', 'observer_sha256': 'e' * 64}
            with mock.patch.object(u, 'build_plan', return_value=plan), mock.patch.object(u.updater, 'perform') as perform:
                self.assertEqual(u.apply_plan(plan, {})['status'], 'awaiting_baseline_acceptance')
            perform.assert_not_called()
            ledger = u.read_ledger(str(root / 'ledger'))
            self.assertEqual(ledger['current']['sha256'], digest)
            self.assertIsNone(ledger['previous'])
            self.assertEqual(ledger['status'], 'awaiting_baseline_acceptance')

    def test_same_version_does_not_rotate_previous(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            binary = root / 'binary'
            binary.write_bytes(b'binary')
            digest = u.updater.sha(str(binary))
            current = {'path': str(binary), 'sha256': digest, 'version': '1.0.0', 'commit': 'a' * 40,
                       'data_compatibility': 'multica-profile-v1'}
            previous = {'path': str(root / 'old'), 'sha256': 'b' * 64, 'version': '0.9.0', 'commit': 'c' * 40,
                        'data_compatibility': 'multica-profile-v1'}
            ledger = {'schema': 1, 'status': 'accepted', 'current': current, 'previous': previous, 'assets': [current, previous]}
            ledger_path = root / 'ledger'
            u.write_ledger(str(ledger_path), ledger)
            plan = {'inventory': {'ledger': str(ledger_path)}, 'manifest_sha256': 'd' * 64,
                    'running_exe': str(binary), 'running_sha256': digest, 'running_version': '1.0.0',
                    'running_commit': 'a' * 40, 'version': '1.0.0', 'commit': 'a' * 40, 'target_sha256': digest}
            with mock.patch.object(u, 'build_plan', return_value=plan):
                self.assertEqual(u.apply_plan(plan, {})['status'], 'no_change')
            self.assertEqual(u.read_ledger(str(ledger_path))['previous'], previous)

    def test_rollback_only_uses_verified_immediate_previous(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            binary = root / 'previous'
            binary.write_bytes(b'old')
            current = {'path': str(root / 'current'), 'sha256': 'a' * 64, 'version': '2.0.0', 'commit': 'b' * 40,
                       'data_compatibility': 'multica-profile-v1'}
            previous = {'path': str(binary), 'sha256': u.updater.sha(str(binary)), 'version': '1.0.0', 'commit': 'c' * 40,
                        'data_compatibility': 'multica-profile-v1'}
            path = root / 'ledger'
            ledger = {'schema': 1, 'status': 'accepted', 'current': current, 'previous': previous, 'assets': [current, previous],
                      'transaction': {'inventory': {'layout': 'binary'}}}
            u.write_ledger(str(path), ledger)
            with mock.patch.object(u, 'build_plan', return_value={'plan': 'ok'}) as build:
                _, manifest = u.rollback_plan(str(path), str(root / 'rollback-evidence'))
                self.assertEqual(manifest['commit'], previous['commit'])
                self.assertEqual(build.call_args.args[0]['staged'], str(binary))
            ledger['previous']['commit'] = None
            ledger['previous']['legacy_baseline'] = {'binary_sha256': previous['sha256']}
            u.write_ledger(str(path), ledger)
            with mock.patch.object(u, 'build_plan', return_value={'plan': 'ok'}) as build:
                _, manifest = u.rollback_plan(str(path), str(root / 'rollback-evidence'))
                self.assertIsNone(manifest['commit'])
                self.assertTrue(build.call_args.kwargs['legacy_target'])
                self.assertEqual(build.call_args.args[0]['staged'], str(binary))
            ledger['previous']['data_compatibility'] = 'other'
            u.write_ledger(str(path), ledger)
            with self.assertRaises(u.updater.Refusal):
                u.rollback_plan(str(path), str(root / 'rollback-evidence'))

    def test_cleanup_only_recorded_older_hashed_assets(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            paths = [root / str(n) for n in range(4)]
            for p in paths:
                p.write_text(p.name)
            assets = [{'path': str(p), 'sha256': u.updater.sha(str(p))} for p in paths[:3]]
            ledger = {'schema': 1, 'current': assets[0], 'previous': assets[1], 'assets': assets}
            self.assertEqual(u.cleanup_managed_assets(ledger, str(root)), assets[:2])
            self.assertTrue(paths[0].exists() and paths[1].exists() and paths[3].exists())
            self.assertFalse(paths[2].exists())

    def test_cleanup_unlink_failure_is_reported(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            current, obsolete = root / 'current', root / 'obsolete'
            current.write_text('current')
            obsolete.write_text('obsolete')
            assets = [{'path': str(p), 'sha256': u.updater.sha(str(p))} for p in (current, obsolete)]
            ledger = {'schema': 1, 'current': assets[0], 'previous': None, 'assets': assets}
            with mock.patch.object(u.os, 'unlink', side_effect=PermissionError('fixture')):
                with self.assertRaises(PermissionError):
                    u.cleanup_managed_assets(ledger, str(root))
            self.assertTrue(obsolete.exists())

    def test_cleanup_refuses_hash_fault_before_deleting(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            paths = [root / str(n) for n in range(3)]
            for p in paths:
                p.write_text(p.name)
            assets = [{'path': str(p), 'sha256': u.updater.sha(str(p))} for p in paths]
            ledger = {'schema': 1, 'current': assets[0], 'previous': None, 'assets': assets}
            paths[2].write_text('changed')
            with self.assertRaises(u.updater.Refusal):
                u.cleanup_managed_assets(ledger, str(root))
            self.assertTrue(all(p.exists() for p in paths))

    def test_acceptance_requires_fresh_matching_acknowledged_task(self):
        transaction = {'daemon_id': 'daemon', 'profile': 'p', 'commit': 'a' * 40,
                       'sha256': 'b' * 64, 'gate_released_at': '2026-11-12T00:01:00+00:00', 'runtime_ids': ['runtime']}
        receipt = {'task_id': 'task', 'runtime_id': 'runtime', 'completed_at': '2026-11-12T00:02:00+00:00', 'ack_at': '2026-11-12T00:03:00+00:00'}
        health = {'status': 'running', 'daemon_id': 'daemon', 'profile': 'p', 'cli_commit': 'a' * 40,
                  'update_attestation': {'binary_sha256': 'b' * 64, 'process_started_at': '2026-11-12T00:00:00+00:00', 'heartbeat_acks': 3, 'runtime_heartbeat_acks': {'runtime': 3}, 'task_receipts': [receipt]}}
        now = datetime.datetime.fromisoformat('2026-11-12T00:04:00+00:00')
        self.assertEqual(u.verify_receipt(health, transaction, now), receipt)
        for key, value in [('runtime_id', 'foreign'), ('completed_at', '2026-11-11T23:59:00+00:00'), ('ack_at', '2026-11-12T00:05:00+00:00')]:
            previous = receipt[key]
            receipt[key] = value
            with self.assertRaises(u.updater.Refusal):
                u.verify_receipt(health, transaction, now)
            receipt[key] = previous
        health['update_attestation']['runtime_heartbeat_acks']['runtime'] = 2
        with self.assertRaises(u.updater.Refusal):
            u.verify_receipt(health, transaction, now)

    def test_rfc3339_nano_receipt_and_strict_time_validation(self):
        for digits in range(1, 10):
            value = '2026-11-12T00:00:00.' + ('1' * digits) + 'Z'
            self.assertEqual(u.updater.time_value(value).tzinfo, datetime.timezone.utc)
        self.assertEqual(u.updater.time_value('2026-11-12T00:00:00.123456789Z').microsecond, 123456)
        for value in ('2026-11-12T00:00:00.1234567890Z', '2026-11-12T00:00:00',
                      '2026-02-30T00:00:00Z', '2026-11-12 00:00:00Z', '2026-11-12T00:00:00+01:99'):
            with self.subTest(value=value), self.assertRaises(u.updater.Refusal):
                u.updater.time_value(value)
        tx = {'daemon_id': 'daemon', 'profile': '', 'commit': 'a' * 40, 'sha256': 'b' * 64,
              'gate_released_at': '2026-11-12T00:00:01.123456000Z', 'runtime_ids': ['runtime']}
        receipt = {'task_id': 'task', 'runtime_id': 'runtime', 'completed_at': '2026-11-12T00:00:02.987654321Z',
                   'ack_at': '2026-11-12T00:00:03.111111111Z'}
        health = {'status': 'running', 'daemon_id': 'daemon', 'profile': '', 'cli_commit': 'a' * 40,
                  'update_attestation': {'binary_sha256': 'b' * 64, 'process_started_at': '2026-11-12T00:00:00.123456789Z',
                                         'runtime_heartbeat_acks': {'runtime': 3}, 'task_receipts': [receipt]}}
        now = datetime.datetime.fromisoformat('2026-11-12T00:00:04+00:00')
        self.assertEqual(u.verify_receipt(health, tx, now), receipt)
        receipt['completed_at'] = '2026-11-12T00:00:01.123456999Z'
        with self.assertRaises(u.PendingReceipt):
            u.verify_receipt(health, tx, now)
        receipt['completed_at'] = '2026-11-12T00:00:02.123456999Z'
        receipt['ack_at'] = '2026-11-12T00:00:02.123456001Z'
        with self.assertRaises(u.PendingReceipt):
            u.verify_receipt(health, tx, now)

    def test_cross_uid_version_probe_drops_privileges(self):
        account = type('User', (), {'pw_dir': '/home/target', 'pw_gid': 44})()
        args = type('Args', (), {'owner_uid': 42})()
        reply = type('Reply', (), {'returncode': 0, 'stdout': json.dumps({'version': '1.0.0', 'build_commit': 'a' * 40, 'daemon_maintenance_gate': 'v1'}).encode()})()
        with mock.patch.object(u.updater.pwd, 'getpwuid', return_value=account), mock.patch.object(u.updater.os, 'geteuid', return_value=0), mock.patch.object(u.updater.subprocess, 'run', return_value=reply) as run:
            operation = u.updater.Updater(args)
            self.assertEqual(operation.version_as_owner('/binary'), ('1.0.0', 'a' * 40))
            self.assertEqual(run.call_args.kwargs['user'], 42)
            self.assertEqual(run.call_args.kwargs['group'], 44)
            self.assertEqual(run.call_args.kwargs['env']['HOME'], '/home/target')

    def test_systemd_scope_exact_unit(self):
        for scope, flag in [('user', '--user'), ('system', '--system')]:
            instance = u.updater.Updater(type('Args', (), {'systemd_scope': scope})())
            self.assertEqual(instance.systemctl('stop', 'example.service'), ['systemctl', flag, 'stop', 'example.service'])

    def test_root_user_unit_uses_owner_bus_and_drops_privileges(self):
        account = type('User', (), {'pw_dir': '/home/owner', 'pw_gid': 44})()
        args = type('Args', (), {'owner_uid': 42, 'systemd_scope': 'user'})()
        directory = type('Stat', (), {'st_uid': 42, 'st_mode': u.stat.S_IFDIR | 0o700})()
        socket = type('Stat', (), {'st_uid': 42, 'st_mode': u.stat.S_IFSOCK | 0o600})()
        def lstat(path):
            return socket if path == '/run/user/42/bus' else directory
        reply = type('Reply', (), {'returncode': 0, 'stdout': b'active'})()
        with mock.patch.object(u.updater.pwd, 'getpwuid', return_value=account), \
             mock.patch.object(u.updater.os, 'geteuid', return_value=0), \
             mock.patch.object(u.updater, 'path_clean', side_effect=lambda p, *a: p), \
             mock.patch.object(u.updater.os, 'lstat', side_effect=lstat), \
             mock.patch.object(u.updater.subprocess, 'run', return_value=reply) as run:
            operation = u.updater.Updater(args)
            command = operation.systemctl('show', 'precise.service', '--property=MainPID', '--value')
            self.assertEqual(command, ['systemctl', '--user', 'show', 'precise.service', '--property=MainPID', '--value'])
            u.updater.run(command)
            self.assertEqual((run.call_args.kwargs['user'], run.call_args.kwargs['group'], run.call_args.kwargs['extra_groups']), (42, 44, []))
            self.assertEqual(run.call_args.kwargs['env']['DBUS_SESSION_BUS_ADDRESS'], 'unix:path=/run/user/42/bus')
            self.assertEqual(run.call_args.kwargs['env']['XDG_RUNTIME_DIR'], '/run/user/42')
            self.assertEqual(set(run.call_args.kwargs['env']), {'HOME', 'PATH', 'LANG', 'XDG_RUNTIME_DIR', 'DBUS_SESSION_BUS_ADDRESS'})
            bad_socket = type('Stat', (), {'st_uid': 99, 'st_mode': u.stat.S_IFSOCK | 0o600})()
            with mock.patch.object(u.updater.os, 'lstat', side_effect=lambda p: bad_socket if p.endswith('/bus') else directory):
                with self.assertRaisesRegex(u.updater.Refusal, 'bus socket'):
                    operation.systemctl('stop', 'precise.service')


class DownloadHTTPSTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        root = Path(cls.temp.name)
        cls.key, cls.cert = root / 'key.pem', root / 'cert.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                        '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost',
                        '-keyout', str(cls.key), '-out', str(cls.cert)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path == '/redirect':
                    self.send_response(302)
                    self.send_header('Location', 'https://127.0.0.1:%d/ok' % self.server.server_port)
                    self.end_headers()
                    return
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'oversized' if self.path == '/oversize' else b'ok')
            def log_message(self, *args):
                pass
        cls.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(str(cls.cert), str(cls.key))
        cls.server.socket = context.wrap_socket(cls.server.socket, server_side=True)
        cls.thread = threading.Thread(target=cls.server.serve_forever)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()
        cls.temp.cleanup()

    def opener(self, trusted):
        context = ssl.create_default_context(cafile=str(self.cert)) if trusted else ssl.create_default_context()
        return urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context), u.SameOriginRedirect())

    def test_tls_download_and_fail_closed(self):
        port = self.server.server_port
        base = 'https://localhost:%d' % port
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            def manifest(url, digest=None):
                return {'assets': [{'role': 'binary', 'url': url, 'size': 2, 'sha256': digest or u.hashlib.sha256(b'ok').hexdigest()}]}
            prepared = root / 'prepared'
            result = u.download_assets(manifest(base + '/ok'), str(prepared), self.opener(True))
            self.assertEqual(Path(result['binary']).read_bytes(), b'ok')
            self.assertEqual(Path(result['binary']).stat().st_mode & 0o777, 0o755)
            cases = [('untrusted', base + '/ok', False, None),
                     ('wronghost', 'https://127.0.0.1:%d/ok' % port, True, None),
                     ('hash', base + '/ok', True, '0' * 64),
                     ('oversize', base + '/oversize', True, None),
                     ('redirect', base + '/redirect', True, None)]
            for name, url, trusted, digest in cases:
                dest = root / name
                with self.subTest(name=name), self.assertRaises((u.updater.Refusal, OSError)):
                    u.download_assets(manifest(url, digest), str(dest), self.opener(trusted))
                self.assertFalse(dest.exists())
            self.assertEqual(sorted(p.name for p in root.iterdir()), ['prepared'])



if __name__ == '__main__':
    unittest.main()
