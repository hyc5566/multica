import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import tempfile
import types
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('stage_macos', Path(__file__).with_name('stage-macos-daemon.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class StageTests(unittest.TestCase):
    def setUp(self):
        context = mock.patch.object(module, 'task_context', return_value=False)
        context.start()
        self.addCleanup(context.stop)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.binary = self.root / 'candidate'
        self.binary.write_bytes(b'candidate')
        self.sha = hashlib.sha256(b'candidate').hexdigest()
        self.args = types.SimpleNamespace(binary=str(self.binary), destination=str(self.root), sha256=self.sha,
                                          version='1.2.3', commit='a' * 40, label='example.daemon', profile='desktop-test',
                                          ca_cert=str(self.binary), path_env='/usr/bin:/bin', working_dir=str(self.root), log_dir=str(self.root))
        self.info = {'version': self.args.version, 'build_commit': self.args.commit,
                     'daemon_maintenance_gate': 'v1', 'daemon_lifecycle': 'launchd-pinned-v1'}

    def run_version(self, *args, **kwargs):
        return types.SimpleNamespace(returncode=0, stdout=json.dumps(self.info).encode())

    def test_stage_only_and_never_overwrite(self):
        folder = Path(module.stage(self.args, platform='darwin', run=self.run_version))
        marker = json.loads((folder / 'daemon-management.json').read_text())
        self.assertEqual(marker['binary'], str(folder / 'multica'))
        self.assertEqual(marker['sha256'], self.sha)
        with (folder / 'launch-agent.plist').open('rb') as f:
            plist = plistlib.load(f)
        self.assertEqual(plist['ProgramArguments'], [marker['binary'], '--profile', 'desktop-test', 'daemon', 'start', '--foreground', '--no-auto-update', '--no-auto-reload'])
        self.assertEqual(plist['EnvironmentVariables']['MULTICA_DAEMON_SUPERVISOR'], 'launchd-pinned')
        with self.assertRaises(FileExistsError):
            module.stage(self.args, platform='darwin', run=self.run_version)

    def test_rejects_wrong_platform_hash_and_capability(self):
        with self.assertRaises(module.Refusal):
            module.stage(self.args, platform='linux', run=self.run_version)
        self.args.sha256 = '0' * 64
        with self.assertRaises(module.Refusal):
            module.stage(self.args, platform='darwin', run=self.run_version)
        self.args.sha256 = self.sha
        del self.info['daemon_lifecycle']
        with self.assertRaises(module.Refusal):
            module.stage(self.args, platform='darwin', run=self.run_version)
        self.assertEqual(set(self.root.iterdir()), {self.binary})

    def test_refuses_symlink_destination_and_task_context(self):
        alias = self.root / 'alias'
        alias.symlink_to(self.root, target_is_directory=True)
        self.args.destination = str(alias)
        with self.assertRaises(module.Refusal):
            module.stage(self.args, platform='darwin', run=self.run_version)
        self.args.destination = str(self.root)
        old = os.environ.get('MULTICA_TASK_ID')
        os.environ['MULTICA_TASK_ID'] = 'fixture'
        try:
            with mock.patch.object(module, 'task_context', return_value=True):
                with self.assertRaises(module.Refusal):
                    module.stage(self.args, platform='darwin', run=self.run_version)
        finally:
            if old is None:
                del os.environ['MULTICA_TASK_ID']
            else:
                os.environ['MULTICA_TASK_ID'] = old


if __name__ == '__main__':
    unittest.main()
