#!/usr/bin/env sh
set -eu
exec python3 "$(dirname "$0")/public_repo_scan.py" "${1:-.}"
