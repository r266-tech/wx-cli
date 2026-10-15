"""Generate Windows candidate provenance from pinned WCDB build evidence."""
import datetime
import json
import re
import subprocess
import sys
from pathlib import Path
from release_lib import SOURCE_REPO, git, load_lock, sha256


def write_manifest(root, version, commit, dependency, evidence, timestamp, go_version):
    root = Path(root)
    if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', version):
        raise ValueError('invalid release version')
    if not re.fullmatch('[0-9a-f]{40}', commit):
        raise ValueError('invalid source commit')
    # The source lock's build flags describe macOS. Windows records its own
    # flags, while requiring the same pinned source URL, archive and commit.
    for key in ('repository', 'version', 'source_url', 'archive_sha256', 'commit_sha'):
        if evidence.get(key) != dependency[key]:
            raise ValueError('Windows WCDB input differs from lock: '+key)
    if evidence.get('build_platform') != 'windows-amd64' or not evidence.get('build_flags'):
        raise ValueError('Windows WCDB build evidence missing')
    artifacts = {}
    for p in sorted(root.rglob('*')):
        if p.is_symlink():
            raise ValueError('packaged symlink')
        if p.is_file() and p != root/'release-manifest.json':
            artifacts[p.relative_to(root).as_posix()] = {'bytes': p.stat().st_size, 'sha256': sha256(p)}
    if not {'wechat-cli.exe', 'libWCDB.dll', 'install.ps1'} <= artifacts.keys():
        raise ValueError('required Windows artifact missing')
    if artifacts['libWCDB.dll']['sha256'] != evidence.get('binary_sha256'):
        raise ValueError('Windows WCDB binary differs from build evidence')
    manifest = {
        'schema_version': 2, 'product': 'wechat-cli', 'source_repository': SOURCE_REPO,
        'module_path': 'github.com/r266-tech/wx-cli/v2', 'version': version, 'commit': commit,
        'platform_arch': 'windows-amd64', 'channel': 'candidate',
        'signing_mode': 'unsigned', 'notarization_status': 'not_applicable',
        'build_timestamp_utc': datetime.datetime.fromtimestamp(timestamp, datetime.timezone.utc).isoformat(),
        'go_version': go_version, 'wcdb_version': dependency['version'],
        'wcdb_source': dependency['source_url'], 'wcdb_sha256': artifacts['libWCDB.dll']['sha256'],
        'dependencies': {'wcdb': evidence}, 'artifacts': artifacts,
    }
    (root/'release-manifest.json').write_text(json.dumps(manifest, indent=2)+'\n', encoding='utf-8')
    return manifest


def main():
    version, directory, evidence_path = sys.argv[1:]
    source = Path(__file__).resolve().parents[1]
    write_manifest(directory, version, git(source, 'rev-parse', 'HEAD'), load_lock(source)['wcdb'],
                   json.loads(Path(evidence_path).read_text(encoding='utf-8-sig')),
                   int(git(source, 'show', '-s', '--format=%ct', 'HEAD')),
                   subprocess.check_output(['go', 'version'], text=True).strip())


if __name__ == '__main__':
    main()
