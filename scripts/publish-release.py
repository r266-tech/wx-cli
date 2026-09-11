#!/usr/bin/env python3
"""Only GitHub App tokens may publish; promotion is a separate fail-closed gate."""
import argparse
import json
import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path
from release_lib import RELEASE_REPO, SOURCE_REPO, extract_zip, sha256, verify_manifest


def gh(*args):
    return subprocess.check_output(['gh',*args],text=True).strip()


def check_receipt(d, manifest, archive_hash):
    if d.get('schema_version')!=1 or not d.get('passed'):
        raise ValueError('acceptance receipt did not pass')
    if (d.get('version'),d.get('commit'),d.get('artifact_sha256'))!=(manifest['version'],manifest['commit'],archive_hash):
        raise ValueError('receipt does not identify these exact artifact bytes')
    required={'install','doctor','keychain','sessions','timeline','context','search','media','digest','archive','update','rollback','uninstall'}
    checks=d.get('checks',{})
    if not required<=checks.keys() or any(checks[k].get('status')!='passed' for k in required):
        raise ValueError('acceptance is incomplete')
    # This receipt is uploaded; reject non-allowlisted fields instead of redacting after upload.
    if set(d)-{'schema_version','passed','version','commit','artifact_sha256','platform','arch','checks'}:
        raise ValueError('receipt has non-public fields')
    for c in checks.values():
        if set(c)-{'status','count','duration_ms','warning_codes'}:raise ValueError('unsafe receipt fields')


def main():
    ap=argparse.ArgumentParser();ap.add_argument('--version',required=True);ap.add_argument('--directory');ap.add_argument('--promote',action='store_true');ap.add_argument('--commit')
    a=ap.parse_args()
    if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?',a.version):raise ValueError('invalid version')
    token=os.environ.get('GH_TOKEN','')
    if not token.startswith('ghs_'):raise ValueError('GitHub App installation token required; no PAT or OAuth fallback')
    tag='v'+a.version;name='wechat-cli-'+tag+'-darwin-arm64.zip'
    temp=Path(tempfile.mkdtemp(prefix='wx-release-verify-'))
    if a.promote:
        release=json.loads(gh('release','view',tag,'--repo',RELEASE_REPO,'--json','assets,isDraft'))
        if not release['isDraft']:raise ValueError('release is already public; no mutation allowed')
        gh('release','download',tag,'--repo',RELEASE_REPO,'--dir',str(temp))
        archive=temp/name
    else:
        archive=Path(a.directory)/name
    expected=Path(str(archive)+'.sha256').read_text().split()[0]
    if sha256(archive)!=expected:raise ValueError('zip checksum mismatch')
    extract_zip(archive,temp/'extract')
    pkg=temp/'extract'/name[:-4]
    manifest=verify_manifest(pkg,a.version,a.commit)
    if a.promote:
        if manifest['signing_mode']!='developer_id' or manifest['notarization_status']!='accepted' or manifest['channel']!='stable':
            raise ValueError('stable requires verified Developer ID and notarization')
        check_receipt(json.loads((temp/'acceptance-receipt.json').read_text()),manifest,expected)
        tag_ref=json.loads(gh('api',f'repos/{SOURCE_REPO}/commits/{tag}'))
        if tag_ref['sha']!=manifest['commit']:raise ValueError('source tag/commit mismatch')
        gh('release','edit',tag,'--repo',RELEASE_REPO,'--draft=false','--latest')
    else:
        # Check existence before any upload; published assets are never clobbered.
        exists=subprocess.run(['gh','release','view',tag,'--repo',RELEASE_REPO],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if exists.returncode==0:raise ValueError('release already exists; refusing overwrite')
        uploads=[archive,Path(str(archive)+'.sha256'),pkg/'release-manifest.json']
        for script in ('install-release.sh','install-release.ps1'):
            p=temp/script;shutil.copy2(pkg/'scripts'/script,p)
            side=Path(str(p)+'.sha256');side.write_text(sha256(p)+'  '+script+'\n');uploads.extend([p,side])
        latest=temp/'wechat-cli-latest-darwin-arm64.zip';shutil.copy2(archive,latest)
        side=Path(str(latest)+'.sha256');side.write_text(expected+'  '+latest.name+'\n');uploads.extend([latest,side])
        gh('release','create',tag,*map(str,uploads),'--repo',RELEASE_REPO,'--draft','--title','wechat-cli '+tag,'--notes',f'Candidate from {SOURCE_REPO} at {manifest["commit"]}. No stable compatibility or notarization claim.')
        readback=temp/'download';readback.mkdir()
        gh('release','download',tag,'--repo',RELEASE_REPO,'--dir',str(readback))
        if {p.name for p in readback.iterdir()}!={p.name for p in uploads}:raise ValueError('remote asset set mismatch')
        if any(sha256(p)!=sha256(readback/p.name) for p in uploads):raise ValueError('remote asset hash mismatch')
    print(json.dumps({'ok':True,'tag':tag,'published':a.promote,'commit':manifest['commit']}))

if __name__=='__main__':main()
