#!/bin/zsh
set -euo pipefail
ROOT="${1:-.}"
cd "$ROOT"
if git ls-files | grep -E '(^|/)(dist/|.*\.(db|sqlite|zip|dylib|dll|exe)$)' >/dev/null; then
  echo 'tracked build or database artifact found' >&2
  exit 1
fi
if rg -n '/Users/admin/|/Volumes/|wxid_3qqa0aja1kf06d|-----BEGIN (RSA|OPENSSH|EC|PRIVATE) KEY-----|gh[pousr]_[A-Za-z0-9_]{20,}' --glob '!scripts/public-repo-scan.sh' --glob '!.git/**' .; then
  echo 'sensitive local path, credential, or account fixture found' >&2
  exit 1
fi
if rg -n 'github\.com/r266-tech/wechat-cli([^ -]|$)' --glob '!.git/**' .; then
  echo 'legacy source repository reference found' >&2
  exit 1
fi
