"""Package from a clean source revision; never replace an existing version asset."""
import argparse
import json
import os
import platform
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path
from release_lib import git, sha256, verify_manifest, load_lock

ROOT=Path(__file__).resolve().parents[1]

def run(argv, **kw):
    subprocess.run(argv,check=True,**kw)

def main():
    ap=argparse.ArgumentParser();ap.add_argument('version');ap.add_argument('--candidate',action='store_true');ap.add_argument('--inputs',default=str(ROOT/'.build/inputs.json'))
    a=ap.parse_args()
    text=(ROOT/'cmd/wechat-cli/product.go').read_text()
    import re
    source_version=re.search(r'appVersion\s*=\s*"([^"]+)"',text).group(1)
    if source_version!=a.version:raise ValueError('package version does not match appVersion')
    if (platform.system(),platform.machine())!=('Darwin','arm64'):raise ValueError('macOS arm64 required')
    if git(ROOT,'status','--porcelain','--untracked-files=normal'):raise ValueError('release requires clean worktree')
    commit=git(ROOT,'rev-parse','HEAD')
    if not a.candidate and git(ROOT,'rev-parse',f'v{a.version}^{{commit}}')!=commit:raise ValueError('tag does not identify source')
    evidence=json.loads(Path(a.inputs).read_text());lock=load_lock(ROOT)
    wx=Path(evidence['wxkey_source']);lib=Path(evidence['wcdb_library'])
    if git(wx,'rev-parse','HEAD')!=lock['wxkey']['commit_sha'] or git(wx,'status','--porcelain'):
        raise ValueError('wxkey source identity mismatch')
    if sha256(lib)!=evidence['dependencies']['wcdb']['binary_sha256']:raise ValueError('WCDB hash mismatch')
    mode=os.environ.get('WECHAT_CLI_SIGNING_MODE','adhoc')
    if mode not in ('adhoc','developer_id'):raise ValueError('unknown signing mode')
    identity='-' if mode=='adhoc' else os.environ['WECHAT_CLI_SIGNING_IDENTITY']
    if not a.candidate and mode!='developer_id':raise ValueError('stable requires Developer ID; use --candidate for local rehearsal')
    out=ROOT/'dist';out.mkdir(exist_ok=True)
    name=f'wechat-cli-v{a.version}-darwin-arm64';archive=out/(name+'.zip')
    if archive.exists():raise ValueError('version archive already exists; refusing overwrite')
    stage=Path(tempfile.mkdtemp(prefix='.stage-',dir=out));pkg=stage/name;pkg.mkdir()
    env=os.environ.copy();env.update(CGO_ENABLED='1',GOOS='darwin',GOARCH='arm64')
    run(['go','build','-trimpath','-ldflags',f'-s -w -X main.sourceCommit={commit}','-o',str(pkg/'wechat-cli'),'./cmd/wechat-cli'],cwd=ROOT,env=env)
    run(['go','build','-trimpath','-ldflags','-s -w','-o',str(pkg/'wxkey'),'./cmd/wxkey'],cwd=wx,env=env)
    shutil.copy2(lib,pkg/'libWCDB.dylib')
    for filename in ('wechat-cli','wxkey','libWCDB.dylib'):
        args=['codesign','--force','--sign',identity]
        if mode=='developer_id': args+=['--options','runtime','--timestamp']
        run([*args,str(pkg/filename)])
        run(['codesign','--verify','--strict',str(pkg/filename)])
    version_doc=json.loads(subprocess.check_output([str(pkg/'wechat-cli'),'--version']))
    if version_doc['data']['version']!=a.version or version_doc['data']['commit']!=commit:raise ValueError('binary identity mismatch')
    for f in ('README.md','AGENTS.md','llms.txt','LICENSE','SECURITY.md','THIRD_PARTY_NOTICES.md','install.sh','release-dependencies.json'):
        shutil.copy2(ROOT/f,pkg/f)
    (pkg/'scripts').mkdir()
    for f in ('install-release.sh','install-release.ps1','release_lib.py','verify-release.py'):
        shutil.copy2(ROOT/'scripts'/f,pkg/'scripts'/f)
    notices=pkg/'licenses';notices.mkdir()
    shutil.copy2(wx/'LICENSE',notices/'wxkey-LICENSE')
    upstream=Path(evidence['wcdb_source_dir'])
    for path in (upstream/'LICENSE',upstream/'sqlcipher/LICENSE'):
        if not path.is_file():raise ValueError('upstream license missing')
        shutil.copy2(path,notices/('WCDB-LICENSE' if path.parent==upstream else 'SQLCipher-LICENSE'))
    env['WECHAT_CLI_BUILD_INPUTS']=str(Path(a.inputs).resolve());env['WECHAT_CLI_SIGNING_MODE']=mode
    env['WECHAT_CLI_RELEASE_CHANNEL']='candidate' if a.candidate else 'stable'
    env['WECHAT_CLI_NOTARIZATION_STATUS']='not_configured'
    if not a.candidate:
        # Notarize the exact executable bytes before creating the manifest.
        submit=stage/'notarize.zip'
        with zipfile.ZipFile(submit,'w',zipfile.ZIP_DEFLATED) as z:
            for f in ('wechat-cli','wxkey','libWCDB.dylib'):z.write(pkg/f,name+'/'+f)
        response=json.loads(subprocess.check_output(['xcrun','notarytool','submit',str(submit),'--keychain-profile',os.environ['WECHAT_CLI_NOTARY_PROFILE'],'--wait','--output-format','json']))
        if response.get('status')!='Accepted':raise ValueError('notarization not accepted')
        env['WECHAT_CLI_NOTARIZATION_STATUS']='accepted'
    run(['python3',str(ROOT/'scripts/release-manifest.py'),a.version,'darwin-arm64',str(pkg)],env=env)
    verify_manifest(pkg,a.version,commit)
    with zipfile.ZipFile(archive,'x',zipfile.ZIP_DEFLATED) as z:
        for p in sorted(pkg.rglob('*')):
            if p.is_file():z.write(p,p.relative_to(stage).as_posix())
    (out/(archive.name+'.sha256')).write_text(sha256(archive)+'  '+archive.name+'\n')
    # Candidate directory is preserved for local install tests. No latest mutation here.
    pkg.rename(out/name)
    print(json.dumps({'ok':True,'archive':str(archive),'commit':commit,'channel':env['WECHAT_CLI_RELEASE_CHANNEL']}))

if __name__=='__main__':main()
