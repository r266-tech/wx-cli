"""Shared validation for source inputs, packaged files and release assets."""
import hashlib
import json
import os
import re
import stat
import subprocess
import zipfile
from pathlib import Path, PurePosixPath

SOURCE_REPO = 'r266-tech/wx-cli'
RELEASE_REPO = 'r266-tech/wechat-cli-releases'


def sha256(path):
    with Path(path).open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], text=True).strip()


def safe_name(name):
    p = PurePosixPath(name)
    if not name or '\\' in name or p.is_absolute() or '..' in p.parts or ':' in name or str(p) != name.rstrip('/'):
        raise ValueError('unsafe archive path')
    return p


def extract_zip(archive, destination, max_bytes=1024**3):
    destination = Path(destination)
    if destination.exists():
        raise ValueError('extraction destination must be new')
    with zipfile.ZipFile(archive) as z:
        entries = z.infolist()
        if sum(i.file_size for i in entries) > max_bytes or len(entries) > 60000:
            raise ValueError('archive budget exceeded')
        seen = set()
        for i in entries:
            safe_name(i.filename)
            if i.filename in seen or stat.S_ISLNK(i.external_attr >> 16):
                raise ValueError('duplicate or symlink archive entry')
            seen.add(i.filename)
        destination.mkdir(parents=True, mode=0o700)
        z.extractall(destination)
        for i in entries:
            if not i.is_dir():
                p = destination / i.filename
                p.chmod(0o755 if (i.external_attr >> 16) & 0o111 else 0o644)


def load_lock(root):
    lock = json.loads((Path(root) / 'release-dependencies.json').read_text())
    if lock['schema_version'] != 1:
        raise ValueError('unknown dependency schema')
    for dep in ('wxkey', 'wcdb'):
        if not re.fullmatch('[0-9a-f]{40}', lock[dep]['commit_sha']):
            raise ValueError('dependency commit must be full SHA')
    if not re.fullmatch('[0-9a-f]{64}', lock['wcdb']['archive_sha256']):
        raise ValueError('dependency archive must have SHA256')
    return lock


def verify_manifest(root, expected_version=None, expected_commit=None):
    root = Path(root)
    d = json.loads((root / 'release-manifest.json').read_text())
    required = ('version','commit','source_repository','platform_arch','build_timestamp_utc','wxkey_version','wxkey_commit','wcdb_version','wcdb_source','wcdb_sha256','signing_mode','notarization_status','artifacts','dependencies')
    if d.get('schema_version') != 2 or any(not d.get(k) or d[k] == 'unknown' for k in required):
        raise ValueError('manifest fields missing or unknown')
    if d['source_repository'] != SOURCE_REPO or d['platform_arch'] != 'darwin-arm64':
        raise ValueError('source or platform mismatch')
    if not re.fullmatch('[0-9a-f]{40}', d['commit']) or not re.fullmatch('[0-9a-f]{40}', d['wxkey_commit']):
        raise ValueError('invalid source commit')
    if expected_version and d['version'] != expected_version:
        raise ValueError('version mismatch')
    if expected_commit and d['commit'] != expected_commit:
        raise ValueError('commit mismatch')
    actual = set()
    for p in root.rglob('*'):
        if p.is_symlink():
            raise ValueError('packaged symlink')
        if p.is_file() and p.name != 'release-manifest.json':
            actual.add(p.relative_to(root).as_posix())
    if actual != set(d['artifacts']):
        raise ValueError('manifest file set mismatch')
    for name, entry in d['artifacts'].items():
        safe_name(name)
        p = root / name
        if p.stat().st_size != entry['bytes'] or sha256(p) != entry['sha256']:
            raise ValueError('manifest hash mismatch: '+name)
    if d['artifacts']['libWCDB.dylib']['sha256'] != d['wcdb_sha256']:
        raise ValueError('WCDB binary mismatch')
    for dep, filename in [('wxkey','wxkey'),('wcdb','libWCDB.dylib')]:
        if d['dependencies'][dep]['binary_sha256'] != d['artifacts'][filename]['sha256']:
            raise ValueError('component hash mismatch')
    return d
