"""Release provenance checks using actual cross-compiled Go binaries."""
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
sys.path.insert(0, str(HERE))
import verify_release_binary as verifier


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


packager = load('package_unified_fixture', 'package-unified-update.py')
finalizer = load('finalize_unified_fixture', 'finalize-unified-release.py')
VERSION = '0.8.1-zh-tw.9'
COMMIT = 'a' * 40
BASE = 'https://10.1.24.90:45671/downloads/' + VERSION


class ReleaseVerification(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temp.name)
        cls.source = cls.root / 'main.go'
        cls.source.write_text('package main\nvar version, buildCommit string\nfunc main() {}\n', encoding='utf-8')
        cls.binaries = {}
        for platform in ('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64'):
            goos, arch = platform.split('-')
            binary = cls.root / platform
            env = dict(os.environ, GOTOOLCHAIN='local', GO111MODULE='off', GOOS=goos, GOARCH=arch, CGO_ENABLED='0')
            subprocess.run(['go', 'build', '-ldflags', f'-X main.version={VERSION} -X main.buildCommit={COMMIT}',
                            '-o', str(binary), str(cls.source)], env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            cls.binaries[platform] = binary
        cls.old = cls.root / 'old-binary'
        env = dict(os.environ, GOTOOLCHAIN='local', GO111MODULE='off', GOOS='linux', GOARCH='amd64', CGO_ENABLED='0')
        subprocess.run(['go', 'build', '-ldflags', f'-X main.version=0.8.1-zh-tw.3 -X main.buildCommit={COMMIT}',
                        '-o', str(cls.old), str(cls.source)], env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def prepare(self, root):
        root = Path(root)
        (root / 'BUILD.txt').write_text(f'version={VERSION}\ncommit={COMMIT}\ngo=go1.26.9\n')
        (root / 'RELEASE-NOTES.md').write_text('release notes\n')
        (root / 's90-ca.crt').write_bytes(b'fixture public ca')
        for platform, binary in self.binaries.items():
            shutil.copyfile(binary, root / f'multica-{VERSION}-{platform}')
        def git(command, **kwargs):
            return COMMIT + '\n' if command[-2:] == ['rev-parse', 'HEAD'] else ''
        with mock.patch.object(packager.subprocess, 'check_output', side_effect=git):
            packager.package(REPO, root, VERSION, COMMIT, BASE)
        app = root / f'multica-desktop-{VERSION}-mac-arm64.zip'
        with zipfile.ZipFile(app, 'w') as archive:
            archive.writestr('Multica.app/Contents/Info.plist', plistlib.dumps({'CFBundleIdentifier': verifier.BUNDLE_ID, 'CFBundleShortVersionString': VERSION}))
            archive.write(self.binaries['darwin-arm64'], 'Multica.app/Contents/Resources/app.asar.unpacked/resources/bin/multica')
        verifier.emit_manifest(root, BASE, 'https://example.test/notes', app)
        return root

    def test_real_buildinfo_rejects_old_commit_or_platform(self):
        verifier.verify_binary(self.binaries['linux-amd64'], VERSION, COMMIT, 'linux', 'amd64')
        for binary, version, commit, goos, arch in ((self.old, VERSION, COMMIT, 'linux', 'amd64'),
                (self.binaries['linux-amd64'], VERSION, 'b' * 40, 'linux', 'amd64'),
                (self.binaries['linux-amd64'], VERSION, COMMIT, 'linux', 'arm64')):
            with self.subTest(binary=str(binary), version=version, commit=commit, arch=arch), self.assertRaises(ValueError):
                verifier.verify_binary(binary, version, commit, goos, arch)

    def test_old_binary_refused_before_packaging(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            (root / 'BUILD.txt').write_text(f'version={VERSION}\ncommit={COMMIT}\n')
            shutil.copyfile(self.old, root / f'multica-{VERSION}-linux-amd64')
            shutil.copyfile(self.binaries['linux-arm64'], root / f'multica-{VERSION}-linux-arm64')
            with mock.patch.object(packager.subprocess, 'check_output', side_effect=[COMMIT + '\n', '']):
                with self.assertRaises(ValueError):
                    packager.package(REPO, root, VERSION, COMMIT, BASE)
            self.assertFalse((root / 'multica-update.sh').exists())

    def test_complete_release_and_missing_assets(self):
        with tempfile.TemporaryDirectory() as root:
            release = self.prepare(root)
            manifest = json.loads((release / 'latest.json').read_text())
            self.assertEqual(manifest['desktop'], {})
            self.assertEqual(manifest['coordinated_desktop']['darwin-arm64']['binary_commit'], COMMIT)
            missing = release / 'update-manifest-linux-arm64.json'
            saved = missing.read_bytes()
            missing.unlink()
            with self.assertRaises(ValueError):
                finalizer.finalize(release)
            self.assertFalse((release / 'SHA256SUMS').exists())
            missing.write_bytes(saved)
            app = release / f'multica-desktop-{VERSION}-mac-arm64.zip'
            saved_app = app.read_bytes()
            app.unlink()
            with self.assertRaises(ValueError):
                finalizer.finalize(release)
            app.write_bytes(saved_app)
            sums = finalizer.finalize(release).read_text()
            self.assertIn(f'  {app.name}\n', sums)
            self.assertNotIn('  install.sh\n', sums)
            self.assertNotIn('  SHA256SUMS\n', sums)

    def test_manifest_shell_refuses_missing_app_and_emits_verified_app(self):
        with tempfile.TemporaryDirectory() as root:
            release = self.prepare(root)
            (release / 'latest.json').unlink()
            app = release / f'multica-desktop-{VERSION}-mac-arm64.zip'
            bad = subprocess.run(['bash', str(HERE / 'manifest.sh'), str(release), BASE, 'https://example.test/notes', str(release / 'missing.zip')], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertNotEqual(bad.returncode, 0)
            self.assertFalse((release / 'latest.json').exists())
            good = subprocess.run(['bash', str(HERE / 'manifest.sh'), str(release), BASE, 'https://example.test/notes', str(app)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertEqual(good.returncode, 0, good.stderr.decode())
            self.assertEqual(json.loads((release / 'latest.json').read_text())['desktop'], {})

    def test_app_mismatch_refused(self):
        with tempfile.TemporaryDirectory() as root:
            archive_path = Path(root) / 'bad.zip'
            with zipfile.ZipFile(archive_path, 'w') as archive:
                archive.writestr('Multica.app/Contents/Info.plist', plistlib.dumps({'CFBundleIdentifier': verifier.BUNDLE_ID, 'CFBundleShortVersionString': '0.8.1-zh-tw.3'}))
                archive.write(self.binaries['darwin-arm64'], 'Multica.app/Contents/Resources/app.asar.unpacked/resources/bin/multica')
            with self.assertRaises(ValueError):
                verifier.verify_app(archive_path, self.binaries['darwin-arm64'], VERSION, COMMIT)
            with zipfile.ZipFile(archive_path, 'w') as archive:
                archive.writestr('Multica.app/Contents/Info.plist', plistlib.dumps({'CFBundleIdentifier': verifier.BUNDLE_ID, 'CFBundleShortVersionString': VERSION}))
                archive.write(self.binaries['darwin-amd64'], 'Multica.app/Contents/Resources/app.asar.unpacked/resources/bin/multica')
            with self.assertRaises(ValueError):
                verifier.verify_app(archive_path, self.binaries['darwin-arm64'], VERSION, COMMIT)


if __name__ == '__main__':
    unittest.main()
