import importlib.util
import hashlib
import json
import os
import stat
import shutil
import subprocess
import tempfile
import unittest
import zipfile
from pathlib import Path
from release_lib import extract_zip, safe_name, verify_manifest, verify_acceptance_artifact
from verify_bootstrap import verify_and_extract

class PreviewBootstrap(unittest.TestCase):
    def package(self, root, channel='candidate', version='2.0.1-rc.5', bad_hash=False):
        payload=b'#!/bin/zsh\nprint -r -- \'{"ok":true,"status":"dry_run"}\'\n'
        manifest={'schema_version':2,'source_repository':'r266-tech/wx-cli','version':version,
                  'platform_arch':'darwin-arm64','commit':'a'*40,'channel':channel,
                  'signing_mode':'adhoc','notarization_status':'not_configured',
                  'artifacts':{'install.sh':{'bytes':len(payload),'sha256':'0'*64 if bad_hash else hashlib.sha256(payload).hexdigest()}}}
        p=Path(root)/'preview.zip'
        with zipfile.ZipFile(p,'w') as z:
            entry=zipfile.ZipInfo('pkg/install.sh');entry.external_attr=(stat.S_IFREG|0o755)<<16
            z.writestr(entry,payload)
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

    def test_bootstrap_json_stdout_remains_one_document(self):
        if not shutil.which('zsh'):
            self.skipTest('zsh unavailable')
        with tempfile.TemporaryDirectory() as d:
            p=self.package(d)
            Path(str(p)+'.sha256').write_text(hashlib.sha256(p.read_bytes()).hexdigest())
            script=Path(__file__).with_name('install-release.sh').resolve()
            # Source only the local bootstrap, then replace network/platform
            # boundaries. The fixture installer never installs or reads state.
            run=subprocess.run(['zsh','-c', '''
source "$1"
fixture="$2"
uname() { if [[ "$1" == '-s' ]]; then print Darwin; else print arm64; fi }
download_file() {
  if [[ "$1" == *.sha256 ]]; then cp "$fixture.sha256" "$2"; else cp "$fixture" "$2"; fi
}
main --repo fixture/release --tag v2.0.1-rc.5 --allow-prerelease --json --dry-run
''','fixture',str(script),str(p)],text=True,capture_output=True)
            self.assertEqual(run.returncode,0,run.stderr)
            self.assertEqual(json.loads(run.stdout),{'ok':True,'status':'dry_run'})

    def test_noncanonical_and_case_aliases_rejected_before_writes(self):
        for name in ('pkg/./install.sh', 'pkg//install.sh', 'pkg/INSTALL.SH', '.'):
            with self.subTest(name=name),tempfile.TemporaryDirectory() as d:
                p=self.package(d)
                with zipfile.ZipFile(p,'a') as z:z.writestr(name,'alias')
                dest=Path(d)/'out'
                with self.assertRaises(ValueError):verify_and_extract(p,dest,'2.0.1-rc.5',True)
                self.assertFalse(dest.exists())

class ArchiveSafety(unittest.TestCase):
    def test_paths(self):
        for name in ('../escape','/absolute','x/../../a','C:/a',r'a\b','a/../b','.'):
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
    def test_case_and_directory_aliases_rejected(self):
        for names in (('pkg/file','pkg/FILE'),('pkg/file','pkg/file/'),('pkg/file','pkg/./file')):
            with self.subTest(names=names),tempfile.TemporaryDirectory() as d:
                p=Path(d)/'bad.zip'
                with zipfile.ZipFile(p,'w') as z:
                    for name in names:z.writestr(name,'fixture')
                dest=Path(d)/'out'
                with self.assertRaises(ValueError):extract_zip(p,dest)
                self.assertFalse(dest.exists())
    def test_manifest_missing_fields(self):
        with tempfile.TemporaryDirectory() as d:
            (Path(d)/'release-manifest.json').write_text('{"schema_version":2}')
            with self.assertRaises(ValueError):verify_manifest(d)
    def test_nested_manifest_is_an_artifact(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'nested').mkdir()
            artifacts={}
            for name in ('wxkey','libWCDB.dylib','nested/release-manifest.json'):
                raw=name.encode();(root/name).write_bytes(raw)
                artifacts[name]={'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
            manifest={'schema_version':2,'version':'2.0.1-rc.5','commit':'a'*40,'source_repository':'r266-tech/wx-cli',
                      'platform_arch':'darwin-arm64','build_timestamp_utc':'fixture','wxkey_version':'1.4.8','wxkey_commit':'b'*40,
                      'wcdb_version':'2.1.16','wcdb_source':'fixture','wcdb_sha256':artifacts['libWCDB.dylib']['sha256'],
                      'signing_mode':'adhoc','notarization_status':'not_configured','artifacts':artifacts,
                      'dependencies':{'wxkey':{'binary_sha256':artifacts['wxkey']['sha256']},'wcdb':{'binary_sha256':artifacts['libWCDB.dylib']['sha256']}}}
            path=root/'release-manifest.json';path.write_text(json.dumps(manifest))
            verify_manifest(root)
            del artifacts['nested/release-manifest.json'];path.write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError,'file set mismatch'):verify_manifest(root)
    def test_unknown_source(self):
        from release_lib import load_lock
        with tempfile.TemporaryDirectory() as d:
            (Path(d)/'release-dependencies.json').write_text(json.dumps({'schema_version':1,'wxkey':{'commit_sha':'1234567'},'wcdb':{}}))
            with self.assertRaises(ValueError):load_lock(d)

class AcceptanceGate(unittest.TestCase):
    def module(self):
        spec=importlib.util.spec_from_file_location('publish',Path(__file__).with_name('publish-release.py'))
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        return module

    def test_partial_receipt_rejected(self):
        for passed in (False, 'false', 1):
            with self.assertRaises(ValueError):self.module().check_receipt({'schema_version':1,'passed':passed},{},'hash')

    def test_receipt_requires_matching_platform_and_complete_typed_checks(self):
        required=('install','doctor','keychain','sessions','timeline','context','search','media','digest','archive','update','rollback','uninstall','clean_machine','release_endpoint_install')
        manifest={'version':'2.0.1','commit':'a'*40}
        for mutation in ('none','schema-type','platform','missing','extra-failed','negative-count','boolean-count','private-field','private-warning'):
            with self.subTest(mutation=mutation):
                receipt=dict(schema_version=1,passed=True,**manifest,artifact_sha256='b'*64,platform='darwin',arch='arm64',checks={k:{'status':'passed'} for k in required})
                if mutation=='schema-type':receipt['schema_version']=True
                elif mutation=='platform':receipt['arch']='amd64'
                elif mutation=='missing':del receipt['checks']['archive']
                elif mutation=='extra-failed':receipt['checks']['other']={'status':'failed'}
                elif mutation=='negative-count':receipt['checks']['sessions']['count']=-1
                elif mutation=='boolean-count':receipt['checks']['sessions']['count']=True
                elif mutation=='private-field':receipt['checks']['sessions']['chat']='private text'
                elif mutation=='private-warning':receipt['checks']['sessions']['warning_codes']=['/Users/example/private/chat text']
                if mutation=='none':self.module().check_receipt(receipt,manifest,'b'*64)
                else:
                    with self.assertRaises(ValueError):self.module().check_receipt(receipt,manifest,'b'*64)

    def assets(self, root):
        root=Path(root);pkg=root/'extract';(pkg/'scripts').mkdir(parents=True)
        archive=root/'wechat-cli-v2.0.1-darwin-arm64.zip'
        archive.write_bytes(b'accepted ZIP')
        (root/'wechat-cli-latest-darwin-arm64.zip').write_bytes(archive.read_bytes())
        for name in ('install-release.sh','install-release.ps1','release-manifest.json'):
            p=pkg/name if name=='release-manifest.json' else pkg/'scripts'/name
            p.write_text('accepted '+name);(root/name).write_bytes(p.read_bytes())
        (root/'acceptance-receipt.json').write_text('{}')
        for name in (archive.name,'wechat-cli-latest-darwin-arm64.zip','install-release.sh','install-release.ps1'):
            (root/(name+'.sha256')).write_text(hashlib.sha256((root/name).read_bytes()).hexdigest())
        return pkg,archive

    def test_promotion_rechecks_every_download(self):
        for mutation in ('none','missing','extra','bootstrap','latest','manifest','checksum'):
            with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as d:
                pkg,archive=self.assets(d);root=Path(d)
                if mutation=='missing':(root/'install-release.ps1').unlink()
                elif mutation=='extra':(root/'unexpected.exe').write_bytes(b'other')
                elif mutation=='checksum':(root/(archive.name+'.sha256')).write_text('0'*64)
                elif mutation in ('bootstrap','latest','manifest'):
                    name={'bootstrap':'install-release.sh','latest':'wechat-cli-latest-darwin-arm64.zip','manifest':'release-manifest.json'}[mutation]
                    (root/name).write_bytes(b'changed after acceptance')
                    if mutation!='manifest':(root/(name+'.sha256')).write_text(hashlib.sha256((root/name).read_bytes()).hexdigest())
                if mutation=='none':self.module().check_promotion_assets(root,pkg,archive)
                else:
                    with self.assertRaises(ValueError):self.module().check_promotion_assets(root,pkg,archive)


class WindowsManifest(unittest.TestCase):
    def test_records_only_actual_windows_components_and_pinned_evidence(self):
        from windows_manifest import write_manifest
        from release_lib import load_lock
        dep=load_lock(Path(__file__).resolve().parents[1])['wcdb']
        for mutation in ('none','source','binary','missing'):
            with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as d:
                root=Path(d)
                for name in ('wechat-cli.exe','libWCDB.dll','install.ps1'):(root/name).write_text(name)
                evidence=dict(dep,build_platform='windows-amd64',build_flags=['fixture'],binary_sha256=hashlib.sha256((root/'libWCDB.dll').read_bytes()).hexdigest())
                if mutation=='source':evidence['archive_sha256']='0'*64
                elif mutation=='binary':(root/'libWCDB.dll').write_bytes(b'tampered')
                elif mutation=='missing':(root/'install.ps1').unlink()
                if mutation=='none':
                    manifest=write_manifest(root,'2.0.1-rc.5','a'*40,dep,evidence,0,'fixture')
                    self.assertEqual(manifest['platform_arch'],'windows-amd64')
                    self.assertEqual(manifest['channel'],'candidate')
                    self.assertEqual(set(manifest['dependencies']),{'wcdb'})
                    self.assertTrue((root/'release-manifest.json').is_file())
                else:
                    with self.assertRaises(ValueError):write_manifest(root,'2.0.1-rc.5','a'*40,dep,evidence,0,'fixture')
                    self.assertFalse((root/'release-manifest.json').exists())


class AcceptanceIdentity(unittest.TestCase):
    def package(self, directory):
        root=Path(directory)/'pkg';root.mkdir();artifacts={}
        for name in ('wechat-cli','wxkey','libWCDB.dylib','install.sh'):
            raw=name.encode();(root/name).write_bytes(raw)
            artifacts[name]={'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
        manifest={'schema_version':2,'version':'2.0.1-rc.5','commit':'a'*40,'source_repository':'r266-tech/wx-cli',
                  'platform_arch':'darwin-arm64','build_timestamp_utc':'fixture','wxkey_version':'1.4.8','wxkey_commit':'b'*40,
                  'wcdb_version':'2.1.16','wcdb_source':'fixture','wcdb_sha256':artifacts['libWCDB.dylib']['sha256'],
                  'signing_mode':'adhoc','notarization_status':'not_configured','artifacts':artifacts,
                  'dependencies':{'wxkey':{'binary_sha256':artifacts['wxkey']['sha256']},'wcdb':{'binary_sha256':artifacts['libWCDB.dylib']['sha256']}}}
        (root/'release-manifest.json').write_text(json.dumps(manifest))
        archive=Path(directory)/'pkg.zip'
        with zipfile.ZipFile(archive,'w') as z:
            for p in root.iterdir():z.write(p,'pkg/'+p.name)
        receipt={'version':manifest['version'],'commit':manifest['commit'],'artifact_sha256':hashlib.sha256(archive.read_bytes()).hexdigest()}
        return root,archive,manifest,receipt

    def test_exact_archive_binary_package_and_receipt_binding(self):
        for mutation in ('none','binary','self-consistent-package','receipt-hash','receipt-commit'):
            with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as d:
                root,archive,manifest,receipt=self.package(d)
                if mutation=='binary':(root/'wechat-cli').write_bytes(b'different build with same version')
                elif mutation=='self-consistent-package':
                    raw=b'different installer';(root/'install.sh').write_bytes(raw)
                    manifest['artifacts']['install.sh']={'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
                    (root/'release-manifest.json').write_text(json.dumps(manifest))
                elif mutation=='receipt-hash':receipt['artifact_sha256']='0'*64
                elif mutation=='receipt-commit':receipt['commit']='0'*40
                if mutation=='none':verify_acceptance_artifact(archive,binary=root/'wechat-cli',package=root,receipt=receipt)
                else:
                    with self.assertRaises(ValueError):verify_acceptance_artifact(archive,binary=root/'wechat-cli',package=root,receipt=receipt)


@unittest.skipUnless(shutil.which('zsh'), 'zsh unavailable')
class MacInstallerSafety(unittest.TestCase):
    def test_worktree_update_is_recognized_without_mutating_it(self):
        with tempfile.TemporaryDirectory() as d:
            (Path(d)/'.git').write_text('gitdir: fixture')
            script=Path(__file__).resolve().parents[1]/'install.sh'
            run=subprocess.run(['zsh','-c','''
source "$1"
SOURCE_DIR="$2"; DRY_RUN=1
update_source
[[ "${ACTIONS[1]}" == "git pull --ff-only in $SOURCE_DIR" && "${#WARNINGS[@]}" == 0 ]]
''','fixture',str(script),d],text=True,capture_output=True)
            self.assertEqual(run.returncode,0,run.stderr)
            self.assertEqual((Path(d)/'.git').read_text(),'gitdir: fixture')

    def test_failed_component_replacement_restores_previous_install(self):
        for failure in ('staging','commit','none'):
            with self.subTest(failure=failure),tempfile.TemporaryDirectory() as d:
                root=Path(d);package=root/'package';install=root/'installed';package.mkdir();install.mkdir()
                names=('wechat-cli','wxkey','libWCDB.dylib','release-manifest.json','.wechat-cli-install')
                for name in names:
                    (install/name).write_text('old '+name)
                    if name!='.wechat-cli-install':
                        (package/name).write_text('new '+name);(package/name).chmod(0o755)
                script=Path(__file__).resolve().parents[1]/'install.sh'
                run=subprocess.run(['zsh','-c','''
source "$1"
SOURCE_DIR="$2"; INSTALL_DIR="$3"; JSON=1; failure="$4"
sign_macho_ad_hoc() { return 0 }
functions[original_atomic_install_path]=$functions[atomic_install_path]
atomic_install_path() {
  if [[ "$failure" == staging && "$2" == */new/wxkey ]]; then die 'injected staging failure'; fi
  original_atomic_install_path "$@"
}
mv() {
  if [[ "$failure" == commit && "$2" == */new/wxkey ]]; then return 1; fi
  command mv "$@"
}
install_components
''','fixture',str(script),str(package),str(install),failure],text=True,capture_output=True)
                if failure=='none':
                    self.assertEqual(run.returncode,0,run.stderr)
                    for name in names[:-1]:self.assertEqual((install/name).read_text(),'new '+name)
                else:
                    self.assertNotEqual(run.returncode,0)
                    for name in names:self.assertEqual((install/name).read_text(),'old '+name)
                self.assertFalse(list(root.glob('.wechat-cli-stage.*')))

    def test_doctor_dry_run_does_not_create_log_or_invoke_cli(self):
        with tempfile.TemporaryDirectory() as d:
            home=Path(d);install=home/'install';install.mkdir()
            marker=home/'called';cli=install/'wechat-cli'
            cli.write_text('#!/bin/sh\ntouch "$HOME/called"\n');cli.chmod(0o755)
            env=os.environ.copy();env.update(HOME=str(home),WECHAT_CLI_INSTALL_DIR=str(install),WECHAT_CLI_LOG_DIR=str(home/'logs'))
            script=Path(__file__).resolve().parents[1]/'install.sh'
            run=subprocess.run(['zsh',str(script),'--doctor','--dry-run','--json'],env=env,text=True,capture_output=True)
            self.assertEqual(run.returncode,0,run.stderr)
            self.assertIn('cache_status_skipped=dry_run',json.loads(run.stdout)['checks'])
            self.assertFalse(marker.exists());self.assertFalse((home/'logs').exists())

    def test_uninstall_matches_executable_boundary(self):
        script=Path(__file__).resolve().parents[1]/'install.sh'
        run=subprocess.run(['zsh','-c','''
source "$1"
INSTALL_DIR='/tmp/fixture install'; LEGACY_INSTALL_DIR='/tmp/legacy'
is_installed_cli_command '/tmp/fixture install/wechat-cli' || exit 1
is_installed_cli_command '/tmp/fixture install/wechat-cli sessions' || exit 2
is_installed_cli_command '/tmp/legacy/wx-mcp --help' || exit 3
is_installed_cli_command '/tmp/fixture install/wechat-cli-helper' && exit 4
is_installed_cli_command '/tmp/fixture install/wechat-cli-other --flag' && exit 5
is_installed_cli_command '/tmp/fixture install-other/wechat-cli' && exit 6
exit 0
''','fixture',str(script)],text=True,capture_output=True)
        self.assertEqual(run.returncode,0,run.stderr)

if __name__=='__main__':unittest.main()
