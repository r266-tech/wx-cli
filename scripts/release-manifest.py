#!/usr/bin/env python3
"""Generate a deterministic release manifest for a packaged wechat-cli build."""
from __future__ import annotations
import hashlib, json, os, subprocess, sys
from pathlib import Path

def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()

def git_value(*args: str) -> str:
    try:
        return subprocess.check_output(['git', *args], text=True, stderr=subprocess.DEVNULL).strip()
    except Exception:
        return 'unknown'

def required_env(name: str) -> str:
    value = os.environ.get(name, '').strip()
    if not value:
        raise SystemExit(f'missing required release metadata: {name}')
    return value

def main() -> int:
    if len(sys.argv) != 4:
        print('usage: release-manifest.py VERSION PLATFORM_ARCH DIST_ROOT', file=sys.stderr)
        return 2
    version, platform_arch, raw_root = sys.argv[1:]
    root = Path(raw_root).resolve()
    artifacts = {}
    for path in sorted(root.rglob('*')):
        if path.is_file() and path.name != 'release-manifest.json':
            artifacts[str(path.relative_to(root)).replace(os.sep, '/')] = {
                'bytes': path.stat().st_size,
                'sha256': sha256(path),
            }
    if not version:
        raise SystemExit('version is required')
    wxkey_commit = required_env('WECHAT_CLI_WXKEY_COMMIT')
    wcdb_source = required_env('WECHAT_CLI_WCDB_SOURCE')
    wcdb_sha256 = required_env('WECHAT_CLI_WCDB_SHA256')
    signing_mode = required_env('WECHAT_CLI_SIGNING_MODE')
    notarization_status = required_env('WECHAT_CLI_NOTARIZATION_STATUS')
    build_timestamp = required_env('SOURCE_DATE_EPOCH')
    manifest = {
        'schema_version': 1,
        'product': 'wechat-cli',
        'source_repository': os.environ.get('WECHAT_CLI_SOURCE_REPOSITORY', 'r266-tech/wx-cli'),
        'version': version,
        'platform_arch': platform_arch,
        'commit': git_value('rev-parse', 'HEAD'),
        'tag': git_value('describe', '--tags', '--exact-match', 'HEAD'),
        'build_timestamp_utc': build_timestamp,
        'wxkey_version': os.environ.get('WECHAT_CLI_WXKEY_VERSION', 'v1.4.8'),
        'wxkey_commit': wxkey_commit,
        'wcdb_version': os.environ.get('WECHAT_CLI_WCDB_VERSION', 'unknown'),
        'wcdb_source': wcdb_source,
        'wcdb_sha256': wcdb_sha256,
        'signing_mode': signing_mode,
        'notarization_status': notarization_status,
        'artifacts': artifacts,
    }
    (root / 'release-manifest.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    return 0

if __name__ == '__main__':
    raise SystemExit(main())
