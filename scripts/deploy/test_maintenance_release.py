"""Regression for the maintenance gate's irreversible release boundary."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('updater', Path(__file__).with_name('update-daemon.py'))
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)

class ReleaseBoundaryTest(unittest.TestCase):
    def test_failed_directory_sync_reports_claims_may_resume(self):
        with tempfile.TemporaryDirectory() as root:
            u = updater.Updater(types.SimpleNamespace())
            u.gate = str(Path(root) / 'daemon.maintenance')
            os.mkdir(u.gate)
            st = os.stat(u.gate)
            u.gate_inode = (st.st_dev, st.st_ino)
            (Path(u.gate) / 'owner').write_text(json.dumps({'id': u.owner, 'pid': os.getpid()}))
            u.result['status'] = 'updated'
            with patch.object(updater, 'fsync_dir', side_effect=OSError('fixture sync failure')):
                with self.assertRaises(OSError):
                    u.remove_gate()
            self.assertFalse(Path(u.gate).exists())
            self.assertTrue(u.gate_released)
            u.report_released_error()
            self.assertEqual(u.result['status'], 'updated_gate_released_error')
            self.assertFalse(u.result['gate_present'])
            self.assertTrue(u.result['claims_may_resume'])

if __name__ == '__main__':
    unittest.main()
