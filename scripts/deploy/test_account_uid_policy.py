import copy
import errno
import io
import os
import unittest
from types import SimpleNamespace
from unittest import mock

import test_multica_update as existing

u = existing.u
v = u.updater


class AccountUidPolicyTests(unittest.TestCase):
    def setUp(self):
        self.owner = 1001
        self.guard = SimpleNamespace(uid=self.owner, unverified_process_images=[], process_managed_inodes=set())
        self.proc = SimpleNamespace(st_dev=1, st_ino=91, st_uid=0)
        self.image = SimpleNamespace(st_dev=2, st_ino=10)
        self.target = SimpleNamespace(st_dev=2, st_ino=10)
        self.uids = (1001, 0, 0, 0)
        self.denied = None
        self.path = '/custom/program'
        self.identities = None

    def run_inventory(self, fn, scope=True):
        def identity(proc, info):
            if self.identities is not None:
                return next(self.identities)
            return (1, 91, 0, self.uids, '123')
        def stat(path):
            if path == '/proc/91': return self.proc
            if path == '/target': return self.target
            if path == '/proc/91/exe':
                if self.denied == 'stat': raise PermissionError(errno.EACCES, 'denied')
                return self.image
            raise AssertionError(path)
        def readlink(path):
            if self.denied == 'readlink': raise PermissionError(errno.EACCES, 'denied')
            return self.path
        with mock.patch.object(v, 'require_visible_proc'), mock.patch.object(v, '_account_guard'), \
             mock.patch.object(v, '_process_account_identity', side_effect=identity), \
             mock.patch.object(v.os, 'listdir', return_value=['91']), \
             mock.patch.object(v.os, 'stat', side_effect=stat), \
             mock.patch.object(v.os, 'readlink', side_effect=readlink):
            return fn(self.guard if scope else None)

    def test_uid_matrix_and_unverified_stat(self):
        self.denied = 'stat'
        for uids, permitted in [((1001, 0, 0, 0), True), ((1001, 1001, 1001, 1001), False),
                                ((1001, 0, 1001, 0), False), ((1001, 0, 0, 1001), False),
                                ((1001, 0, 2, 0), False), ((0, 0, 1001, 0), False)]:
            with self.subTest(uids=uids):
                self.uids = uids
                if permitted:
                    self.assertEqual(self.run_inventory(v.process_inventory), [])
                    item = self.guard.unverified_process_images[-1]
                    self.assertEqual(item['uids'], list(uids))
                    self.assertIn('unverified', item['status'])
                else:
                    with self.assertRaises(v.Refusal): self.run_inventory(v.process_inventory)
        self.uids = (1001, 0, 0, 0)
        with self.assertRaises(v.Refusal): self.run_inventory(v.process_inventory, scope=False)

    def test_readlink_denial_and_name_independence(self):
        self.denied = 'readlink'
        for name in ('sudo', 'multica', 'unrelated'):
            self.path = '/bin/' + name
            self.assertEqual(self.run_inventory(v.process_inventory), [])
        self.assertEqual(self.guard.unverified_process_images[0]['operation'], 'readlink')
        self.assertEqual(self.guard.unverified_process_images[0]['observed_image_inode'], [2, 10])
        with self.assertRaisesRegex(v.Refusal, 'shared'):
            self.run_inventory(lambda guard: v.executable_users('/target', guard))

    def test_denied_stat_in_executable_users_records_unverified(self):
        self.denied = 'stat'
        self.assertEqual(self.run_inventory(lambda guard: v.executable_users('/target', guard)), [])
        self.assertIn('unverified', self.guard.unverified_process_images[0]['status'])

    def test_readable_inode_before_denial_is_checked_in_both_orders(self):
        self.run_inventory(lambda guard: v.executable_users('/target', guard))
        self.denied = 'readlink'
        with self.assertRaisesRegex(v.Refusal, 'shared'):
            self.run_inventory(v.process_inventory)

    def test_only_eacces_is_tolerated(self):
        ident = (1, 91, 0, self.uids, '123')
        self.assertFalse(v._account_unverified(self.guard, '/proc/91', ident,
                                              PermissionError(errno.EPERM, 'denied'), 'stat'))

    def test_identity_drift_during_access_refuses(self):
        self.denied = 'stat'
        base = (1, 91, 0, self.uids, '123')
        for changed in [(1, 91, 0, (1001, 0, 1, 0), '123'), (1, 92, 0, self.uids, '123'),
                        (1, 91, 1, self.uids, '123'), (1, 91, 0, self.uids, '124')]:
            self.identities = iter([base, base, changed])
            with self.assertRaisesRegex(v.Refusal, 'identity changed'): self.run_inventory(v.process_inventory)

    def test_readable_managed_app_deleted_and_drift(self):
        users = self.run_inventory(lambda guard: v.executable_users('/target', guard))
        self.assertEqual(users, [{'pid': 91, 'uid': 0}])
        with self.assertRaises(v.Refusal):
            self.run_inventory(lambda guard: v.require_exclusive_executable('/target', 90, 1001, guard))
        self.path = '/app/Multica.AppImage'
        with self.assertRaisesRegex(v.Refusal, 'desktop application running'):
            self.run_inventory(v.require_no_running_app)
        self.path = '/app/program (deleted)'
        with self.assertRaisesRegex(v.Refusal, 'deleted'): self.run_inventory(v.process_inventory)
        self.path = '/app/program'
        images = iter([self.image, SimpleNamespace(st_dev=2, st_ino=11)])
        def access(proc, identity, operation):
            return next(images) if operation is os.stat else self.path
        with mock.patch.object(v, '_account_image_access', side_effect=access):
            with self.assertRaisesRegex(v.Refusal, 'executable changed'): self.run_inventory(v.process_inventory)

    def test_missing_uid_field_refuses(self):
        def fixture(path, *args, **kwargs):
            return io.StringIO('Uid:\t1001\t0\t0\n')
        with mock.patch('builtins.open', side_effect=fixture):
            with self.assertRaisesRegex(v.Refusal, 'invalid process UID'): v._process_account_identity('/proc/91', self.proc)



class AccountPlanEvidenceTests(unittest.TestCase):
    def test_build_plan_binds_unverified_evidence_and_reconstructs_scope(self):
        # Reuse the real account transaction fixture, including its private
        # files and synthetic systemd/health. Interpose only the proc scan.
        real_build = u.build_plan
        real_inventory = v.process_inventory
        calls = []
        def build(config, manifest, *args, **kwargs):
            def processes(uid, guard):
                proc = SimpleNamespace(st_dev=1, st_ino=91, st_uid=uid + 1)
                identity = (1, 91, uid + 1, (uid, uid + 1, uid + 1, uid + 1), '123')
                original_stat, original_listdir = os.stat, os.listdir
                def stat(path, *a, **kw):
                    if path == '/proc/91': return proc
                    if path == '/proc/91/exe': raise PermissionError(errno.EACCES, 'denied')
                    return original_stat(path, *a, **kw)
                with mock.patch.object(v, 'require_visible_proc'), \
                     mock.patch.object(v, '_process_account_identity', return_value=identity), \
                     mock.patch.object(v, 'process_inventory', side_effect=real_inventory), \
                     mock.patch.object(os, 'stat', side_effect=stat), \
                     mock.patch.object(os, 'listdir', side_effect=lambda path: ['91'] if path == '/proc' else original_listdir(path)):
                    self.assertEqual(real_inventory(guard), [])
                return [{'pid': fixture_pid[0], 'uid': uid, 'exe': str(fixture.binary), 'app': False}]
            # The enclosing fixture supplies the current daemon PID in health.
            fixture_pid[0] = v.Updater.health(None)['pid']
            with mock.patch.object(u, 'process_inventory', side_effect=processes):
                plan = real_build(config, manifest, *args, **kwargs)
            evidence = plan['account_scope_evidence']
            self.assertEqual(evidence['process_uid_policy'], 'ruid-owner-esfs-same-foreign-eacces-unverified-v1')
            observations = plan['unverified_process_images']
            self.assertEqual(len(observations), 1)
            self.assertEqual(observations[0]['uids'], [config['owner_uid'], *([config['owner_uid'] + 1] * 3)])
            self.assertEqual(observations[0]['operation'], 'stat')
            self.assertIn('unverified', observations[0]['status'])
            digest = u.fingerprint(plan)
            changed = copy.deepcopy(plan)
            changed['unverified_process_images'][0]['pid'] += 1
            self.assertNotEqual(digest, u.fingerprint(changed))
            changed = copy.deepcopy(plan)
            changed['account_scope_evidence']['process_uid_policy'] = 'different'
            self.assertNotEqual(digest, u.fingerprint(changed))
            self.assertEqual(u.account_guard_from_evidence(evidence, config).evidence(), evidence)
            with self.assertRaisesRegex(v.Refusal, 'scope evidence changed'):
                u.account_guard_from_evidence(changed['account_scope_evidence'], config)
            calls.append(True)
            return plan
        fixture = existing.AccountScopeTests('test_account_apply_accept_real_transaction_with_synthetic_services')
        fixture_pid = [0]
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        with mock.patch.object(u, 'build_plan', side_effect=build):
            fixture.test_account_apply_accept_real_transaction_with_synthetic_services()
        self.assertTrue(calls, 'fixture never invoked the real build_plan')


if __name__ == '__main__': unittest.main()
