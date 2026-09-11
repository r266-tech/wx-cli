"""Scan only tracked files; errors reveal locations and rule IDs, never values."""
import json
import re
import subprocess
import sys
from pathlib import Path

DENIED_SUFFIXES = {'.db', '.sqlite', '.sqlite3', '.zip', '.dylib', '.dll', '.exe', '.pem', '.p12', '.pfx', '.pyc'}
RULES = {
    'private_key': re.compile(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----'),
    'github_token': re.compile(r'gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}'),
    'local_identity_path': re.compile(r'/Users/(?!example/|me/|<)[\w.-]+/|C:\\\\Users\\\\(?!example)[\w.-]+'),
    'account_identifier': re.compile(r'wxid_[a-z0-9]{12,}(?:_[a-f0-9]{4})?'),
    'numeric_chat_identifier': re.compile(r'\b\d{9,}@chatroom'),
}

def scan(root):
    root = Path(root).resolve()
    paths = subprocess.check_output(['git', '-C', str(root), 'ls-files', '-z']).decode().split('\0')
    errors = []
    for name in filter(None, paths):
        path = root / name
        if path.is_symlink():
            errors.append({'path': name, 'rule': 'symlink'}); continue
        if 'dist' in path.parts or path.suffix.lower() in DENIED_SUFFIXES:
            errors.append({'path': name, 'rule': 'artifact'}); continue
        raw = path.read_bytes()
        if b'\0' in raw or len(raw) > 2_000_000:
            errors.append({'path': name, 'rule': 'binary_or_oversize'}); continue
        text = raw.decode('utf-8', errors='strict')
        for line_no, line in enumerate(text.splitlines(), 1):
            for rule, pattern in RULES.items():
                if rule == 'account_identifier' and name.endswith('_test.go') and ('wxid_test' + 'talker0001') in line: continue
                if pattern.search(line): errors.append({'path': name, 'line': line_no, 'rule': rule})
        # Source/module URL check is distinct from the deliberately retained releases URL.
        if re.search(r'github\.com/r266-tech/wechat-cli(?:/|["\s]|$)', text):
            errors.append({'path': name, 'rule': 'legacy_source_url'})
        if ('github.com/r266-tech/' + 'wx-cli-releases') in text:
            errors.append({'path': name, 'rule': 'invalid_release_url'})
    if not (root / 'LICENSE').read_text().startswith('MIT License'):
        errors.append({'path': 'LICENSE', 'rule': 'license'})
    return errors

if __name__ == '__main__':
    findings = scan(sys.argv[1] if len(sys.argv) > 1 else '.')
    print(json.dumps({'ok': not findings, 'findings': findings}))
    raise SystemExit(bool(findings))
