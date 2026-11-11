import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import signal
import tempfile
import types
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('manage_macos', Path(__file__).with_name('manage-macos-daemon.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class FakeOS:
    fixture = True
    def __init__(self, args, old, old_commit):
        self.a, self.old, self.old_commit = args, old, old_commit
        self.pid = 101
        self.exe = old
        self.job = None
        self.tick = 0
        self.fail_new_identity = False
        self.no_heartbeat = False
        self.busy = False
        self.unhealthy = False
        self.gate_supported = True
        self.events = []
    def process(self, pid):
        if self.pid != pid: return None
        st = os.stat(self.exe)
        return ('start-' + str(pid), self.exe, st.st_dev, st.st_ino, m.stage.digest(self.exe))
    def app_absent(self): return True
    def job_pid(self): return self.job
    def health(self):
        if self.pid is None or self.unhealthy: raise m.Refusal('stopped')
        managed = self.job == self.pid
        new = self.exe != self.old
        return {'status':'running', 'profile':self.a.profile, 'pid':self.pid, 'daemon_id':'wrong' if new and self.fail_new_identity else 'daemon-1',
                'server_url':'https://example.invalid', 'workspaces':[{'id':'ws', 'runtimes':['runtime']}], 'active_task_count':1 if self.busy else 0,
                'cli_version':'new' if new else 'old', 'cli_commit':('b' * 40 if new else self.old_commit),
                'lifecycle_manager':'launchd-pinned' if managed else '', 'desktop_protocol':1,
                'maintenance_gate':{'supported':self.gate_supported, 'path':self.a.profile_dir + '/daemon.maintenance',
                                    'blocked':os.path.isdir(self.a.profile_dir + '/daemon.maintenance'),
                                    'ready':os.path.isdir(self.a.profile_dir + '/daemon.maintenance'), 'claims_in_flight':0}}
    def runtimes(self, base):
        if not self.no_heartbeat: self.tick += 1
        return {'ws':{'runtime':self.tick}}
    def terminate(self, pid, identity):
        assert self.process(pid) == identity
        self.events.append('term')
        self.pid = None
    def bootout(self):
        self.events.append('bootout')
        self.pid = self.job = None
    def bootstrap(self, path):
        self.events.append('bootstrap')
        with open(path, 'rb') as f: self.exe = plistlib.load(f)['ProgramArguments'][0]
        self.pid = 102 if self.exe != self.old else 103
        self.job = self.pid
    def start_app_daemon(self, executable, environment):
        self.events.append('restore-app')
        self.exe = executable
        self.pid, self.job = 103, None
    def sleep(self, seconds): pass
    def version(self, path):
        return types.SimpleNamespace(stdout=json.dumps({'version':'old' if path == self.old else 'new','build_commit':'a'*40 if path == self.old else 'b'*40,'daemon_maintenance_gate':'v1','daemon_lifecycle':'launchd-pinned-v1'}).encode())


class Transactions(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.home = root / 'home'
        self.home.mkdir()
        (self.home / 'Library' / 'LaunchAgents').mkdir(parents=True)
        self.profile = self.home / '.multica' / 'profiles' / 'desk'
        self.profile.mkdir(parents=True)
        self.old = root / 'old'
        self.old.write_bytes(b'old')
        candidate = root / 'stage'
        candidate.mkdir()
        self.new = candidate / 'multica'
        self.new.write_bytes(b'new')
        self.logs = root / 'logs'
        self.logs.mkdir()
        self.ca = root / 'ca'
        self.ca.write_bytes(b'fixture')
        self.label = 'test.multica'
        self.args = types.SimpleNamespace(mode='migrate', profile='desk', profile_dir=str(self.profile), label=self.label,
          plist_path=str(self.home / 'Library' / 'LaunchAgents' / (self.label + '.plist')), candidate_dir=str(candidate), cli=str(self.old),
          app_executable='/Applications/Test.app/Contents/MacOS/Test', app_quit_confirmed=True, timeout=0.01, interval=0.001, evidence_dir=str(root/'evidence'), original_ca_cert=str(self.ca), original_ca_sha256=m.stage.digest(str(self.ca)), original_path_env='/usr/bin:/bin')
        self.marker = {'schema_version':1,'manager':'launchd-pinned','label':self.label,'binary':str(self.new),
                       'sha256':m.stage.digest(str(self.new)),'version':'new','build_commit':'b'*40}
        (candidate / 'daemon-management.json').write_text(json.dumps(self.marker))
        self.plist = {'Label':self.label, 'ProgramArguments':[str(self.new),'--profile','desk','daemon','start','--foreground','--no-auto-update','--no-auto-reload'],
          'RunAtLoad':True,'KeepAlive':True,'WorkingDirectory':str(root),'StandardOutPath':str(self.logs/'out'),'StandardErrorPath':str(self.logs/'err'),
          'EnvironmentVariables':{'MULTICA_DAEMON_SUPERVISOR':'launchd-pinned','MULTICA_DAEMON_AUTO_UPDATE':'false','MULTICA_DAEMON_AUTO_RELOAD':'false','MULTICA_ZH_TW_DESKTOP_SELF_RELOAD':'0','HOME':str(self.home),'PATH':'/usr/bin:/bin','MULTICA_CA_CERT_FILE':str(self.ca)}}
        with (candidate / 'launch-agent.plist').open('wb') as f: plistlib.dump(self.plist, f)
        patch = mock.patch.object(m.os.path, 'expanduser', return_value=str(self.home))
        patch.start(); self.addCleanup(patch.stop)
        self.fake = FakeOS(self.args, str(self.old), 'a'*40)
        self.tx = m.Transaction(self.args, self.fake)
        self.addCleanup(self.close_lock)
    def close_lock(self):
        if getattr(self.tx, 'lock', None): self.tx.lock.close()
    def test_migration_success(self):
        self.tx.execute()
        self.assertEqual(self.tx.result, 'updated_verified')
        self.assertEqual(self.fake.events, ['term','bootstrap'])
        self.assertFalse((self.profile/'daemon.maintenance').exists())
        self.assertTrue((self.profile/'daemon-management.json').exists())
        self.assertGreaterEqual(self.fake.tick, 5)

    def test_nonpersistent_plist_location_is_refused(self):
        self.args.plist_path = str(self.home / (self.label + '.plist'))
        with self.assertRaises(m.Refusal):
            self.tx.execute()
        self.assertEqual(self.fake.events, [])
    def test_update_from_managed_profile(self):
        self.args.mode = 'update'
        old_marker = dict(self.marker, binary=str(self.old), sha256=m.stage.digest(str(self.old)), version='old', build_commit='a'*40)
        (self.profile/'daemon-management.json').write_text(json.dumps(old_marker))
        old_plist = dict(self.plist, ProgramArguments=[str(self.old),'--profile','desk','daemon','start','--foreground','--no-auto-update','--no-auto-reload'])
        with open(self.args.plist_path, 'wb') as f: plistlib.dump(old_plist, f)
        self.fake.job = self.fake.pid
        self.tx.execute()
        self.assertEqual(self.fake.events, ['bootout','bootstrap'])
        self.assertEqual(self.tx.result, 'updated_verified')
        self.assertFalse((self.profile/'daemon.maintenance').exists())
    def test_managed_failure_restores_saved_plist_and_marker(self):
        self.args.mode = 'update'
        old_marker = dict(self.marker, binary=str(self.old), sha256=m.stage.digest(str(self.old)), version='old', build_commit='a'*40)
        marker_path = self.profile/'daemon-management.json'
        marker_path.write_text(json.dumps(old_marker))
        old_plist = dict(self.plist, ProgramArguments=[str(self.old),'--profile','desk','daemon','start','--foreground','--no-auto-update','--no-auto-reload'])
        with open(self.args.plist_path, 'wb') as f: plistlib.dump(old_plist, f)
        self.fake.job = self.fake.pid
        self.fake.fail_new_identity = True
        with self.assertRaises(m.Refusal): self.tx.execute()
        self.tx.rollback()
        self.assertEqual(json.loads(marker_path.read_text()), old_marker)
        with open(self.args.plist_path, 'rb') as f: self.assertEqual(plistlib.load(f), old_plist)
        self.assertEqual(self.fake.events, ['bootout','bootstrap','bootout','bootstrap'])
        self.assertFalse((self.profile/'daemon.maintenance').exists())
    def test_failure_rolls_back_and_removes_marker_only_after_verification(self):
        self.fake.fail_new_identity = True
        with self.assertRaises(m.Refusal): self.tx.execute()
        self.assertTrue((self.profile/'daemon.maintenance').exists())
        self.tx.rollback()
        self.assertEqual(self.tx.result, 'rolled_back_verified')
        self.assertEqual(self.fake.events, ['term','bootstrap','bootout','restore-app'])
        self.assertFalse((self.profile/'daemon-management.json').exists())
        self.assertFalse((self.profile/'daemon.maintenance').exists())
    def test_busy_or_unhealthy_candidate_is_never_stopped(self):
        self.fake.fail_new_identity = True
        with self.assertRaises(m.Refusal): self.tx.execute()
        for unhealthy in (False, True):
            self.fake.busy = not unhealthy
            self.fake.unhealthy = unhealthy
            with self.assertRaises(m.Refusal): self.tx.rollback()
            self.assertEqual(self.fake.events, ['term', 'bootstrap'])
            self.assertTrue((self.profile/'daemon.maintenance').exists())

    def test_original_version_mismatch_prevents_stop(self):
        original_version = self.fake.version
        def bad_version(path):
            if path == self.fake.old:
                return types.SimpleNamespace(stdout=json.dumps({'version':'old','build_commit':'c'*40,'daemon_maintenance_gate':'v1'}).encode())
            return original_version(path)
        self.fake.version = bad_version
        with self.assertRaises(m.Refusal): self.tx.execute()
        self.assertEqual(self.fake.events, [])

    def test_evidence_and_release_failure_are_explicit(self):
        original_sync = m.sync_dir
        def fail_after_release(path):
            if path == str(self.profile) and self.tx.released:
                raise OSError('fixture sync failure')
            return original_sync(path)
        with mock.patch.object(m, 'sync_dir', side_effect=fail_after_release):
            with self.assertRaises(OSError): self.tx.execute()
        self.assertTrue(self.tx.released)
        self.assertFalse((self.profile/'daemon.maintenance').exists())
        self.assertTrue((Path(self.args.evidence_dir)/'baseline.json').exists())
        self.assertEqual(len((Path(self.args.evidence_dir)/'heartbeats.jsonl').read_text().splitlines()), 3)

    def test_interrupt_during_gate_creation_preserves_owned_gate(self):
        self.tx.real_profile = str(self.profile)
        st = self.profile.stat()
        self.tx.profile_identity = (st.st_dev, st.st_ino, st.st_uid)
        self.tx.gate = str(self.profile/'daemon.maintenance')
        original_sync = m.sync_dir
        def interrupt_at_gate(path):
            if path == self.tx.gate:
                signal.raise_signal(signal.SIGINT)
            return original_sync(path)
        old = signal.signal(signal.SIGINT, lambda signum, frame: (_ for _ in ()).throw(m.Refusal('fixture interrupt')))
        try:
            with mock.patch.object(m, 'sync_dir', side_effect=interrupt_at_gate):
                with self.assertRaises(m.Refusal): self.tx.gate_create()
        finally:
            signal.signal(signal.SIGINT, old)
        self.tx.own_gate()
        self.assertTrue((self.profile/'daemon.maintenance'/'owner').is_file())

    def test_profile_replacement_blocks_rollback_stop(self):
        self.fake.fail_new_identity = True
        with self.assertRaises(m.Refusal): self.tx.execute()
        original = self.profile.with_name('original-desk')
        self.profile.rename(original)
        self.profile.mkdir()
        with self.assertRaises(m.Refusal): self.tx.rollback()
        self.assertTrue(self.tx.profile_changed)
        self.assertTrue((original/'daemon.maintenance').exists())
        self.assertEqual(self.fake.events, ['term', 'bootstrap'])

    def test_missing_gate_and_wrong_hash_refuse_before_stop(self):
        self.fake.gate_supported = False
        with self.assertRaises(m.Refusal): self.tx.execute()
        self.assertEqual(self.fake.events, [])
        self.fake.gate_supported = True
        self.new.write_bytes(b'tampered')
        with self.assertRaises(m.Refusal): self.tx.execute()
        self.assertEqual(self.fake.events, [])
    def test_failed_rollback_keeps_gate_and_marker(self):
        self.fake.fail_new_identity = True
        with self.assertRaises(m.Refusal): self.tx.execute()
        self.fake.no_heartbeat = True
        with self.assertRaises(m.Refusal): self.tx.rollback()
        self.assertTrue((self.profile/'daemon.maintenance').exists())
        self.assertTrue((self.profile/'daemon-management.json').exists())


class AdapterTests(unittest.TestCase):
    def test_process_exit_during_inspection_requires_independent_absence(self):
        for disappears in (True, False):
            with self.subTest(disappears=disappears):
                def output(argv, **kwargs):
                    if argv[0] == '/usr/sbin/lsof':
                        raise m.Refusal('process exited during lsof')
                    if 'pid=' in argv:
                        return types.SimpleNamespace(returncode=1 if disappears else 0,
                                                     stdout=b'' if disappears else b'99', stderr=b'')
                    if 'comm=' in argv:
                        return types.SimpleNamespace(stdout=b'/fixture/multica', returncode=0)
                    return types.SimpleNamespace(stdout=('%d Mon Jan 1 00:00:00 2024' % os.getuid()).encode(), returncode=0)
                adapter = m.MacOS(types.SimpleNamespace())
                with mock.patch.object(m, 'command', side_effect=output):
                    if disappears:
                        self.assertIsNone(adapter.process(99))
                    else:
                        with self.assertRaises(m.Refusal):
                            adapter.process(99)

    def test_process_probe_error_is_never_absence(self):
        bad = types.SimpleNamespace(returncode=1, stdout=b'', stderr=b'permission denied')
        with mock.patch.object(m, 'command', return_value=bad):
            with self.assertRaises(m.Refusal):
                m.MacOS(types.SimpleNamespace()).process(99)

    def test_process_requires_matching_lsof_device_and_inode(self):
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory)/'multica'
            binary.write_bytes(b'fixture')
            st = binary.stat()
            a = types.SimpleNamespace()
            adapter = m.MacOS(a)
            def output(argv, **kwargs):
                if argv[0] == '/usr/sbin/lsof':
                    return types.SimpleNamespace(stdout=('p99\nftxt\nD%x\ni%d\nn%s\n' % (st.st_dev, st.st_ino, binary)).encode(), returncode=0)
                if 'comm=' in argv:
                    return types.SimpleNamespace(stdout=str(binary).encode(), returncode=0)
                return types.SimpleNamespace(stdout=('%d Mon Jan  1 00:00:00 2024' % os.getuid()).encode(), returncode=0)
            with mock.patch.object(m, 'command', side_effect=output):
                self.assertEqual(adapter.process(99)[2:4], (st.st_dev, st.st_ino))
            def bad_output(argv, **kwargs):
                result = output(argv, **kwargs)
                if argv[0] == '/usr/sbin/lsof': result.stdout = result.stdout.replace(('i%d' % st.st_ino).encode(), b'i1')
                return result
            with mock.patch.object(m, 'command', side_effect=bad_output):
                with self.assertRaises(m.Refusal): adapter.process(99)

    def test_health_follows_changed_port_record(self):
        with tempfile.TemporaryDirectory() as directory:
            a = types.SimpleNamespace(profile_dir=directory, profile='desk', health_url='http://127.0.0.1:1111/health')
            record = Path(directory)/'daemon.port'
            adapter = m.MacOS(a)
            class Response(io.BytesIO):
                status = 200
            class Opener:
                def __init__(self): self.urls = []
                def open(self, url, timeout):
                    self.urls.append(url)
                    return Response(json.dumps({'profile':'desk','pid':222}).encode())
            opener = Opener()
            with mock.patch.object(m.urllib.request, 'build_opener', return_value=opener):
                record.write_text(json.dumps({'port':4444,'pid':222}))
                self.assertEqual(adapter.health()['pid'], 222)
                record.write_text(json.dumps({'port':5555,'pid':222}))
                self.assertEqual(adapter.health()['pid'], 222)
            self.assertEqual(opener.urls, ['http://127.0.0.1:4444/health','http://127.0.0.1:5555/health'])


if __name__ == '__main__': unittest.main()
