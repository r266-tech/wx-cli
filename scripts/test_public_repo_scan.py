import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('public_repo_scan', Path(__file__).with_name('public_repo_scan.py'))
public_repo_scan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(public_repo_scan)


class PublicRepoScan(unittest.TestCase):
    def test_deleted_tracked_files_are_skipped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'LICENSE').write_text('MIT License\n')
            tracked = root / 'tracked.txt'
            tracked.write_text('fixture\n')
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            subprocess.run(['git', '-C', str(root), 'config', 'user.name', 'Fixture'], check=True)
            subprocess.run(['git', '-C', str(root), 'config', 'user.email', 'fixture@example.invalid'], check=True)
            subprocess.run(['git', '-C', str(root), 'add', 'LICENSE', 'tracked.txt'], check=True)
            subprocess.run(['git', '-C', str(root), 'commit', '-qm', 'fixture'], check=True)
            tracked.unlink()

            self.assertEqual([], public_repo_scan.scan(root))


if __name__ == '__main__':
    unittest.main()
