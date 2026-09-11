import importlib.util
import json
import stat
import tempfile
import unittest
import zipfile
from pathlib import Path
from release_lib import extract_zip, safe_name, verify_manifest

class ArchiveSafety(unittest.TestCase):
    def test_paths(self):
        for name in ('../escape','/absolute','x/../../a','C:/a',r'a\b','a/../b'):
            with self.subTest(name=name), self.assertRaises(ValueError):safe_name(name)
    def test_symlink_and_budget(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'bad.zip'
            with zipfile.ZipFile(p,'w') as z:
                i=zipfile.ZipInfo('link');i.external_attr=(stat.S_IFLNK|0o777)<<16;z.writestr(i,'../escape')
            with self.assertRaises(ValueError):extract_zip(p,Path(d)/'out')
            self.assertFalse((Path(d)/'out').exists())
    def test_duplicate(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'bad.zip'
            with zipfile.ZipFile(p,'w') as z:z.writestr('a','1');z.writestr('a','2')
            with self.assertRaises(ValueError):extract_zip(p,Path(d)/'out')
    def test_manifest_missing_fields(self):
        with tempfile.TemporaryDirectory() as d:
            (Path(d)/'release-manifest.json').write_text('{"schema_version":2}')
            with self.assertRaises(ValueError):verify_manifest(d)
    def test_unknown_source(self):
        from release_lib import load_lock
        with tempfile.TemporaryDirectory() as d:
            (Path(d)/'release-dependencies.json').write_text(json.dumps({'schema_version':1,'wxkey':{'commit_sha':'1234567'},'wcdb':{}}))
            with self.assertRaises(ValueError):load_lock(d)

class AcceptanceGate(unittest.TestCase):
    def test_partial_receipt_rejected(self):
        spec=importlib.util.spec_from_file_location('publish',Path(__file__).with_name('publish-release.py'))
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        with self.assertRaises(ValueError):module.check_receipt({'schema_version':1,'passed':False},{},'hash')

if __name__=='__main__':unittest.main()
