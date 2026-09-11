#!/usr/bin/env python3
"""Fetch content-addressed build inputs and compile WCDB without local fallbacks."""
import argparse
import json
import os
import shutil
import subprocess
import sys
import zipfile
import stat
import posixpath
import urllib.request
from pathlib import Path
from release_lib import extract_zip, git, load_lock, sha256

ROOT = Path(__file__).resolve().parents[1]

def main():
    ap=argparse.ArgumentParser();ap.add_argument('--output',default=str(ROOT/'.build'))
    a=ap.parse_args();out=Path(a.output).resolve();out.mkdir(parents=True, exist_ok=True)
    lock=load_lock(ROOT);wx=out/'wxkey'
    if not wx.exists():
        subprocess.run(['git','clone','--branch',lock['wxkey']['tag'],'--depth','1','https://github.com/'+lock['wxkey']['repository']+'.git',str(wx)],check=True)
    if git(wx,'rev-parse','HEAD') != lock['wxkey']['commit_sha'] or git(wx,'status','--porcelain'):
        raise ValueError('wxkey source identity mismatch')
    dep=lock['wcdb']; archive=out/('wcdb-'+dep['version']+'.zip')
    if not archive.exists():
        tmp=archive.with_suffix('.part')
        with urllib.request.urlopen(dep['source_url'], timeout=120) as response, tmp.open('wb') as f:
            shutil.copyfileobj(response,f)
        if sha256(tmp)!=dep['archive_sha256']: raise ValueError('WCDB source checksum mismatch')
        tmp.rename(archive)
    if sha256(archive)!=dep['archive_sha256']: raise ValueError('WCDB source checksum mismatch')
    source=out/'wcdb-source'
    if not source.exists():
        # The pinned upstream source has symlink headers. Materialize only targets
        # contained in this verified archive; release/user archives still reject links.
        with zipfile.ZipFile(archive) as z:
            infos={i.filename:i for i in z.infolist()}
            if len(infos)!=len(z.infolist()): raise ValueError('duplicate source member')
            def content(name,seen=None):
                seen=set() if seen is None else seen
                if name in seen or name not in infos: raise ValueError('source link cycle or missing target')
                seen.add(name); info=infos[name]
                if stat.S_ISLNK(info.external_attr>>16):
                    target=z.read(name).decode()
                    if target.startswith('/'): raise ValueError('absolute source link')
                    target=posixpath.normpath(posixpath.join(posixpath.dirname(name),target))
                    if not target.startswith('wcdb-'+dep['version']+'/'): raise ValueError('source link escapes')
                    return content(target,seen)
                return z.read(name)
            for name,i in infos.items():
                if not name.startswith('wcdb-'+dep['version']+'/') or i.is_dir(): continue
                from release_lib import safe_name
                safe_name(name);dest=source/name;dest.parent.mkdir(parents=True,exist_ok=True)
                dest.write_bytes(content(name))

    wcdb=source/('wcdb-'+dep['version']);build=out/'wcdb-build'
    subprocess.run(['cmake','-S',str(wcdb/'src'),'-B',str(build),*dep['build_flags']],check=True)
    subprocess.run(['cmake','--build',str(build),'--config','Release','--parallel','4'],check=True)
    libs=list(build.rglob('libWCDB.dylib'))
    if not libs: libs=list({p.resolve() for p in build.glob('WCDB.framework/Versions/*/WCDB') if p.is_file()})
    if len(libs)!=1: raise ValueError('expected one WCDB dylib')
    portable=out/'libWCDB.dylib'
    shutil.copy2(libs[0],portable)
    subprocess.run(['install_name_tool','-id','@rpath/libWCDB.dylib',str(portable)],check=True)
    libs=[portable]
    result={'schema_version':1,'wxkey_source':str(wx),'wcdb_library':str(libs[0]),'wcdb_source_dir':str(wcdb),'dependencies':lock}
    result['dependencies']['wcdb']['binary_sha256']=sha256(libs[0])
    (out/'inputs.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({'ok':True,'inputs':str(out/'inputs.json')}))

if __name__=='__main__': main()
