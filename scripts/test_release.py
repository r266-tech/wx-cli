import importlib.util
import hashlib
import json
import stat
import tempfile
import unittest
import zipfile
from pathlib import Path
from release_lib import extract_zip, safe_name, verify_manifest
from verify_bootstrap import verify_and_extract

class PreviewBootstrap(unittest.TestCase):
    def package(self, root, channel='candidate', version='2.0.1-rc.5', bad_hash=False):
        payload=b'#!/bin/zsh\nexit 0\n'
        manifest={'schema_version':2,'source_repository':'r266-tech/wx-cli','version':version,
                  'platform_arch':'darwin-arm64','commit':'a'*40,'channel':channel,
                  'signing_mode':'adhoc','notarization_status':'not_configured',
                  'artifacts':{'install.sh':{'bytes':len(payload),'sha256':'0'*64 if bad_hash else hashlib.sha256(payload).hexdigest()}}}
        p=Path(root)/'preview.zip'
        with zipfile.ZipFile(p,'w') as z:
            z.writestr('pkg/install.sh',payload)
            z.writestr('pkg/release-manifest.json',json.dumps(manifest))
        return p

    def test_preview_requires_opt_in(self):
        with tempfile.TemporaryDirectory() as d:
            p=self.package(d);dest=Path(d)/'out'
            with self.assertRaises(ValueError):verify_and_extract(p,dest,'2.0.1-rc.5')
            self.assertFalse(dest.exists())
            self.assertTrue((verify_and_extract(p,dest,'2.0.1-rc.5',True)/'install.sh').is_file())

    def test_preview_cannot_skip_hash_or_identity(self):
        for kw,version in (({'bad_hash':True},'2.0.1-rc.5'),({},'2.0.1-rc.6'),({'channel':'stable'},'2.0.1-rc.5'),({'version':'2.0.1'},'2.0.1')):
            with self.subTest(kw=kw),tempfile.TemporaryDirectory() as d:
                p=self.package(d,**kw);dest=Path(d)/'out'
                with self.assertRaises(ValueError):verify_and_extract(p,dest,version,True)
                self.assertFalse(dest.exists())

    def test_embedded_verifier_matches_source(self):
        scripts=Path(__file__).parent
        embedded=(scripts/'install-release.sh').read_text().split("<<'VERIFY_RELEASE_PY'\n",1)[1].split('\nVERIFY_RELEASE_PY',1)[0]
        self.assertEqual(embedded,(scripts/'verify_bootstrap.py').read_text().rstrip())

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
