"""Standalone bootstrap verifier. Kept dependency-free for embedding in installers."""
import hashlib
import json
import re
import stat
import sys
import zipfile
from pathlib import Path, PurePosixPath


def verify_and_extract(archive, destination, expected_version, allow_prerelease=False):
    dest=Path(destination)
    with zipfile.ZipFile(archive) as z:
        infos=z.infolist()
        if len(infos)>2000 or sum(i.file_size for i in infos)>256*1024*1024:raise ValueError('release archive budget exceeded')
        names=set();roots=set()
        for i in infos:
            p=PurePosixPath(i.filename)
            if p.is_absolute() or '..' in p.parts or '\\' in i.filename or ':' in i.filename or stat.S_ISLNK(i.external_attr>>16):raise ValueError('unsafe release path')
            if i.filename in names:raise ValueError('duplicate release path')
            names.add(i.filename);roots.add(p.parts[0])
        if len(roots)!=1:raise ValueError('expected one release root')
        prefix=roots.pop()+'/'
        d=json.loads(z.read(prefix+'release-manifest.json'))
        if d.get('schema_version')!=2 or d.get('source_repository')!='r266-tech/wx-cli':raise ValueError('manifest schema/source mismatch')
        if d.get('version')!=expected_version or d.get('platform_arch')!='darwin-arm64':raise ValueError('release version/platform mismatch')
        if not re.fullmatch('[0-9a-f]{40}',d.get('commit','')):raise ValueError('invalid source commit')
        preview = allow_prerelease in (True, '1', 'true')
        stable = d.get('channel')=='stable' and d.get('signing_mode')=='developer_id' and d.get('notarization_status')=='accepted'
        candidate = (preview and re.fullmatch(r'\d+\.\d+\.\d+-[0-9A-Za-z.-]+', expected_version)
                     and d.get('channel')=='candidate' and d.get('signing_mode') in ('adhoc','developer_id')
                     and d.get('notarization_status') in ('not_configured','accepted'))
        if not (stable or candidate):raise ValueError('release is not a verified stable package; preview requires an explicit prerelease tag and --allow-prerelease')
        files={i.filename[len(prefix):] for i in infos if not i.is_dir()}-{'release-manifest.json'}
        if files!=set(d['artifacts']):raise ValueError('manifest file set mismatch')
        for name,expected in d['artifacts'].items():
            raw=z.read(prefix+name)
            if len(raw)!=expected['bytes'] or hashlib.sha256(raw).hexdigest()!=expected['sha256']:raise ValueError('manifest artifact hash mismatch')
        if dest.exists():raise ValueError('destination must be new')
        dest.mkdir(parents=True,mode=0o700);z.extractall(dest)
        for i in infos:
            if not i.is_dir():(dest/i.filename).chmod(0o755 if (i.external_attr>>16)&0o111 else 0o644)
    return dest/prefix

if __name__=='__main__':print(verify_and_extract(*sys.argv[1:]))
