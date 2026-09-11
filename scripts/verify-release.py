#!/usr/bin/env python3
import argparse
import json
from pathlib import Path
from release_lib import extract_zip, sha256, verify_manifest

ap=argparse.ArgumentParser();ap.add_argument('--root');ap.add_argument('--archive');ap.add_argument('--sha256-file');ap.add_argument('--extract');ap.add_argument('--version');ap.add_argument('--commit');ap.add_argument('--stable',action='store_true')
a=ap.parse_args()
if a.archive:
    if not a.sha256_file:raise ValueError('checksum required')
    expected=Path(a.sha256_file).read_text().split()[0]
    if sha256(a.archive)!=expected:raise ValueError('checksum mismatch')
    extract_zip(a.archive,a.extract)
    roots=[p for p in Path(a.extract).iterdir() if p.is_dir()]
    if len(roots)!=1:raise ValueError('expected one package root')
    a.root=roots[0]
d=verify_manifest(a.root,a.version,a.commit)
if a.stable and (d['channel']!='stable' or d['signing_mode']!='developer_id' or d['notarization_status']!='accepted'):raise ValueError('stable publication gate failed')
print(json.dumps({'ok':True,'version':d['version'],'commit':d['commit'],'channel':d['channel']}))
