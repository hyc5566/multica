import os
import errno
from pathlib import Path
import stat
import subprocess
import tempfile
import types
import unittest
from unittest import mock

from test_multica_update import u


class ExtendedHomeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.shared = self.root / 'shared'
        self.extended = self.shared / 'user'
        self.asset = self.extended / 'assets'
        self.home = self.root / 'home'
        self.state = self.home / '.multica'
        self.unitdir = self.extended / 'config'
        self.linkdir = self.home / '.config/systemd/user'
        for path in (self.asset, self.state, self.unitdir, self.linkdir):
            path.mkdir(parents=True, exist_ok=True)
        for path in (self.home, self.home / '.config', self.home / '.config/systemd', self.linkdir, self.extended, self.unitdir):
            path.chmod(0o700)
        self.asset.chmod(0o700)
        self.state.chmod(0o700)
        self.binary = self.asset / 'multica'
        self.binary.write_text('binary')
        self.binary.chmod(0o600)
        self.target = self.unitdir / 'test.service'
        self.target.write_text('unit')
        self.target.chmod(0o600)
        self.link = self.linkdir / 'test.service'
        self.link.symlink_to(self.target)
        self.policy = {'extended_home': str(self.extended), 'sticky_parent': str(self.shared)}
        self.uid = os.geteuid()
        self.original_lstat = os.lstat
        self.shared_mode, self.shared_uid, self.child_uid = 0o1775, 0, self.uid
        self.target_uid = self.uid
        def synthetic_lstat(path, *args, **kwargs):
            result = self.original_lstat(path, *args, **kwargs)
            path = str(path)
            fields = list(result)
            if path == str(self.shared):
                fields[0] = stat.S_IFDIR | self.shared_mode
                fields[4] = self.shared_uid
            elif path == str(self.extended):
                fields[4] = self.child_uid
            elif path == str(self.target):
                fields[4] = self.target_uid
            elif path != str(self.root) and str(self.root).startswith(path.rstrip('/') + '/'):
                fields[0] &= ~0o022
            return os.stat_result(fields) if fields != list(result) else result
        patch = mock.patch.object(u.updater.os, 'lstat', side_effect=synthetic_lstat)
        patch.start()
        self.addCleanup(patch.stop)
        patch = mock.patch.object(u.updater.pwd, 'getpwuid', return_value=types.SimpleNamespace(pw_dir=str(self.home)))
        patch.start()
        self.addCleanup(patch.stop)
        self.start = '{ path=%s ; argv[]=%s daemon start --foreground ; ignore_errors=no ; }' % (self.binary, self.binary)
        self.ancillary = {name: '' for name in u.updater.ACCOUNT_UNIT_ANCILLARY}

    def guard(self, policy=None, fragment=None):
        return u.updater.AccountScope(self.uid, str(self.asset), str(self.state), [str(self.binary)],
            [str(self.asset / 'ledger')], 'test.service', 'user', self.start, str(self.link) if fragment is None else fragment,
            str(self.binary), '', '', '/org/freedesktop/systemd1/unit/test_2eservice', self.ancillary,
            self.policy if policy is None else policy)

    def test_explicit_sticky_and_registration_are_bound(self):
        guard = self.guard()
        evidence = guard.evidence()
        self.assertEqual(evidence['account_path_policy'], self.policy)
        self.assertEqual(evidence['registration'][-1], str(self.target))
        self.assertEqual(evidence['fragment_target'], str(self.target))
        guard.revalidate()
        self.link.unlink()
        self.link.symlink_to(self.target)
        with self.assertRaisesRegex(u.updater.Refusal, 'registration changed'):
            guard.revalidate()

    def test_reject_bad_sticky_and_policy(self):
        for mode, owner, child in ((0o775, 0, self.uid), (0o1775, self.uid, self.uid), (0o1775, 0, self.uid + 1)):
            self.shared_mode, self.shared_uid, self.child_uid = mode, owner, child
            with self.subTest(mode=mode, owner=owner, child=child), self.assertRaises(u.updater.Refusal):
                self.guard()
        self.shared_mode, self.shared_uid, self.child_uid = 0o1775, 0, self.uid
        with self.assertRaisesRegex(u.updater.Refusal, 'unknown account path policy'):
            self.guard(dict(self.policy, unknown=True))
        self.extended.chmod(0o775)
        with self.assertRaises(u.updater.Refusal):
            self.guard()

    def test_acl_access_default_and_late_drift(self):
        for option in ('u:65534:rwx', 'd:u:65534:rwx'):
            subprocess.run(['setfacl', '-m', option, str(self.extended)], check=True)
            with self.assertRaisesRegex(u.updater.Refusal, 'ACL unsupported'):
                self.guard()
            subprocess.run(['setfacl', '-b', '-k', str(self.extended)], check=True)
        guard = self.guard()
        subprocess.run(['setfacl', '-m', 'd:u:65534:rwx', str(self.unitdir)], check=True)
        with self.assertRaisesRegex(u.updater.Refusal, 'ACL unsupported'):
            guard.revalidate()

    def test_link_target_and_extra_symlink_rejected(self):
        guard = self.guard()
        self.target.write_text('changed')
        with self.assertRaisesRegex(u.updater.Refusal, 'fragment changed'):
            guard.revalidate()
        self.link.unlink()
        other = self.extended / 'second-link'
        other.symlink_to(self.target)
        self.link.symlink_to(other)
        with self.assertRaisesRegex(u.updater.Refusal, 'symlink'):
            self.guard()

    def test_roundtrip_and_late_revalidation(self):
        guard = self.guard()
        evidence = guard.evidence()
        cfg = {'owner_uid': self.uid, 'profile': '', 'unit': 'test.service', 'systemd_scope': 'user',
               'asset_root': str(self.asset), 'account_path_policy': self.policy}
        props = {'ExecStart': self.start, 'FragmentPath': str(self.link), 'DropInPaths': ''}
        with mock.patch.object(u.updater.Updater, 'account_unit_property', lambda _, name: props[name]), \
             mock.patch.object(u.updater.Updater, 'account_unit_ancillary', return_value=('/org/freedesktop/systemd1/unit/test_2eservice', self.ancillary)):
            rebuilt = u.account_guard_from_evidence(evidence, cfg)
            self.assertEqual(rebuilt.evidence(), evidence)
            self.assertEqual(u.fingerprint(rebuilt.evidence()), u.fingerprint(evidence))
            changed = dict(cfg, account_path_policy=dict(self.policy, extra='unknown'))
            with self.assertRaises(u.updater.Refusal):
                u.account_guard_from_evidence(evidence, changed)
        self.target_uid += 1
        with self.assertRaises(u.updater.Refusal):
            guard.revalidate_while_leased(str(self.binary))
        self.target_uid = self.uid
        self.link.unlink()
        self.link.symlink_to(self.target)
        with self.assertRaisesRegex(u.updater.Refusal, 'registration changed'):
            guard.revalidate_while_leased(str(self.binary))

    def test_canonical_fragment_also_binds_registration(self):
        guard = self.guard(fragment=str(self.target))
        self.assertEqual(guard.evidence()['registration_path'], str(self.link))
        self.link.unlink()
        self.link.symlink_to(self.target)
        with self.assertRaisesRegex(u.updater.Refusal, 'registration changed'):
            guard.revalidate()

    def test_missing_standard_registration_rejected(self):
        self.link.unlink()
        with self.assertRaisesRegex(u.updater.Refusal, 'standard registration symlink required'):
            self.guard(fragment=str(self.target))

    def test_ordinary_standard_registration_rejected(self):
        self.link.unlink()
        self.link.write_text('unregistered unit')
        self.link.chmod(0o600)
        with self.assertRaisesRegex(u.updater.Refusal, 'standard registration symlink required'):
            self.guard(fragment=str(self.target))

    def test_unregistered_fragment_outside_extended_home_rejected(self):
        self.link.unlink()
        outside = self.home / 'outside.service'
        outside.write_text('unit')
        outside.chmod(0o600)
        with self.assertRaisesRegex(u.updater.Refusal, 'standard registration symlink required'):
            self.guard(fragment=str(outside))

    def test_target_inode_and_acl_late_changes_refused(self):
        guard = self.guard()
        replacement = self.unitdir / 'replacement'
        replacement.write_text('unit')
        replacement.chmod(0o600)
        os.replace(replacement, self.target)
        with self.assertRaisesRegex(u.updater.Refusal, 'fragment changed'):
            guard.revalidate_while_leased(str(self.binary))
        guard = self.guard()
        subprocess.run(['setfacl', '-m', 'u:65534:r--', str(self.target)], check=True)
        with self.assertRaisesRegex(u.updater.Refusal, 'ACL unsupported'):
            guard.revalidate()

    def test_acl_unknown_fails_closed(self):
        real = os.getxattr
        def unavailable(path, *args, **kwargs):
            if str(path) == str(self.extended):
                raise OSError(errno.ENOTSUP, 'unsupported')
            return real(path, *args, **kwargs)
        with mock.patch.object(u.updater.os, 'getxattr', side_effect=unavailable):
            with self.assertRaisesRegex(u.updater.Refusal, 'ACL inspection unavailable'):
                self.guard()

    def test_registration_target_outside_extended_home_rejected(self):
        self.link.unlink()
        self.link.symlink_to(self.binary.parent.parent.parent / 'outside')
        with self.assertRaisesRegex(u.updater.Refusal, 'registration target'):
            self.guard()


if __name__ == '__main__':
    unittest.main()
