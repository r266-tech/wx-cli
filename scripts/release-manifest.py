#!/usr/bin/env python3
"""Generate provenance from the actual staged bytes, after signing."""
import datetime
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from release_lib import SOURCE_REPO, git, load_lock, sha256, verify_manifest


def main():
    version, platform, directory = sys.argv[1:]
    source=Path(__file__).resolve().parents[1];root=Path(directory).resolve()
    lock=load_lock(source)
    evidence=json.loads(Path(os.environ['WECHAT_CLI_BUILD_INPUTS']).read_text())
    if {k:evidence['dependencies']['wxkey'][k] for k in lock['wxkey']} != lock['wxkey']:
        raise ValueError('wxkey inputs differ from lock')
    for k,v in lock['wcdb'].items():
        if evidence['dependencies']['wcdb'][k]!=v: raise ValueError('WCDB inputs differ from lock')
    if sha256(evidence['wcdb_library'])!=evidence['dependencies']['wcdb']['binary_sha256']:
        raise ValueError('WCDB input changed')
    commit=git(source,'rev-parse','HEAD')
    mode=os.environ['WECHAT_CLI_SIGNING_MODE']
    if mode not in ('adhoc','developer_id'): raise ValueError('unknown signing mode')
    channel=os.environ.get('WECHAT_CLI_RELEASE_CHANNEL','candidate')
    notarized=os.environ.get('WECHAT_CLI_NOTARIZATION_STATUS','not_configured')
    if channel=='stable' and (mode!='developer_id' or notarized!='accepted'):
        raise ValueError('stable requires Developer ID and accepted notarization')
    artifacts={}
    for p in sorted(root.rglob('*')):
        if p.is_symlink(): raise ValueError('packaged symlink')
        if p.is_file() and p.name!='release-manifest.json':
            artifacts[p.relative_to(root).as_posix()]={'bytes':p.stat().st_size,'sha256':sha256(p)}
    for dep,filename in [('wxkey','wxkey'),('wcdb','libWCDB.dylib')]:
        lock[dep]['binary_sha256']=artifacts[filename]['sha256']
    stamp=int(os.environ.get('SOURCE_DATE_EPOCH',git(source,'show','-s','--format=%ct','HEAD')))
    d={'schema_version':2,'product':'wechat-cli','source_repository':SOURCE_REPO,'module_path':'github.com/r266-tech/wx-cli/v2','version':version,'commit':commit,'platform_arch':platform,'channel':channel,'build_timestamp_utc':datetime.datetime.fromtimestamp(stamp,datetime.timezone.utc).isoformat(),'go_version':subprocess.check_output(['go','version'],text=True).strip(),'signing_mode':mode,'notarization_status':notarized,'wxkey_version':lock['wxkey']['tag'],'wxkey_commit':lock['wxkey']['commit_sha'],'wcdb_version':lock['wcdb']['version'],'wcdb_source':lock['wcdb']['source_url'],'wcdb_sha256':artifacts['libWCDB.dylib']['sha256'],'dependencies':lock,'artifacts':artifacts}
    (root/'release-manifest.json').write_text(json.dumps(d,indent=2)+'\n')
    verify_manifest(root,version,commit)

if __name__=='__main__':main()
